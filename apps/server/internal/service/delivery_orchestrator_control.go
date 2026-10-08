package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// resumeModeRetryFailed / resumeModeSkipFailed 熔断 / 准备失败暂停的继续模式（spec §4.4.5；对齐前端 ResumeBody.mode）。
const (
	resumeModeRetryFailed = "retry_failed"
	resumeModeSkipFailed  = "skip_failed"
)

// rollbackBackupMissingReason 备份缺失目标的回滚失败原因（脱敏，spec §4.7.2 step1）。
const rollbackBackupMissingReason = "覆盖前备份不存在，无法文件回滚"

// rollbackInFlightError 已在回滚中（pending / running）的目标被再次发起回滚时的拒绝错误。
// 必须显式拒绝而不是把目标打回 pending：置初态会与在途命令叠加，同一台被两次下发（二次覆盖磁盘）。
func rollbackInFlightError(serverID string) error {
	return apperr.New(http.StatusConflict, "rollback_in_progress",
		fmt.Sprintf("目标 %s 已在回滚中，等它到终态后再发起", serverID))
}

// firstInFlightRollbackTarget 返回 targets 中第一个仍在途（pending / running）的回滚目标；无则 nil。
func firstInFlightRollbackTarget(targets []model.ChangeTarget) *model.ChangeTarget {
	for i := range targets {
		switch targets[i].RollbackStatus {
		case model.RollbackStatusPending, model.RollbackStatusRunning:
			return &targets[i]
		}
	}
	return nil
}

// rollbackTargetLimit 单次目标级回滚可选的 serverId 上限（对齐本域目标量级，防一次请求无界放大）。
const rollbackTargetLimit = 1000

// deliveredVersionQueryLimit 交付版本批量查询的 serverId 上限（列表接口一律有界，FR-271）。
const deliveredVersionQueryLimit = 100

// serverIDScopeSpec 是 serverId 集合归一化的**场景文案**：同一个归一逻辑服务两类用途，拒绝文案必须各自贴合——
// 把只读查询的报错写成「回滚」会让调用方以为自己触发了写操作，是误导性错误（ADR-0057 要求错误可读且不误导）。
type serverIDScopeSpec struct {
	emptyCode, emptyMessage string
	limitCode, limitMessage string
}

// rollbackTargetScope 写路径（目标级回滚）的文案。
var rollbackTargetScope = serverIDScopeSpec{
	emptyCode: "missing_targets", emptyMessage: "必须至少选择一个目标",
	limitCode: "too_many_targets", limitMessage: "一次最多回滚 %d 台目标",
}

// deliveredVersionScope 只读查询（交付版本批量查询）的文案。
var deliveredVersionScope = serverIDScopeSpec{
	emptyCode: "missing_server_ids", emptyMessage: "必须至少提供一个 serverId",
	limitCode: "too_many_server_ids", limitMessage: "一次最多查询 %d 台服务器",
}

// normalizeServerIDs 归一 serverId 集合：去空白、去重、保序；空集与超限一律拒绝（不部分执行）。
func normalizeServerIDs(serverIDs []string, limit int, spec serverIDScopeSpec) ([]string, error) {
	unique := make([]string, 0, len(serverIDs))
	seen := make(map[string]struct{}, len(serverIDs))
	for _, raw := range serverIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil, apperr.New(http.StatusBadRequest, spec.emptyCode, spec.emptyMessage)
	}
	if limit > 0 && len(unique) > limit {
		return nil, apperr.New(http.StatusBadRequest, spec.limitCode, fmt.Sprintf(spec.limitMessage, limit))
	}
	return unique, nil
}

// Pause 人工暂停（POST .../pause，spec §4.4.5）：rolling→paused(manual)，不打断在途目标（推进器继续收口在途到终态）。
func (s *DeliveryOrchestrator) Pause(id uint, operator, clientIP string) (*ChangeOrderDetailView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusRolling {
		return nil, changeIllegalState(order.Status, "暂停")
	}
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.repo.WithTx(tx).UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusRolling},
			map[string]any{"status": model.ChangeOrderStatusPaused, "pause_kind": model.PauseKindManual, "pause_reason": ""})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderPause, order.ID,
			map[string]any{"orderId": order.ID})
	})
	if err != nil {
		return nil, mapCASConflict(err, order.Status, "暂停")
	}
	return s.detailView(order.ID)
}

// Resume 禁止旧公开继续入口，防止调用方绕过统一审批 worker 扩大灰度影响。
func (s *DeliveryOrchestrator) Resume(_ uint, _, _, _, _ string) (*ChangeOrderDetailView, error) {
	return nil, apperr.ErrForbidden
}

