package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// TestDeliveryApprovalAdapterStartsAndReceiptsInTx 确保批准执行在同一事务内启动变更单并写入回执。
func TestDeliveryApprovalAdapterStartsAndReceiptsInTx(t *testing.T) {
	h := newOrchestratorHarness(t)
	if err := h.env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批回执表失败: %v", err)
	}
	order := createDraftOrder(t, h.f)
	seedFileItemWithBlob(t, h.env.db, order.ID, "plugins/demo.yml", repeatHex("ab", 64), 8)
	if _, err := h.env.orders.applySubmit(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	stored, err := h.env.orders.requireOrder(order.ID)
	if err != nil {
		t.Fatalf("读取变更单失败: %v", err)
	}
	snapshotHash, err := deliveryOrderSnapshotHash(h.env.orders.repo, stored)
	if err != nil {
		t.Fatalf("计算冻结摘要失败: %v", err)
	}
	payload, err := json.Marshal(deliveryApprovePayload{
		OrderID: order.ID, ExpectedStatus: model.ChangeOrderStatusPendingApproval,
		SnapshotHash: snapshotHash,
		Operator:     "admin", ClientIP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("编码载荷失败: %v", err)
	}
	now := time.Now().UTC()
	sum := sha256.Sum256(payload)
	payloadHash := fmt.Sprintf("%x", sum[:])
	req := authz.ApprovalRequest{
		RequestID: "apr-delivery-1", OperationKey: authz.OperationDeliveryApprove,
		Operation: authz.Operation{Kind: authz.OperationDeliveryApprove, Resource: model.TargetTypeChangeOrder,
			ResourceID: strconv.FormatUint(uint64(order.ID), 10), Reason: "已确认影响面"},
		SchemaVersion: approvalSchemaVersion, RequiredCapability: auth.CapabilityApprovalRequest,
		Payload: payload, PayloadHash: payloadHash, Status: model.ApprovalStatusExecuting,
		LeaseOwner: "lease-token", LeaseUntil: ptrTime(now.Add(time.Minute)), DeciderType: auth.PrincipalKindHuman,
		DeciderID: "admin", ApprovedAt: &now, Actor: "admin", Version: 1,
	}
	approvedBy := "human:admin"
	if err := h.env.db.Create(&model.ApprovalRequest{
		RequestID: req.RequestID, OperationKey: req.OperationKey, OperationKind: req.Operation.Kind,
		SchemaVersion: req.SchemaVersion, RequiredCapability: req.RequiredCapability,
		ResourceType: req.Operation.Resource, ResourceID: req.Operation.ResourceID, RequestReason: req.Operation.Reason,
		Payload: string(req.Payload), FrozenPayloadSHA256: req.PayloadHash, Status: req.Status,
		LeaseOwner: req.LeaseOwner, LeaseUntil: req.LeaseUntil, DeciderType: req.DeciderType, DeciderID: req.DeciderID,
		ApprovedAt: req.ApprovedAt, ApprovedBy: &approvedBy, Version: req.Version,
	}).Error; err != nil {
		t.Fatalf("写入审批行失败：%v", err)
	}
	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, h.env.orders, h.orch)
	evidence := registry.ReadEvidence(req)
	if evidence.EvidenceStatus != "available" || len(evidence.CurrentFactsSummary) != 3 {
		t.Fatalf("交付实时证据不符：%+v", evidence)
	}
	var afterCommit func()
	err = h.env.db.Transaction(func(tx *gorm.DB) error {
		var executeErr error
		afterCommit, executeErr = registry.ExecuteInTx(tx, req.RequestID, req.LeaseOwner)
		return executeErr
	})
	if err != nil {
		t.Fatalf("审批适配器执行失败: %v", err)
	}
	if afterCommit != nil {
		afterCommit()
	}
	got, err := h.env.orders.Get(order.ID)
	if err != nil || got.Status != model.ChangeOrderStatusRolling {
		t.Fatalf("审批后应已持久启动: %v / %+v", err, got)
	}
	var receipts []model.ApprovalExecutionReceipt
	if err := h.env.db.Where("request_id = ?", req.RequestID).Find(&receipts).Error; err != nil || len(receipts) != 1 {
		t.Fatalf("审批执行回执应随事务落库: %v / %+v", err, receipts)
	}
}

