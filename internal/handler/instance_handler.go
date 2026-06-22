package handler

import (
	"encoding/json"
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
	ZoneDefaultEntry bool `json:"zoneDefaultEntry"`
	// Proxy 是 bc（bungee 代理）专属负载指标（FR-34，仅 bc 非零、bukkit 恒零）；供代理服管理页逐台展示底层参数（FR-52）。
	// 这是控制面已持有的内存事实补暴露在逐实例视图上（此前仅在 metrics/summary 聚合暴露），加法且向后兼容。
	Proxy        proxyMetricsView `json:"proxy"`
	RegisteredAt time.Time        `json:"registeredAt"`
}

// proxyMetricsView 是 bc 专属负载指标对外视图（FR-34，仅展示不参与决策；bukkit 恒为零值）。
type proxyMetricsView struct {
	OnlineConnections   int     `json:"onlineConnections"`   // 代理在线连接数
	ThreadCount         int     `json:"threadCount"`         // JVM 活动线程数
	UptimeMs            int64   `json:"uptimeMs"`            // JVM 运行毫秒数
	BackendUp           int     `json:"backendUp"`           // 可达后端子服数
	BackendTotal        int     `json:"backendTotal"`        // 配置的后端子服总数
	BackendAvgLatencyMs float64 `json:"backendAvgLatencyMs"` // 到可达后端的平均 ping 延迟（毫秒），-1=无可达后端（不可用）
}

// toProxyMetricsView 把运行态 BC 指标映射为对外视图（bukkit 传零值时各字段恒为 0）。
func toProxyMetricsView(p runtime.ProxyMetrics) proxyMetricsView {
	return proxyMetricsView{
		OnlineConnections:   p.OnlineConnections,
		ThreadCount:         p.ThreadCount,
		UptimeMs:            p.UptimeMs,
		BackendUp:           p.BackendUp,
		BackendTotal:        p.BackendTotal,
		BackendAvgLatencyMs: p.BackendAvgLatencyMs,
	}
}

// toInstanceView 渲染单实例视图；defaultEntries 为该环境的默认入口 serverId 集合（命中即标 zoneDefaultEntry，FR-48）。
func toInstanceView(i *runtime.Instance, defaultEntries map[string]bool) instanceView {
	return instanceView{
		Namespace: i.Namespace, ServerID: i.ServerID, Role: i.Role, Group: i.ResolvedGroup,
		Zone: nilIfEmpty(i.ResolvedZone), Assigned: i.Assigned, Address: i.Address, Version: i.Version,
		Status: i.Status, Capacity: i.Capacity, Weight: i.Weight, Metadata: i.Metadata,
		LastHeartbeat: i.LastHeartbeat, AppliedMD5: i.AppliedMD5, PlayerCount: i.PlayerCount,
		TPS: i.TPS, Backends: i.Backends, ZoneDefaultEntry: defaultEntries[i.ServerID],
		Proxy: toProxyMetricsView(i.Proxy), RegisteredAt: i.RegisteredAt,
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

// offlineRequest 是主动下线请求体（reason 可选自由文本，FR-49）。
type offlineRequest struct {
	Reason string `json:"reason"`
}

// Offline 处理 POST /admin/v1/instances/{serverId}/offline?namespace=（主动下线：落 DB 拒绝态 + 移出内存，FR-49）。
// reason 经请求体可选传入（空体也允许）。
func (h *InstanceHandler) Offline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	serverID := chi.URLParam(r, "serverId")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	// reason 可选：空请求体不视为错误（按钮直点无备注亦可下线）。
	var req offlineRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if err := h.svc.Offline(ns, serverID, req.Reason, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// offlineMarkerView 是主动下线标记对外视图（FR-49）。
type offlineMarkerView struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
	Reason    string `json:"reason"`
}

// ListOffline 处理 GET /admin/v1/instances/offline?namespace=（列出当前主动下线标记，FR-49）。
// 已下线实例不在注册表（List）出现，前端据此展示「已下线（可取消）」。
func (h *InstanceHandler) ListOffline(w http.ResponseWriter, r *http.Request) {
	offs, err := h.svc.ListOffline(r.URL.Query().Get("namespace"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]offlineMarkerView, 0, len(offs))
	for _, o := range offs {
		views = append(views, offlineMarkerView{Namespace: o.NamespaceCode, ServerID: o.ServerID, Reason: o.Reason})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// Online 处理 DELETE /admin/v1/instances/{serverId}/offline?namespace=（取消主动下线：清除拒绝态，FR-49）。
func (h *InstanceHandler) Online(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	serverID := chi.URLParam(r, "serverId")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if err := h.svc.Online(ns, serverID, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
