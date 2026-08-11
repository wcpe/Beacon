package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

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
	List(filter service.ApprovalListFilter, principal auth.Principal) ([]model.ApprovalRequest, error)
	Detail(ref string, principal auth.Principal) (model.ApprovalRequest, error)
	Approve(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error)
	Reject(ref string, principal auth.Principal, clientIP, reason string) (model.ApprovalRequest, error)
	Withdraw(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error)
}

// ApprovalPageService 是加法兼容的分页查询能力。
type ApprovalPageService interface {
	ListPage(filter service.ApprovalListFilter, principal auth.Principal) ([]model.ApprovalRequest, int64, error)
}

// ApprovalEvidenceService 是审批详情的可选领域实时证据能力。
type ApprovalEvidenceService interface {
	DetailEvidence(ref string, principal auth.Principal) (model.ApprovalRequest, authz.ApprovalEvidence, error)
}

// ApprovalSensitiveAccessGrantService 是详情中一次性敏感内容授权引用的可选投影能力。
type ApprovalSensitiveAccessGrantService interface {
	DetailSensitiveAccessGrant(ref string, principal auth.Principal) (*service.SensitiveAccessGrantReference, error)
}

// CredentialSecretRedeemer 是一次性兑换审批凭据明文的最小服务接口。
type CredentialSecretRedeemer interface {
	RedeemCredentialSecret(requestID string, principal auth.Principal) (string, error)
}

// ApprovalHandler 处理统一审批 REST API。
type ApprovalHandler struct {
	svc                      ApprovalService
	credentialSecretRedeemer CredentialSecretRedeemer
}

// NewApprovalHandler 构造审批处理器；凭据明文兑换器显式注入，避免审批查询服务意外获得明文读取能力。
func NewApprovalHandler(svc ApprovalService, redeemers ...CredentialSecretRedeemer) *ApprovalHandler {
	h := &ApprovalHandler{svc: svc}
	if len(redeemers) != 0 {
		h.credentialSecretRedeemer = redeemers[0]
	}
	return h
}

type approvalRejectBody struct {
	Reason string `json:"reason"`
}

// List 处理 GET /admin/v2/approval-requests。
func (h *ApprovalHandler) List(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	q := r.URL.Query()
	filter, err := approvalListFilter(q)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items, total, err := h.listPage(filter, principal)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, toApprovalView(item, principal))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views, "total": total, "page": filter.Page, "pageSize": filter.PageSize})
}

func approvalListFilter(q url.Values) (service.ApprovalListFilter, error) {
	page, pageSize, err := parseApprovalPage(q["page"], q["pageSize"])
	if err != nil {
		return service.ApprovalListFilter{}, err
	}
	filter := service.ApprovalListFilter{
		Status: q.Get("status"), OperationKey: q.Get("operationKey"), RiskLevel: q.Get("riskLevel"),
		RequesterType: q.Get("requesterType"), RequesterID: q.Get("requesterId"), Keyword: q.Get("keyword"), Page: page, PageSize: pageSize,
	}
	namespaceID := q.Get("namespaceId")
	if namespaceID == "global" {
		filter.GlobalOnly = true
	} else if namespaceID != "" {
		parsed, parseErr := strconv.ParseUint(namespaceID, 10, 64)
		if parseErr != nil || parsed == 0 {
			return service.ApprovalListFilter{}, apperr.ErrInvalidParam
		}
		value := uint(parsed)
		filter.NamespaceID = &value
	}
	for key, target := range map[string]**time.Time{
		"createdFrom": &filter.CreatedFrom, "createdTo": &filter.CreatedTo,
		"expiresFrom": &filter.ExpiresFrom, "expiresTo": &filter.ExpiresTo,
	} {
		if raw := q.Get(key); raw != "" {
			parsed, parseErr := time.Parse(time.RFC3339, raw)
			if parseErr != nil {
				return service.ApprovalListFilter{}, apperr.ErrInvalidParam
			}
			parsed = parsed.UTC()
			*target = &parsed
		}
	}
	return filter, nil
}

func (h *ApprovalHandler) listPage(filter service.ApprovalListFilter, principal auth.Principal) ([]model.ApprovalRequest, int64, error) {
	if paged, ok := h.svc.(ApprovalPageService); ok {
		return paged.ListPage(filter, principal)
	}
	items, err := h.svc.List(filter, principal)
	return items, int64(len(items)), err
}