// applyResume 供同包测试复用；生产执行必须经 applyResumeInTx。
func (s *DeliveryOrchestrator) applyResume(id uint, mode, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusPaused {
		return nil, changeIllegalState(order.Status, "继续")
	}
	if err := validateResumeArgs(order.PauseKind, mode, reason); err != nil {
		return nil, err
	}
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	notifySource := order.PauseKind == model.PauseKindPrepareFailed
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return s.applyResumeInTx(tx, order, nsCode, mode, reason, operator, clientIP)
	}); err != nil {
		return nil, err
	}
	if notifySource {
		s.notifyAgent(nsCode, order.SourceServerID) // 重试准备的新上传命令：唤醒模板源拉取
	}
	s.wake() // 恢复后立即推进（重推 / 重试准备 / 继续下发）
	return s.detailView(order.ID)
}

// validateResumeArgs 校验继续入参：熔断需 mode + 原因；准备失败需原因；人工暂停无需。
func validateResumeArgs(pauseKind, mode, reason string) error {
	switch pauseKind {
	case model.PauseKindCircuitBreak:
		if strings.TrimSpace(reason) == "" || (mode != resumeModeRetryFailed && mode != resumeModeSkipFailed) {
			return apperr.ErrChangeResumeModeRequired
		}
	case model.PauseKindPrepareFailed:
		if strings.TrimSpace(reason) == "" {
			return apperr.ErrChangeResumeModeRequired
		}
	}
	return nil
}

// applyResume 按暂停来源执行恢复：人工 / 准备失败 / 熔断（retry_failed / skip_failed）分别落库 + 审计。
func (s *DeliveryOrchestrator) applyResumeInTx(tx *gorm.DB, order *model.ChangeOrder, nsCode, mode, reason, operator, clientIP string) error {
	detail := map[string]any{"orderId": order.ID, "pauseKind": order.PauseKind}
	if mode != "" {
		detail["mode"] = mode
	}
	if strings.TrimSpace(reason) != "" {
		detail["reason"] = reason
	}
	repoTx := s.repo.WithTx(tx)
	if err := s.resumeBody(tx, repoTx, order, nsCode, mode); err != nil {
		return err
	}
	return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderResume, order.ID, detail)
}

// resumeBody 事务内按暂停来源迁移状态：清暂停字段回 rolling；准备失败重下上传命令 + 回 uploading；熔断按 mode 处置熔断批。
func (s *DeliveryOrchestrator) resumeBody(tx *gorm.DB, repoTx *repository.ChangeOrderRepository,
	order *model.ChangeOrder, nsCode, mode string) error {
	updates := map[string]any{"status": model.ChangeOrderStatusRolling, "pause_kind": "", "pause_reason": ""}
	switch order.PauseKind {
	case model.PauseKindPrepareFailed:
		// 重试 payload 准备：回 uploading + 新建一条 delivery_upload 命令令模板源重传（agent 内部单文件重试上限已耗尽才走到这里）。
		updates["payload_state"] = model.PayloadStateUploading
		cmd := newDeliveryCommand(nsCode, order.SourceServerID, model.CommandTypeDeliveryUpload,
			deliveryUploadPayload{OrderID: order.ID})
		if e := s.cmdRepo.WithTx(tx).Create(cmd); e != nil {
			return e
		}
	case model.PauseKindCircuitBreak:
		if e := s.resumeCircuitBatch(repoTx, order, mode); e != nil {
			return e
		}
	}
	ok, err := repoTx.UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusPaused}, updates)
	if err != nil || !ok {
		return errOrSkip(err, ok)
	}
	return nil
}

// resumeCircuitBatch 恢复熔断批：retry_failed 把批回 running 并重置失败 / skipped 目标为 pending；skip_failed 批进推进门。
func (s *DeliveryOrchestrator) resumeCircuitBatch(repoTx *repository.ChangeOrderRepository, order *model.ChangeOrder, mode string) error {
	batch, err := s.findFailedBatch(repoTx, order.ID)
	if err != nil || batch == nil {
		return err
	}
	if mode == resumeModeSkipFailed {
		// skip_failed：保留失败记录，熔断批进推进门等待人工确认后放行下一批（spec §4.4.5，本域取「进推进门」口径）。
		_, e := repoTx.UpdateBatchCAS(batch.ID, []string{model.ChangeBatchStatusFailed},
			map[string]any{"status": model.ChangeBatchStatusAwaitingConfirm, "break_reason": ""})
		return e
	}
	// retry_failed：熔断批回 running，批内 failed / skipped 目标重置 pending 重推。
	if _, e := repoTx.BulkUpdateTargetStatusByBatch(batch.ID,
		[]string{model.ChangeTargetStatusFailed, model.ChangeTargetStatusSkipped},
		map[string]any{"status": model.ChangeTargetStatusPending, "error": "", "pushed_at": nil, "activated_at": nil}); e != nil {
		return e
	}
	_, e := repoTx.UpdateBatchCAS(batch.ID, []string{model.ChangeBatchStatusFailed},
		map[string]any{"status": model.ChangeBatchStatusRunning, "break_reason": "", "finished_at": nil})
	return e
}

