package service

import (
	"errors"
	"net/http"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// countApprovalRequests 统计库内审批申请条数（断言「未建申请」/「未重复建申请」用）。
func countApprovalRequests(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.ApprovalRequest{}).Count(&n).Error; err != nil {
		t.Fatalf("统计审批申请失败: %v", err)
	}
	return n
}

// draftSubmitOrder 建一张满足全部提交前置的 draft 单（文件项 + 模板源在线 + 点名目标）。
func draftSubmitOrder(t *testing.T, h *orchestratorHarness) *model.ChangeOrder {
	t.Helper()
	detail := createDraftOrder(t, h.f)
	seedFileItemWithBlob(t, h.env.db, detail.ID, "plugins/demo.jar", repeatHex("ab", 64), 64)
	order, err := repository.NewChangeOrderRepository(h.env.db).FindByID(detail.ID)
	if err != nil || order == nil {
		t.Fatalf("回读 draft 单失败: %v", err)
	}
	return order
}

// TestRequestSubmitDoesNotFreezeOrderWhenApprovalRequestFails 锁定 FR-259 第一种死结：
// 提审是「冻结单据」+「创建审批申请」两步，非事务时第二步失败会把单永久冻结在 pending_approval。
// 真机实证：前端漏带 Idempotency-Key → 400 INVALID_PARAM，单被冻结且 submit / cancel 均 illegal_state。
// 修后：建申请失败（缺幂等键）→ 事务整体回滚，单仍是可编辑的 draft，可修正后重试。
func TestRequestSubmitDoesNotFreezeOrderWhenApprovalRequestFails(t *testing.T) {
	h, _ := newTicketApprovalHarness(t)
	order := draftSubmitOrder(t, h)

	// 缺 Idempotency-Key：审批申请创建期即被拒（400 INVALID_PARAM）。
	_, err := h.env.orders.RequestSubmit(order.ID, "提交审批", auth.HumanPrincipal("ops"), "", "ops", "10.0.0.1")
	if err == nil {
		t.Fatal("缺幂等键的提审应被拒绝")
	}
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		t.Fatalf("缺幂等键应返回 400，实际 %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusDraft {
		t.Fatalf("建申请失败后单应仍为 draft（不被冻结），实际 %s", got.Status)
	}
	if got := countApprovalRequests(t, h.env.db); got != 0 {
		t.Fatalf("建申请失败不应留下审批申请，实际 %d 条", got)
	}
	if got := countAudit(t, h.env.db, model.ActionDeliveryOrderSubmit); got != 0 {
		t.Fatalf("建申请失败不应留下提审计，实际 %d 条", got)
	}

	// 修正后重试必须成功：单没被冻死，运维能自己走出来。
	ticket, err := h.env.orders.RequestSubmit(order.ID, "提交审批", auth.HumanPrincipal("ops"), "submit-retry", "ops", "10.0.0.1")
	if err != nil {
		t.Fatalf("补上幂等键后重试提审应成功: %v", err)
	}
	if ticket.ApprovalRequestID == "" {
		t.Fatal("重试提审应返回票据申请号")
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusPendingApproval {
		t.Fatalf("重试提审后单应 pending_approval，实际 %s", got.Status)
	}
}

// TestRequestSubmitSelfHealsPendingApprovalWithoutLiveRequest 锁定 FR-259 第二种死结：
// 审批票据**执行失败**（真机 start_conflict 目标集冲突）后，单停在 pending_approval、票据已终结且不可撤回，
// submit / cancel 均 illegal_state —— 单据既推不动也退不回。
// 修后：pending_approval 且无未终结审批申请即认定为「有状态无申请」的死结，允许重新提审自愈。
func TestRequestSubmitSelfHealsPendingApprovalWithoutLiveRequest(t *testing.T) {
	h, _ := newTicketApprovalHarness(t)
	order := draftSubmitOrder(t, h)
	// 铺死结现场：单已冻结，但关联申请已 failed（执行失败终态，不可撤回）。
	setOrderStatus(t, h.env.db, order.ID, model.ChangeOrderStatusPendingApproval)
	mustCreate(t, h.env.db, &model.ApprovalRequest{
		RequestID: "apr_failed_exec", OperationKey: "delivery.approve", OperationKind: "delivery.approve",
		ResourceType: model.TargetTypeChangeOrder, ResourceID: "1", IdempotencyKey: "submit-old",
		Payload: "{}", Status: model.ApprovalStatusFailed, Version: 1,
	})

	ticket, err := h.env.orders.RequestSubmit(order.ID, "执行失败后重新提审", auth.HumanPrincipal("ops"), "submit-heal", "ops", "10.0.0.1")
	if err != nil {
		t.Fatalf("死结单应允许重新提审自愈: %v", err)
	}
	if ticket.ApprovalRequestID == "" || ticket.OrderID != order.ID {
		t.Fatalf("自愈提审应返回新票据，实际 %+v", ticket)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusPendingApproval {
		t.Fatalf("自愈提审后单应仍在 pending_approval 等审批，实际 %s", got.Status)
	}
	var live int64
	if err := h.env.db.Model(&model.ApprovalRequest{}).
		Where("status IN ?", []string{model.ApprovalStatusPending, model.ApprovalStatusExecuting}).
		Count(&live).Error; err != nil {
		t.Fatalf("统计未终结申请失败: %v", err)
	}
	if live != 1 {
		t.Fatalf("自愈后应恰有 1 条未终结申请，实际 %d", live)
	}
}

// TestRequestSubmitRejectsDuplicateWhileApprovalPending 守护自愈不得被滥用：
// 单已有未终结审批申请（pending / executing）时重提会叠出两条并行的同单审批，必须拒绝且不建申请。
func TestRequestSubmitRejectsDuplicateWhileApprovalPending(t *testing.T) {
	h, _ := newTicketApprovalHarness(t)
	order := draftSubmitOrder(t, h)
	if _, err := h.env.orders.RequestSubmit(order.ID, "提交审批", auth.HumanPrincipal("ops"), "submit-1", "ops", ""); err != nil {
		t.Fatalf("首次提审失败: %v", err)
	}
	_, err := h.env.orders.RequestSubmit(order.ID, "重复提审", auth.HumanPrincipal("ops"), "submit-2", "ops", "")
	if err == nil {
		t.Fatal("已有未终结审批申请时重提应被拒绝")
	}
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		t.Fatalf("重提应返回 409 illegal_state，实际 %v", err)
	}
	if got := countApprovalRequests(t, h.env.db); got != 1 {
		t.Fatalf("重提不应建第二条申请，实际 %d 条", got)
	}
}
