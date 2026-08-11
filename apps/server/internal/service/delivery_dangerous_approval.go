package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// deliveryApprovePayload 是交付审批执行所需的冻结字段。
type deliveryApprovePayload struct {
	OrderID        uint   `json:"orderId"`
	ExpectedStatus string `json:"expectedStatus"`
	SnapshotHash   string `json:"snapshotHash"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

// deliveryDraftDeletePayload 冻结草稿删除所依据的单与状态，避免审批期间目标被替换。
type deliveryDraftDeletePayload struct {
	OrderID        uint   `json:"orderId"`
	ExpectedStatus string `json:"expectedStatus"`
	Reason         string `json:"reason"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

// deliveryResumePayload 冻结继续灰度所需的当前状态与操作参数。
type deliveryResumePayload struct {
	OrderID        uint   `json:"orderId"`
	ExpectedStatus string `json:"expectedStatus"`
	PauseKind      string `json:"pauseKind"`
	Mode           string `json:"mode"`
	Reason         string `json:"reason"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

type deliveryRollbackPayload struct {
	OrderID        uint   `json:"orderId"`
	ExpectedStatus string `json:"expectedStatus"`
	Reason         string `json:"reason"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

type deliveryConfirmBatchPayload struct {
	OrderID        uint   `json:"orderId"`
	ExpectedStatus string `json:"expectedStatus"`
	BatchNo        int    `json:"batchNo"`
	BatchID        uint   `json:"batchId"`
	BatchStatus    string `json:"batchStatus"`
	TargetHash     string `json:"targetHash"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

type deliveryRollbackFinishPayload struct {
	OrderID        uint   `json:"orderId"`
	ExpectedStatus string `json:"expectedStatus"`
	TargetHash     string `json:"targetHash"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

// RegisterDeliveryApprovalAdapter 登记交付审批的事务执行适配器。
func RegisterDeliveryApprovalAdapter(registry *authz.ApprovalRegistry, orders *DeliveryOrderService,
	orchestrator *DeliveryOrchestrator) {
	if registry == nil || orders == nil || orchestrator == nil {
		return
	}
	registry.RegisterDescriptor(authz.OperationDescriptor{
		Key: authz.OperationDeliveryApprove, SchemaVersion: approvalSchemaVersion,
		Capability: auth.CapabilityApprovalRequest, RiskLevel: "high", RequiresTerminalCallback: true,
	}, deliveryApprovalAdapter{orders: orders, orchestrator: orchestrator})
	registry.RegisterDescriptor(authz.OperationDescriptor{
		Key: authz.OperationDeliveryRollbackFinish, SchemaVersion: approvalSchemaVersion,
		Capability: auth.CapabilityApprovalRequest, RiskLevel: "high",
	}, deliveryApprovalAdapter{orders: orders, orchestrator: orchestrator})
	registry.RegisterDescriptor(authz.OperationDescriptor{
		Key: authz.OperationDeliveryConfirmBatch, SchemaVersion: approvalSchemaVersion,
		Capability: auth.CapabilityApprovalRequest, RiskLevel: "high",
	}, deliveryApprovalAdapter{orders: orders, orchestrator: orchestrator})
	registry.RegisterDescriptor(authz.OperationDescriptor{
		Key: authz.OperationDeliveryRollback, SchemaVersion: approvalSchemaVersion,
		Capability: auth.CapabilityApprovalRequest, RiskLevel: "critical",
	}, deliveryApprovalAdapter{orders: orders, orchestrator: orchestrator})
	registry.RegisterDescriptor(authz.OperationDescriptor{
		Key: authz.OperationDeliveryResume, SchemaVersion: approvalSchemaVersion,
		Capability: auth.CapabilityApprovalRequest, RiskLevel: "high",
	}, deliveryApprovalAdapter{orders: orders, orchestrator: orchestrator})
	registry.RegisterDescriptor(authz.OperationDescriptor{
		Key: authz.OperationDeliveryDraftDelete, SchemaVersion: approvalSchemaVersion,
		Capability: auth.CapabilityApprovalRequest, RiskLevel: "high",
	}, deliveryApprovalAdapter{orders: orders, orchestrator: orchestrator})
}

type deliveryApprovalAdapter struct {
	orders       *DeliveryOrderService
	orchestrator *DeliveryOrchestrator
}

func (deliveryApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a deliveryApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	return executeDeliveryApprovalInTx(tx, req, permit, a.orders, a.orchestrator)
}

// ReadApprovalEvidence 按持久化变更单目标读取当前脱敏事实，不解析冻结载荷。
func (a deliveryApprovalAdapter) ReadApprovalEvidence(req authz.ApprovalRequest) (authz.ApprovalEvidence, error) {
	if a.orders == nil || a.orders.repo == nil {
		return authz.ApprovalEvidence{}, apperr.ErrInternal
	}
	orderID, err := strconv.ParseUint(req.Operation.ResourceID, 10, 64)
	if err != nil || orderID == 0 {
		return authz.ApprovalEvidence{}, apperr.ErrApprovalTargetChanged
	}
	order, err := a.orders.repo.FindByID(uint(orderID))
	if err != nil || order == nil {
		return authz.ApprovalEvidence{}, apperr.ErrApprovalTargetChanged
	}
	return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: []authz.ApprovalEvidenceLine{
		{Label: "变更单", Value: strconv.FormatUint(uint64(order.ID), 10)},
		{Label: "当前状态", Value: order.Status},
		{Label: "命名空间", Value: strconv.FormatUint(uint64(order.NamespaceID), 10)},
	}}, nil
}

// RequiresExecutionReceipt 标记交付启动必须在领域事务内写回执。
func (deliveryApprovalAdapter) RequiresExecutionReceipt() bool { return true }

// CompleteTerminalInTx 让审批终态与变更单回草稿在同一事务内提交。
func (a deliveryApprovalAdapter) CompleteTerminalInTx(tx *gorm.DB, req authz.ApprovalRequest, status string) error {
	if status != model.ApprovalStatusRejected && status != model.ApprovalStatusWithdrawn && status != model.ApprovalStatusExpired {
		return apperr.ErrForbidden
	}
	if req.OperationKey == authz.OperationDeliveryDraftDelete {
		return nil
	}
	var payload deliveryApprovePayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.OrderID == 0 ||
		req.Operation.ResourceID != strconv.FormatUint(uint64(payload.OrderID), 10) ||
		payload.ExpectedStatus != model.ChangeOrderStatusPendingApproval {
		return apperr.ErrApprovalTargetChanged
	}
	orders := *a.orders
	orders.db = tx
	orders.repo = a.orders.repo.WithTx(tx)
	order, err := orders.requireOrder(payload.OrderID)
	if err != nil {
		return err
	}
	if order.Status != model.ChangeOrderStatusPendingApproval {
		return apperr.ErrApprovalTargetChanged
	}
	updates := map[string]any{"status": model.ChangeOrderStatusDraft, "approved_by": "", "approved_at": nil}
	auditAction := model.ActionDeliveryOrderWithdraw
	detail := map[string]any{"orderId": order.ID, "approvalStatus": status}
	if status == model.ApprovalStatusRejected {
		updates["reject_reason"] = req.DecisionReason
		auditAction = model.ActionDeliveryOrderReject
		detail["reason"] = req.DecisionReason
	}
	nsCode, err := orders.namespaceCode(order.NamespaceID)
	if err != nil {
		return err
	}
	ok, err := orders.repo.UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusPendingApproval}, updates)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrApprovalTargetChanged
	}
	return orders.writeAudit(tx, nsCode, req.Actor, "", auditAction, order.ID, detail)
}

func executeDeliveryApprovalInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit,
	orders *DeliveryOrderService, orchestrator *DeliveryOrchestrator) (func(), error) {
	if tx == nil {
		return nil, apperr.ErrForbidden
	}
	if err := ensureRequestPermit(req, permit); err != nil {
		return nil, apperr.ErrForbidden
	}
	if permit.Operation() == authz.OperationDeliveryResume {
		return executeDeliveryResumeInTx(tx, req, permit, orders, orchestrator)
	}
	if permit.Operation() == authz.OperationDeliveryRollback {
		return executeDeliveryRollbackInTx(tx, req, orchestrator)
	}
	if permit.Operation() == authz.OperationDeliveryConfirmBatch {
		return executeDeliveryConfirmBatchInTx(tx, req, orchestrator)
	}
	if permit.Operation() == authz.OperationDeliveryRollbackFinish {
		return executeDeliveryRollbackFinishInTx(tx, req, orchestrator)
	}
	if permit.Operation() == authz.OperationDeliveryDraftDelete {
		return executeDeliveryDraftDeleteInTx(tx, req, orders)
	}
	if permit.Operation() != authz.OperationDeliveryApprove {
		return nil, apperr.ErrForbidden
	}
	var payload deliveryApprovePayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return nil, apperr.ErrInvalidParam
	}
	if payload.OrderID == 0 || req.Operation.ResourceID != strconv.FormatUint(uint64(payload.OrderID), 10) ||
		payload.ExpectedStatus != model.ChangeOrderStatusPendingApproval || payload.SnapshotHash == "" {
		return nil, apperr.ErrApprovalTargetChanged
	}
	transactionalOrders := *orders
	transactionalOrders.db = tx
	transactionalOrders.repo = orders.repo.WithTx(tx)
	order, err := transactionalOrders.requireOrder(payload.OrderID)
	if err != nil {
		return nil, err
	}
	snapshotHash, err := deliveryOrderSnapshotHash(transactionalOrders.repo, order)
	if err != nil || snapshotHash != payload.SnapshotHash {
		return nil, apperr.ErrApprovalTargetChanged
	}
	approved, err := transactionalOrders.applyApprove(payload.OrderID, req.Operation.Reason, req.Actor, payload.ClientIP)
	if err != nil {
		return nil, err
	}
	if approved.Status != model.ChangeOrderStatusApproved {
		return nil, apperr.ErrApprovalTargetChanged
	}
	afterCommit, err := orchestrator.applyStartApprovedInTx(tx, payload.OrderID, req.Operation.Reason, req.Actor, payload.ClientIP)
	if err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{
		RequestID: req.RequestID, OperationKey: req.OperationKey,
		PayloadHash: req.PayloadHash, ResultRef: "change-order-" + strconv.FormatUint(uint64(payload.OrderID), 10),
	}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	return afterCommit, nil
}

