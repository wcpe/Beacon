package handler

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

const beaconTokenHeader = "X-Beacon-Token"
const beaconIdentityHeader = "X-Beacon-Identity"

// V2ControlPlaneHandler 处理第二版身份、namespace 隔离与区服权威端点。
type V2ControlPlaneHandler struct {
	svc *service.V2ControlPlaneService
}

type v2LifecycleApprovalRequest struct {
	OperationKey string `json:"operationKey"`
	Parameters   struct {
		NamespaceID          uint   `json:"namespaceId"`
		ServerRowID          uint   `json:"serverRowId"`
		ConfirmationCode     string `json:"confirmationCode"`
		ConfirmationServerID string `json:"confirmationServerId"`
	} `json:"parameters"`
	Reason string `json:"reason"`
}

// CreateLifecycleApprovalRequest 处理统一审批申请中的生命周期操作。
func (h *V2ControlPlaneHandler) CreateLifecycleApprovalRequest(w http.ResponseWriter, r *http.Request) {
	var req v2LifecycleApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	params := service.NamespaceLifecycleParams{
		Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}
	principal := requestPrincipal(r)
	key := r.Header.Get("Idempotency-Key")
	var ticket service.ApprovalTicketView
	var err error
	switch req.OperationKey {
	case "namespace.archive":
		ticket, err = h.svc.RequestArchiveNamespace(req.Parameters.NamespaceID, params, principal, key)
	case "namespace.restore":
		ticket, err = h.svc.RequestRestoreNamespace(req.Parameters.NamespaceID, params, principal, key)
	case "namespace.permanent_delete":
		params.Confirmation = req.Parameters.ConfirmationCode
		ticket, err = h.svc.RequestPermanentDeleteNamespace(req.Parameters.NamespaceID, params, principal, key)
	case "server.archive":
		ticket, err = h.svc.RequestArchiveServer(req.Parameters.ServerRowID, params, principal, key)
	case "server.restore":
		ticket, err = h.svc.RequestRestoreServer(req.Parameters.ServerRowID, params, principal, key)
	case "server.permanent_delete":
		params.Confirmation = req.Parameters.ConfirmationServerID
		ticket, err = h.svc.RequestPermanentDeleteServer(req.Parameters.ServerRowID, params, principal, key)
	default:
		err = apperr.ErrInvalidParam
	}
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/admin/v2/approval-requests/"+ticket.ApprovalRequestID)
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// NamespaceDirectoryResync 处理 POST /admin/v2/namespaces/{id}/bc-directory-resyncs。
func (h *V2ControlPlaneHandler) NamespaceDirectoryResync(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	result, err := h.svc.RequestNamespaceDirectoryResync(id, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, result)
}

// ServerDirectoryResync 处理 POST /admin/v2/servers/{id}/bc-directory-resyncs。
func (h *V2ControlPlaneHandler) ServerDirectoryResync(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	result, err := h.svc.RequestServerDirectoryResync(id, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, result)
}

// NewV2ControlPlaneHandler 构造第二版控制面处理器。
func NewV2ControlPlaneHandler(svc *service.V2ControlPlaneService) *V2ControlPlaneHandler {
	return &V2ControlPlaneHandler{svc: svc}
}

// AuthenticateAgentV2 供 legacy v1 数据面中间件兼容已确认 v2 身份。
func (h *V2ControlPlaneHandler) AuthenticateAgentV2(token, identityID, bootID string) error {
	return h.svc.AuthenticateAgentV2(token, identityID, bootID)
}

// AuthenticateAgentReport 供 v2 agent 数据面中间件鉴权指标 / 调度端点并取权威绑定身份（FR-144，见 §5.1）。
// bootID / addr 供并发身份冲突检测（FR-177，spec §4.5）。
func (h *V2ControlPlaneHandler) AuthenticateAgentReport(token, identityID, bootID, addr string) (agentauth.Identity, error) {
	return h.svc.AuthenticateAgentReport(token, identityID, bootID, addr)
}

type v2AgentRegisterRequest struct {
	IdentityID   string `json:"identityId"`
	ServerID     string `json:"serverId"`
	Kind         string `json:"kind"`
	BootID       string `json:"bootId"`
	AgentVersion string `json:"agentVersion"`
	// ServerWorkDir agent 上报的服务器工作目录绝对路径（FR-226，可选）。
	ServerWorkDir string          `json:"serverWorkDir"`
	Addr          string          `json:"addr"`
	Address       string          `json:"address"`
	ListenPort    *int            `json:"listenPort"`
	Listeners     json.RawMessage `json:"listeners"`
}

type v2AgentListener struct {
	BindHost string `json:"bindHost"`
	Port     int    `json:"port"`
	Ordinal  int    `json:"ordinal"`
}

type v2AgentRegistrationView struct {
	Status             string                      `json:"status"`
	Namespace          string                      `json:"namespace"`
	ServerID           *string                     `json:"serverId"`
	ExpiresAt          *time.Time                  `json:"expiresAt,omitempty"`
	BoundAt            *time.Time                  `json:"boundAt"`
	BindingFingerprint *string                     `json:"bindingFingerprint"`
	BindingSource      string                      `json:"bindingSource"`
	MigrationState     string                      `json:"migrationState"`
	Address            string                      `json:"address"`
	Endpoints          []service.AgentEndpointView `json:"endpoints"`
}

// AgentRegister 处理 POST /beacon/v2/agent/register。
func (h *V2ControlPlaneHandler) AgentRegister(w http.ResponseWriter, r *http.Request) {
	var req v2AgentRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	detectedHost, err := tcpRemoteHost(r)
	if err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	addr := req.Addr
	if addr == "" {
		addr = req.Address
	}
	listeners, listenersProvided := []service.AgentEndpointReport(nil), len(req.Listeners) > 0
	if listenersProvided {
		var reported []v2AgentListener
		if err := json.Unmarshal(req.Listeners, &reported); err != nil {
			render.WriteError(w, r, apperr.ErrInvalidParam)
			return
		}
		listeners = make([]service.AgentEndpointReport, 0, len(reported))
		for _, listener := range reported {
			listeners = append(listeners, service.AgentEndpointReport{
				BindHost: listener.BindHost, Port: listener.Port, Ordinal: listener.Ordinal,
			})
		}
	}
	res, err := h.svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: r.Header.Get(beaconTokenHeader), IdentityID: req.IdentityID, ServerID: req.ServerID,
		Kind: req.Kind, BootID: req.BootID, AgentVersion: req.AgentVersion,
		ServerWorkDir: req.ServerWorkDir,
		Addr:          addr, DetectedHost: detectedHost, ListenPort: req.ListenPort,
		Listeners: listeners, ListenersProvided: listenersProvided, ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Status == model.AgentIdentityStatusPending {
		status = http.StatusAccepted
	}
	render.WriteJSON(w, status, v2AgentRegistrationView{
		Status: res.Status, Namespace: res.Namespace, ServerID: res.ServerID, ExpiresAt: res.ExpiresAt,
		BoundAt: res.BoundAt, BindingFingerprint: res.BindingFingerprint,
		BindingSource: res.BindingSource, MigrationState: res.MigrationState,
		Address: res.Address, Endpoints: res.Endpoints,
	})
}

// tcpRemoteHost 仅从 HTTP 原始 TCP 对端提取探测 host，禁止混入 X-Forwarded-For 等审计口径。
func tcpRemoteHost(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return "", apperr.ErrInvalidParam
	}
	return host, nil
}

