package service

import (
	"errors"
	"net/http"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// 熔断 / 准备失败暂停的继续模式（spec §4.4.5；对齐前端 ResumeBody.mode）。
const (
	resumeModeRetryFailed = "retry_failed"
	resumeModeSkipFailed  = "skip_failed"
)

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

// Resume 继续暂停单（POST .../resume，spec §4.4.5）：manual 直接恢复；circuit_break / prepare_failed 需原因，
// circuit_break 还需 mode（retry_failed 重推熔断批失败 / skipped 目标；skip_failed 保留失败记录进推进门）。
func (s *DeliveryOrchestrator) Resume(id uint, mode, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
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
	if err := s.applyResume(order, nsCode, mode, reason, operator, clientIP); err != nil {
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
func (s *DeliveryOrchestrator) applyResume(order *model.ChangeOrder, nsCode, mode, reason, operator, clientIP string) error {
	detail := map[string]any{"orderId": order.ID, "pauseKind": order.PauseKind}
	if mode != "" {
		detail["mode"] = mode
	}
	if strings.TrimSpace(reason) != "" {
		detail["reason"] = reason
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		if err := s.resumeBody(tx, repoTx, order, nsCode, mode); err != nil {
			return err
		}
		return s.writeOrchestratorAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderResume, order.ID, detail)
	})
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

// ConfirmBatch 推进门放行（POST .../batches/{batchNo}/confirm，spec §4.4.3）：
// awaiting_confirm→completed，触发下一批 pending→running；末批确认即单 completed。
func (s *DeliveryOrchestrator) ConfirmBatch(id uint, batchNo int, operator, clientIP string) (*ChangeOrderDetailView, error) {
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
	if err := s.persistConfirm(order, batch, batches, last, nsCode, operator, clientIP); err != nil {
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
	batches, err := s.repo.ListBatches(orderID)
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
func (s *DeliveryOrchestrator) persistConfirm(order *model.ChangeOrder, batch *model.ChangeBatch, batches []model.ChangeBatch,
	last bool, nsCode, operator, clientIP string) error {
	now := s.now()
	return s.db.Transaction(func(tx *gorm.DB) error {
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
	})
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
