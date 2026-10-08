package service

import (
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// advanceRollingBack 推进 rolling_back 单（FR-167，spec §4.7.2）：一次性全量回滚（无批次）——
// 逐目标按 rollback_status 推进：pending 下发 delivery_rollback 令 agent 还原备份、running 判回执终态
// （restart 生效方式还原后 agent 关服，复用心跳回归判定）。全回滚目标终态且无 failed→单自动 rolled_back，
// 有 failed→停在 rolling_back 待人工 FinishRollback。
//
// rollback_status 为空的目标按「是否曾覆盖磁盘」穷尽归类（FR-262r）：从未覆盖 = 非回滚目标（显式计数）、
// 曾覆盖 = 就地补回滚初态纳入本次推进。空值不落任何分支会让目标被静默丢弃，一旦单内回滚目标全部为空，
// 自动收单条件 `terminal > 0` 永假、单永久停在 rolling_back。
func (s *DeliveryOrchestrator) advanceRollingBack(rt *orderRuntime) {
	pending, running, terminal, failed, notApplicable := 0, 0, 0, 0, 0
	for i := range rt.targets {
		t := &rt.targets[i]
		switch t.RollbackStatus {
		case model.RollbackStatusPending:
			s.dispatchRollback(rt, t)
			pending++
		case model.RollbackStatusRunning:
			s.reconcileRollback(rt, t)
			switch t.RollbackStatus {
			case model.RollbackStatusRolledBack:
				terminal++
			case model.RollbackStatusFailed:
				failed++
			default:
				running++
			}
		case model.RollbackStatusFailed:
			failed++
		case model.RollbackStatusRolledBack:
			terminal++
		default:
			// 回滚态为空：从未覆盖磁盘 → 无文件可回滚；曾覆盖 → 补初态后立即纳入本次推进。
			if t.PushedAt == nil {
				notApplicable++
				continue
			}
			switch s.initTargetRollback(rt, t) {
			case rollbackInitPending:
				s.dispatchRollback(rt, t)
				pending++
			case rollbackInitFailed:
				failed++
			default:
				// 补初态未命中（库内状态已被他处改写）：本 tick 按在途处理，下 tick 按库内真值重判。
				pending++
			}
		}
	}
	// 无在途且有回滚目标：全 rolled_back 自动完成整单；有 failed 则停待人工 FinishRollback。
	// 全部目标都是非回滚目标（无实际回滚工作）时同样收口，不再永久停留。
	if pending == 0 && running == 0 && failed == 0 && (terminal > 0 || notApplicable > 0) {
		s.autoFinishRollback(rt, notApplicable)
	}
}

// rollbackInitResult 是回滚初态自愈结果（FR-262r）。
type rollbackInitResult int

const (
	// rollbackInitCASMiss 补初态未命中：库内回滚态非空，交由下一 tick 按库内真值重判
	rollbackInitCASMiss rollbackInitResult = iota
	// rollbackInitPending 已补为 pending（有备份，可实际下发还原）
	rollbackInitPending
	// rollbackInitFailed 已补为 failed（无备份，无法文件回滚）
	rollbackInitFailed
)

// initTargetRollback 为曾覆盖磁盘但回滚态为空的目标就地补回滚初态并更新快照（FR-262r），
// 返回补态结果；无备份目标直接判 failed（与整单回滚预检 §4.7.2 step1 同口径）。
func (s *DeliveryOrchestrator) initTargetRollback(rt *orderRuntime, t *model.ChangeTarget) rollbackInitResult {
	ok, err := s.repo.InitTargetRollbackIfEmpty(t.ID, t.BackupPresent, rollbackBackupMissingReason)
	if err != nil {
		slog.Error("交付编排补目标回滚初态失败", "targetId", t.ID, "错误", err)
		return rollbackInitCASMiss
	}
	if !ok {
		return rollbackInitCASMiss
	}
	if !t.BackupPresent {
		t.RollbackStatus = model.RollbackStatusFailed
		t.RollbackError = rollbackBackupMissingReason
		s.emitTargetEvent(rt, t)
		return rollbackInitFailed
	}
	t.RollbackStatus = model.RollbackStatusPending
	t.RollbackError = ""
	s.emitTargetEvent(rt, t)
	return rollbackInitPending
}

// dispatchRollback 下发回滚命令（rollback_status pending→running + delivery_rollback 命令，一事务原子），提交后唤醒 agent。
func (s *DeliveryOrchestrator) dispatchRollback(rt *orderRuntime, t *model.ChangeTarget) {
	payload := deliveryActivatePayload{OrderID: rt.order.ID, ActivationMethod: rt.order.ActivationMethod}
	cmd := newDeliveryCommand(rt.nsCode, t.ServerID, model.CommandTypeDeliveryRollback, payload)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.repo.WithTx(tx).UpdateTargetRollbackCAS(t.ID, []string{model.RollbackStatusPending},
			map[string]any{"rollback_status": model.RollbackStatusRunning})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		return s.cmdRepo.WithTx(tx).Create(cmd)
	})
	if err != nil {
		if err != errCASSkip {
			slog.Error("交付编排下发回滚命令失败", "orderId", rt.order.ID, "serverId", t.ServerID, "错误", err)
		}
		return
	}
	t.RollbackStatus = model.RollbackStatusRunning
	s.notifyAgent(rt.nsCode, t.ServerID)
	s.emitTargetEvent(rt, t)
}

