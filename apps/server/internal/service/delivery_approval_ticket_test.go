package service

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// newTicketApprovalHarness 建交付测试环境 + 真审批服务（迁移审批表并注册交付适配器），
// 供六类申请点的票据字段断言复用。
func newTicketApprovalHarness(t *testing.T) (*orchestratorHarness, *ApprovalService) {
	t.Helper()
	h := newOrchestratorHarness(t)
	if err := h.env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, h.env.orders, h.orch)
	approval := NewApprovalService(h.env.db, repository.NewApprovalRequestRepository(h.env.db),
		repository.NewAuditLogRepository(h.env.db), registry)
	h.env.orders.SetApprovalService(approval)
	h.orch.SetApprovalService(approval)
	return h, approval
}

// assertTicketImpact 断言票据带归属单号与创建时刻的影响摘要计数。
func assertTicketImpact(t *testing.T, ticket DeliveryApprovalTicketView, orderID uint, want DeliveryImpactSummaryView) {
	t.Helper()
	if ticket.ApprovalRequestID == "" || ticket.Status == "" || ticket.OperationKey == "" {
		t.Fatalf("票据三要素不应为空: %+v", ticket)
	}
	if ticket.OrderID != orderID {
		t.Fatalf("票据 orderId 应为 %d，实际 %d（票据=%+v）", orderID, ticket.OrderID, ticket)
	}
	if ticket.ImpactSummary != want {
		t.Fatalf("票据 impactSummary 应为 %+v，实际 %+v", want, ticket.ImpactSummary)
	}
}

// assertImpactSummaryNotFrozen 断言影响摘要没有写进冻结 payload：
// 票据是创建时刻的即时读数，冻结它会与后续推进漂移。
func assertImpactSummaryNotFrozen(t *testing.T, db *gorm.DB, requestID string) {
	t.Helper()
	var row model.ApprovalRequest
	if err := db.Where("request_id = ?", requestID).First(&row).Error; err != nil {
		t.Fatalf("读取审批行失败: %v", err)
	}
	for _, key := range []string{"impactSummary", "targetCount", "batchCount", "payloadFiles", "payloadConfigs"} {
		if strings.Contains(row.Payload, key) {
			t.Fatalf("冻结 payload 不应含影响摘要键 %s: %s", key, row.Payload)
		}
	}
}

