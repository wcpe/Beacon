package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// ApprovalService 是统一审批处理器依赖的服务接口。
type ApprovalService interface {
	Request(op authz.Operation, payload map[string]any, principal auth.Principal, clientIP string) (model.ApprovalRequest, error)
	List(filter service.ApprovalListFilter, principal auth.Principal) ([]model.ApprovalRequest, error)
	Detail(ref string, principal auth.Principal) (model.ApprovalRequest, error)
	Approve(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error)
	Reject(ref string, principal auth.Principal, clientIP, reason string) (model.ApprovalRequest, error)
	Withdraw(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error)
}

// ApprovalHandler 处理统一审批 REST API。
type ApprovalHandler struct {
	svc ApprovalService
}

// NewApprovalHandler 构造审批处理器。
func NewApprovalHandler(svc ApprovalService) *ApprovalHandler {
	return &ApprovalHandler{svc: svc}
}

type approvalRequestBody struct {
	OperationKey   string         `json:"operationKey"`
	OperationKind  string         `json:"operationKind"`
	ResourceType   string         `json:"resourceType"`
	ResourceID     string         `json:"resourceId"`
	IdempotencyKey string         `json:"idempotencyKey"`
	RiskLevel      string         `json:"riskLevel"`
	Reason         string         `json:"reason"`
	Parameters     map[string]any `json:"parameters"`
	Payload        map[string]any `json:"payload"`
}

type approvalRejectBody struct {
	Reason string `json:"reason"`
}

// Request 处理 POST /admin/v2/approval-requests。
func (h *ApprovalHandler) Request(w http.ResponseWriter, r *http.Request) {
	var body approvalRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	operationKey := body.OperationKey
	if operationKey == "" {
		operationKey = body.OperationKind
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		idempotencyKey = body.IdempotencyKey
	}
	payload := body.Parameters
	if payload == nil {
		payload = body.Payload
	}
	operation := authz.Operation{
		Kind: operationKey, Resource: body.ResourceType, ResourceID: body.ResourceID,
		IdempotencyKey: idempotencyKey, RiskLevel: body.RiskLevel, Reason: body.Reason,
	}
	created, err := h.svc.Request(operation, payload, principal, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/admin/v2/approval-requests/"+created.RequestID)
	render.WriteJSON(w, http.StatusAccepted, toApprovalView(created))
}

// List 处理 GET /admin/v2/approval-requests。
func (h *ApprovalHandler) List(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	q := r.URL.Query()
	items, err := h.svc.List(service.ApprovalListFilter{
		Status: q.Get("status"), OperationKey: q.Get("operationKey"), RiskLevel: q.Get("riskLevel"),
		RequesterType: q.Get("requesterType"), RequesterID: q.Get("requesterId"),
	}, principal)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, toApprovalView(item))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// Detail 处理 GET /admin/v2/approval-requests/{id}。
func (h *ApprovalHandler) Detail(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	detail, err := h.svc.Detail(approvalRef(r), principal)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toApprovalView(detail))
}

// Approve 处理 POST /admin/v2/approval-requests/{id}/approve。
func (h *ApprovalHandler) Approve(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	approved, err := h.svc.Approve(approvalRef(r), principal, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, toApprovalView(approved))
}

// Reject 处理 POST /admin/v2/approval-requests/{id}/reject。
func (h *ApprovalHandler) Reject(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	var body approvalRejectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	rejected, err := h.svc.Reject(approvalRef(r), principal, clientIP(r), body.Reason)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toApprovalView(rejected))
}

// Withdraw 处理 POST /admin/v2/approval-requests/{id}/withdraw。
func (h *ApprovalHandler) Withdraw(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	withdrawn, err := h.svc.Withdraw(approvalRef(r), principal, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toApprovalView(withdrawn))
}

func approvalRef(r *http.Request) string {
	if v := chi.URLParam(r, "requestId"); v != "" {
		return v
	}
	return chi.URLParam(r, "id")
}

func toApprovalView(req model.ApprovalRequest) map[string]any {
	return map[string]any{
		"id": req.ID, "requestId": req.RequestID, "request_id": req.RequestID,
		"operationKey": req.OperationKey, "operation_key": req.OperationKey,
		"operationKind": req.OperationKind, "operation_kind": req.OperationKind,
		"resourceType": req.ResourceType, "resource_type": req.ResourceType,
		"resourceId": req.ResourceID, "resource_id": req.ResourceID,
		"riskLevel": req.RiskLevel, "risk_level": req.RiskLevel, "status": req.Status,
		"requestReason": req.RequestReason, "request_reason": req.RequestReason,
		"safeSummary": req.SafeSummary, "safe_summary": req.SafeSummary,
		"frozenPayloadSha256": req.FrozenPayloadSHA256, "frozen_payload_sha256": req.FrozenPayloadSHA256,
		"requesterType": req.RequesterType, "requester_type": req.RequesterType,
		"requesterId": req.RequesterID, "requester_id": req.RequesterID, "requestedBy": req.RequestedBy,
		"deciderType": req.DeciderType, "decider_type": req.DeciderType,
		"deciderId": req.DeciderID, "decider_id": req.DeciderID, "approvedBy": req.ApprovedBy,
		"rejectReason": req.RejectReason, "reject_reason": req.RejectReason,
		"decisionReason": req.DecisionReason, "decision_reason": req.DecisionReason,
		"expiresAt": req.ExpiresAt, "expires_at": req.ExpiresAt, "version": req.Version,
	}
}