func executeDeliveryDraftDeleteInTx(tx *gorm.DB, req authz.ApprovalRequest, orders *DeliveryOrderService) (func(), error) {
	var payload deliveryDraftDeletePayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.OrderID == 0 ||
		req.Operation.ResourceID != strconv.FormatUint(uint64(payload.OrderID), 10) ||
		payload.ExpectedStatus != model.ChangeOrderStatusDraft || strings.TrimSpace(payload.Reason) == "" {
		return nil, apperr.ErrApprovalTargetChanged
	}
	transactionalOrders := *orders
	transactionalOrders.db = tx
	transactionalOrders.repo = orders.repo.WithTx(tx)
	if err := transactionalOrders.applyDelete(payload.OrderID, payload.Reason, req.Actor, payload.ClientIP); err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey,
		PayloadHash: req.PayloadHash, ResultRef: "change-order-" + strconv.FormatUint(uint64(payload.OrderID), 10)}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	return nil, nil
}

func executeDeliveryRollbackFinishInTx(tx *gorm.DB, req authz.ApprovalRequest, orchestrator *DeliveryOrchestrator) (func(), error) {
	var payload deliveryRollbackFinishPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.OrderID == 0 ||
		req.Operation.ResourceID != strconv.FormatUint(uint64(payload.OrderID), 10) {
		return nil, apperr.ErrApprovalTargetChanged
	}
	order, err := orchestrator.repo.WithTx(tx).FindByID(payload.OrderID)
	if err != nil || order == nil || order.Status != payload.ExpectedStatus {
		return nil, apperr.ErrApprovalTargetChanged
	}
	targetHash, err := deliveryBatchTargetHash(orchestrator.repo.WithTx(tx), order.ID, 0)
	if err != nil || targetHash != payload.TargetHash {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := orchestrator.applyFinishRollbackInTx(tx, order, req.Actor, payload.ClientIP); err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey,
		PayloadHash: req.PayloadHash, ResultRef: fmt.Sprintf("change-order-%d", order.ID)}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	return nil, nil
}