// TestDeliveryApprovalTerminalCallbacks 收口统一审批终态与变更单状态，旧领域入口不得旁路。
func TestDeliveryApprovalTerminalCallbacks(t *testing.T) {
	for _, terminal := range []string{model.ApprovalStatusRejected, model.ApprovalStatusWithdrawn, model.ApprovalStatusExpired} {
		t.Run(terminal, func(t *testing.T) {
			h := newOrchestratorHarness(t)
			if err := h.env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
				t.Fatalf("迁移审批表失败: %v", err)
			}
			registry := authz.NewApprovalRegistry()
			RegisterDeliveryApprovalAdapter(registry, h.env.orders, h.orch)
			approval := NewApprovalService(h.env.db, repository.NewApprovalRequestRepository(h.env.db),
				repository.NewAuditLogRepository(h.env.db), registry)
			h.env.orders.SetApprovalService(approval)
			order := createDraftOrder(t, h.f)
			seedConfigItem(t, h.env.db, order.ID)
			if _, err := h.env.orders.applySubmit(order.ID, "ops-chen", ""); err != nil {
				t.Fatalf("提交失败: %v", err)
			}
			ticket, err := h.env.orders.requestApprovePending(order.ID, "需要审批", auth.HumanPrincipal("ops-chen"),
				"delivery-"+terminal, "ops-chen", "")
			if err != nil {
				t.Fatalf("创建审批申请失败: %v", err)
			}
			switch terminal {
			case model.ApprovalStatusRejected:
				_, err = approval.Reject(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), "", "风险过高")
			case model.ApprovalStatusWithdrawn:
				_, err = approval.Withdraw(ticket.ApprovalRequestID, auth.HumanPrincipal("ops-chen"), "")
			case model.ApprovalStatusExpired:
				if err = h.env.db.Model(&model.ApprovalRequest{}).Where("request_id = ?", ticket.ApprovalRequestID).
					Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error; err == nil {
					_, err = approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), "")
				}
			}
			if terminal == model.ApprovalStatusExpired {
				if err != apperr.ErrApprovalExpired {
					t.Fatalf("过期审批应返回过期错误，实际 %v", err)
				}
			} else if err != nil {
				t.Fatalf("审批终态迁移失败: %v", err)
			}
			got, err := h.env.orders.Get(order.ID)
			if err != nil || got.Status != model.ChangeOrderStatusDraft {
				t.Fatalf("审批终态应同步回草稿: %v / %+v", err, got)
			}
			if terminal == model.ApprovalStatusRejected && (got.RejectReason == nil || *got.RejectReason != "风险过高") {
				t.Fatalf("驳回原因应同步到变更单: %+v", got)
			}
		})
	}
}

