package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"beacon/internal/handler"
)

// NewRouter 装配 HTTP 路由：admin API + 内嵌前端（SPA 回退）。
// 中间件自外向内：recover → traceId → 访问日志。
func NewRouter(nsHandler *handler.NamespaceHandler, configHandler *handler.ConfigHandler, webHandler http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverMiddleware, traceMiddleware, accessLog)

	r.Route("/admin/v1", func(r chi.Router) {
		r.Get("/namespaces", nsHandler.List)
		r.Post("/namespaces", nsHandler.Create)

		// 配置中心
		r.Get("/configs", configHandler.List)
		r.Post("/configs", configHandler.Create)
		r.Get("/configs/{id}", configHandler.Get)
		r.Put("/configs/{id}", configHandler.Publish)
		r.Delete("/configs/{id}", configHandler.Delete)
		r.Get("/configs/{id}/revisions", configHandler.ListRevisions)
		r.Get("/configs/{id}/revisions/{version}", configHandler.GetRevision)
		r.Post("/configs/{id}/rollback", configHandler.Rollback)
		r.Get("/configs/{id}/diff", configHandler.Diff)
	})

	// 非 API、非静态文件的路径交给内嵌前端（含 SPA history 回退）
	r.NotFound(webHandler.ServeHTTP)
	return r
}