func executeDeliveryConfirmBatchInTx(tx *gorm.DB, req authz.ApprovalRequest, orchestrator *DeliveryOrchestrator) (func(), error) {
	var payload deliveryConfirmBatchPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.OrderID == 0 || payload.BatchID == 0 ||
		req.Operation.ResourceID != strconv.FormatUint(uint64(payload.OrderID), 10) {
		return nil, apperr.ErrApprovalTargetChanged
	}
	order, err := orchestrator.repo.WithTx(tx).FindByID(payload.OrderID)
	if err != nil || order == nil || order.Status != payload.ExpectedStatus {
		return nil, apperr.ErrApprovalTargetChanged
	}
	batch, batches, err := loadConfirmBatch(orchestrator.repo.WithTx(tx), order.ID, payload.BatchNo)
	if err != nil || batch.ID != payload.BatchID || batch.Status != payload.BatchStatus {
		return nil, apperr.ErrApprovalTargetChanged
	}
	targetHash, err := deliveryBatchTargetHash(orchestrator.repo.WithTx(tx), order.ID, batch.ID)
	if err != nil || targetHash != payload.TargetHash {
		return nil, apperr.ErrApprovalTargetChanged
	}
	nsCode, err := changeNamespaceCode(tx, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	last := isLastBatch(batches, payload.BatchNo)
	if err := orchestrator.persistConfirmInTx(tx, order, batch, batches, last, nsCode, req.Actor, payload.ClientIP); err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey,
		PayloadHash: req.PayloadHash, ResultRef: fmt.Sprintf("change-order-%d-batch-%d", order.ID, batch.BatchNo)}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	return func() {
		orchestrator.clearObserve(order.ID)
		if !last {
			orchestrator.wake()
		}
	}, nil
}

func executeDeliveryRollbackInTx(tx *gorm.DB, req authz.ApprovalRequest, orchestrator *DeliveryOrchestrator) (func(), error) {
	var payload deliveryRollbackPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.OrderID == 0 ||
		req.Operation.ResourceID != strconv.FormatUint(uint64(payload.OrderID), 10) || strings.TrimSpace(payload.Reason) == "" {
		return nil, apperr.ErrApprovalTargetChanged
	}
	order, err := orchestrator.repo.WithTx(tx).FindByID(payload.OrderID)
	if err != nil || order == nil || order.Status != payload.ExpectedStatus {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := orchestrator.applyRollbackInTx(tx, order, payload.Reason, req.Actor, payload.ClientIP); err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey,
		PayloadHash: req.PayloadHash, ResultRef: "change-order-" + strconv.FormatUint(uint64(order.ID), 10)}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	return orchestrator.wake, nil
}

