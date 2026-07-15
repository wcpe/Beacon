package service

import (
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// advanceRolling 推进一个 rolling 单：payload 未就绪先做准备；否则回执驱动在途目标 + 推进当前活动批。
func (s *DeliveryOrchestrator) advanceRolling(rt *orderRuntime) {
	if rt.order.PayloadState != model.PayloadStateReady {
		s.advancePayloadPrep(rt)
		return
	}
	// 单一驱动源：回执只落命令 CAS，目标终态在此由推进器统一落（避免并发改状态）。
	s.reconcileInFlightTargets(rt)
	s.refreshAllBatchCounts(rt)
	batch := activeBatch(rt.batches)
	if batch == nil {
		return
	}
	switch batch.Status {
	case model.ChangeBatchStatusRunning:
		s.advanceRunningBatch(rt, batch)
	case model.ChangeBatchStatusObserving:
		s.advanceObservingBatch(rt, batch)
	}
}

// —— 在途目标回执驱动（rolling / paused 均执行：收口 pushing / pushed / activating 到终态，不下发新目标）——

// reconcileInFlightTargets 逐个收口在途目标：pushing 判推送回执、pushed 接续生效、activating 判生效回执。
func (s *DeliveryOrchestrator) reconcileInFlightTargets(rt *orderRuntime) {
	for i := range rt.targets {
		t := &rt.targets[i]
		switch t.Status {
		case model.ChangeTargetStatusPushing:
			s.reconcilePushing(rt, t)
		case model.ChangeTargetStatusPushed:
			s.activateTarget(rt, t)
		case model.ChangeTargetStatusActivating:
			s.reconcileActivating(rt, t)
		}
	}
}

// reconcilePushing 判推送阶段命令终态：done→pushed（落实际变更 / 备份计数）并接续生效；failed/expired/超时→failed。
func (s *DeliveryOrchestrator) reconcilePushing(rt *orderRuntime, t *model.ChangeTarget) {
	cmd, err := s.latestDeliveryCommand(rt.nsCode, t.ServerID, model.CommandTypeDeliveryPush, rt.order.ID)
	if err != nil || cmd == nil {
		return
	}
	switch cmd.Status {
	case model.CommandStatusDone:
		res := parseDeliveryCmdResult(cmd.ResultDetail)
		if s.casTarget(rt, t, []string{model.ChangeTargetStatusPushing}, model.ChangeTargetStatusPushed, map[string]any{
			"pushed_at": s.now(), "changed_file_count": res.ChangedFileCount,
			"skipped_file_count": res.SkippedFileCount, "backup_present": res.BackupPresent,
		}) {
			s.activateTarget(rt, t) // 推送落定即接续生效判定（push_only 立即 activated，其它下发 delivery_activate）
		}
	case model.CommandStatusFailed:
		s.failTarget(rt, t, model.ChangeTargetStatusPushing, targetErrorOr(parseDeliveryCmdResult(cmd.ResultDetail).Error, "推送失败"))
	case model.CommandStatusExpired:
		s.failTarget(rt, t, model.ChangeTargetStatusPushing, "推送命令过期（agent 离线或长时间未回执）")
	default:
		s.failOnTimeout(rt, t, cmd, model.ChangeTargetStatusPushing, "推送超时（agent 离线或未在超时内回执）")
	}
}

// activateTarget 接续生效：push_only 推送落盘即 activated；restart / hot_reload 下发 delivery_activate 转 activating。
// 进入 activating 时记 activating_started_at 锚点（restart 心跳回归判定用），与命令下发同事务原子落库。
func (s *DeliveryOrchestrator) activateTarget(rt *orderRuntime, t *model.ChangeTarget) {
	if rt.order.ActivationMethod == model.ActivationMethodPushOnly {
		s.casTarget(rt, t, []string{model.ChangeTargetStatusPushed}, model.ChangeTargetStatusActivated,
			map[string]any{"activated_at": s.now()})
		return
	}
	payload := deliveryActivatePayload{OrderID: rt.order.ID, ActivationMethod: rt.order.ActivationMethod}
	if rt.order.ActivationMethod == model.ActivationMethodRestart {
		payload.ActivateTimeoutSec = rt.order.ActivateTimeoutSec
	}
	cmd := newDeliveryCommand(rt.nsCode, t.ServerID, model.CommandTypeDeliveryActivate, payload)
	startedAt := s.now() // activating 起始锚点：restart 只认此刻之后接收的心跳批为回归（排除关服前残留）
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.repo.WithTx(tx).UpdateTargetCAS(t.ID, []string{model.ChangeTargetStatusPushed},
			map[string]any{"status": model.ChangeTargetStatusActivating, "activating_started_at": startedAt})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		return s.cmdRepo.WithTx(tx).Create(cmd)
	})
	if err != nil {
		if err != errCASSkip {
			slog.Error("交付编排下发生效命令失败", "orderId", rt.order.ID, "serverId", t.ServerID, "错误", err)
		}
		return
	}
	t.Status = model.ChangeTargetStatusActivating
	t.ActivatingStartedAt = &startedAt
	s.notifyAgent(rt.nsCode, t.ServerID)
	s.emitTargetEvent(rt, t)
}

