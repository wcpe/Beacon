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

// 健康阈值设置 key（与 runtime / service.SettingsService 同字面值，FR-61）。
// 在 handler 本地声明避免引 service 常量耦合；与 runtime keyHealth* 同一字面真源。
const (
	keyHealthDegradedAfterSec = "health.degraded-after-sec"
	keyHealthTTLSec           = "health.ttl-sec"
	keyHealthOfflineGraceSec  = "health.offline-grace-sec"
)

// healthThresholds 是实例视图对健康阈值的窄读依赖（由 service.SettingsService 实现，FR-61）。
// 每次渲染读最新阈值即热生效，与健康扫描判定同源（FR-81）。
type healthThresholds interface {
	GetInt(key string) int
}

// configTimelineResolver 是实例处理器对「有效配置变更时间线」的窄依赖（由 service.EffectiveService 实现，FR-80）。
type configTimelineResolver interface {
	ConfigTimeline(ns, serverID, groupHint string) (service.ConfigTimeline, error)
}

// InstanceHandler 处理实例与健康相关的 admin 请求（读内存注册表）。
type InstanceHandler struct {
	svc      *service.InstanceService
	health   healthThresholds       // 渲染健康原因文案读当前阈值（FR-81）
	timeline configTimelineResolver // 解析 per-server 有效配置变更时间线（FR-80）
	now      func() time.Time       // 算心跳陈旧度的当前时刻（注入便于测试；默认 UTC 墙钟）
}