// findFailedBatch 取单内唯一的熔断批（status=failed）；无则 (nil, nil)。
func (s *DeliveryOrchestrator) findFailedBatch(repoTx *repository.ChangeOrderRepository, orderID uint) (*model.ChangeBatch, error) {
	batches, err := repoTx.ListBatches(orderID)
	if err != nil {
		return nil, err
	}
	for i := range batches {
		if batches[i].Status == model.ChangeBatchStatusFailed {
			return &batches[i], nil
		}
	}
	return nil, nil
}

// Cancel 紧急终止（POST .../cancel，spec §4.1）：原因必填；rolling/paused→cancelled，
// 未开始批 / 目标置 skipped；在途推送尽力中止（不主动打断）、已进入生效的目标不中断。
func (s *DeliveryOrchestrator) Cancel(id uint, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.New(http.StatusBadRequest, "missing_reason", "紧急终止原因必填")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusRolling && order.Status != model.ChangeOrderStatusPaused {
		return nil, changeIllegalState(order.Status, "紧急终止")
	}
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		ok, e := repoTx.UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusRolling, model.ChangeOrderStatusPaused},
			map[string]any{"status": model.ChangeOrderStatusCancelled, "cancel_reason": reason, "finished_at": now})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		if _, e := repoTx.BulkUpdateTargetStatusByOrder(order.ID, []string{model.ChangeTargetStatusPending},
			map[string]any{"status": model.ChangeTargetStatusSkipped}); e != nil {
			return e
		}
		if _, e := repoTx.BulkUpdateBatchStatusByOrder(order.ID, []string{model.ChangeBatchStatusPending},
			map[string]any{"status": model.ChangeBatchStatusSkipped}); e != nil {
			return e
		}
		return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderCancel, order.ID,
			map[string]any{"orderId": order.ID, "reason": reason})
	})
	if err != nil {
		return nil, mapCASConflict(err, order.Status, "紧急终止")
	}
	s.clearObserve(order.ID)
	return s.detailView(order.ID)
}

// Rollback 禁止旧公开回滚入口，防止调用方绕过统一审批 worker 恢复已交付内容。
func (s *DeliveryOrchestrator) Rollback(_ uint, _, _, _ string) (*ChangeOrderDetailView, error) {
	return nil, apperr.ErrForbidden
}

// applyRollback 供同包测试复用；生产执行必须经 applyRollbackInTx。
// 整单回滚：原因必填；completed/paused/cancelled→rolling_back，
// 曾推送目标（pushed_at 非空）置回滚初态（备份缺失直接 failed）；首次进入做 config 版本回退记账（幂等）。
// 已 rolling_back 单再调 = 重试（spec §4.7.2「失败目标可重试」）：仅把 failed 目标重置 pending，不重做 config 回退（避免污染不可变链）。
func (s *DeliveryOrchestrator) applyRollback(id uint, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.New(http.StatusBadRequest, "missing_reason", "整单回滚原因必填")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return nil, err
	}
	// 重试语义：已在回滚中，仅重置失败目标重推（不重做 config 版本回退）。
	if order.Status == model.ChangeOrderStatusRollingBack {
		err := s.db.Transaction(func(tx *gorm.DB) error {
			return s.retryRollbackInTx(tx, order, reason, operator, clientIP)
		})
		if err != nil {
			return nil, err
		}
		s.wake()
		return s.detailView(order.ID)
	}
	if order.Status != model.ChangeOrderStatusCompleted && order.Status != model.ChangeOrderStatusPaused &&
		order.Status != model.ChangeOrderStatusCancelled {
		return nil, changeIllegalState(order.Status, "整单回滚")
	}
	n, err := s.repo.CountTargetsToRollback(order.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, apperr.ErrChangeNoRollbackTarget
	}
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		if e := s.rollbackConfigVersionsInTx(tx, order, reason, operator, clientIP); e != nil {
			return e
		}
		ok, e := repoTx.UpdateStatusCAS(order.ID,
			[]string{model.ChangeOrderStatusCompleted, model.ChangeOrderStatusPaused, model.ChangeOrderStatusCancelled},
			map[string]any{"status": model.ChangeOrderStatusRollingBack, "rollback_by": operator,
				"rollback_reason": reason, "rollback_at": now})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		if e := repoTx.InitTargetRollbackByOrder(order.ID, rollbackBackupMissingReason); e != nil {
			return e
		}
		rollbackTargets, e := rollbackTargetsAfterInitTx(repoTx, order.ID)
		if e != nil {
			return e
		}
		recordID, e := s.writeRollbackRecord(tx, order.ID, model.RollbackKindOrder, reason, operator, true, rollbackTargets)
		if e != nil {
			return e
		}
		return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderRollback, order.ID,
			map[string]any{"orderId": order.ID, "reason": reason, "kind": model.RollbackKindOrder,
				"targetCount": n, "configRolledBack": true, "recordId": recordID})
	})
	if err != nil {
		return nil, mapCASConflict(err, order.Status, "整单回滚")
	}
	s.wake()
	return s.detailView(order.ID)
}