// reconcileActivating 判 activating 目标生效终态，按生效方式分野（spec §4.6.1）：
//   - push_only 不入此路径（activateTarget 已直接置 activated，不下发 activate 命令）。
//   - restart 走心跳回归观测（reconcileActivatingRestart）：进程已关，agent 无法可靠回执「起来了」，
//     activated 由控制面观测 identity 心跳回归判定（注册 / 健康真源 = Go 进程内存，ADR-0070）。
//   - hot_reload 仍按 agent 回执驱动（reconcileActivatingByAck，下一迭代精化为配置热更回调确认）。
func (s *DeliveryOrchestrator) reconcileActivating(rt *orderRuntime, t *model.ChangeTarget) {
	if rt.order.ActivationMethod == model.ActivationMethodRestart {
		s.reconcileActivatingRestart(rt, t)
		return
	}
	s.reconcileActivatingByAck(rt, t)
}

// reconcileActivatingRestart 判 restart 生效（spec §4.6.1 / ADR-0070）：
//   - agent 回执 activate failed（关服指令本身失败）→ 直接 failed，不等心跳回归。
//   - 观测该 identity 心跳回归（起始之后有新指标批且当前 online）→ activated。
//   - activate_timeout_sec 内未回归 → failed（「关了没起来」安全阀，计入熔断失败率）。
//
// 注意：agent 回执 activate success 只表示「已开始关服」，**不**直接判 activated——进程已关，需真心跳回归才算数。
func (s *DeliveryOrchestrator) reconcileActivatingRestart(rt *orderRuntime, t *model.ChangeTarget) {
	cmd, err := s.latestDeliveryCommand(rt.nsCode, t.ServerID, model.CommandTypeDeliveryActivate, rt.order.ID)
	if err != nil {
		return
	}
	// 关服指令回执失败 → 直接 failed（spec §4.6.1 failed 判据之一）。
	if cmd != nil && cmd.Status == model.CommandStatusFailed {
		s.failTarget(rt, t, model.ChangeTargetStatusActivating,
			targetErrorOr(parseDeliveryCmdResult(cmd.ResultDetail).Error, "关服指令回执失败"))
		return
	}
	// 心跳回归即 activated；否则按 activate_timeout_sec 从 activating 起始计时判超时。
	if s.heartbeatReturned(rt.order.NamespaceID, t.ServerID, t.ActivatingStartedAt) {
		s.casTarget(rt, t, []string{model.ChangeTargetStatusActivating}, model.ChangeTargetStatusActivated,
			map[string]any{"activated_at": s.now()})
		return
	}
	s.failRestartOnTimeout(rt, t)
}

// reconcileActivatingByAck 判 hot_reload 生效（回执驱动，本迭代沿用 M3 现状，下一迭代精化）：
// done→activated；failed/expired/超时→failed（超时从命令创建计时）。
func (s *DeliveryOrchestrator) reconcileActivatingByAck(rt *orderRuntime, t *model.ChangeTarget) {
	cmd, err := s.latestDeliveryCommand(rt.nsCode, t.ServerID, model.CommandTypeDeliveryActivate, rt.order.ID)
	if err != nil || cmd == nil {
		return
	}
	switch cmd.Status {
	case model.CommandStatusDone:
		s.casTarget(rt, t, []string{model.ChangeTargetStatusActivating}, model.ChangeTargetStatusActivated,
			map[string]any{"activated_at": s.now()})
	case model.CommandStatusFailed:
		s.failTarget(rt, t, model.ChangeTargetStatusActivating, targetErrorOr(parseDeliveryCmdResult(cmd.ResultDetail).Error, "生效失败"))
	case model.CommandStatusExpired:
		s.failTarget(rt, t, model.ChangeTargetStatusActivating, "生效命令过期（agent 离线或长时间未回执）")
	default:
		s.failOnTimeout(rt, t, cmd, model.ChangeTargetStatusActivating, "生效超时（agent 未在 activateTimeoutSec 内回执）")
	}
}