// reconcileRollback 判回滚命令终态：done→按生效方式收口（push_only 直接 rolled_back / restart 心跳回归）；
// failed/expired/超时→rollback_status=failed（脱敏原因）。
func (s *DeliveryOrchestrator) reconcileRollback(rt *orderRuntime, t *model.ChangeTarget) {
	cmd, err := s.latestDeliveryCommand(rt.nsCode, t.ServerID, model.CommandTypeDeliveryRollback, rt.order.ID)
	if err != nil || cmd == nil {
		return
	}
	switch cmd.Status {
	case model.CommandStatusDone:
		s.completeRollback(rt, t)
	case model.CommandStatusFailed:
		s.failRollback(rt, t, targetErrorOr(parseDeliveryCmdResult(cmd.ResultDetail).Error, "回滚失败"))
	case model.CommandStatusExpired:
		s.failRollback(rt, t, "回滚命令过期（agent 离线或长时间未回执）")
	default:
		s.rollbackOnTimeout(rt, t, cmd)
	}
}

// completeRollback 处理回滚命令 done（agent 已完成对应回滚动作）：
//   - push_only：备份还原完成后直接 rolled_back，随目标下次自然重启读盘。
//   - hot_reload：备份还原与配置变更回调均成功后直接 rolled_back。
//   - restart：agent 还原后已 gracefulShutdown，须重置回滚重启锚点（首次 done、锚点尚为正推旧值）后判心跳回归；
//     心跳回归→rolled_back，activate_timeout 内未回归→failed（「关了没起来」，与正推 restart 同构）。
func (s *DeliveryOrchestrator) completeRollback(rt *orderRuntime, t *model.ChangeTarget) {
	if rt.order.ActivationMethod != model.ActivationMethodRestart {
		s.casRollback(rt, t, model.RollbackStatusRolledBack, nil)
		return
	}
	// 首次读到 done 时锚点仍为正推旧值（早于回滚触发）→ 重置为 now 作回滚重启心跳锚点，等新心跳回归。
	if !s.rollbackAnchorReset(rt.order, t) {
		now := s.now()
		if s.casRollbackAnchor(t, now) {
			t.ActivatingStartedAt = &now
		}
		return
	}
	if s.heartbeatReturned(rt.order.NamespaceID, t.ServerID, t.ActivatingStartedAt) {
		s.casRollback(rt, t, model.RollbackStatusRolledBack, nil)
		return
	}
	s.rollbackRestartTimeout(rt, t)
}

// rollbackAnchorReset 判目标回滚重启心跳锚点是否已重置（activating_started_at 已 ≥ 回滚触发时刻）：
// 正推遗留的锚点早于回滚触发时刻，据此区分「首次 done 需重置锚点」与「已重置、等心跳回归」。
func (s *DeliveryOrchestrator) rollbackAnchorReset(order *model.ChangeOrder, t *model.ChangeTarget) bool {
	if t.ActivatingStartedAt == nil {
		return false
	}
	if order.RollbackAt == nil {
		return true // 回滚触发时刻缺失（异常）：保守认为已重置，避免重置死循环
	}
	return !t.ActivatingStartedAt.Before(*order.RollbackAt)
}

