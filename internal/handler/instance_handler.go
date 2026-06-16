package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"beacon/internal/apperr"
	"beacon/internal/render"
	"beacon/internal/runtime"
	"beacon/internal/service"
)

// InstanceHandler 处理实例与健康相关的 admin 请求（读内存注册表）。
type InstanceHandler struct {
	svc *service.InstanceService
}

// NewInstanceHandler 构造处理器。
func NewInstanceHandler(svc *service.InstanceService) *InstanceHandler {
	return &InstanceHandler{svc: svc}
}

// instanceView 是实例对外视图（未分配时 zone 为 null）。
type instanceView struct {
	Namespace     string            `json:"namespace"`
	ServerID      string            `json:"serverId"`
	Role          string            `json:"role"`
	Group         string            `json:"group"`
	Zone          *string           `json:"zone"`
	Assigned      bool              `json:"assigned"`
	Address       string            `json:"address"`
	Version       string            `json:"version"`
	Status        string            `json:"status"`
	Capacity      int               `json:"capacity"`
	Weight        int               `json:"weight"`
	Metadata      map[string]string `json:"metadata"`
	LastHeartbeat time.Time         `json:"lastHeartbeat"`
	AppliedMD5    string            `json:"appliedMd5"`
	PlayerCount   int               `json:"playerCount"`
	TPS           float64           `json:"tps"`
	RegisteredAt  time.Time         `json:"registeredAt"`
}

func toInstanceView(i *runtime.Instance) instanceView {
	return instanceView{
		Namespace: i.Namespace, ServerID: i.ServerID, Role: i.Role, Group: i.ResolvedGroup,
		Zone: nilIfEmpty(i.ResolvedZone), Assigned: i.Assigned, Address: i.Address, Version: i.Version,
		Status: i.Status, Capacity: i.Capacity, Weight: i.Weight, Metadata: i.Metadata,
		LastHeartbeat: i.LastHeartbeat, AppliedMD5: i.AppliedMD5, PlayerCount: i.PlayerCount,
		TPS: i.TPS, RegisteredAt: i.RegisteredAt,
	}
}

func toInstanceViews(insts []*runtime.Instance) []instanceView {
	views := make([]instanceView, 0, len(insts))
	for _, i := range insts {
		views = append(views, toInstanceView(i))
	}
	return views
}

// List 处理 GET /admin/v1/instances。
func (h *InstanceHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	insts := h.svc.List(runtime.Filter{
		Namespace: q.Get("namespace"), Group: q.Get("group"), Zone: q.Get("zone"),
		Role: q.Get("role"), Status: q.Get("status"),
	})
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": toInstanceViews(insts)})
}

// Get 处理 GET /admin/v1/instances/{serverId}?namespace=。
func (h *InstanceHandler) Get(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	serverID := chi.URLParam(r, "serverId")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	inst, err := h.svc.Get(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toInstanceView(inst))
}

// Offline 处理 POST /admin/v1/instances/{serverId}/offline?namespace=&operator=。
func (h *InstanceHandler) Offline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	serverID := chi.URLParam(r, "serverId")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if err := h.svc.Offline(ns, serverID, q.Get("operator")); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
