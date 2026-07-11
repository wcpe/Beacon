package handler

import (
	"encoding/json"
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

// NewV2ControlPlaneHandler 构造第二版控制面处理器。
func NewV2ControlPlaneHandler(svc *service.V2ControlPlaneService) *V2ControlPlaneHandler {
	return &V2ControlPlaneHandler{svc: svc}
}

// AuthenticateAgentV2 供 legacy v1 数据面中间件兼容已确认 v2 身份。
func (h *V2ControlPlaneHandler) AuthenticateAgentV2(token, identityID, bootID string) error {
	return h.svc.AuthenticateAgentV2(token, identityID, bootID)
}

// AuthenticateAgentReport 供 v2 agent 数据面中间件鉴权指标 / 调度端点并取权威绑定身份（FR-144，见 §5.1）。
func (h *V2ControlPlaneHandler) AuthenticateAgentReport(token, identityID string) (agentauth.Identity, error) {
	return h.svc.AuthenticateAgentReport(token, identityID)
}

type v2AgentRegisterRequest struct {
	IdentityID   string `json:"identityId"`
	ServerID     string `json:"serverId"`
	Kind         string `json:"kind"`
	BootID       string `json:"bootId"`
	AgentVersion string `json:"agentVersion"`
	Addr         string `json:"addr"`
}

type v2AgentRegistrationView struct {
	Status    string     `json:"status"`
	Namespace string     `json:"namespace"`
	ServerID  string     `json:"serverId"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// AgentRegister 处理 POST /beacon/v2/agent/register。
func (h *V2ControlPlaneHandler) AgentRegister(w http.ResponseWriter, r *http.Request) {
	var req v2AgentRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	res, err := h.svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: r.Header.Get(beaconTokenHeader), IdentityID: req.IdentityID, ServerID: req.ServerID,
		Kind: req.Kind, BootID: req.BootID, AgentVersion: req.AgentVersion,
		Addr: addrOrClientIP(req.Addr, r), ClientIP: clientIP(r),
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
	})
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
		"status": res.Status, "namespace": res.Namespace, "serverId": res.ServerID, "reason": res.Reason,
	})
}

type v2CreateNamespaceRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type v2NamespaceView struct {
	ID               uint      `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	ServerCount      int64     `json:"serverCount"`
	BCClusterCount   int64     `json:"bcClusterCount"`
	ActiveTrustCount int64     `json:"activeTrustCount"`
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
		Name: req.Name, Description: req.Description, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, v2NamespaceResponse(ns, token))
}

// ListNamespaces 处理 GET /admin/v2/namespaces（附 server 数 / BC 集群数 / 生效信任数摘要）。
func (h *V2ControlPlaneHandler) ListNamespaces(w http.ResponseWriter, r *http.Request) {
	stats, err := h.svc.ListNamespacesWithStats()
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
}

