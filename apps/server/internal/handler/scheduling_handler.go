package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// SchedulingHandler 处理流量调度 admin 请求（FR-10）：落位建议（query-only）+ drain 标记。
// 控制面只给决策、不执行玩家连接（架构红线，见 ADR-0017）。
type SchedulingHandler struct {
	svc   *service.SchedulingService
	v2svc *service.V2ControlPlaneService
}

// NewSchedulingHandler 构造处理器。
func NewSchedulingHandler(svc *service.SchedulingService, v2svc ...*service.V2ControlPlaneService) *SchedulingHandler {
	h := &SchedulingHandler{svc: svc}
	if len(v2svc) > 0 {
		h.v2svc = v2svc[0]
	}
	return h
}

// placementCandidateView 是落位候选对外视图。
type placementCandidateView struct {
	ServerID string `json:"serverId"`
	Address  string `json:"address"`
	Weight   int    `json:"weight"`
	Capacity int    `json:"capacity"`
	Drained  bool   `json:"drained"`
}

// drainView 是 drain 标记对外视图。
type drainView struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
	Reason    string `json:"reason"`
}

// Placement 处理 GET /admin/v1/scheduling/placement?namespace=&group=&zone=。
func (h *SchedulingHandler) Placement(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cands, err := h.svc.Placement(q.Get("namespace"), q.Get("group"), q.Get("zone"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]placementCandidateView, 0, len(cands))
	for _, c := range cands {
		views = append(views, placementCandidateView{
			ServerID: c.ServerID, Address: c.Address, Weight: c.Weight, Capacity: c.Capacity, Drained: c.Drained,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"candidates": views})
}

// drainRequest 是标记 drain 的请求体。
type drainRequest struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
	Reason    string `json:"reason"`
}

// Drain 处理 PUT /admin/v1/scheduling/drains。
func (h *SchedulingHandler) Drain(w http.ResponseWriter, r *http.Request) {
	var req drainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	d, err := h.svc.Drain(req.Namespace, req.ServerID, req.Reason, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, drainView{Namespace: d.NamespaceCode, ServerID: d.ServerID, Reason: d.Reason})
}

// Undrain 处理 DELETE /admin/v1/scheduling/drains?namespace=&serverId=（兼容审批申请）。
func (h *SchedulingHandler) Undrain(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if h.v2svc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	ticket, err := h.v2svc.RequestLegacyUndrain(q.Get("namespace"), q.Get("serverId"), q.Get("reason"), auth.Operator(r.Context()), clientIP(r), r.Header.Get("Idempotency-Key"), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// ListDrains 处理 GET /admin/v1/scheduling/drains?namespace=。
func (h *SchedulingHandler) ListDrains(w http.ResponseWriter, r *http.Request) {
	drains, err := h.svc.ListDrains(r.URL.Query().Get("namespace"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]drainView, 0, len(drains))
	for _, d := range drains {
		views = append(views, drainView{Namespace: d.NamespaceCode, ServerID: d.ServerID, Reason: d.Reason})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}
