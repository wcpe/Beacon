package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/service"
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
	// Backends 是 bc（bungee）当前代理的后端子服 serverId 集合（仅 bc 非空、bukkit 恒空，FR-36）；供拓扑连线消费（FR-37）。
	Backends []string `json:"backends"`
	// ZoneDefaultEntry 标记该 bukkit 子服是否被指定为其小区的默认入口（FR-48）；BC agent 据此设 BungeeCord 默认/fallback 服。
	ZoneDefaultEntry bool      `json:"zoneDefaultEntry"`
	RegisteredAt     time.Time `json:"registeredAt"`
}

// toInstanceView 渲染单实例视图；defaultEntries 为该环境的默认入口 serverId 集合（命中即标 zoneDefaultEntry，FR-48）。
func toInstanceView(i *runtime.Instance, defaultEntries map[string]bool) instanceView {
	return instanceView{
		Namespace: i.Namespace, ServerID: i.ServerID, Role: i.Role, Group: i.ResolvedGroup,
		Zone: nilIfEmpty(i.ResolvedZone), Assigned: i.Assigned, Address: i.Address, Version: i.Version,
		Status: i.Status, Capacity: i.Capacity, Weight: i.Weight, Metadata: i.Metadata,
		LastHeartbeat: i.LastHeartbeat, AppliedMD5: i.AppliedMD5, PlayerCount: i.PlayerCount,
		TPS: i.TPS, Backends: i.Backends, ZoneDefaultEntry: defaultEntries[i.ServerID], RegisteredAt: i.RegisteredAt,
	}
}

func toInstanceViews(insts []*runtime.Instance, defaultEntries map[string]bool) []instanceView {
	views := make([]instanceView, 0, len(insts))
	for _, i := range insts {
		views = append(views, toInstanceView(i, defaultEntries))
	}
	return views
}

// List 处理 GET /admin/v1/instances。
func (h *InstanceHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	insts := h.svc.List(runtime.Filter{
		Namespace: ns, Group: q.Get("group"), Zone: q.Get("zone"),
		Role: q.Get("role"), Status: q.Get("status"),
	})
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": toInstanceViews(insts, h.svc.DefaultEntrySet(ns))})
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
	render.WriteJSON(w, http.StatusOK, toInstanceView(inst, h.svc.DefaultEntrySet(ns)))
}

// Offline 处理 POST /admin/v1/instances/{serverId}/offline?namespace=。
func (h *InstanceHandler) Offline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	serverID := chi.URLParam(r, "serverId")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if err := h.svc.Offline(ns, serverID, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