// heartbeatReturned 判目标 identity 心跳是否已回归（restart 生效判据，spec §4.6.1）：
// 只认 activating 起始之后接收的指标批（ReceivedAtMs > 起始 ms，排除关服前残留心跳误判），
// 且当前仍新鲜（online，新鲜度 ≤ healthLostAfterMs，与健康域在线口径一致）。注册 / 健康真源 = Go 进程内存。
func (s *DeliveryOrchestrator) heartbeatReturned(nsID uint, serverID string, startedAt *time.Time) bool {
	if startedAt == nil {
		return false // 无起始锚点无法判「起始之后」，保守判未回归（防旧心跳误判）
	}
	latest, ok := s.metrics.Latest(nsID, serverID)
	if !ok {
		return false
	}
	nowMs := s.now().UnixMilli()
	return latest.ReceivedAtMs > startedAt.UnixMilli() && nowMs-latest.ReceivedAtMs <= healthLostAfterMs
}

// failRestartOnTimeout restart 生效超时判定：从 activating 起始计满 activate_timeout_sec 仍未心跳回归
// → failed（「关了没起来」安全阀，计入熔断失败率，spec §4.6.1 / ADR-0070）。起始锚点缺失（异常）保守判超时。
// 不动 activate 命令终态：agent 回执「已开始关服」(done) 与「关了没起来」(target failed) 是两回事，各自留痕。
func (s *DeliveryOrchestrator) failRestartOnTimeout(rt *orderRuntime, t *model.ChangeTarget) {
	timeout := time.Duration(rt.order.ActivateTimeoutSec) * time.Second
	if t.ActivatingStartedAt != nil && s.now().Sub(*t.ActivatingStartedAt) < timeout {
		return // 未到超时，继续等待心跳回归
	}
	s.failTarget(rt, t, model.ChangeTargetStatusActivating,
		"生效超时（关服后 activateTimeoutSec 内心跳未回归；宿主未拉起进程或启动过慢）")
}

// failOnTimeout 命令仍在途（pending/fetched）时按 activateTimeoutSec 判超时：超时则目标 failed 并尽力过期命令。
func (s *DeliveryOrchestrator) failOnTimeout(rt *orderRuntime, t *model.ChangeTarget, cmd *model.AgentCommand, from, reason string) {
	timeout := time.Duration(rt.order.ActivateTimeoutSec) * time.Second
	if s.now().Sub(cmd.CreatedAt) < timeout {
		return
	}
	if s.failTarget(rt, t, from, reason) {
		// 尽力把在途命令置 expired，防迟到回执再改命令态造成困惑（目标终态权威在此）。
		if _, e := s.cmdRepo.UpdateStatus(cmd.ID, cmd.Status, model.CommandStatusExpired, ""); e != nil {
			slog.Warn("交付编排超时置命令过期失败", "commandId", cmd.ID, "错误", e)
		}
	}
}

// —— 当前活动批推进 ——

// advanceRunningBatch 推进 running 批：下发未开始目标 → 熔断评估 → 全终态转 observing（计数已在 reconcile 后统一刷新）。
func (s *DeliveryOrchestrator) advanceRunningBatch(rt *orderRuntime, batch *model.ChangeBatch) {
	members := targetsInBatch(rt.targets, batch.ID)
	s.dispatchPending(rt, batch, members)
	s.sampleObserve(rt.order.ID, batch.BatchNo, rt.order.NamespaceID, members)
	if s.evalAndTrip(rt, batch, members, false) {
		return
	}
	if allTargetsTerminal(members) {
		now := s.now()
		if s.casBatch(rt, batch, []string{model.ChangeBatchStatusRunning}, model.ChangeBatchStatusObserving,
			map[string]any{"observe_started_at": now}) {
			s.markObserveStarted(rt.order.ID, batch.BatchNo, now)
		}
	}
}

