package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// unmatchedRoute 是请求未命中任何 chi 路由模板时使用的固定 route 标签值。
// 绝不能退回 r.URL.Path 兜底：真实路径含 id / serverId / sha256 等高基数取值，
// 每个取值都会生成一条新时序，会瞬间打爆 Prometheus 指标基数。
const unmatchedRoute = "unmatched"

// HTTPMetricsObserver 记录 HTTP 请求维度的 RED 指标；未注入即 no-op。
// 以窄接口反转依赖：server 包只依赖本接口，不 import metrics 包，保持分层单向（见 ADR-0020）。
type HTTPMetricsObserver interface {
	ObserveHTTP(method, route string, status int, seconds float64)
}

// httpMetricsMiddleware 记录每个请求的方法 / 路由模板 / 状态码 / 耗时到 RED 指标。
//
// 装配位置是硬约束：本中间件必须位于 recoverMiddleware **外层**，即
// r.Use(httpMetricsMiddleware(...), recoverMiddleware, traceMiddleware, accessLog)。
// 理由是 recoverMiddleware 捕获 panic 后用它自己那一层的 w 写 500，
// 只有位于它外层的包装器才能观测到这次状态码写入；顺序一旦反了（把本中间件放进 recover 内层），
// panic 产生的 500 会静默不计入指标——指标看着平平稳稳，实际服务在 500。
func httpMetricsMiddleware(obs HTTPMetricsObserver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// 未注入指标观察者（如未装配 metrics 的运行模式）：直连 next，零开销。
		if obs == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			// 复用访问日志同款包装器（status 预置 200，覆盖 handler 直接 Write 隐式写头的情况）。
			// 它已实现 Flush 透传，SSE 等流式端点不会因被包裹而令 w.(http.Flusher) 断言失败（历史事故）。
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			// 路由模板只能在 next 返回后读：chi 是在匹配过程中把 RoutePattern 写进 RouteContext 的。
			obs.ObserveHTTP(r.Method, routePattern(r), sw.status, time.Since(start).Seconds())
		})
	}
}

// routePattern 取 chi 已匹配的路由模板（如 /admin/v2/config-files/{id}）作为 route 标签。
// 未匹配任何路由（404 / 405 / SPA 回退）或不在 chi 上下文内时归一成固定值 unmatchedRoute。
func routePattern(r *http.Request) string {
	rc := chi.RouteContext(r.Context())
	if rc == nil {
		return unmatchedRoute
	}
	if p := rc.RoutePattern(); p != "" {
		return p
	}
	return unmatchedRoute
}