// applyRollbackTargetsInTx 在审批执行事务内执行目标级（子集）回滚（FR-270，spec §3.3）：
// 只回滚选中目标的**文件**，配置版本不回退、单主状态不变；全选时回落整单回滚路径（「全选 = 整单回滚」）。
// 结果落在目标级 rollback_status / rollback_error 与一条 kind=targets 的动作记录上。
func (s *DeliveryOrchestrator) applyRollbackTargetsInTx(tx *gorm.DB, order *model.ChangeOrder,
	serverIDs []string, reason, operator, clientIP string) error {
	if tx == nil {
		return apperr.ErrInternal
	}
	if strings.TrimSpace(reason) == "" {
		return apperr.ErrApprovalReasonRequired
	}
	if order.Status != model.ChangeOrderStatusCompleted && order.Status != model.ChangeOrderStatusPaused &&
		order.Status != model.ChangeOrderStatusCancelled {
		return changeIllegalState(order.Status, "目标级回滚")
	}
	selected, err := normalizeServerIDs(serverIDs, rollbackTargetLimit, rollbackTargetScope)
	if err != nil {
		return err
	}
	repoTx := s.repo.WithTx(tx)
	targets, err := repoTx.ListTargetsByOrder(order.ID)
	if err != nil {
		return err
	}
	// 回滚目标集 = 曾覆盖磁盘的目标（spec §4.7.2）；选中集合必须完整落在其中，越界即整单拒绝（不部分执行）。
	eligible := make(map[string]*model.ChangeTarget, len(targets))
	for i := range targets {
		if targets[i].PushedAt != nil {
			eligible[targets[i].ServerID] = &targets[i]
		}
	}
	picked := make([]*model.ChangeTarget, 0, len(selected))
	for _, serverID := range selected {
		target, ok := eligible[serverID]
		if !ok {
			return apperr.New(http.StatusBadRequest, "invalid_rollback_target",
				fmt.Sprintf("目标 %s 不在本单可回滚目标内（未启动或从未推送）", serverID))
		}
		picked = append(picked, target)
	}
	// 在途目标（pending / running）拒绝再次置初态：否则会与在途命令叠加、同一台被两次下发。
	for _, t := range picked {
		if t.RollbackStatus == model.RollbackStatusPending || t.RollbackStatus == model.RollbackStatusRunning {
			return rollbackInFlightError(t.ServerID)
		}
	}
	// 全选等价整单回滚：覆盖全部可回滚目标时回落整单路径（含配置版本回退与单主状态迁移）。
	if len(picked) == len(eligible) {
		return s.applyRollbackInTx(tx, order, reason, operator, clientIP)
	}
	nsCode, err := changeNamespaceCode(tx, order.NamespaceID)
	if err != nil {
		return err
	}
	if err := repoTx.InitTargetRollbackByServerIDs(order.ID, selected, rollbackBackupMissingReason); err != nil {
		return err
	}
	// 就地同步快照：动作记录要落「每台在本次动作里的初始结果」，必须用更新后的真实状态。
	for _, t := range picked {
		t.RollbackStatus = model.RollbackStatusPending
		t.RollbackError = ""
		if !t.BackupPresent {
			t.RollbackStatus = model.RollbackStatusFailed
			t.RollbackError = rollbackBackupMissingReason
		}
	}
	recordID, err := s.writeRollbackRecord(tx, order.ID, model.RollbackKindTargets, reason, operator, false, picked)
	if err != nil {
		return err
	}
	return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderRollback, order.ID,
		map[string]any{"orderId": order.ID, "reason": reason, "kind": model.RollbackKindTargets,
			"targetCount": len(picked), "serverIds": selected, "configRolledBack": false, "recordId": recordID})
}