// AgentRegistration 处理 GET /beacon/v2/agent/registration。
func (h *V2ControlPlaneHandler) AgentRegistration(w http.ResponseWriter, r *http.Request) {
	identityID := r.Header.Get(beaconIdentityHeader)
	if identityID == "" {
		identityID = r.URL.Query().Get("identityId")
	}
	res, err := h.svc.GetAgentRegistrationV2(r.Header.Get(beaconTokenHeader), identityID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"status": res.Status, "namespace": res.Namespace, "serverId": res.ServerID,
		"boundAt": res.BoundAt, "bindingFingerprint": res.BindingFingerprint, "reason": res.Reason,
		"address": res.Address, "endpoints": res.Endpoints,
	})
}

type v2CreateNamespaceRequest struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

type v2NamespaceView struct {
	ID               uint      `json:"id"`
	Name             string    `json:"name"`
	Code             string    `json:"code"`
	DisplayName      string    `json:"displayName"`
	Description      string    `json:"description"`
	ServerCount      int64     `json:"serverCount"`
	BCClusterCount   int64     `json:"bcClusterCount"`
	ActiveTrustCount int64     `json:"activeTrustCount"`
	Lifecycle        string    `json:"lifecycle"`
	LifecycleStatus  string    `json:"lifecycleStatus"`
	EffectiveActive  bool      `json:"effectiveActive"`
	Tombstone        any       `json:"tombstone"`
	AccessToken      string    `json:"accessToken,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// CreateNamespace 处理 POST /admin/v2/namespaces。
func (h *V2ControlPlaneHandler) CreateNamespace(w http.ResponseWriter, r *http.Request) {
	var req v2CreateNamespaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ns, token, err := h.svc.CreateV2Namespace(service.CreateV2NamespaceParams{
		Name: req.Name, Code: req.Code, DisplayName: req.DisplayName, Description: req.Description,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, v2NamespaceResponse(ns, token))
}

// DeleteNamespace 处理 DELETE /admin/v2/namespaces/{id}：旧删除端点已迁移，禁止硬删。
func (h *V2ControlPlaneHandler) DeleteNamespace(w http.ResponseWriter, r *http.Request) {
	render.WriteError(w, r, apperr.ErrNamespaceDeleteMigrated)
}

// ListNamespaces 处理 GET /admin/v2/namespaces（附 server 数 / BC 集群数 / 生效信任数摘要）。
func (h *V2ControlPlaneHandler) ListNamespaces(w http.ResponseWriter, r *http.Request) {
	stats, err := h.svc.ListNamespacesWithStats(r.URL.Query().Get("lifecycleStatus"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]v2NamespaceView, 0, len(stats))
	for i := range stats {
		views = append(views, v2NamespaceStatView(stats[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views, "total": len(views)})
}

type v2GrantTrustRequest struct {
	FromNamespaceID uint   `json:"fromNamespaceId"`
	ToNamespaceID   uint   `json:"toNamespaceId"`
	Capability      string `json:"capability"`
	Note            string `json:"note"`
	Reason          string `json:"reason"`
}

// GrantNamespaceTrust 处理 POST /admin/v2/namespace-trusts。
func (h *V2ControlPlaneHandler) GrantNamespaceTrust(w http.ResponseWriter, r *http.Request) {
	var req v2GrantTrustRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestGrantNamespaceTrust(service.GrantNamespaceTrustParams{
		FromNamespaceID: req.FromNamespaceID, ToNamespaceID: req.ToNamespaceID,
		Capability: req.Capability, Note: req.Note, Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// ListNamespaceTrusts 处理 GET /admin/v2/namespace-trusts。
func (h *V2ControlPlaneHandler) ListNamespaceTrusts(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListNamespaceTrusts()
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

type v2RevokeTrustRequest struct {
	Reason string `json:"reason"`
}

// RevokeNamespaceTrust 处理 POST /admin/v2/namespace-trusts/{id}/revoke。
func (h *V2ControlPlaneHandler) RevokeNamespaceTrust(w http.ResponseWriter, r *http.Request) {
	var req v2RevokeTrustRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.RevokeNamespaceTrust(id, req.Reason, auth.Operator(r.Context())); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "status": model.NamespaceTrustStatusRevoked})
}

type v2ApproveIdentityRequest struct {
	ServerID            string `json:"serverId"`
	ForceUnbindOccupier bool   `json:"forceUnbindOccupier"`
	Reason              string `json:"reason"`
	// Target 用 RawMessage 承接以区分三态：缺省（无键）/ 显式 null（换区确认但暂不分配）/ 对象目标（换区落区）。
	Target json.RawMessage `json:"target"`
}

type v2EndpointOverrideRequest struct {
	OverrideAddress json.RawMessage `json:"overrideAddress"`
	Reason          string          `json:"reason"`
}

// SetAgentEndpointOverride 处理 PUT /admin/v2/agent-identities/{identityId}/endpoints/{endpointKey}。
func (h *V2ControlPlaneHandler) SetAgentEndpointOverride(w http.ResponseWriter, r *http.Request) {
	var req v2EndpointOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if len(req.OverrideAddress) == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	var overrideAddress *string
	if strings.TrimSpace(string(req.OverrideAddress)) != "null" {
		var value string
		if err := json.Unmarshal(req.OverrideAddress, &value); err != nil {
			render.WriteError(w, r, apperr.ErrInvalidParam)
			return
		}
		overrideAddress = &value
	}
	view, err := h.svc.SetAgentEndpointOverride(chi.URLParam(r, "identityId"), chi.URLParam(r, "endpointKey"), service.AgentEndpointOverrideParams{
		OverrideAddress: overrideAddress, Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
		TraceID: render.TraceID(r.Context()),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

type v2Target struct {
	Kind string `json:"kind"`
	ID   uint   `json:"id"`
}

// ApproveAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/approve。
func (h *V2ControlPlaneHandler) ApproveAgentIdentity(w http.ResponseWriter, r *http.Request) {
	var req v2ApproveIdentityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	params := service.ApproveAgentIdentityParams{
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r), Reason: req.Reason,
		ServerID:            req.ServerID,
		ForceUnbindOccupier: req.ForceUnbindOccupier,
	}
	if err := applyApproveTarget(&params, req.Target); err != nil {
		render.WriteError(w, r, err)
		return
	}
	ticket, err := h.svc.RequestApproveAgentIdentity(chi.URLParam(r, "identityId"), params, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// applyApproveTarget 解析 approve 请求的 target 三态并落到 service 参数：
// 无键=换区重确认取预填目标；显式 null=确认但暂不分配；对象=换区落区到该目标。
func applyApproveTarget(params *service.ApproveAgentIdentityParams, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		params.TargetExplicitNull = true
		return nil
	}
	var target v2Target
	if err := json.Unmarshal(raw, &target); err != nil {
		return apperr.ErrInvalidParam
	}
	params.TargetKind = target.Kind
	id := target.ID
	params.TargetID = &id
	return nil
}

// GetAgentIdentity 处理 GET /admin/v2/agent-identities/{identityId}（只读单条详情，附换区预填目标）。
func (h *V2ControlPlaneHandler) GetAgentIdentity(w http.ResponseWriter, r *http.Request) {
	ident, prefill, err := h.svc.GetAgentIdentityReadView(chi.URLParam(r, "identityId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, agentIdentityDetailReadView(ident, prefill))
}

type v2ReasonRequest struct {
	Reason string `json:"reason"`
}

// ListAgentIdentities 处理 GET /admin/v2/agent-identities。
func (h *V2ControlPlaneHandler) ListAgentIdentities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items, total, err := h.svc.ListAgentIdentityReadViews(service.ListAgentIdentitiesParams{
		NamespaceID: namespaceID, Status: q.Get("status"), Keyword: q.Get("keyword"),
		Page: intQuery(q.Get("page")), PageSize: intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for i := range items {
		views = append(views, agentIdentityReadView(&items[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views, "total": total})
}

// RejectAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/reject。
func (h *V2ControlPlaneHandler) RejectAgentIdentity(w http.ResponseWriter, r *http.Request) {
	h.transitionIdentity(w, r, h.svc.RejectAgentIdentity)
}

// AllowAgentIdentityReapply 处理 POST /admin/v2/agent-identities/{identityId}/allow-reapply。
func (h *V2ControlPlaneHandler) AllowAgentIdentityReapply(w http.ResponseWriter, r *http.Request) {
	var req v2ReasonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestAllowAgentIdentityReapply(chi.URLParam(r, "identityId"), service.IdentityTransitionParams{
		Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// DisableAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/disable。
func (h *V2ControlPlaneHandler) DisableAgentIdentity(w http.ResponseWriter, r *http.Request) {
	h.transitionIdentity(w, r, h.svc.DisableAgentIdentity)
}

// EnableAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/enable。
func (h *V2ControlPlaneHandler) EnableAgentIdentity(w http.ResponseWriter, r *http.Request) {
	var req v2ReasonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestEnableAgentIdentity(chi.URLParam(r, "identityId"), service.IdentityTransitionParams{
		Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// UnbindAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/unbind。
func (h *V2ControlPlaneHandler) UnbindAgentIdentity(w http.ResponseWriter, r *http.Request) {
	var req v2ReasonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestUnbindAgentIdentity(chi.URLParam(r, "identityId"), service.IdentityTransitionParams{
		Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

type v2ResolveConflictRequest struct {
	KeepBootID string `json:"keepBootId"`
	Reason     string `json:"reason"`
}

// ResolveAgentIdentityConflict 处理 POST /admin/v2/agent-identities/{identityId}/resolve-conflict（FR-177，spec §5.2）。
// 保留指定实例恢复 active，落败方后续持续 409；非 conflict → 409，keepBootId 不在冲突双方 → 400。
func (h *V2ControlPlaneHandler) ResolveAgentIdentityConflict(w http.ResponseWriter, r *http.Request) {
	var req v2ResolveConflictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestResolveAgentIdentityConflict(chi.URLParam(r, "identityId"), service.ResolveConflictParams{
		KeepBootID: req.KeepBootID, Reason: req.Reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

func (h *V2ControlPlaneHandler) transitionIdentity(w http.ResponseWriter, r *http.Request, fn func(string, service.IdentityTransitionParams) (*model.AgentIdentity, error)) {
	var req v2ReasonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ident, err := fn(chi.URLParam(r, "identityId"), service.IdentityTransitionParams{
		Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, agentIdentityView(ident))
}

type v2CreateBCClusterRequest struct {
	NamespaceID uint   `json:"namespaceId"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

// CreateBCCluster 处理 POST /admin/v2/bc-clusters。
func (h *V2ControlPlaneHandler) CreateBCCluster(w http.ResponseWriter, r *http.Request) {
	var req v2CreateBCClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	cluster, err := h.svc.CreateBCCluster(service.CreateBCClusterParams{
		NamespaceID: req.NamespaceID, Name: req.Name, Code: req.Code, DisplayName: req.DisplayName, Description: req.Description,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, cluster)
}

type v2CreateRegionRequest struct {
	BCClusterID uint   `json:"bcClusterId"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

// CreateRegion 处理 POST /admin/v2/regions。
func (h *V2ControlPlaneHandler) CreateRegion(w http.ResponseWriter, r *http.Request) {
	var req v2CreateRegionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	region, err := h.svc.CreateRegion(service.CreateRegionParams{
		BCClusterID: req.BCClusterID, Name: req.Name, Code: req.Code, DisplayName: req.DisplayName, Description: req.Description,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, region)
}

type v2CreateZoneRequest struct {
	RegionID    uint   `json:"regionId"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

// CreateZone 处理 POST /admin/v2/zones。
func (h *V2ControlPlaneHandler) CreateZone(w http.ResponseWriter, r *http.Request) {
	var req v2CreateZoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	zone, err := h.svc.CreateZone(service.CreateZoneParams{
		RegionID: req.RegionID, Name: req.Name, Code: req.Code, DisplayName: req.DisplayName, Description: req.Description,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, zone)
}

type v2UpdateDisplayRequest struct {
	Name        *string `json:"name"`
	Code        *string `json:"code"`
	DisplayName *string `json:"displayName"`
	Description *string `json:"description"`
}

type v2UpdateServerRequest struct {
	ServerID    *string `json:"serverId"`
	DisplayName *string `json:"displayName"`
}

func (h *V2ControlPlaneHandler) UpdateNamespace(w http.ResponseWriter, r *http.Request) {
	h.updateDisplay(w, r, func(p service.UpdateDisplayResourceParams) (any, error) { return h.svc.UpdateNamespace(p) })
}

func (h *V2ControlPlaneHandler) UpdateBCCluster(w http.ResponseWriter, r *http.Request) {
	h.updateDisplay(w, r, func(p service.UpdateDisplayResourceParams) (any, error) { return h.svc.UpdateBCCluster(p) })
}

func (h *V2ControlPlaneHandler) UpdateRegion(w http.ResponseWriter, r *http.Request) {
	h.updateDisplay(w, r, func(p service.UpdateDisplayResourceParams) (any, error) { return h.svc.UpdateRegion(p) })
}

func (h *V2ControlPlaneHandler) UpdateZone(w http.ResponseWriter, r *http.Request) {
	h.updateDisplay(w, r, func(p service.UpdateDisplayResourceParams) (any, error) { return h.svc.UpdateZone(p) })
}

func (h *V2ControlPlaneHandler) updateDisplay(w http.ResponseWriter, r *http.Request, fn func(service.UpdateDisplayResourceParams) (any, error)) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req v2UpdateDisplayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := fn(service.UpdateDisplayResourceParams{
		ID: id, Code: req.Code, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

func (h *V2ControlPlaneHandler) UpdateServer(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req v2UpdateServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.UpdateServerDisplayName(service.UpdateServerDisplayNameParams{
		ID: id, ServerID: req.ServerID, DisplayName: req.DisplayName,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// DeleteBCCluster 处理 DELETE /admin/v2/bc-clusters/{id}。
func (h *V2ControlPlaneHandler) DeleteBCCluster(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.DeleteBCCluster(service.DeleteNodeParams{
		ID: id, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteRegion 处理 DELETE /admin/v2/regions/{id}。
func (h *V2ControlPlaneHandler) DeleteRegion(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.DeleteRegion(service.DeleteNodeParams{
		ID: id, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteZone 处理 DELETE /admin/v2/zones/{id}。
func (h *V2ControlPlaneHandler) DeleteZone(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.DeleteZone(service.DeleteNodeParams{
		ID: id, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type v2ServerAssignmentRequest struct {
	ServerIDs      []uint    `json:"serverIds"`
	Target         *v2Target `json:"target"`
	IsDefaultEntry bool      `json:"isDefaultEntry"`
	Reason         string    `json:"reason"`
}

type v2RezoneRequest struct {
	ServerIDs []uint    `json:"serverIds"`
	Target    *v2Target `json:"target"`
	Reason    string    `json:"reason"`
}

type v2DrainingRequest struct {
	Draining bool   `json:"draining"`
	Reason   string `json:"reason"`
}

type v2DefaultEntryRequest struct {
	Value  bool   `json:"value"`
	Reason string `json:"reason"`
}

type v2ServerPlacementTransferRequest struct {
	ServerID string          `json:"serverId"`
	Target   json.RawMessage `json:"target"`
	Reason   string          `json:"reason"`
}

// AssignServers 处理 POST /admin/v2/server-assignments。
// target 对象 = 首次分配；target 显式 null = 解除分配（原因必填，见 v2-zone-authority §4.3）。
func (h *V2ControlPlaneHandler) AssignServers(w http.ResponseWriter, r *http.Request) {
	var req v2ServerAssignmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	// 用指针区分：缺省/未传 target → 400；显式 null → 解除分配；对象 → 首次分配。
	// Decode 到 *v2Target 时 JSON null 得到 nil，对象得到非 nil——与 mock 契约一致。
	// 显式 null：TargetKind/TargetID 保持零值，服务层走 unassign。
	// 若请求体根本没带 target 键，Go json 同样是 nil——与 mock「target 必填（解除分配传 null）」对齐：
	// 这里允许 nil 进入 unassign，由服务层校验 reason 非空；无 reason 即 400。
	params := service.AssignServersParams{
		ServerIDs: req.ServerIDs, IsDefaultEntry: req.IsDefaultEntry, Reason: req.Reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}
	if req.Target != nil {
		params.TargetKind = req.Target.Kind
		params.TargetID = req.Target.ID
	}
	ticket, err := h.svc.RequestAssignServers(params, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// RezoneServers 处理 POST /admin/v2/server-rezones（批量发起换区工单）。
func (h *V2ControlPlaneHandler) RezoneServers(w http.ResponseWriter, r *http.Request) {
	var req v2RezoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if req.Target == nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestRezoneServers(service.RezoneServersParams{
		ServerIDs: req.ServerIDs, TargetKind: req.Target.Kind, TargetID: req.Target.ID,
		Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// ZoneTree 处理 GET /admin/v2/zone-tree?namespaceId=（区服结构树只读聚合）。
func (h *V2ControlPlaneHandler) ZoneTree(w http.ResponseWriter, r *http.Request) {
	namespaceID, err := optionalUintQuery(r.URL.Query().Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	tree, err := h.svc.ZoneTree(namespaceID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, tree)
}

// ListLobbyClusters 处理 GET /admin/v2/lobby-clusters（大厅摘要服务端分页）。
func (h *V2ControlPlaneHandler) ListLobbyClusters(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	ready, err := optionalBoolQuery(q.Get("ready"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	page, err := nonNegativeIntQuery(q.Get("page"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	pageSize, err := nonNegativeIntQuery(q.Get("pageSize"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	result, err := h.svc.ListLobbyClusters(service.ListLobbyClustersParams{
		NamespaceID: namespaceID, Ready: ready, Page: page, PageSize: pageSize,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, result)
}

// GetLobbyCluster 处理 GET /admin/v2/lobby-clusters/{id}（成员服务端分页）。
func (h *V2ControlPlaneHandler) GetLobbyCluster(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	q := r.URL.Query()
	memberPage, err := nonNegativeIntQuery(q.Get("memberPage"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	memberPageSize, err := nonNegativeIntQuery(q.Get("memberPageSize"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	status := q.Get("status")
	if status != "" && status != "online" && status != "offline" && status != "schedulable" && status != "unschedulable" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	detail, err := h.svc.GetLobbyCluster(id, service.LobbyClusterDetailParams{
		MemberPage: memberPage, MemberPageSize: memberPageSize, Keyword: q.Get("keyword"), Status: status,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, detail)
}

// TransferServerPlacement 处理 POST /admin/v2/server-placement-transfers。
func (h *V2ControlPlaneHandler) TransferServerPlacement(w http.ResponseWriter, r *http.Request) {
	var req v2ServerPlacementTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Target) == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	params := service.ServerPlacementTransferParams{
		ServerID: req.ServerID, Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}
	if strings.TrimSpace(string(req.Target)) != "null" {
		var target v2Target
		if err := json.Unmarshal(req.Target, &target); err != nil {
			render.WriteError(w, r, apperr.ErrInvalidParam)
			return
		}
		params.TargetKind = target.Kind
		params.TargetID = target.ID
	}
	ticket, err := h.svc.RequestTransferServerPlacement(params, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// SetServerDraining 处理 PUT /admin/v2/servers/{serverRef}/draining（切换排空标记，路径为业务 serverId）。
func (h *V2ControlPlaneHandler) SetServerDraining(w http.ResponseWriter, r *http.Request) {
	var req v2DrainingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	params := service.SetServerDrainingParams{
		ServerID: chi.URLParam(r, "serverRef"), Draining: req.Draining, Reason: req.Reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}
	if req.Draining {
		view, err := h.svc.SetServerDraining(params)
		if err != nil {
			render.WriteError(w, r, err)
			return
		}
		render.WriteJSON(w, http.StatusOK, view)
		return
	}
	ticket, err := h.svc.RequestDisableServerDraining(params, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// SetServerDefaultEntry 处理 PUT /admin/v2/servers/{serverRef}/default-entry（路径为 server 行数字 id）。
func (h *V2ControlPlaneHandler) SetServerDefaultEntry(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "serverRef")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req v2DefaultEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestSetServerDefaultEntry(service.SetServerDefaultEntryParams{
		ServerRowID: id, Value: req.Value, Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}, requestPrincipal(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// ServerLifecycleImpact 处理 GET /admin/v2/servers/{id}/lifecycle-impact。
func (h *V2ControlPlaneHandler) ServerLifecycleImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	impact, err := h.svc.GetServerLifecycleImpact(id, r.URL.Query().Get("action"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, impact)
}

// ServerPermanentDeletionImpact 处理 server 永久删除前的只读影响预览。
func (h *V2ControlPlaneHandler) ServerPermanentDeletionImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	impact, err := h.svc.GetServerLifecycleImpact(id, "permanent-delete")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, impact)
}

// NamespaceLifecycleImpact 处理 namespace 归档或恢复前的只读影响预览。
func (h *V2ControlPlaneHandler) NamespaceLifecycleImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	impact, err := h.svc.GetNamespaceLifecycleImpact(id, r.URL.Query().Get("action"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, impact)
}

// NamespacePermanentDeletionImpact 处理 namespace 永久删除前的只读影响预览。
func (h *V2ControlPlaneHandler) NamespacePermanentDeletionImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	impact, err := h.svc.GetNamespacePermanentDeletionImpact(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, impact)
}

// ListServers 处理 GET /admin/v2/servers。
func (h *V2ControlPlaneHandler) ListServers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	assigned, err := optionalBoolQuery(q.Get("assigned"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items, total, err := h.svc.ListServers(service.ListServersParams{
		NamespaceID: namespaceID, Kind: q.Get("kind"), Assigned: assigned, Keyword: q.Get("keyword"),
		LifecycleStatus: q.Get("lifecycleStatus"), Lifecycle: q.Get("lifecycle"),
		Tags: parseServerTagQuery(q["tag"]),
		Page: intQuery(q.Get("page")), PageSize: intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// parseServerTagQuery 解析重复查询参数 tag=k:v（FR-227，多 tag 取交集）；非法（无冒号 / 空 key）项忽略。
func parseServerTagQuery(values []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for _, raw := range values {
		idx := strings.Index(raw, ":")
		if idx <= 0 {
			continue
		}
		out[raw[:idx]] = raw[idx+1:]
	}
	return out
}

// v2ServerTagsRequest 是标签增改请求体（按 key 增改，未出现的既有 key 保留）。
type v2ServerTagsRequest struct {
	Tags map[string]string `json:"tags"`
}

// SetServerTags 处理 PUT /admin/v2/servers/{serverId}/tags：按 key 增改标签（FR-227，低风险直执 + 强审计）。
func (h *V2ControlPlaneHandler) SetServerTags(w http.ResponseWriter, r *http.Request) {
	var req v2ServerTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.SetServerTags(service.SetServerTagsParams{
		ServerID: chi.URLParam(r, "serverId"), Tags: req.Tags,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// DeleteServerTag 处理 DELETE /admin/v2/servers/{serverId}/tags/{key}：删除单个标签（幂等）。
func (h *V2ControlPlaneHandler) DeleteServerTag(w http.ResponseWriter, r *http.Request) {
	view, err := h.svc.DeleteServerTag(service.DeleteServerTagParams{
		ServerID: chi.URLParam(r, "serverId"), TagKey: chi.URLParam(r, "key"),
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// v2NamespaceResponse 构造 namespace 创建响应（新建 namespace 计数恒为 0）。
func v2NamespaceResponse(ns *model.Namespace, token string) v2NamespaceView {
	return v2NamespaceView{
		ID: ns.ID, Name: ns.Code, Code: ns.Code, DisplayName: ns.Name, Description: ns.Description,
		Lifecycle: namespaceLifecycleView(*ns), LifecycleStatus: namespaceLifecycleView(*ns), EffectiveActive: namespaceLifecycleView(*ns) == model.NamespaceLifecycleActive,
		Tombstone: namespaceTombstoneView(*ns), AccessToken: token, CreatedAt: ns.CreatedAt, UpdatedAt: ns.UpdatedAt,
	}
}

// v2NamespaceStatView 构造 namespace 列表项（附统计摘要）。
func v2NamespaceStatView(stat service.NamespaceStat) v2NamespaceView {
	ns := stat.Namespace
	return v2NamespaceView{
		ID: ns.ID, Name: ns.Code, Code: ns.Code, DisplayName: ns.Name, Description: ns.Description,
		ServerCount: stat.ServerCount, BCClusterCount: stat.BCClusterCount,
		ActiveTrustCount: stat.ActiveTrustCount, Lifecycle: stat.Lifecycle, LifecycleStatus: stat.Lifecycle,
		EffectiveActive: stat.EffectiveActive, Tombstone: namespaceTombstoneView(ns), CreatedAt: ns.CreatedAt, UpdatedAt: ns.UpdatedAt,
	}
}

func namespaceLifecycleView(ns model.Namespace) string {
	if ns.Lifecycle == "" {
		return model.NamespaceLifecycleActive
	}
	return ns.Lifecycle
}

func namespaceTombstoneView(ns model.Namespace) any {
	if ns.TombstonedAt == nil {
		return nil
	}
	return map[string]any{
		"tombstonedAt": ns.TombstonedAt, "tombstonedBy": ns.TombstonedBy,
		"tombstoneReason": ns.TombstoneReason, "approvalRequestId": ns.TombstoneApprovalRequestID,
	}
}

func agentIdentityView(ident *model.AgentIdentity) map[string]any {
	// FR-226：工作目录未上报（旧 agent）时输出 null，与「已上报空值」区分。
	var serverWorkDir any
	if ident.ServerWorkDir != "" {
		serverWorkDir = ident.ServerWorkDir
	}
	return map[string]any{
		"id": ident.ID, "identityId": ident.IdentityID, "namespaceId": ident.NamespaceID,
		"serverId": optionalAgentIdentityServerID(ident), "kind": ident.Kind, "status": ident.Status,
		"bootId": ident.BootID, "lastAddr": ident.LastAddr, "agentVersion": ident.AgentVersion,
		"serverWorkDir":    serverWorkDir,
		"pendingExpiresAt": ident.PendingExpiresAt, "boundAt": ident.BoundAt,
		"statusChangedAt": ident.StatusChangedAt, "conflictReason": ident.ConflictReason,
		"bindingSource": ident.BindingSource, "legacyMigratedAt": ident.LegacyMigratedAt,
	}
}

// agentIdentityReadView 补管理面查询契约的迁移状态；绑定指纹仅在详情返回。
func agentIdentityReadView(ident *service.AgentIdentityReadView) map[string]any {
	view := agentIdentityView(&ident.Identity)
	view["migrationState"] = ident.MigrationState
	return view
}

// optionalAgentIdentityServerID 把未分配身份的零值映射为 JSON null，禁止以空串伪装未分配。
func optionalAgentIdentityServerID(ident *model.AgentIdentity) *string {
	if !ident.ServerID.Assigned() {
		return nil
	}
	serverID := string(ident.ServerID)
	return &serverID
}

// agentIdentityDetailView 在身份基础视图上补详情字段：conflictPeers（Q4 冲突双方 boot 明细，FR-177）与换区预填目标。
// 非冲突态 conflictPeers 为 null；冲突态回显持久化的 {bootId,lastAddr,lastSeenAt}（spec §5.2）。
func agentIdentityDetailView(ident *model.AgentIdentity, prefill *service.RezonePrefillView) map[string]any {
	view := agentIdentityView(ident)
	if peers := service.ParseConflictPeers(ident.ConflictPeers); len(peers) > 0 {
		view["conflictPeers"] = peers
	} else {
		view["conflictPeers"] = nil
	}
	view["rezonePrefill"] = prefill
	return view
}

// agentIdentityDetailReadView 在查询基础视图上追加服务端权威绑定指纹。
func agentIdentityDetailReadView(ident *service.AgentIdentityReadView, prefill *service.RezonePrefillView) map[string]any {
	view := agentIdentityDetailView(&ident.Identity, prefill)
	view["migrationState"] = ident.MigrationState
	view["bindingFingerprint"] = ident.BindingFingerprint
	view["address"] = ident.Address
	view["endpoints"] = ident.Endpoints
	return view
}

func requestPrincipal(r *http.Request) auth.Principal {
	if principal, ok := auth.FromContext(r.Context()); ok {
		return principal
	}
	return auth.HumanPrincipal(auth.Operator(r.Context()))
}

func uintURLParam(r *http.Request, name string) (uint, error) {
	raw := chi.URLParam(r, name)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, apperr.ErrInvalidParam
	}
	return uint(id), nil
}

func optionalUintQuery(raw string) (uint, error) {
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, apperr.ErrInvalidParam
	}
	return uint(id), nil
}

func optionalBoolQuery(raw string) (*bool, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, apperr.ErrInvalidParam
	}
	return &value, nil
}

func intQuery(raw string) int {
	value, _ := strconv.Atoi(raw)
	return value
}

func nonNegativeIntQuery(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, apperr.ErrInvalidParam
	}
	return value, nil
}
