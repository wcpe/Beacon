package handler

import (
	"net/http"
	"time"

	"beacon/internal/render"
	"beacon/internal/service"
)

// SystemHandler 处理控制面自身状态 admin 请求（FR-33）：
// 版本 / 运行时长 / DB 连通 / 在线实例数 / 采样器状态 / Go 运行时资源。
// 区别于 FR-32 的 agent 网络聚合指标——这里是控制面进程本身的健康。
type SystemHandler struct {
	svc *service.SystemService
}

// NewSystemHandler 构造处理器。
func NewSystemHandler(svc *service.SystemService) *SystemHandler {
	return &SystemHandler{svc: svc}
}

// dbStatusView 是数据库连通性对外视图。
type dbStatusView struct {
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

// runtimeStatsView 是 Go 运行时资源对外视图（字节单位由前端格式化）。
type runtimeStatsView struct {
	Goroutines int    `json:"goroutines"`
	HeapAlloc  uint64 `json:"heapAlloc"`
	HeapSys    uint64 `json:"heapSys"`
}

// systemStatusView 是控制面自身状态对外视图。
type systemStatusView struct {
	Version         string           `json:"version"`
	StartedAt       time.Time        `json:"startedAt"`
	UptimeSeconds   int64            `json:"uptimeSeconds"`
	DB              dbStatusView     `json:"db"`
	OnlineInstances int              `json:"onlineInstances"`
	SamplerEnabled  bool             `json:"samplerEnabled"`
	Runtime         runtimeStatsView `json:"runtime"`
	// CPUAvailable=false 表示进程 CPU% 不可用（未引入额外依赖，见 FR-33）；为 true 时 CPUPercent 才有意义。
	CPUAvailable bool    `json:"cpuAvailable"`
	CPUPercent   float64 `json:"cpuPercent"`
}

// Status 处理 GET /admin/v1/system/status：返回控制面自身状态快照。
func (h *SystemHandler) Status(w http.ResponseWriter, _ *http.Request) {
	st := h.svc.Status()
	render.WriteJSON(w, http.StatusOK, systemStatusView{
		Version:         st.Version,
		StartedAt:       st.StartedAt,
		UptimeSeconds:   st.UptimeSeconds,
		DB:              dbStatusView{Connected: st.DB.Connected, Error: st.DB.Error},
		OnlineInstances: st.OnlineInstances,
		SamplerEnabled:  st.SamplerEnabled,
		Runtime: runtimeStatsView{
			Goroutines: st.Runtime.Goroutines,
			HeapAlloc:  st.Runtime.HeapAlloc,
			HeapSys:    st.Runtime.HeapSys,
		},
		CPUAvailable: st.CPUAvailable,
		CPUPercent:   0, // 不可用时恒 0；CPUAvailable=false 时前端展示「不可用」
	})
}
