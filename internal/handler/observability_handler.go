package handler

import (
	"net/http"

	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/service"
)

// ObservabilityHandler 处理控制面自观测 admin 请求（FR-82）：
// DB 连接池 / 长轮询挂起 / 注册表规模 / 命令队列深度。
// 区别于 FR-33 页眉条（SystemHandler，版本 / 运行时长等）与 FR-32 agent 网络负载——这里是控制面进程内部运行态，只读。
type ObservabilityHandler struct {
	svc *service.ObservabilityService
}

// NewObservabilityHandler 构造处理器。
func NewObservabilityHandler(svc *service.ObservabilityService) *ObservabilityHandler {
	return &ObservabilityHandler{svc: svc}
}

// dbPoolView 是数据库连接池统计对外视图（取自 sql.DBStats，非方言）。
type dbPoolView struct {
	MaxOpenConnections int   `json:"maxOpenConnections"`
	OpenConnections    int   `json:"openConnections"`
	InUse              int   `json:"inUse"`
	Idle               int   `json:"idle"`
	WaitCount          int64 `json:"waitCount"`
	WaitDurationMs     int64 `json:"waitDurationMs"`
}

// longpollView 是长轮询四通道挂起数对外视图。
type longpollView struct {
	Config   int `json:"config"`
	File     int `json:"file"`
	Topology int `json:"topology"`
	Command  int `json:"command"`
	Total    int `json:"total"`
}

// observabilityView 是控制面自观测快照对外视图。
type observabilityView struct {
	DBPool           dbPoolView     `json:"dbPool"`
	Longpoll         longpollView   `json:"longpoll"`
	RegistryByStatus map[string]int `json:"registryByStatus"`
	RegistryTotal    int            `json:"registryTotal"`
	CommandByStatus  map[string]int `json:"commandByStatus"`
}

// Observability 处理 GET /admin/v1/system/observability：返回控制面内部运行态快照。
func (h *ObservabilityHandler) Observability(w http.ResponseWriter, _ *http.Request) {
	o := h.svc.Snapshot()
	render.WriteJSON(w, http.StatusOK, observabilityView{
		DBPool: dbPoolView{
			MaxOpenConnections: o.DBPool.MaxOpenConnections,
			OpenConnections:    o.DBPool.OpenConnections,
			InUse:              o.DBPool.InUse,
			Idle:               o.DBPool.Idle,
			WaitCount:          o.DBPool.WaitCount,
			WaitDurationMs:     o.DBPool.WaitDurationMs,
		},
		Longpoll: longpollView{
			Config:   o.Longpoll.Config,
			File:     o.Longpoll.File,
			Topology: o.Longpoll.Topology,
			Command:  o.Longpoll.Command,
			Total:    o.Longpoll.Total,
		},
		RegistryByStatus: o.RegistryByStatus,
		RegistryTotal:    o.RegistryTotal,
		CommandByStatus:  o.CommandByStatus,
	})
}