// TestDeliveryResumeApprovalWorkerWritesReceipt 确保继续灰度只能由审批 worker 在同一事务执行并写回执。
func TestDeliveryResumeApprovalWorkerWritesReceipt(t *testing.T) {
	h := newOrchestratorHarness(t)
	if err := h.env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
		"status": model.ChangeOrderStatusPaused, "pause_kind": model.PauseKindManual,
	}).Error; err != nil {
		t.Fatalf("准备暂停变更单失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, h.env.orders, h.orch)
	approval := NewApprovalService(h.env.db, repository.NewApprovalRequestRepository(h.env.db),
		repository.NewAuditLogRepository(h.env.db), registry)
	h.orch.SetApprovalService(approval)
	if _, err := h.orch.Resume(order.ID, "", "", "ops", ""); err != apperr.ErrForbidden {
		t.Fatalf("旧继续入口应失败关闭，实际: %v", err)
	}
	ticket, err := h.orch.RequestResume(order.ID, "", "继续灰度", auth.HumanPrincipal("ops"), "resume-1", "ops", "")
	if err != nil {
		t.Fatalf("创建继续审批申请失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准继续审批失败: %v", err)
	}
	if n, err := NewApprovalWorker(approval).RunOnce(); err != nil || n != 1 {
		t.Fatalf("继续审批 worker 执行失败: %d / %v", n, err)
	}
	got, err := h.env.orders.Get(order.ID)
	if err != nil || got.Status != model.ChangeOrderStatusRolling {
		t.Fatalf("批准后应恢复 rolling: %v / %+v", err, got)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := h.env.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil ||
		receipt.OperationKey != authz.OperationDeliveryResume {
		t.Fatalf("继续审批应写同事务回执: %v / %+v", err, receipt)
	}
}

// TestDeliveryRollbackApprovalWorkerWritesReceipt 确保整单回滚只能由审批 worker 在同一事务执行。
func TestDeliveryRollbackApprovalWorkerWritesReceipt(t *testing.T) {
	h := newOrchestratorHarness(t)
	if err := h.env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	order := h.completedPushOnlyOrder(t)
	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, h.env.orders, h.orch)
	approval := NewApprovalService(h.env.db, repository.NewApprovalRequestRepository(h.env.db),
		repository.NewAuditLogRepository(h.env.db), registry)
	h.orch.SetApprovalService(approval)
	if _, err := h.orch.Rollback(order.ID, "回滚", "ops", ""); err != apperr.ErrForbidden {
		t.Fatalf("旧回滚入口应失败关闭，实际: %v", err)
	}
	ticket, err := h.orch.RequestRollback(order.ID, "恢复上一批", auth.HumanPrincipal("ops"), "rollback-1", "ops", "")
	if err != nil {
		t.Fatalf("创建回滚审批申请失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准回滚审批失败: %v", err)
	}
	if n, err := NewApprovalWorker(approval).RunOnce(); err != nil || n != 1 {
		t.Fatalf("回滚审批 worker 执行失败: %d / %v", n, err)
	}
	got, err := h.env.orders.Get(order.ID)
	if err != nil || got.Status != model.ChangeOrderStatusRollingBack {
		t.Fatalf("批准后应进入 rolling_back: %v / %+v", err, got)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := h.env.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil ||
		receipt.OperationKey != authz.OperationDeliveryRollback {
		t.Fatalf("回滚审批应写同事务回执: %v / %+v", err, receipt)
	}
}

// TestDeliveryConfirmBatchApprovalWorkerWritesReceipt 确保推进门确认冻结批次和目标状态后由审批 worker 执行。
func TestDeliveryConfirmBatchApprovalWorkerWritesReceipt(t *testing.T) {
	h := newOrchestratorHarness(t)
	if err := h.env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", ""); err != nil {
		t.Fatalf("准备灰度失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick()
	h.advance(6 * time.Second)
	h.tick()
	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, h.env.orders, h.orch)
	approval := NewApprovalService(h.env.db, repository.NewApprovalRequestRepository(h.env.db),
		repository.NewAuditLogRepository(h.env.db), registry)
	h.orch.SetApprovalService(approval)
	if _, err := h.orch.ConfirmBatch(order.ID, 1, "ops", ""); err != apperr.ErrForbidden {
		t.Fatalf("旧批次确认入口应失败关闭，实际: %v", err)
	}
	ticket, err := h.orch.RequestConfirmBatch(order.ID, 1, auth.HumanPrincipal("ops"), "confirm-1", "ops", "")
	if err != nil {
		t.Fatalf("创建批次确认审批申请失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准批次确认审批失败: %v", err)
	}
	if n, err := NewApprovalWorker(approval).RunOnce(); err != nil || n != 1 {
		t.Fatalf("批次确认 worker 执行失败: %d / %v", n, err)
	}
	got, err := h.env.orders.Get(order.ID)
	if err != nil || got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("末批批准确认后应完成: %v / %+v", err, got)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := h.env.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil ||
		receipt.OperationKey != authz.OperationDeliveryConfirmBatch {
		t.Fatalf("批次确认审批应写同事务回执: %v / %+v", err, receipt)
	}
}

func TestDeliveryRollbackFinishApprovalWorkerWritesReceipt(t *testing.T) {
	h := newOrchestratorHarness(t)
	if err := h.env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	order := h.completedPushOnlyOrder(t)
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).Update("status", model.ChangeOrderStatusRollingBack).Error; err != nil {
		t.Fatalf("准备回滚状态失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, h.env.orders, h.orch)
	approval := NewApprovalService(h.env.db, repository.NewApprovalRequestRepository(h.env.db), repository.NewAuditLogRepository(h.env.db), registry)
	h.orch.SetApprovalService(approval)
	if _, err := h.orch.FinishRollback(order.ID, "ops", ""); err != apperr.ErrForbidden {
		t.Fatalf("旧结束回滚入口应失败关闭，实际: %v", err)
	}
	ticket, err := h.orch.RequestFinishRollback(order.ID, auth.HumanPrincipal("ops"), "finish-1", "ops", "")
	if err != nil {
		t.Fatalf("创建结束回滚审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准结束回滚失败: %v", err)
	}
	if n, err := NewApprovalWorker(approval).RunOnce(); err != nil || n != 1 {
		t.Fatalf("结束回滚 worker 执行失败: %d / %v", n, err)
	}
	got, err := h.env.orders.Get(order.ID)
	if err != nil || got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("批准后应进入 rolled_back: %v / %+v", err, got)
	}
}

// TestDeliveryDraftDeleteApprovalWorker 确保草稿删除没有公开旁路，批准与删除回执同事务提交。
func TestDeliveryDraftDeleteApprovalWorker(t *testing.T) {
	h := newOrchestratorHarness(t)
	if err := h.env.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	RegisterDeliveryApprovalAdapter(registry, h.env.orders, h.orch)
	approval := NewApprovalService(h.env.db, repository.NewApprovalRequestRepository(h.env.db), repository.NewAuditLogRepository(h.env.db), registry)
	h.env.orders.SetApprovalService(approval)
	order := createDraftOrder(t, h.f)
	if err := h.env.orders.Delete(order.ID, "不再需要", "ops", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开删除入口应失败关闭，实际: %v", err)
	}
	ticket, err := h.env.orders.RequestDelete(order.ID, "不再需要", auth.HumanPrincipal("ops"), "delete-1", "ops", "")
	if err != nil {
		t.Fatalf("创建删除审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准删除审批失败: %v", err)
	}
	if n, err := NewApprovalWorker(approval).RunOnce(); err != nil || n != 1 {
		t.Fatalf("删除审批 worker 执行失败: %d / %v", n, err)
	}
	if _, err := h.env.orders.Get(order.ID); err != apperr.ErrChangeOrderNotFound {
		t.Fatalf("批准后草稿应已删除，实际: %v", err)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := h.env.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil || receipt.OperationKey != authz.OperationDeliveryDraftDelete {
		t.Fatalf("删除审批应写同事务回执: %v / %+v", err, receipt)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