func executeDeliveryResumeInTx(tx *gorm.DB, req authz.ApprovalRequest, _ authz.Permit,
	orders *DeliveryOrderService, orchestrator *DeliveryOrchestrator) (func(), error) {
	var payload deliveryResumePayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.OrderID == 0 ||
		req.Operation.ResourceID != strconv.FormatUint(uint64(payload.OrderID), 10) ||
		payload.ExpectedStatus != model.ChangeOrderStatusPaused {
		return nil, apperr.ErrApprovalTargetChanged
	}
	order, err := orders.repo.WithTx(tx).FindByID(payload.OrderID)
	if err != nil || order == nil || order.Status != payload.ExpectedStatus || order.PauseKind != payload.PauseKind {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := validateResumeArgs(payload.PauseKind, payload.Mode, payload.Reason); err != nil {
		return nil, err
	}
	nsCode, err := changeNamespaceCode(tx, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	if err := orchestrator.applyResumeInTx(tx, order, nsCode, payload.Mode, payload.Reason, req.Actor, payload.ClientIP); err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey,
		PayloadHash: req.PayloadHash, ResultRef: "change-order-" + strconv.FormatUint(uint64(order.ID), 10)}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	return func() {
		if payload.PauseKind == model.PauseKindPrepareFailed {
			orchestrator.notifyAgent(nsCode, order.SourceServerID)
		}
		orchestrator.wake()
	}, nil
}

// RequestResume 冻结暂停单当前状态并创建统一审批申请，不直接恢复灰度。
func (s *DeliveryOrchestrator) RequestResume(id uint, mode, reason string, principal auth.Principal,
	idempotencyKey, operator, clientIP string) (DeliveryApprovalTicketView, error) {
	if s.approval == nil {
		return DeliveryApprovalTicketView{}, apperr.ErrForbidden
	}
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	if order.Status != model.ChangeOrderStatusPaused {
		return DeliveryApprovalTicketView{}, changeIllegalState(order.Status, "申请继续")
	}
	if err := validateResumeArgs(order.PauseKind, mode, reason); err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	payload := map[string]any{"orderId": order.ID, "expectedStatus": order.Status, "pauseKind": order.PauseKind,
		"mode": mode, "reason": reason, "operator": operator, "clientIP": clientIP}
	created, err := s.approval.Request(authz.Operation{Kind: authz.OperationDeliveryResume,
		NamespaceID: &order.NamespaceID, Resource: model.TargetTypeChangeOrder,
		ResourceID: strconv.FormatUint(uint64(order.ID), 10), IdempotencyKey: idempotencyKey, RiskLevel: "high", Reason: reason,
		EvidenceSnapshot: []authz.ApprovalEvidenceLine{{Label: "变更单", Value: strconv.FormatUint(uint64(order.ID), 10)}, {Label: "当前状态", Value: order.Status}, {Label: "暂停原因", Value: order.PauseKind}}}, payload, principal, clientIP)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	return DeliveryApprovalTicketView{ApprovalRequestID: created.RequestID, Status: created.Status, OperationKey: created.OperationKey}, nil
}

// RequestRollback 冻结可回滚单的当前状态和原因，创建统一审批申请。
func (s *DeliveryOrchestrator) RequestRollback(id uint, reason string, principal auth.Principal,
	idempotencyKey, operator, clientIP string) (DeliveryApprovalTicketView, error) {
	if s.approval == nil || strings.TrimSpace(reason) == "" {
		return DeliveryApprovalTicketView{}, apperr.ErrApprovalReasonRequired
	}
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	if order.Status != model.ChangeOrderStatusCompleted && order.Status != model.ChangeOrderStatusPaused && order.Status != model.ChangeOrderStatusCancelled {
		return DeliveryApprovalTicketView{}, changeIllegalState(order.Status, "申请整单回滚")
	}
	payload := map[string]any{"orderId": order.ID, "expectedStatus": order.Status, "reason": reason,
		"operator": operator, "clientIP": clientIP}
	created, err := s.approval.Request(authz.Operation{Kind: authz.OperationDeliveryRollback,
		NamespaceID: &order.NamespaceID, Resource: model.TargetTypeChangeOrder,
		ResourceID: strconv.FormatUint(uint64(order.ID), 10), IdempotencyKey: idempotencyKey, RiskLevel: "critical", Reason: reason,
		EvidenceSnapshot: []authz.ApprovalEvidenceLine{{Label: "变更单", Value: strconv.FormatUint(uint64(order.ID), 10)}, {Label: "当前状态", Value: order.Status}}}, payload, principal, clientIP)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	return DeliveryApprovalTicketView{ApprovalRequestID: created.RequestID, Status: created.Status, OperationKey: created.OperationKey}, nil
}

// RequestConfirmBatch 冻结当前待确认批和目标状态哈希，创建统一审批申请。
func (s *DeliveryOrchestrator) RequestConfirmBatch(id uint, batchNo int, principal auth.Principal,
	idempotencyKey, operator, clientIP string) (DeliveryApprovalTicketView, error) {
	if s.approval == nil {
		return DeliveryApprovalTicketView{}, apperr.ErrForbidden
	}
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	if order.Status != model.ChangeOrderStatusRolling {
		return DeliveryApprovalTicketView{}, changeIllegalState(order.Status, "申请批次确认")
	}
	batch, _, err := s.loadConfirmBatch(order.ID, batchNo)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	targetHash, err := deliveryBatchTargetHash(s.repo, order.ID, batch.ID)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	payload := map[string]any{"orderId": order.ID, "expectedStatus": order.Status, "batchNo": batchNo,
		"batchId": batch.ID, "batchStatus": batch.Status, "targetHash": targetHash, "operator": operator, "clientIP": clientIP}
	created, err := s.approval.Request(authz.Operation{Kind: authz.OperationDeliveryConfirmBatch,
		NamespaceID: &order.NamespaceID, Resource: model.TargetTypeChangeOrder,
		ResourceID: strconv.FormatUint(uint64(order.ID), 10), IdempotencyKey: idempotencyKey, RiskLevel: "high", Reason: "确认交付批次",
		EvidenceSnapshot: []authz.ApprovalEvidenceLine{{Label: "变更单", Value: strconv.FormatUint(uint64(order.ID), 10)}, {Label: "批次", Value: strconv.Itoa(batchNo)}, {Label: "目标状态哈希", Value: targetHash}}}, payload, principal, clientIP)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	return DeliveryApprovalTicketView{ApprovalRequestID: created.RequestID, Status: created.Status, OperationKey: created.OperationKey}, nil
}

// RequestFinishRollback 冻结回滚中的目标状态，创建结束回滚审批申请。
func (s *DeliveryOrchestrator) RequestFinishRollback(id uint, principal auth.Principal,
	idempotencyKey, operator, clientIP string) (DeliveryApprovalTicketView, error) {
	if s.approval == nil {
		return DeliveryApprovalTicketView{}, apperr.ErrForbidden
	}
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	if order.Status != model.ChangeOrderStatusRollingBack {
		return DeliveryApprovalTicketView{}, changeIllegalState(order.Status, "申请结束回滚")
	}
	targetHash, err := deliveryBatchTargetHash(s.repo, order.ID, 0)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	payload := map[string]any{"orderId": order.ID, "expectedStatus": order.Status, "targetHash": targetHash,
		"operator": operator, "clientIP": clientIP}
	created, err := s.approval.Request(authz.Operation{Kind: authz.OperationDeliveryRollbackFinish,
		NamespaceID: &order.NamespaceID, Resource: model.TargetTypeChangeOrder,
		ResourceID: strconv.FormatUint(uint64(order.ID), 10), IdempotencyKey: idempotencyKey, RiskLevel: "high", Reason: "结束交付回滚",
		EvidenceSnapshot: []authz.ApprovalEvidenceLine{{Label: "变更单", Value: strconv.FormatUint(uint64(order.ID), 10)}, {Label: "目标状态哈希", Value: targetHash}}}, payload, principal, clientIP)
	if err != nil {
		return DeliveryApprovalTicketView{}, err
	}
	return DeliveryApprovalTicketView{ApprovalRequestID: created.RequestID, Status: created.Status, OperationKey: created.OperationKey}, nil
}

func deliveryBatchTargetHash(repo *repository.ChangeOrderRepository, orderID, batchID uint) (string, error) {
	targets, err := repo.ListTargetsByOrder(orderID)
	if err != nil {
		return "", err
	}
	rows := make([]struct{ ServerID, Status string }, 0)
	for i := range targets {
		if batchID == 0 || targets[i].BatchID == batchID {
			rows = append(rows, struct{ ServerID, Status string }{targets[i].ServerID, targets[i].Status})
		}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), nil
}

// deliveryOrderSnapshotHash 对审批前的变更单与有序变更项求摘要，执行时必须保持一致。
func deliveryOrderSnapshotHash(repo *repository.ChangeOrderRepository, order *model.ChangeOrder) (string, error) {
	items, err := repo.ListItems(order.ID)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Order *model.ChangeOrder      `json:"order"`
		Items []model.ChangeOrderItem `json:"items"`
	}{Order: order, Items: items})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), nil
}