// advanceObservingBatch 推进 observing 批：采样观察窗 → 熔断评估（含健康恶化）→ 观察窗到点转 awaiting_confirm。
func (s *DeliveryOrchestrator) advanceObservingBatch(rt *orderRuntime, batch *model.ChangeBatch) {
	members := targetsInBatch(rt.targets, batch.ID)
	s.sampleObserve(rt.order.ID, batch.BatchNo, rt.order.NamespaceID, members)
	if s.evalAndTrip(rt, batch, members, true) {
		return
	}
	if batch.ObserveStartedAt == nil {
		return
	}
	window := time.Duration(rt.order.ObserveWindowSec) * time.Second
	if s.now().Sub(*batch.ObserveStartedAt) >= window {
		s.casBatch(rt, batch, []string{model.ChangeBatchStatusObserving}, model.ChangeBatchStatusAwaitingConfirm, nil)
	}
}

// dispatchPending 下发批内未开始目标（pending→pushing + delivery_push 命令，一事务原子），提交后唤醒各 agent。
func (s *DeliveryOrchestrator) dispatchPending(rt *orderRuntime, batch *model.ChangeBatch, members []*model.ChangeTarget) {
	pending := make([]*model.ChangeTarget, 0, len(members))
	for _, t := range members {
		if t.Status == model.ChangeTargetStatusPending {
			pending = append(pending, t)
		}
	}
	if len(pending) == 0 {
		return
	}
	fileCount, totalBytes := s.manifestSummary(rt.order.ID)
	dispatched := make([]*model.ChangeTarget, 0, len(pending))
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		cmdTx := s.cmdRepo.WithTx(tx)
		for _, t := range pending {
			ok, e := repoTx.UpdateTargetCAS(t.ID, []string{model.ChangeTargetStatusPending},
				map[string]any{"status": model.ChangeTargetStatusPushing})
			if e != nil {
				return e
			}
			if !ok {
				continue // 已被并发推进（幂等护栏），跳过
			}
			cmd := newDeliveryCommand(rt.nsCode, t.ServerID, model.CommandTypeDeliveryPush,
				deliveryPushPayload{OrderID: rt.order.ID, FileCount: fileCount, TotalBytes: totalBytes})
			if e := cmdTx.Create(cmd); e != nil {
				return e
			}
			dispatched = append(dispatched, t)
		}
		return nil
	})
	if err != nil {
		slog.Error("交付编排下发推送命令失败", "orderId", rt.order.ID, "batchNo", batch.BatchNo, "错误", err)
		return
	}
	for _, t := range dispatched {
		t.Status = model.ChangeTargetStatusPushing
		s.notifyAgent(rt.nsCode, t.ServerID)
		s.emitTargetEvent(rt, t)
	}
}

// manifestSummary 求某单文件项的清单摘要（文件数 / 总字节）：写入 delivery_push 命令载荷，仅摘要绝不含内容。
func (s *DeliveryOrchestrator) manifestSummary(orderID uint) (int, int64) {
	items, err := s.repo.ListItems(orderID)
	if err != nil {
		return 0, 0
	}
	count := 0
	var total int64
	for i := range items {
		if items[i].Kind != model.ChangeItemKindFileDiff {
			continue
		}
		count++
		if items[i].SizeBytes != nil {
			total += *items[i].SizeBytes
		}
	}
	return count, total
}

// refreshAllBatchCounts 对全部批次按目标终态重算计数（幂等写回；reconcile 后统一调，保各批视图计数准确）。
func (s *DeliveryOrchestrator) refreshAllBatchCounts(rt *orderRuntime) {
	for i := range rt.batches {
		s.refreshBatchCounts(&rt.batches[i], targetsInBatch(rt.targets, rt.batches[i].ID))
	}
}