// writeRollbackRecord 在事务内落一条回滚动作记录 + 逐台结果行（FR-270 / FR-271），返回记录 ID。
// 与审计写在同一事务：记录是「发生过的动作」，审计是「谁做的」，两者必须同生共死，否则会互相指不到。
func (s *DeliveryOrchestrator) writeRollbackRecord(tx *gorm.DB, orderID uint, kind, reason, operator string,
	configRolledBack bool, targets []*model.ChangeTarget) (uint, error) {
	repoTx := s.repo.WithTx(tx)
	record := &model.ChangeRollbackRecord{
		OrderID: orderID, Kind: kind, Reason: reason, Operator: operator,
		ConfigRolledBack: configRolledBack, TargetCount: len(targets),
	}
	if err := repoTx.CreateRollbackRecord(record); err != nil {
		return 0, err
	}
	rows := rollbackRecordTargetRows(targets)
	for i := range rows {
		rows[i].RecordID = record.ID
	}
	if err := repoTx.CreateRollbackRecordTargets(rows); err != nil {
		return 0, err
	}
	return record.ID, nil
}

// retryRollbackInTx 在给定事务内执行回滚重试（spec §4.7.2「失败目标可重试」，FR-262r）：
// 只把单内 failed 目标重置 pending 重推——不重做配置版本回退（回退在不可变链上只发生一次，重做即污染链）、
// 不改单主状态、不刷新 rollback_at；每次重试写一条领域审计，使「谁在何时为何重试过、重置了几台」可追溯。
// 生产入口（统一审批执行适配器）与同包测试路径共用本函数，杜绝重试语义只存在于测试可达路径。
func (s *DeliveryOrchestrator) retryRollbackInTx(tx *gorm.DB, order *model.ChangeOrder, reason, operator, clientIP string) error {
	if tx == nil {
		return apperr.ErrInternal
	}
	nsCode, err := changeNamespaceCode(tx, order.NamespaceID)
	if err != nil {
		return err
	}
	repoTx := s.repo.WithTx(tx)
	// 先取「本次动作要重推的那几台」：重置之后再查就分不出哪些是本次动作的台了。
	targets, err := repoTx.ListTargetsByOrder(order.ID)
	if err != nil {
		return err
	}
	failedTargets := make([]*model.ChangeTarget, 0, len(targets))
	for i := range targets {
		if targets[i].RollbackStatus == model.RollbackStatusFailed {
			failedTargets = append(failedTargets, &targets[i])
		}
	}
	reset := int64(0)
	if len(failedTargets) == 0 {
		// 无失败目标即无事可做：显式拒绝，而不是静默落一条 targetCount=0 的空动作记录。
		return apperr.New(http.StatusBadRequest, "no_failed_rollback_target", "单内无回滚失败目标可重试")
	}
	reset, err = repoTx.ResetFailedRollbackToPending(order.ID)
	if err != nil {
		return err
	}
	for _, t := range failedTargets {
		t.RollbackStatus = model.RollbackStatusPending
		t.RollbackError = ""
	}
	// 重试恒不回退配置版本（回退在不可变链上只发生一次），故 configRolledBack 固定为假——界面据此明示「配置未回退」。
	recordID, err := s.writeRollbackRecord(tx, order.ID, model.RollbackKindOrder, reason, operator, false, failedTargets)
	if err != nil {
		return err
	}
	return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderRollback, order.ID,
		map[string]any{"orderId": order.ID, "reason": reason, "retry": true, "resetCount": reset,
			"kind": model.RollbackKindOrder, "configRolledBack": false, "recordId": recordID})
}