// GrantNamespaceTrust 处理 POST /admin/v2/namespace-trusts。
func (h *V2ControlPlaneHandler) GrantNamespaceTrust(w http.ResponseWriter, r *http.Request) {
	var req v2GrantTrustRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	trust, err := h.svc.GrantNamespaceTrust(service.GrantNamespaceTrustParams{
		FromNamespaceID: req.FromNamespaceID, ToNamespaceID: req.ToNamespaceID,
		Capability: req.Capability, Note: req.Note, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, trust)
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
	ForceUnbindOccupier bool `json:"forceUnbindOccupier"`
	// Target 用 RawMessage 承接以区分三态：缺省（无键）/ 显式 null（换区确认但暂不分配）/ 对象目标（换区落区）。
	Target json.RawMessage `json:"target"`
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
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
		ForceUnbindOccupier: req.ForceUnbindOccupier,
	}
	if err := applyApproveTarget(&params, req.Target); err != nil {
		render.WriteError(w, r, err)
		return
	}
	ident, err := h.svc.ApproveAgentIdentity(chi.URLParam(r, "identityId"), params)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, agentIdentityView(ident))
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
	ident, prefill, err := h.svc.GetAgentIdentity(chi.URLParam(r, "identityId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, agentIdentityDetailView(ident, prefill))
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
	items, total, err := h.svc.ListAgentIdentities(service.ListAgentIdentitiesParams{
		NamespaceID: namespaceID, Status: q.Get("status"), Keyword: q.Get("keyword"),
		Page: intQuery(q.Get("page")), PageSize: intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for i := range items {
		views = append(views, agentIdentityView(&items[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views, "total": total})
}

// RejectAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/reject。
func (h *V2ControlPlaneHandler) RejectAgentIdentity(w http.ResponseWriter, r *http.Request) {
	h.transitionIdentity(w, r, h.svc.RejectAgentIdentity)
}

// AllowAgentIdentityReapply 处理 POST /admin/v2/agent-identities/{identityId}/allow-reapply。
func (h *V2ControlPlaneHandler) AllowAgentIdentityReapply(w http.ResponseWriter, r *http.Request) {
	h.transitionIdentity(w, r, h.svc.AllowAgentIdentityReapply)
}

// DisableAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/disable。
func (h *V2ControlPlaneHandler) DisableAgentIdentity(w http.ResponseWriter, r *http.Request) {
	h.transitionIdentity(w, r, h.svc.DisableAgentIdentity)
}

// EnableAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/enable。
func (h *V2ControlPlaneHandler) EnableAgentIdentity(w http.ResponseWriter, r *http.Request) {
	ident, err := h.svc.EnableAgentIdentity(chi.URLParam(r, "identityId"), service.IdentityTransitionParams{
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, agentIdentityView(ident))
}

// UnbindAgentIdentity 处理 POST /admin/v2/agent-identities/{identityId}/unbind。
func (h *V2ControlPlaneHandler) UnbindAgentIdentity(w http.ResponseWriter, r *http.Request) {
	h.transitionIdentity(w, r, h.svc.UnbindAgentIdentity)
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
		NamespaceID: req.NamespaceID, Name: req.Name, Description: req.Description,
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
		BCClusterID: req.BCClusterID, Name: req.Name, Description: req.Description,
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
		RegionID: req.RegionID, Name: req.Name, Description: req.Description,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, zone)
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
	Value bool `json:"value"`
}

// AssignServers 处理 POST /admin/v2/server-assignments。
func (h *V2ControlPlaneHandler) AssignServers(w http.ResponseWriter, r *http.Request) {
	var req v2ServerAssignmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if req.Target == nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	servers, err := h.svc.AssignServers(service.AssignServersParams{
		ServerIDs: req.ServerIDs, TargetKind: req.Target.Kind, TargetID: req.Target.ID,
		IsDefaultEntry: req.IsDefaultEntry, Reason: req.Reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	results := make([]service.AssignmentResult, 0, len(servers))
	for i := range servers {
		results = append(results, service.AssignmentResult{ID: servers[i].ID, ServerID: servers[i].ServerID, Ok: true})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"results": results})
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
	results, err := h.svc.RezoneServers(service.RezoneServersParams{
		ServerIDs: req.ServerIDs, TargetKind: req.Target.Kind, TargetID: req.Target.ID,
		Reason: req.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"results": results})
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

// SetServerDraining 处理 PUT /admin/v2/servers/{serverRef}/draining（切换排空标记，路径为业务 serverId）。
func (h *V2ControlPlaneHandler) SetServerDraining(w http.ResponseWriter, r *http.Request) {
	var req v2DrainingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.SetServerDraining(service.SetServerDrainingParams{
		ServerID: chi.URLParam(r, "serverRef"), Draining: req.Draining, Reason: req.Reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
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
	view, err := h.svc.SetServerDefaultEntry(service.SetServerDefaultEntryParams{
		ServerRowID: id, Value: req.Value, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
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
		Page: intQuery(q.Get("page")), PageSize: intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// v2NamespaceResponse 构造 namespace 创建响应（新建 namespace 计数恒为 0）。
func v2NamespaceResponse(ns *model.Namespace, token string) v2NamespaceView {
	return v2NamespaceView{
		ID: ns.ID, Name: ns.Code, Description: ns.Description,
		AccessToken: token, CreatedAt: ns.CreatedAt, UpdatedAt: ns.UpdatedAt,
	}
}

// v2NamespaceStatView 构造 namespace 列表项（附统计摘要）。
func v2NamespaceStatView(stat service.NamespaceStat) v2NamespaceView {
	ns := stat.Namespace
	return v2NamespaceView{
		ID: ns.ID, Name: ns.Code, Description: ns.Description,
		ServerCount: stat.ServerCount, BCClusterCount: stat.BCClusterCount,
		ActiveTrustCount: stat.ActiveTrustCount, CreatedAt: ns.CreatedAt, UpdatedAt: ns.UpdatedAt,
	}
}

func agentIdentityView(ident *model.AgentIdentity) map[string]any {
	return map[string]any{
		"id": ident.ID, "identityId": ident.IdentityID, "namespaceId": ident.NamespaceID,
		"serverId": ident.ServerID, "kind": ident.Kind, "status": ident.Status,
		"bootId": ident.BootID, "lastAddr": ident.LastAddr, "agentVersion": ident.AgentVersion,
		"pendingExpiresAt": ident.PendingExpiresAt, "boundAt": ident.BoundAt,
		"statusChangedAt": ident.StatusChangedAt, "conflictReason": ident.ConflictReason,
	}
}

// agentIdentityDetailView 在身份基础视图上补详情字段：conflictPeers（Q4 冲突处置延后，恒 null）与换区预填目标。
func agentIdentityDetailView(ident *model.AgentIdentity, prefill *service.RezonePrefillView) map[string]any {
	view := agentIdentityView(ident)
	view["conflictPeers"] = nil
	view["rezonePrefill"] = prefill
	return view
}

func addrOrClientIP(addr string, r *http.Request) string {
	if addr != "" {
		return addr
	}
	return clientIP(r)
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