// refreshBatchCounts 按目标终态重算批计数并幂等写回（success=activated / failed / skipped）。
func (s *DeliveryOrchestrator) refreshBatchCounts(batch *model.ChangeBatch, members []*model.ChangeTarget) {
	counts := countByStatus(members)
	success := counts[model.ChangeTargetStatusActivated]
	failed := counts[model.ChangeTargetStatusFailed]
	skipped := counts[model.ChangeTargetStatusSkipped]
	if success == batch.SuccessCount && failed == batch.FailedCount && skipped == batch.SkippedCount {
		return
	}
	batch.SuccessCount, batch.FailedCount, batch.SkippedCount = success, failed, skipped
	if err := s.repo.UpdateBatchColumns(batch.ID, map[string]any{
		"success_count": success, "failed_count": failed, "skipped_count": skipped,
	}); err != nil {
		slog.Error("交付编排刷新批计数失败", "batchId", batch.ID, "错误", err)
	}
}

// —— payload 准备（启动后 uploading 态推进；spec §4.4.2）——

// advancePayloadPrep 推进 payload 准备：全部就绪→ready 并首批 running；上传命令终态仍缺→prepare_failed 暂停。
func (s *DeliveryOrchestrator) advancePayloadPrep(rt *orderRuntime) {
	missing, err := s.blobs.MissingBlobs(rt.order.ID)
	if err != nil {
		slog.Error("交付编排查缺失 blob 失败", "orderId", rt.order.ID, "错误", err)
		return
	}
	if len(missing) == 0 {
		s.markPayloadReady(rt)
		return
	}
	cmd, err := s.latestDeliveryCommand(rt.nsCode, rt.order.SourceServerID, model.CommandTypeDeliveryUpload, rt.order.ID)
	if err != nil {
		return
	}
	// 上传命令已终态（完成 / 失败 / 过期）却仍缺 blob → 准备失败暂停（spec §4.4.2；修复后可 resume 重试准备）。
	if cmd != nil && (cmd.Status == model.CommandStatusDone || cmd.Status == model.CommandStatusFailed ||
		cmd.Status == model.CommandStatusExpired) {
		s.markPrepareFailed(rt, "模板源上传未覆盖全部缺失 blob（上传失败或中断），payload 准备失败")
	}
	// 命令仍在途（pending/fetched）或不存在：等待模板源上传（大文件上传耗时长，不设独立超时，靠命令过期兜底）。
}

// markPayloadReady 事务内把 payload 置 ready 并首批 pending→running，唤醒推进器立即下发首批。
func (s *DeliveryOrchestrator) markPayloadReady(rt *orderRuntime) {
	now := s.now()
	first := firstBatch(rt.batches)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		// CAS 于 rolling→rolling 只改 payload_state（单一驱动源，目标列更新不改状态机）。
		if ok, e := repoTx.UpdateStatusCAS(rt.order.ID, []string{model.ChangeOrderStatusRolling},
			map[string]any{"status": model.ChangeOrderStatusRolling, "payload_state": model.PayloadStateReady}); e != nil || !ok {
			return errOrSkip(e, ok)
		}
		if first != nil {
			if _, e := repoTx.UpdateBatchCAS(first.ID, []string{model.ChangeBatchStatusPending},
				map[string]any{"status": model.ChangeBatchStatusRunning, "started_at": now}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		if err != errCASSkip {
			slog.Error("交付编排置 payload 就绪失败", "orderId", rt.order.ID, "错误", err)
		}
		return
	}
	rt.order.PayloadState = model.PayloadStateReady
	if first != nil {
		first.Status = model.ChangeBatchStatusRunning
		first.StartedAt = &now
		s.emitBatchEvent(rt, first)
	}
	s.wake() // 立即再跑一轮下发首批
}

// markPrepareFailed 事务内把 payload 置 failed + 单 rolling→paused(prepare_failed)，落脱敏原因。
func (s *DeliveryOrchestrator) markPrepareFailed(rt *orderRuntime, reason string) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		ok, e := repoTx.UpdateStatusCAS(rt.order.ID, []string{model.ChangeOrderStatusRolling}, map[string]any{
			"status": model.ChangeOrderStatusPaused, "pause_kind": model.PauseKindPrepareFailed,
			"pause_reason": reason, "payload_state": model.PayloadStateFailed,
		})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		return nil
	})
	if err != nil {
		if err != errCASSkip {
			slog.Error("交付编排置准备失败暂停出错", "orderId", rt.order.ID, "错误", err)
		}
		return
	}
	rt.order.Status = model.ChangeOrderStatusPaused
	rt.order.PayloadState = model.PayloadStateFailed
	slog.Warn("交付编排 payload 准备失败，单已暂停", "orderId", rt.order.ID, "原因", reason)
	s.emitOrderEvent(rt)
}