// rollbackOnTimeout 回滚命令仍在途（pending/fetched）时按 activateTimeoutSec 判超时：超时置 failed 并尽力过期命令。
func (s *DeliveryOrchestrator) rollbackOnTimeout(rt *orderRuntime, t *model.ChangeTarget, cmd *model.AgentCommand) {
	timeout := time.Duration(rt.order.ActivateTimeoutSec) * time.Second
	if s.now().Sub(cmd.CreatedAt) < timeout {
		return
	}
	if s.failRollback(rt, t, "回滚超时（agent 离线或未在超时内回执）") {
		if _, e := s.cmdRepo.UpdateStatus(cmd.ID, cmd.Status, model.CommandStatusExpired, ""); e != nil {
			slog.Warn("交付编排回滚超时置命令过期失败", "commandId", cmd.ID, "错误", e)
		}
	}
}

// rollbackRestartTimeout restart 回滚重启超时：从回滚锚点计满 activate_timeout_sec 仍未心跳回归→failed。
func (s *DeliveryOrchestrator) rollbackRestartTimeout(rt *orderRuntime, t *model.ChangeTarget) {
	timeout := time.Duration(rt.order.ActivateTimeoutSec) * time.Second
	if t.ActivatingStartedAt != nil && s.now().Sub(*t.ActivatingStartedAt) < timeout {
		return
	}
	s.failRollback(rt, t, "回滚重启后 activateTimeoutSec 内心跳未回归（宿主未拉起进程或启动过慢）")
}

// autoFinishRollback 无在途回滚目标时自动收单（rolling_back→rolled_back + 系统审计）；有 failed 不走此路径（待人工）。
// notApplicable 为「从未覆盖磁盘、无文件可回滚」的目标数，记入审计便于区分「全回滚成功」与「本就无回滚工作」。
//
// 收单即走统一终态释放出口（FR-265）：单已终态就不再会被推进器装载，缓冲留着只会随单累积。
// 本组最初的断言「自动收单是唯一不经释放的终态出口」并不成立——人工「结束回滚」
// 同样不释放；故改为在所有终态出口统一调 releaseTerminalMemory。
func (s *DeliveryOrchestrator) autoFinishRollback(rt *orderRuntime, notApplicable int) {
	now := s.now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.repo.WithTx(tx).UpdateStatusCAS(rt.order.ID, []string{model.ChangeOrderStatusRollingBack},
			map[string]any{"status": model.ChangeOrderStatusRolledBack, "finished_at": now})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		return s.writeOrchestratorAudit(tx, rt.nsCode, "system", "", model.ActionDeliveryOrderRollbackFinish, rt.order.ID,
			map[string]any{"orderId": rt.order.ID, "auto": true, "notApplicableCount": notApplicable})
	})
	if err != nil {
		if err != errCASSkip {
			slog.Error("交付编排回滚自动完成失败", "orderId", rt.order.ID, "错误", err)
		}
		return
	}
	rt.order.Status = model.ChangeOrderStatusRolledBack
	// 自动收单即单终态化：走统一释放出口（FR-265）。
	s.releaseTerminalMemory(rt.order.ID)
	s.emitOrderEvent(rt)
}

// advanceTargetRollbacks 推进「目标级（子集）回滚」目标（FR-270）：子集回滚**不改单主状态**，
// 故这些单不会被按状态筛选的活动单列表选中，必须单独扫描——按「目标是否处于回滚推进态」定位单，
// 逐台复用整单回滚同一套 dispatchRollback / reconcileRollback（同一命令类型、同一心跳回归判定）。
// 与整单回滚的互斥：扫描排除 rolling_back 单（见仓库查询），同一目标不会被两条路径同时下发。
// 不做自动收单——单可能长期停在 completed 而个别目标在回滚，收口是目标级的事，记录里逐台可见。
func (s *DeliveryOrchestrator) advanceTargetRollbacks() {
	orders, err := s.repo.ListOrdersWithPendingTargetRollback()
	if err != nil {
		slog.Error("交付编排装载目标级回滚单失败", "错误", err)
		return
	}
	for i := range orders {
		rt, e := s.loadOrderRuntime(&orders[i])
		if e != nil {
			slog.Error("交付编排装载目标级回滚单快照失败", "orderId", orders[i].ID, "错误", e)
			continue
		}
		for j := range rt.targets {
			t := &rt.targets[j]
			switch t.RollbackStatus {
			case model.RollbackStatusPending:
				s.dispatchRollback(rt, t)
			case model.RollbackStatusRunning:
				s.reconcileRollback(rt, t)
			}
		}
	}
}

