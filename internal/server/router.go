package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"beacon/internal/handler"
)

// Handlers 汇集各 HTTP 处理器，供路由装配（避免过长的位置参数）。
type Handlers struct {
	Namespace *handler.NamespaceHandler
	Config    *handler.ConfigHandler
	Agent     *handler.AgentHandler
	Instance  *handler.InstanceHandler
	Zone      *handler.ZoneHandler
	Web       http.Handler
}

// NewRouter 装配 HTTP 路由：agent API（挂 token）+ admin API + 内嵌前端（SPA 回退）。
// 中间件自外向内：recover → traceId → 访问日志。
func NewRouter(h Handlers, agentToken string) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverMiddleware, traceMiddleware, accessLog)

	// agent 侧：内网信任，仅以共享 token 防误连
	r.Route("/beacon/v1/agent", func(r chi.Router) {
		r.Use(agentTokenMiddleware(agentToken))
		r.Post("/register", h.Agent.Register)
		r.Post("/heartbeat", h.Agent.Heartbeat)
		r.Post("/report", h.Agent.Report)
		r.Get("/discovery", h.Agent.Discover)
	})

	// admin 侧
	r.Route("/admin/v1", func(r chi.Router) {
		r.Get("/namespaces", h.Namespace.List)
		r.Post("/namespaces", h.Namespace.Create)

		// 配置中心
		r.Get("/configs", h.Config.List)
		r.Post("/configs", h.Config.Create)
		r.Get("/configs/{id}", h.Config.Get)
		r.Put("/configs/{id}", h.Config.Publish)
		r.Delete("/configs/{id}", h.Config.Delete)
		r.Get("/configs/{id}/revisions", h.Config.ListRevisions)
		r.Get("/configs/{id}/revisions/{version}", h.Config.GetRevision)
		r.Post("/configs/{id}/rollback", h.Config.Rollback)
		r.Get("/configs/{id}/diff", h.Config.Diff)

		// 实例与健康
		r.Get("/instances", h.Instance.List)
		r.Get("/instances/{serverId}", h.Instance.Get)
		r.Post("/instances/{serverId}/offline", h.Instance.Offline)

		// zone 分配
		r.Get("/zones/assignments", h.Zone.ListAssignments)
		r.Put("/zones/assignments", h.Zone.Assign)
		r.Delete("/zones/assignments", h.Zone.Unassign)
		r.Get("/zones", h.Zone.Summary)
	})

	// 非 API、非静态文件的路径交给内嵌前端（含 SPA history 回退）
	r.NotFound(h.Web.ServeHTTP)
	return r
}