// —— 熔断评估（两条独立阈值，任一触发即熔断，spec §4.4.4）——

// evalAndTrip 评估失败率 / 健康恶化熔断，命中即熔断（批 failed + 单 paused + 未下发目标 skipped）；返回是否已熔断。
// includeHealth=false（running 期）只评失败率；true（observing 期）两条都评。
func (s *DeliveryOrchestrator) evalAndTrip(rt *orderRuntime, batch *model.ChangeBatch, members []*model.ChangeTarget, includeHealth bool) bool {
	if reason, tripped := s.evalFailureRate(rt.order, batch, members); tripped {
		s.tripBreaker(rt, batch, reason)
		return true
	}
	if includeHealth {
		if reason, tripped := s.evalHealthDegradation(rt.order, members); tripped {
			s.tripBreaker(rt, batch, reason)
			return true
		}
	}
	return false
}

// evalFailureRate 失败率熔断：批内 failed/planned_count×100 ≥ 阈值（0=关闭）。
func (s *DeliveryOrchestrator) evalFailureRate(order *model.ChangeOrder, batch *model.ChangeBatch, members []*model.ChangeTarget) (string, bool) {
	threshold := order.FailureRateThresholdPercent
	if threshold <= 0 {
		return "", false
	}
	failed := countByStatus(members)[model.ChangeTargetStatusFailed]
	if pct(failed, batch.PlannedCount) >= threshold {
		return breakReason("失败率", failed, threshold, batch.PlannedCount), true
	}
	return "", false
}

// evalHealthDegradation 健康恶化熔断：批内 activated 目标中健康等级 unhealthy 占比 ≥ 阈值（0=关闭）；健康读内存真源。
// restart 生效目标在重启预热宽限期内整体排除评估（不计分母 / 分子）——冷启动健康未定型，不参与健康恶化判定。
func (s *DeliveryOrchestrator) evalHealthDegradation(order *model.ChangeOrder, members []*model.ChangeTarget) (string, bool) {
	threshold := order.UnhealthyRateThresholdPercent
	if threshold <= 0 {
		return "", false
	}
	activated, unhealthy := 0, 0
	for _, t := range members {
		if t.Status != model.ChangeTargetStatusActivated {
			continue
		}
		if s.inRestartWarmup(order, t) {
			continue // 重启预热期内健康未定型，排除健康恶化评估
		}
		activated++
		if view, ok := s.health.Get(order.NamespaceID, t.ServerID); ok && view.Level == healthLevelUnhealthy {
			unhealthy++
		}
	}
	if activated > 0 && pct(unhealthy, activated) >= threshold {
		return breakReason("健康恶化", unhealthy, threshold, activated), true
	}
	return "", false
}

// inRestartWarmup 判目标是否处于 restart 重启预热期（activated 后 restartHealthWarmup 内）。
// restart 生效必然重启服务器，冷启动期间健康评分未预热（关服断供 lost → 恢复后样本不足的低分），
// 健康状态未定型；此期间把该目标排除出健康恶化熔断评估，避免重启固有的短暂不健康被误判为生效导致的健康恶化。
// 仅 restart 适用——push_only/hot_reload 不重启服务器，无冷启动预热期，activated 即处稳定运行态。
func (s *DeliveryOrchestrator) inRestartWarmup(order *model.ChangeOrder, t *model.ChangeTarget) bool {
	if order.ActivationMethod != model.ActivationMethodRestart || t.ActivatedAt == nil {
		return false
	}
	return s.now().Sub(*t.ActivatedAt) < restartHealthWarmup
}