// —— 回滚动作记录（FR-270 / FR-271，spec delivery-rollback-resilience §3.1 / §3.6）——

// rollbackRecordTargetRows 由目标快照生成逐台结果行：初始结果取目标当时的真实回滚态
// （备份缺失在入态时已是 failed，故无需二次回填初始结果）。
func rollbackRecordTargetRows(targets []*model.ChangeTarget) []model.ChangeRollbackRecordTarget {
	rows := make([]model.ChangeRollbackRecordTarget, 0, len(targets))
	for _, t := range targets {
		rows = append(rows, model.ChangeRollbackRecordTarget{
			ServerID: t.ServerID, Result: t.RollbackStatus, Error: t.RollbackError,
		})
	}
	return rows
}

// recordRollbackTargetResult 把某台目标的终态结果写回**它所属那次回滚动作**的逐台行（FR-271）。
// 归属定位见仓库方法注释；本写回在推进事务**之外**执行（推进本身走 CAS 逐台提交，不为留痕改写事务边界），
// 故写失败不阻断推进（记录是留痕，不是推进前置条件），仅告警——否则一次记录写失败会卡住真实回滚。
func (s *DeliveryOrchestrator) recordRollbackTargetResult(orderID uint, serverID, result, reason string) {
	if err := s.repo.UpdateRollbackRecordTargetResult(orderID, serverID, result, reason); err != nil {
		slog.Warn("交付编排写回滚动作逐台结果失败", "orderId", orderID, "serverId", serverID, "错误", err)
	}
}

// —— 回滚状态 CAS（rollback_status 独立于主状态，就地更新快照）——

// casRollback 事务外 CAS 迁移目标 rollback_status（从 running）并就地更新快照，成功发目标事件、返回是否命中。
func (s *DeliveryOrchestrator) casRollback(rt *orderRuntime, t *model.ChangeTarget, to string, extra map[string]any) bool {
	updates := map[string]any{"rollback_status": to}
	for k, v := range extra {
		updates[k] = v
	}
	ok, err := s.repo.UpdateTargetRollbackCAS(t.ID, []string{model.RollbackStatusRunning}, updates)
	if err != nil {
		slog.Error("交付编排回滚状态迁移失败", "targetId", t.ID, "to", to, "错误", err)
		return false
	}
	if !ok {
		return false
	}
	t.RollbackStatus = to
	reason := ""
	if raw, has := extra["rollback_error"]; has {
		reason, _ = raw.(string)
	}
	t.RollbackError = reason
	// 逐台结果写回本次动作记录：状态墙看「现在」，记录看「每次动作各自的结果」（FR-271）。
	s.recordRollbackTargetResult(rt.order.ID, t.ServerID, to, reason)
	s.emitTargetEvent(rt, t)
	return true
}

// failRollback 把回滚中目标置 failed 并落脱敏原因（ADR-0057），返回是否命中。
func (s *DeliveryOrchestrator) failRollback(rt *orderRuntime, t *model.ChangeTarget, reason string) bool {
	return s.casRollback(rt, t, model.RollbackStatusFailed, map[string]any{"rollback_error": reason})
}

// casRollbackAnchor 重置回滚重启心跳锚点（activating_started_at=now，rollback_status 保持 running）：命中返回 true。
func (s *DeliveryOrchestrator) casRollbackAnchor(t *model.ChangeTarget, now time.Time) bool {
	ok, err := s.repo.UpdateTargetRollbackCAS(t.ID, []string{model.RollbackStatusRunning},
		map[string]any{"activating_started_at": now})
	if err != nil {
		slog.Error("交付编排重置回滚重启锚点失败", "targetId", t.ID, "错误", err)
		return false
	}
	return ok
}
