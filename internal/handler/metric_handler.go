package handler

import (
	"net/http"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/service"
)

// MetricHandler 处理负载指标 admin 请求（FR-32，见 ADR-0023）：
// 当前快照聚合 + 历史趋势。仅返回负载数字（健康事实），绝不含玩家名单 / 身份。
type MetricHandler struct {
	svc *service.MetricService
}

// NewMetricHandler 构造处理器。
func NewMetricHandler(svc *service.MetricService) *MetricHandler {
	return &MetricHandler{svc: svc}
}

// serverPlayersView 是每服人数明细对外视图（仅计数，不含名单）。
type serverPlayersView struct {
	ServerID    string `json:"serverId"`
	Role        string `json:"role"` // 实例角色（bukkit / bungee），供前端按角色分组明细（FR-43）
	PlayerCount int    `json:"playerCount"`
}

// bcSummaryView 是 bc（bungee 代理）维度聚合对外视图（FR-34，仅负载数字，不含名单）。
type bcSummaryView struct {
	ProxyCount          int     `json:"proxyCount"`          // 在线 bc 代理数
	TotalConnections    int     `json:"totalConnections"`    // bc 在线连接数合计
	AvgThreadCount      float64 `json:"avgThreadCount"`      // bc 平均 JVM 线程数
	BackendUp           int     `json:"backendUp"`           // bc 可达后端数合计
	BackendTotal        int     `json:"backendTotal"`        // bc 配置后端总数合计
	AvgBackendLatencyMs float64 `json:"avgBackendLatencyMs"` // bc 平均后端延迟（-1.0=无可用样本）
}

// summaryView 是当前快照聚合对外视图。
type summaryView struct {
	TotalPlayers   int                 `json:"totalPlayers"`
	OnlineServers  int                 `json:"onlineServers"`
	Servers        []serverPlayersView `json:"servers"`
	AvgTPS         float64             `json:"avgTps"`
	AvgMemUsed     int64               `json:"avgMemUsed"`
	AvgMemMax      int64               `json:"avgMemMax"`
	AvgCPULoad     float64             `json:"avgCpuLoad"`     // -1.0 表示无可用 CPU 样本
	CPUSampleCount int                 `json:"cpuSampleCount"` // 参与 CPU 平均的可用样本数
	BC             bcSummaryView       `json:"bc"`             // bc 代理维度聚合（FR-34）
}

// trendPointView 是趋势时间序列点对外视图。
type trendPointView struct {
	SampledAt    time.Time `json:"sampledAt"`
	TotalPlayers int       `json:"totalPlayers"`
	AvgTPS       float64   `json:"avgTps"`
	AvgMemUsed   int64     `json:"avgMemUsed"`
	AvgMemMax    int64     `json:"avgMemMax"`
	AvgCPULoad   float64   `json:"avgCpuLoad"`
}

// Summary 处理 GET /admin/v1/metrics/summary?namespace=。
// namespace 可选：空则聚合全部环境在线实例。
func (h *MetricHandler) Summary(w http.ResponseWriter, r *http.Request) {
	sum := h.svc.Summary(r.URL.Query().Get("namespace"))
	servers := make([]serverPlayersView, 0, len(sum.Servers))
	for _, s := range sum.Servers {
		servers = append(servers, serverPlayersView{ServerID: s.ServerID, Role: s.Role, PlayerCount: s.PlayerCount})
	}
	render.WriteJSON(w, http.StatusOK, summaryView{
		TotalPlayers: sum.TotalPlayers, OnlineServers: sum.OnlineServers, Servers: servers,
		AvgTPS: sum.AvgTPS, AvgMemUsed: sum.AvgMemUsed, AvgMemMax: sum.AvgMemMax,
		AvgCPULoad: sum.AvgCPULoad, CPUSampleCount: sum.CPUSampleCount,
		BC: bcSummaryView{
			ProxyCount: sum.BC.ProxyCount, TotalConnections: sum.BC.TotalConnections,
			AvgThreadCount: sum.BC.AvgThreadCount, BackendUp: sum.BC.BackendUp,
			BackendTotal: sum.BC.BackendTotal, AvgBackendLatencyMs: sum.BC.AvgBackendLatencyMs,
		},
	})
}

// Trend 处理 GET /admin/v1/metrics/trend?namespace=&serverId=&window=1h|6h|24h（或 from=&to= RFC3339）。
func (h *MetricHandler) Trend(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tq := service.TrendQuery{
		Namespace: q.Get("namespace"),
		ServerID:  q.Get("serverId"),
		Window:    q.Get("window"),
	}
	// 自定义时间窗（RFC3339）；任一解析失败即参数错误（不静默忽略）。
	if v := q.Get("from"); v != "" {
		from, err := time.Parse(time.RFC3339, v)
		if err != nil {
			render.WriteError(w, r, apperr.ErrInvalidParam)
			return
		}
		tq.From = from.UTC()
	}
	if v := q.Get("to"); v != "" {
		to, err := time.Parse(time.RFC3339, v)
		if err != nil {
			render.WriteError(w, r, apperr.ErrInvalidParam)
			return
		}
		tq.To = to.UTC()
	}
	points, err := h.svc.Trend(tq)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]trendPointView, 0, len(points))
	for _, p := range points {
		views = append(views, trendPointView{
			SampledAt: p.SampledAt, TotalPlayers: p.TotalPlayers,
			AvgTPS: p.AvgTPS, AvgMemUsed: p.AvgMemUsed, AvgMemMax: p.AvgMemMax, AvgCPULoad: p.AvgCPULoad,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"points": views})
}