// tripBreaker 执行熔断（系统动作，spec §4.4.4）：批 failed + 单 paused(circuit_break) + 批内未下发目标 skipped + 系统审计。
func (s *DeliveryOrchestrator) tripBreaker(rt *orderRuntime, batch *model.ChangeBatch, reason string) {
	now := s.now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		if _, e := repoTx.UpdateBatchCAS(batch.ID,
			[]string{model.ChangeBatchStatusRunning, model.ChangeBatchStatusObserving},
			map[string]any{"status": model.ChangeBatchStatusFailed, "break_reason": reason, "finished_at": now}); e != nil {
			return e
		}
		if _, e := repoTx.UpdateStatusCAS(rt.order.ID, []string{model.ChangeOrderStatusRolling}, map[string]any{
			"status": model.ChangeOrderStatusPaused, "pause_kind": model.PauseKindCircuitBreak, "pause_reason": reason,
		}); e != nil {
			return e
		}
		if _, e := repoTx.BulkUpdateTargetStatusByBatch(batch.ID, []string{model.ChangeTargetStatusPending},
			map[string]any{"status": model.ChangeTargetStatusSkipped}); e != nil {
			return e
		}
		return s.writeOrchestratorAudit(tx, rt.nsCode, "system", "", model.ActionDeliveryOrderCircuitBreak, rt.order.ID,
			map[string]any{"orderId": rt.order.ID, "batchNo": batch.BatchNo, "reason": reason})
	})
	if err != nil {
		slog.Error("交付编排熔断落库失败", "orderId", rt.order.ID, "batchNo", batch.BatchNo, "错误", err)
		return
	}
	batch.Status = model.ChangeBatchStatusFailed
	batch.BreakReason = reason
	rt.order.Status = model.ChangeOrderStatusPaused
	slog.Warn("交付编排触发自动熔断", "orderId", rt.order.ID, "batchNo", batch.BatchNo, "原因", reason)
	s.emitBatchEvent(rt, batch)
	s.emitOrderEvent(rt)
}

// —— CAS 迁移 + 事件封装（就地更新快照，成功才发事件）——

// casTarget 事务外单表 CAS 迁移目标状态并就地更新快照，成功发目标事件、返回是否命中。
func (s *DeliveryOrchestrator) casTarget(rt *orderRuntime, t *model.ChangeTarget, from []string, to string, updates map[string]any) bool {
	full := map[string]any{"status": to}
	for k, v := range updates {
		full[k] = v
	}
	ok, err := s.repo.UpdateTargetCAS(t.ID, from, full)
	if err != nil {
		slog.Error("交付编排目标状态迁移失败", "targetId", t.ID, "to", to, "错误", err)
		return false
	}
	if !ok {
		return false
	}
	t.Status = to
	// 就地同步 activated_at 到内存快照（同 casBatch 处理 observe_started_at）：
	// restart 预热宽限判定读 t.ActivatedAt，若只写库不更新内存则恒为 nil、宽限失效。
	if v, has := updates["activated_at"]; has {
		if ts, okc := v.(time.Time); okc {
			t.ActivatedAt = &ts
		}
	}
	s.emitTargetEvent(rt, t)
	return true
}

// failTarget 把目标从 from 迁 failed 并落脱敏原因（ADR-0057），返回是否命中。
func (s *DeliveryOrchestrator) failTarget(rt *orderRuntime, t *model.ChangeTarget, from, reason string) bool {
	return s.casTarget(rt, t, []string{from}, model.ChangeTargetStatusFailed, map[string]any{"error": reason})
}

// casBatch 事务外单表 CAS 迁移批状态并就地更新快照，成功发批事件、返回是否命中。
func (s *DeliveryOrchestrator) casBatch(rt *orderRuntime, batch *model.ChangeBatch, from []string, to string, updates map[string]any) bool {
	full := map[string]any{"status": to}
	for k, v := range updates {
		full[k] = v
	}
	ok, err := s.repo.UpdateBatchCAS(batch.ID, from, full)
	if err != nil {
		slog.Error("交付编排批状态迁移失败", "batchId", batch.ID, "to", to, "错误", err)
		return false
	}
	if !ok {
		return false
	}
	batch.Status = to
	if v, has := updates["observe_started_at"]; has {
		if ts, okc := v.(time.Time); okc {
			batch.ObserveStartedAt = &ts
		}
	}
	s.emitBatchEvent(rt, batch)
	return true
}

// —— 小工具 ——

// firstBatch 取批次序号最小的批（首批）。
func firstBatch(batches []model.ChangeBatch) *model.ChangeBatch {
	var first *model.ChangeBatch
	for i := range batches {
		if first == nil || batches[i].BatchNo < first.BatchNo {
			first = &batches[i]
		}
	}
	return first
}

// targetErrorOr 回执含脱敏原因则用之，否则用兜底文案。
func targetErrorOr(reported, fallback string) string {
	if reported != "" {
		return reported
	}
	return fallback
}