func parseApprovalPage(rawPage, rawPageSize []string) (int, int, error) {
	page, pageSize := 1, 100
	for raw, target := range map[string]*int{"page": &page, "pageSize": &pageSize} {
		values := rawPage
		if raw == "pageSize" {
			values = rawPageSize
		}
		value := ""
		if len(values) > 0 {
			value = values[0]
		}
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return 0, 0, apperr.ErrInvalidParam
		}
		*target = parsed
	}
	return page, pageSize, nil
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
	var view map[string]any
	if evidenceSvc, ok := h.svc.(ApprovalEvidenceService); ok {
		_, evidence, evidenceErr := evidenceSvc.DetailEvidence(approvalRef(r), principal)
		if evidenceErr == nil {
			view = toApprovalDetailView(detail, principal, evidence)
		}
	}
	if view == nil {
		view = toApprovalView(detail, principal)
	}
	if grantSvc, ok := h.svc.(ApprovalSensitiveAccessGrantService); ok {
		grant, grantErr := grantSvc.DetailSensitiveAccessGrant(approvalRef(r), principal)
		if grantErr == nil && grant != nil {
			view["sensitiveAccessGrant"] = map[string]string{"grantId": grant.GrantID}
		}
	}
	render.WriteJSON(w, http.StatusOK, view)
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
	render.WriteJSON(w, http.StatusAccepted, toApprovalView(approved, principal))
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
	render.WriteJSON(w, http.StatusOK, toApprovalView(rejected, principal))
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
	render.WriteJSON(w, http.StatusOK, toApprovalView(withdrawn, principal))
}

// RedeemCredentialSecret 处理 POST /admin/v2/approval-requests/{id}/credential-secret/redeem。
func (h *ApprovalHandler) RedeemCredentialSecret(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	if h.credentialSecretRedeemer == nil {
		render.WriteError(w, r, apperr.ErrCredentialSecretLost)
		return
	}
	plaintext, err := h.credentialSecretRedeemer.RedeemCredentialSecret(approvalRef(r), principal)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]string{"secret": plaintext})
}

func approvalRef(r *http.Request) string {
	if v := chi.URLParam(r, "requestId"); v != "" {
		return v
	}
	return chi.URLParam(r, "id")
}

func toApprovalView(req model.ApprovalRequest, principal auth.Principal) map[string]any {
	return map[string]any{
		"id": req.ID, "requestId": req.RequestID, "request_id": req.RequestID,
		"operationKey": req.OperationKey, "operation_key": req.OperationKey,
		"operationKind": req.OperationKind, "operation_kind": req.OperationKind,
		"schemaVersion": req.SchemaVersion, "schema_version": req.SchemaVersion,
		"requiredCapability": req.RequiredCapability, "required_capability": req.RequiredCapability,
		"resourceType": req.ResourceType, "resource_type": req.ResourceType,
		"resourceId": req.ResourceID, "resource_id": req.ResourceID,
		"riskLevel": req.RiskLevel, "risk_level": req.RiskLevel, "status": req.Status,
		"requestReason": req.RequestReason, "request_reason": req.RequestReason,
		"safeSummary": req.SafeSummary, "safe_summary": req.SafeSummary,
		"preconditionSummary": req.PreconditionSummary, "precondition_summary": req.PreconditionSummary,
		"impactSummary": req.ImpactSummary, "impact_summary": req.ImpactSummary,
		"frozenPayloadSha256": req.FrozenPayloadSHA256, "frozen_payload_sha256": req.FrozenPayloadSHA256,
		"attempt": req.Attempt, "resultRef": req.ResultRef, "result_ref": req.ResultRef,
		"failureSummary": req.FailureSummary, "failure_summary": req.FailureSummary,
		"requesterType": req.RequesterType, "requester_type": req.RequesterType,
		"requesterId": req.RequesterID, "requester_id": req.RequesterID, "requestedBy": req.RequestedBy,
		"deciderType": req.DeciderType, "decider_type": req.DeciderType,
		"deciderId": req.DeciderID, "decider_id": req.DeciderID, "approvedBy": req.ApprovedBy,
		"rejectReason": req.RejectReason, "reject_reason": req.RejectReason,
		"decisionReason": req.DecisionReason, "decision_reason": req.DecisionReason,
		"expiresAt": req.ExpiresAt, "expires_at": req.ExpiresAt, "version": req.Version,
		"namespaceId": req.NamespaceID, "namespace_id": req.NamespaceID,
		"createdAt": req.CreatedAt, "created_at": req.CreatedAt, "updatedAt": req.UpdatedAt, "updated_at": req.UpdatedAt,
		"evidenceStatus": "unavailable", "currentDiff": []any{}, "currentFactsSummary": []any{},
		"frozenPayloadSummary": safeSnapshotSummary(req), "timeline": approvalTimeline(req),
		"canApprove": canDecideApproval(req, principal), "canReject": canDecideApproval(req, principal), "canWithdraw": canWithdrawApproval(req, principal),
	}
}