// TestDeliveryApprovalTicketCarriesOrderIDAndImpactSummary 覆盖六类交付申请点（提交 / 删除 / 继续 /
// 批次确认 / 整单回滚 / 结束回滚）：每类票据都带 orderId 与创建时刻即时统计的 impactSummary。
// 计数为 0 时保留 0（不省略），且一律不进冻结 payload。
func TestDeliveryApprovalTicketCarriesOrderIDAndImpactSummary(t *testing.T) {
	// 提交：draft 阶段目标与批次尚未固化，故两项计数为 0；载荷按变更项种类分计。
	t.Run("提交", func(t *testing.T) {
		h, _ := newTicketApprovalHarness(t)
		order := createDraftOrder(t, h.f)
		seedFileItemWithBlob(t, h.env.db, order.ID, "plugins/demo.jar", repeatHex("cd", 64), 64)
		seedConfigItem(t, h.env.db, order.ID)
		ticket, err := h.env.orders.RequestSubmit(order.ID, "提交审批", auth.HumanPrincipal("ops"), "ticket-submit", "ops", "")
		if err != nil {
			t.Fatalf("创建提交审批申请失败: %v", err)
		}
		assertTicketImpact(t, ticket, order.ID, DeliveryImpactSummaryView{PayloadFiles: 1, PayloadConfigs: 1})
		assertImpactSummaryNotFrozen(t, h.env.db, ticket.ApprovalRequestID)
	})

	// 删除：单仍为 draft、无目标无批次；只放一条文件项以证明配置项计数保留 0。
	t.Run("删除", func(t *testing.T) {
		h, _ := newTicketApprovalHarness(t)
		order := createDraftOrder(t, h.f)
		seedFileItemWithBlob(t, h.env.db, order.ID, "plugins/demo.jar", repeatHex("ce", 64), 64)
		ticket, err := h.env.orders.RequestDelete(order.ID, "不再需要", auth.HumanPrincipal("ops"), "ticket-delete", "ops", "")
		if err != nil {
			t.Fatalf("创建删除审批申请失败: %v", err)
		}
		assertTicketImpact(t, ticket, order.ID, DeliveryImpactSummaryView{PayloadFiles: 1})
		assertImpactSummaryNotFrozen(t, h.env.db, ticket.ApprovalRequestID)
	})

	// 继续：单已启动（目标与批次已固化）后暂停，故三项计数都应有值。
	t.Run("继续", func(t *testing.T) {
		h, _ := newTicketApprovalHarness(t)
		order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
		if _, err := h.orch.applyStart(order.ID, "", "ops", ""); err != nil {
			t.Fatalf("启动变更单失败: %v", err)
		}
		if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
			"status": model.ChangeOrderStatusPaused, "pause_kind": model.PauseKindManual,
		}).Error; err != nil {
			t.Fatalf("准备暂停变更单失败: %v", err)
		}
		ticket, err := h.orch.RequestResume(order.ID, "", "继续灰度", auth.HumanPrincipal("ops"), "ticket-resume", "ops", "")
		if err != nil {
			t.Fatalf("创建继续审批申请失败: %v", err)
		}
		assertTicketImpact(t, ticket, order.ID, DeliveryImpactSummaryView{TargetCount: 2, BatchCount: 1, PayloadFiles: 1})
		assertImpactSummaryNotFrozen(t, h.env.db, ticket.ApprovalRequestID)
	})

	// 批次确认：末批进入待确认后申请，计数同样取自落库行。
	t.Run("批次确认", func(t *testing.T) {
		h, _ := newTicketApprovalHarness(t)
		order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
		if _, err := h.orch.applyStart(order.ID, "", "ops", ""); err != nil {
			t.Fatalf("启动变更单失败: %v", err)
		}
		h.tick()
		h.completeAllPushes(t, order.ID)
		h.tick()
		h.advance(6 * time.Second)
		h.tick()
		ticket, err := h.orch.RequestConfirmBatch(order.ID, 1, auth.HumanPrincipal("ops"), "ticket-confirm", "ops", "")
		if err != nil {
			t.Fatalf("创建批次确认审批申请失败: %v", err)
		}
		assertTicketImpact(t, ticket, order.ID, DeliveryImpactSummaryView{TargetCount: 2, BatchCount: 1, PayloadFiles: 1})
		assertImpactSummaryNotFrozen(t, h.env.db, ticket.ApprovalRequestID)
	})

	// 整单回滚：已完成单保留目标与批次行，计数反映整单影响面。
	t.Run("整单回滚", func(t *testing.T) {
		h, _ := newTicketApprovalHarness(t)
		order := h.completedPushOnlyOrder(t)
		ticket, err := h.orch.RequestRollback(order.ID, "恢复上一批", auth.HumanPrincipal("ops"), "ticket-rollback", "ops", "")
		if err != nil {
			t.Fatalf("创建回滚审批申请失败: %v", err)
		}
		assertTicketImpact(t, ticket, order.ID, DeliveryImpactSummaryView{TargetCount: 2, BatchCount: 1, PayloadFiles: 1})
		assertImpactSummaryNotFrozen(t, h.env.db, ticket.ApprovalRequestID)
	})

	// 结束回滚：回滚中的单同样带完整影响摘要。
	t.Run("结束回滚", func(t *testing.T) {
		h, _ := newTicketApprovalHarness(t)
		order := h.completedPushOnlyOrder(t)
		if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).
			Update("status", model.ChangeOrderStatusRollingBack).Error; err != nil {
			t.Fatalf("准备回滚状态失败: %v", err)
		}
		ticket, err := h.orch.RequestFinishRollback(order.ID, auth.HumanPrincipal("ops"), "ticket-finish", "ops", "")
		if err != nil {
			t.Fatalf("创建结束回滚审批申请失败: %v", err)
		}
		assertTicketImpact(t, ticket, order.ID, DeliveryImpactSummaryView{TargetCount: 2, BatchCount: 1, PayloadFiles: 1})
		assertImpactSummaryNotFrozen(t, h.env.db, ticket.ApprovalRequestID)
	})
}
