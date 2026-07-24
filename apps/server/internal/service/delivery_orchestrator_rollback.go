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
func (s *DeliveryOrchestrator) advanceRollingBack(rt *orderRuntime) {
	pending, running, terminal, failed := 0, 0, 0, 0
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
		}
	}
	// 无在途且有回滚目标：全 rolled_back 自动完成整单；有 failed 则停待人工 FinishRollback。
	if pending == 0 && running == 0 && failed == 0 && terminal > 0 {
		s.autoFinishRollback(rt)
	}
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

// autoFinishRollback 全回滚目标 rolled_back 时自动收单（rolling_back→rolled_back + 系统审计）；有 failed 不走此路径（待人工）。
func (s *DeliveryOrchestrator) autoFinishRollback(rt *orderRuntime) {
	now := s.now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.repo.WithTx(tx).UpdateStatusCAS(rt.order.ID, []string{model.ChangeOrderStatusRollingBack},
			map[string]any{"status": model.ChangeOrderStatusRolledBack, "finished_at": now})
		if e != nil || !ok {
			return errOrSkip(e, ok)
		}
		return s.writeOrchestratorAudit(tx, rt.nsCode, "system", "", model.ActionDeliveryOrderRollbackFinish, rt.order.ID,
			map[string]any{"orderId": rt.order.ID, "auto": true})
	})
	if err != nil {
		if err != errCASSkip {
			slog.Error("交付编排回滚自动完成失败", "orderId", rt.order.ID, "错误", err)
		}
		return
	}
	rt.order.Status = model.ChangeOrderStatusRolledBack
	s.emitOrderEvent(rt)
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