func toApprovalDetailView(req model.ApprovalRequest, principal auth.Principal, evidence authz.ApprovalEvidence) map[string]any {
	view := toApprovalView(req, principal)
	snapshot := approvalSnapshot(req)
	view["evidenceStatus"] = evidence.EvidenceStatus
	view["driftStatus"] = evidence.DriftStatus
	view["frozenPayloadSummary"] = snapshot
	view["currentFactsSummary"] = evidence.CurrentFactsSummary
	view["currentDiff"] = approvalCurrentDiff(snapshot, evidence)
	return view
}

func approvalSnapshot(req model.ApprovalRequest) []authz.ApprovalEvidenceLine {
	if req.EvidenceSnapshot != "" {
		var lines []authz.ApprovalEvidenceLine
		if json.Unmarshal([]byte(req.EvidenceSnapshot), &lines) == nil {
			return lines
		}
	}
	return []authz.ApprovalEvidenceLine{{Label: "操作", Value: req.OperationKey}, {Label: "目标", Value: req.ResourceType + ":" + req.ResourceID}, {Label: "冻结哈希", Value: req.FrozenPayloadSHA256}}
}

func approvalCurrentDiff(snapshot []authz.ApprovalEvidenceLine, evidence authz.ApprovalEvidence) []authz.ApprovalEvidenceDiffLine {
	current := make(map[string]string, len(evidence.CurrentFactsSummary))
	for _, line := range evidence.CurrentFactsSummary {
		current[line.Label] = line.Value
	}
	diff := make([]authz.ApprovalEvidenceDiffLine, 0, len(snapshot))
	for _, line := range snapshot {
		if value, ok := current[line.Label]; ok {
			diff = append(diff, authz.ApprovalEvidenceDiffLine{Label: line.Label, Snapshot: line.Value, Current: value, Changed: line.Value != value})
		}
	}
	return diff
}

func canDecideApproval(req model.ApprovalRequest, principal auth.Principal) bool {
	return req.Status == model.ApprovalStatusPending && principal.IsHuman() && principal.HasCapability(auth.CapabilityApprovalDecide)
}

func canWithdrawApproval(req model.ApprovalRequest, principal auth.Principal) bool {
	return req.Status == model.ApprovalStatusPending && principal.HasCapability(auth.CapabilityApprovalWithdrawOwn) && req.RequesterType == principal.StableKind() && req.RequesterID == principal.StableID()
}

func safeSnapshotSummary(req model.ApprovalRequest) []map[string]string {
	return []map[string]string{
		{"label": "操作", "value": req.OperationKey},
		{"label": "目标", "value": req.ResourceType + ":" + req.ResourceID},
		{"label": "冻结哈希", "value": req.FrozenPayloadSHA256},
	}
}

func approvalTimeline(req model.ApprovalRequest) []map[string]any {
	timeline := []map[string]any{{"type": "requested", "at": req.CreatedAt, "actorType": req.RequesterType, "actorId": req.RequesterID, "note": req.RequestReason}}
	if req.DecidedAt != nil {
		typeName := "approved"
		if req.Status == model.ApprovalStatusRejected {
			typeName = model.ApprovalStatusRejected
		}
		timeline = append(timeline, map[string]any{"type": typeName, "at": req.DecidedAt, "actorType": req.DeciderType, "actorId": req.DeciderID, "note": req.DecisionReason})
	}
	if req.FinishedAt != nil {
		timeline = append(timeline, map[string]any{"type": req.Status, "at": req.FinishedAt, "actorType": nil, "actorId": nil, "note": req.FailureSummary})
	}
	return timeline
}