// applyRollbackInTx 在审批 worker 事务内执行整单回滚（首次进入）或回滚重试（已在 rolling_back）并写领域审计。
func (s *DeliveryOrchestrator) applyRollbackInTx(tx *gorm.DB, order *model.ChangeOrder, reason, operator, clientIP string) error {
	if tx == nil {
		return apperr.ErrInternal
	}
	if strings.TrimSpace(reason) == "" {
		return apperr.ErrApprovalReasonRequired
	}
	// 重试：已在回滚中的单只重置失败目标，不重做配置版本回退、不改单主状态。
	if order.Status == model.ChangeOrderStatusRollingBack {
		return s.retryRollbackInTx(tx, order, reason, operator, clientIP)
	}
	if order.Status != model.ChangeOrderStatusCompleted && order.Status != model.ChangeOrderStatusPaused &&
		order.Status != model.ChangeOrderStatusCancelled {
		return changeIllegalState(order.Status, "整单回滚")
	}
	repoTx := s.repo.WithTx(tx)
	n, err := repoTx.CountTargetsToRollback(order.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.ErrChangeNoRollbackTarget
	}
	// 与子集路径同口径：在途目标不得被整单回滚再次置初态（会与在途命令叠加、同一台被两次下发）。
	existing, err := repoTx.ListTargetsByOrder(order.ID)
	if err != nil {
		return err
	}
	if inFlight := firstInFlightRollbackTarget(existing); inFlight != nil {
		return rollbackInFlightError(inFlight.ServerID)
	}
	nsCode, err := changeNamespaceCode(tx, order.NamespaceID)
	if err != nil {
		return err
	}
	if err := s.rollbackConfigVersionsInTx(tx, order, reason, operator, clientIP); err != nil {
		return err
	}
	now := s.now()
	ok, err := repoTx.UpdateStatusCAS(order.ID,
		[]string{model.ChangeOrderStatusCompleted, model.ChangeOrderStatusPaused, model.ChangeOrderStatusCancelled},
		map[string]any{"status": model.ChangeOrderStatusRollingBack, "rollback_by": operator,
			"rollback_reason": reason, "rollback_at": now})
	if err != nil || !ok {
		return errOrSkip(err, ok)
	}
	if err := repoTx.InitTargetRollbackByOrder(order.ID, rollbackBackupMissingReason); err != nil {
		return err
	}
	rollbackTargets, err := rollbackTargetsAfterInitTx(repoTx, order.ID)
	if err != nil {
		return err
	}
	recordID, err := s.writeRollbackRecord(tx, order.ID, model.RollbackKindOrder, reason, operator, true, rollbackTargets)
	if err != nil {
		return err
	}
	return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderRollback, order.ID,
		map[string]any{"orderId": order.ID, "reason": reason, "kind": model.RollbackKindOrder,
			"targetCount": n, "configRolledBack": true, "recordId": recordID})
}

// rollbackTargetsAfterInitTx 取本单回滚目标（曾覆盖磁盘）的最新快照，供动作记录落逐台行。
// **必须在置回滚初态之后调用**——动作记录要落的是「每台在本次动作里的初始结果」，早于置态就全是空值。
func rollbackTargetsAfterInitTx(repoTx *repository.ChangeOrderRepository, orderID uint) ([]*model.ChangeTarget, error) {
	targets, err := repoTx.ListTargetsByOrder(orderID)
	if err != nil {
		return nil, err
	}
	rollbackTargets := make([]*model.ChangeTarget, 0, len(targets))
	for i := range targets {
		if targets[i].PushedAt != nil {
			rollbackTargets = append(rollbackTargets, &targets[i])
		}
	}
	return rollbackTargets, nil
}

// FinishRollback 禁止旧公开结束回滚入口，防止调用方绕过统一审批改变回滚终态。
func (s *DeliveryOrchestrator) FinishRollback(_ uint, _, _ string) (*ChangeOrderDetailView, error) {
	return nil, apperr.ErrForbidden
}

// applyFinishRollback 供同包测试复用；生产执行必须经 applyFinishRollbackInTx。
func (s *DeliveryOrchestrator) applyFinishRollback(id uint, operator, clientIP string) (*ChangeOrderDetailView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusRollingBack {
		return nil, changeIllegalState(order.Status, "结束回滚")
	}
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.repo.WithTx(tx).UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusRollingBack},
			map[string]any{"status": model.ChangeOrderStatusRolledBack, "finished_at": now})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderRollbackFinish, order.ID,
			map[string]any{"orderId": order.ID})
	})
	if err != nil {
		return nil, mapCASConflict(err, order.Status, "结束回滚")
	}
	return s.detailView(order.ID)
}

func (s *DeliveryOrchestrator) applyFinishRollbackInTx(tx *gorm.DB, order *model.ChangeOrder, operator, clientIP string) error {
	if tx == nil {
		return apperr.ErrInternal
	}
	if order.Status != model.ChangeOrderStatusRollingBack {
		// 状态不符走本文件既有的 illegal_state(409) 口径，与同函数族一致（此前误报 403 越权）。
		return changeIllegalState(order.Status, "结束回滚")
	}
	nsCode, err := changeNamespaceCode(tx, order.NamespaceID)
	if err != nil {
		return err
	}
	now := s.now()
	ok, err := s.repo.WithTx(tx).UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusRollingBack},
		map[string]any{"status": model.ChangeOrderStatusRolledBack, "finished_at": now})
	if err != nil || !ok {
		return errOrSkip(err, ok)
	}
	return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderRollbackFinish, order.ID,
		map[string]any{"orderId": order.ID})
}