// NewInstanceHandler 构造处理器（health 提供健康阈值热读，供渲染 lastHeartbeatAgeSec / healthReason，FR-81；
// timeline 解析某服覆盖链涉及 config 项的发布历史，供 config-timeline 端点，FR-80）。
func NewInstanceHandler(svc *service.InstanceService, health healthThresholds, timeline configTimelineResolver) *InstanceHandler {
	return &InstanceHandler{svc: svc, health: health, timeline: timeline, now: func() time.Time { return time.Now().UTC() }}
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
	// LastHeartbeatAgeSec 是距上次心跳的秒数（按渲染时刻 UTC 算，负值归零；仅展示，FR-81）。
	LastHeartbeatAgeSec int `json:"lastHeartbeatAgeSec"`
	// HealthReason 是触发当前状态的原因文案（如「35s 未心跳 > ttl 30s」；online 时空串，FR-81）。
	HealthReason string  `json:"healthReason"`
	AppliedMD5   string  `json:"appliedMd5"`
	PlayerCount  int     `json:"playerCount"`
	TPS          float64 `json:"tps"`
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

// healthRenderCtx 是渲染实例视图时算健康原因所需的上下文（渲染时刻 + 当前三档阈值，FR-81）。
// 每请求构建一次，传给渲染函数；阈值热读保证文案与健康扫描判定同源。
type healthRenderCtx struct {
	now           time.Time
	degradedAfter time.Duration
	ttl           time.Duration
	offlineGrace  time.Duration
}

// newHealthRenderCtx 从健康阈值窄读接口构建渲染上下文（读当前生效阈值，FR-61）。
func newHealthRenderCtx(now time.Time, health healthThresholds) healthRenderCtx {
	return healthRenderCtx{
		now:           now,
		degradedAfter: time.Duration(health.GetInt(keyHealthDegradedAfterSec)) * time.Second,
		ttl:           time.Duration(health.GetInt(keyHealthTTLSec)) * time.Second,
		offlineGrace:  time.Duration(health.GetInt(keyHealthOfflineGraceSec)) * time.Second,
	}
}

// toInstanceView 渲染单实例视图；defaultEntries 为该环境的默认入口 serverId 集合（命中即标 zoneDefaultEntry，FR-48）。
// hc 提供渲染时刻与阈值，按之算 lastHeartbeatAgeSec / healthReason（FR-81，纯内存派生不落 DB）。
func toInstanceView(i *runtime.Instance, defaultEntries map[string]bool, hc healthRenderCtx) instanceView {
	age := hc.now.Sub(i.LastHeartbeat)
	if age < 0 {
		age = 0 // 时钟回拨防御：不出负秒数
	}
	return instanceView{
		Namespace: i.Namespace, ServerID: i.ServerID, Role: i.Role, Group: i.ResolvedGroup,
		Zone: nilIfEmpty(i.ResolvedZone), Assigned: i.Assigned, Address: i.Address, Version: i.Version,
		Status: i.Status, Capacity: i.Capacity, Weight: i.Weight, Metadata: i.Metadata,
		LastHeartbeat:       i.LastHeartbeat,
		LastHeartbeatAgeSec: int(age.Seconds()),
		HealthReason:        runtime.HealthReason(age, hc.degradedAfter, hc.ttl, hc.offlineGrace, i.Status),
		AppliedMD5:          i.AppliedMD5, PlayerCount: i.PlayerCount,
		TPS: i.TPS, Backends: i.Backends, ZoneDefaultEntry: defaultEntries[i.ServerID],
		Proxy: toProxyMetricsView(i.Proxy), RegisteredAt: i.RegisteredAt,
	}
}

func toInstanceViews(insts []*runtime.Instance, defaultEntries map[string]bool, hc healthRenderCtx) []instanceView {
	views := make([]instanceView, 0, len(insts))
	for _, i := range insts {
		views = append(views, toInstanceView(i, defaultEntries, hc))
	}
	return views
}

// renderCtx 构建本处理器本次渲染的健康上下文（当前时刻 + 当前阈值）。
func (h *InstanceHandler) renderCtx() healthRenderCtx {
	return newHealthRenderCtx(h.now(), h.health)
}

// List 处理 GET /admin/v1/instances。
func (h *InstanceHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	insts := h.svc.List(runtime.Filter{
		Namespace: ns, Group: q.Get("group"), Zone: q.Get("zone"),
		Role: q.Get("role"), Status: q.Get("status"),
	})
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": toInstanceViews(insts, h.svc.DefaultEntrySet(ns), h.renderCtx())})
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
	render.WriteJSON(w, http.StatusOK, toInstanceView(inst, h.svc.DefaultEntrySet(ns), h.renderCtx()))
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

// timelineEntryView 是某服覆盖链上一次配置发布的对外视图（不含 content，FR-80）。
type timelineEntryView struct {
	ConfigItemID uint      `json:"configItemId"`
	DataID       string    `json:"dataId"`
	ScopeLevel   string    `json:"scopeLevel"`
	ScopeTarget  string    `json:"scopeTarget"`
	Version      int64     `json:"version"`
	MD5          string    `json:"md5"`
	Operator     string    `json:"operator"`
	Comment      string    `json:"comment"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ConfigTimeline 处理 GET /admin/v1/instances/{serverId}/config-timeline?namespace=&group=（FR-80）。
// 只读返回某子服当前覆盖链涉及的全部 config 项的发布历史（含首发 / 发布 / 回滚），按时间倒序，供服务器详情「变更历史」展示。
func (h *InstanceHandler) ConfigTimeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	serverID := chi.URLParam(r, "serverId")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	tl, err := h.timeline.ConfigTimeline(ns, serverID, q.Get("group"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]timelineEntryView, 0, len(tl.Entries))
	for _, e := range tl.Entries {
		items = append(items, timelineEntryView{
			ConfigItemID: e.ConfigItemID, DataID: e.DataID, ScopeLevel: e.ScopeLevel, ScopeTarget: e.ScopeTarget,
			Version: e.Version, MD5: e.MD5, Operator: e.Operator, Comment: e.Comment, CreatedAt: e.CreatedAt,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"namespace": tl.Namespace, "serverId": tl.ServerID, "group": tl.Group, "zone": tl.Zone, "items": items,
	})
}