// rollbackConfigVersionsInTx 逐 config_change 项在交付审批领域事务内做回退记账。
// from!=nil→RollbackVersion(from) 使 head 回 from；from==nil→RemoveScopeContribution 撤销该层贡献；
// 均幂等——撞 ErrConfigNoChange（head 已=from）或「无可撤销贡献」当成功（回滚重试 / 已对齐）。未装配 config 则跳过。
func (s *DeliveryOrchestrator) rollbackConfigVersionsInTx(tx *gorm.DB, order *model.ChangeOrder, reason, operator, clientIP string) error {
	if s.config == nil {
		return nil
	}
	config, ok := s.config.(interface {
		rollbackVersionInTx(*gorm.DB, uint, string, string, string) (*ConfigSaveResultView, error)
		removeScopeContributionInTx(*gorm.DB, uint, string, uint, string, string, string) (*ConfigRevokeResultView, error)
	})
	if !ok {
		return apperr.ErrForbidden
	}
	items, err := s.repo.WithTx(tx).ListItems(order.ID)
	if err != nil {
		return err
	}
	for i := range items {
		it := &items[i]
		if it.Kind != model.ChangeItemKindConfigChange {
			continue
		}
		if it.ConfigFromVersionID != nil {
			if _, e := config.rollbackVersionInTx(tx, *it.ConfigFromVersionID, reason, operator, clientIP); e != nil &&
				!errors.Is(e, apperr.ErrConfigNoChange) {
				return e
			}
			continue
		}
		fileID, e := s.configFileIDOf(tx, it)
		if e != nil {
			return e
		}
		if _, e := config.removeScopeContributionInTx(tx, fileID, derefString(it.ConfigScopeKind), derefUint(it.ConfigScopeID),
			reason, operator, clientIP); e != nil && !isConfigRollbackIdempotent(e) {
			return e
		}
	}
	return nil
}

// configFileIDOf 反查 config_change 项对应配置文件 id（经 to_version → 版本行 → configFileID）。
func (s *DeliveryOrchestrator) configFileIDOf(tx *gorm.DB, it *model.ChangeOrderItem) (uint, error) {
	if s.cfgVers == nil || it.ConfigToVersionID == nil {
		return 0, apperr.ErrChangeConfigVersionInvalid
	}
	v, err := s.cfgVers.WithTx(tx).FindByID(*it.ConfigToVersionID)
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, apperr.ErrChangeConfigVersionInvalid
	}
	return v.ConfigFileID, nil
}

// isConfigRollbackIdempotent 判 config 回退错误是否为可幂等吞掉的「已对齐」态（ADR-0071 决策6）：
// RollbackVersion 撞 ErrConfigNoChange（head 已=from）、RemoveScopeContribution 撞「无可撤销贡献」(INVALID_PARAM)。
func isConfigRollbackIdempotent(err error) bool {
	if errors.Is(err, apperr.ErrConfigNoChange) {
		return true
	}
	var ae *apperr.Error
	if errors.As(err, &ae) && ae.Code == "INVALID_PARAM" {
		return true
	}
	return false
}

// ConfirmBatch 禁止旧公开推进门入口，防止调用方绕过统一审批 worker 放量下一批。
func (s *DeliveryOrchestrator) ConfirmBatch(_ uint, _ int, _, _ string) (*ChangeOrderDetailView, error) {
	return nil, apperr.ErrForbidden
}

// applyConfirmBatch 供同包测试复用；生产执行必须经 applyConfirmBatchInTx。
func (s *DeliveryOrchestrator) applyConfirmBatch(id uint, batchNo int, operator, clientIP string) (*ChangeOrderDetailView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusRolling {
		return nil, changeIllegalState(order.Status, "批次确认")
	}
	batch, batches, err := s.loadConfirmBatch(order.ID, batchNo)
	if err != nil {
		return nil, err
	}
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	last := isLastBatch(batches, batchNo)
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return s.persistConfirmInTx(tx, order, batch, batches, last, nsCode, operator, clientIP)
	}); err != nil {
		return nil, err
	}
	s.clearObserve(order.ID)
	if !last {
		s.wake() // 下一批已置 running，唤醒推进器下发
	}
	return s.detailView(order.ID)
}

// loadConfirmBatch 取待确认批并校验其处 awaiting_confirm；返回该批与全批列表（末批判定用）。
func (s *DeliveryOrchestrator) loadConfirmBatch(orderID uint, batchNo int) (*model.ChangeBatch, []model.ChangeBatch, error) {
	return loadConfirmBatch(s.repo, orderID, batchNo)
}

func loadConfirmBatch(repo *repository.ChangeOrderRepository, orderID uint, batchNo int) (*model.ChangeBatch, []model.ChangeBatch, error) {
	batches, err := repo.ListBatches(orderID)
	if err != nil {
		return nil, nil, err
	}
	for i := range batches {
		if batches[i].BatchNo == batchNo {
			if batches[i].Status != model.ChangeBatchStatusAwaitingConfirm {
				return nil, nil, changeIllegalState(batches[i].Status, "批次确认")
			}
			return &batches[i], batches, nil
		}
	}
	return nil, nil, apperr.ErrChangeBatchNotFound
}

// persistConfirm 事务内落推进门确认：批 awaiting_confirm→completed；非末批启动下一批；末批则单 completed（含配置切版接缝）。
func (s *DeliveryOrchestrator) persistConfirmInTx(tx *gorm.DB, order *model.ChangeOrder, batch *model.ChangeBatch, batches []model.ChangeBatch,
	last bool, nsCode, operator, clientIP string) error {
	now := s.now()
	repoTx := s.repo.WithTx(tx)
	ok, e := repoTx.UpdateBatchCAS(batch.ID, []string{model.ChangeBatchStatusAwaitingConfirm},
		map[string]any{"status": model.ChangeBatchStatusCompleted, "gate_confirmed_by": operator,
			"gate_confirmed_at": now, "finished_at": now})
	if e != nil || !ok {
		return errOrSkip(e, ok)
	}
	if last {
		// 末批确认即单 completed。含 config_change 项的正式切版（ADR-0071 决策4）：单 completed 后
		// 「该作用域已交付版本 = to_version」由 FindLatestDeliveredToVersionID 从 completed 单历史反查、
		// 无需另写版本指针；pin 清除 = 单不再活动自然清（灰度渲染只在活动期按 to_version 冻结一次）。
		// 此处仅补一条配置切版审计供运维观测切了哪些作用域到哪个版本。
		if _, e := repoTx.UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusRolling},
			map[string]any{"status": model.ChangeOrderStatusCompleted, "finished_at": now}); e != nil {
			return e
		}
		if e := s.auditConfigSwitch(tx, repoTx, order, nsCode, operator, clientIP); e != nil {
			return e
		}
	} else if next := batchByNo(batches, batch.BatchNo+1); next != nil {
		if _, e := repoTx.UpdateBatchCAS(next.ID, []string{model.ChangeBatchStatusPending},
			map[string]any{"status": model.ChangeBatchStatusRunning, "started_at": now}); e != nil {
			return e
		}
	}
	return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderBatchConfirm, order.ID,
		map[string]any{"orderId": order.ID, "batchNo": batch.BatchNo, "last": last})
}

// auditConfigSwitch 末批确认后为含配置项单记一条配置正式切版审计（ADR-0071 决策4）：
// detail 列出各作用域的 from→to 版本指针（版本 id，绝不含配置明文，spec §4.8.2）；无配置项则空操作。
func (s *DeliveryOrchestrator) auditConfigSwitch(tx *gorm.DB, repoTx *repository.ChangeOrderRepository,
	order *model.ChangeOrder, nsCode, operator, clientIP string) error {
	items, err := repoTx.ListItems(order.ID)
	if err != nil {
		return err
	}
	switches := make([]map[string]any, 0)
	for i := range items {
		if items[i].Kind != model.ChangeItemKindConfigChange {
			continue
		}
		switches = append(switches, map[string]any{
			"scopeKind":     derefString(items[i].ConfigScopeKind),
			"scopeId":       items[i].ConfigScopeID,       // *uint（配置项非空）
			"fromVersionId": items[i].ConfigFromVersionID, // *uint，可空 → null（首次交付无锚点）
			"toVersionId":   items[i].ConfigToVersionID,   // *uint，正式切到的版本
		})
	}
	if len(switches) == 0 {
		return nil
	}
	return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderConfigSwitch, order.ID,
		map[string]any{"orderId": order.ID, "configSwitches": switches})
}

// —— 小工具 ——

// isLastBatch 判某批号是否为末批（批号最大）。
func isLastBatch(batches []model.ChangeBatch, batchNo int) bool {
	maxNo := 0
	for i := range batches {
		if batches[i].BatchNo > maxNo {
			maxNo = batches[i].BatchNo
		}
	}
	return batchNo == maxNo
}

// batchByNo 按批号取批（无则 nil）。
func batchByNo(batches []model.ChangeBatch, batchNo int) *model.ChangeBatch {
	for i := range batches {
		if batches[i].BatchNo == batchNo {
			return &batches[i]
		}
	}
	return nil
}

// mapCASConflict 把事务内 CAS 未命中的哨兵错误转成状态冲突（并发迁移），真错误原样返回。
func mapCASConflict(err error, current, action string) error {
	if errors.Is(err, errCASSkip) {
		return changeIllegalState(current, action)
	}
	return err
}
