package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/metrics"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// observedCall 是一次 ObserveHTTP 调用被记录的维度（耗时不在断言范围）。
type observedCall struct {
	method string
	route  string
	status int
}

// recordingObserver 是 HTTPMetricsObserver 的测试桩：收集调用，供断言中间件写入的维度。
type recordingObserver struct {
	calls []observedCall
}

func (o *recordingObserver) ObserveHTTP(method, route string, status int, _ float64) {
	o.calls = append(o.calls, observedCall{method: method, route: route, status: status})
}

// onlyCall 断言恰好记录一次并返回该次调用。
func (o *recordingObserver) onlyCall(t *testing.T) observedCall {
	t.Helper()
	if len(o.calls) != 1 {
		t.Fatalf("ObserveHTTP 调用次数 = %d，期望 1（实际 %+v）", len(o.calls), o.calls)
	}
	return o.calls[0]
}

// doGet 向测试路由发一次请求并返回响应记录器。
func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestHTTPMetricsMiddlewareRecordsRouteTemplate 验证成功请求被记录，且 route 取**路由模板**而非真实路径
// （真实路径含高基数 id，当标签会把 Prometheus 时序打爆）。
func TestHTTPMetricsMiddlewareRecordsRouteTemplate(t *testing.T) {
	obs := &recordingObserver{}
	r := chi.NewRouter()
	r.Use(httpMetricsMiddleware(obs))
	r.Get("/admin/v2/config-files/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if got := doGet(t, r, "/admin/v2/config-files/7f3a-real-uuid").Code; got != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", got)
	}
	call := obs.onlyCall(t)
	if call.route != "/admin/v2/config-files/{id}" {
		t.Fatalf("route 标签 = %q，期望路由模板 %q", call.route, "/admin/v2/config-files/{id}")
	}
	if call.method != http.MethodGet || call.status != http.StatusOK {
		t.Fatalf("方法 / 状态 = %s / %d，期望 GET / 200", call.method, call.status)
	}
}

// TestHTTPMetricsMiddlewareRecordsUnmatchedRoute 验证未匹配任何路由时 route 归一成固定兜底值 unmatched，
// 绝不回落到含高基数取值的真实路径。
func TestHTTPMetricsMiddlewareRecordsUnmatchedRoute(t *testing.T) {
	obs := &recordingObserver{}
	r := chi.NewRouter()
	r.Use(httpMetricsMiddleware(obs))
	r.Get("/only", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if got := doGet(t, r, "/admin/v2/config-files/7f3a-real-uuid").Code; got != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", got)
	}
	call := obs.onlyCall(t)
	if call.route != unmatchedRoute {
		t.Fatalf("未匹配路由的 route 标签 = %q，期望兜底值 %q", call.route, unmatchedRoute)
	}
	if call.status != http.StatusNotFound {
		t.Fatalf("状态码标签 = %d，期望 404", call.status)
	}
}

// TestHTTPMetricsMiddlewareRecordsRecoveredPanicAs500 锁定中间件装配顺序：指标中间件必须在 recoverMiddleware
// **外层**，才能观测到 recover 写出的 500。用例顺序与 NewRouter 中 r.Use(...) 逐字一致。
func TestHTTPMetricsMiddlewareRecordsRecoveredPanicAs500(t *testing.T) {
	obs := &recordingObserver{}
	r := chi.NewRouter()
	r.Use(httpMetricsMiddleware(obs), recoverMiddleware)
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("测试用 panic") })

	if got := doGet(t, r, "/boom").Code; got != http.StatusInternalServerError {
		t.Fatalf("panic 请求状态码 = %d，期望 500", got)
	}
	call := obs.onlyCall(t)
	if call.status != http.StatusInternalServerError {
		t.Fatalf("panic 请求的状态码标签 = %d，期望 500（顺序反了会漏计）", call.status)
	}
	if call.route != "/boom" {
		t.Fatalf("panic 请求的 route 标签 = %q，期望 %q", call.route, "/boom")
	}
}

// TestHTTPMetricsMiddlewareNilObserverIsNoOp 验证未注入观察者时请求照常处理且不 panic（ADR-0020：未注入即 no-op）。
func TestHTTPMetricsMiddlewareNilObserverIsNoOp(t *testing.T) {
	r := chi.NewRouter()
	r.Use(httpMetricsMiddleware(nil))
	r.Get("/plain", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	if got := doGet(t, r, "/plain").Code; got != http.StatusNoContent {
		t.Fatalf("状态码 = %d，期望 204", got)
	}
}

// TestHTTPMetricsMiddlewareRecordsNon2xx 验证非 2xx 状态码被正确记录。
// 此处用组内中间件短路（401）模拟生产 adminAuthMiddleware 的未认证分支：chi 的匹配是**分段**的，
// 组内中间件短路时内层路由匹配尚未发生，RoutePattern 停在组通配 "/admin/v2/*"（固定值、基数有界）。
func TestHTTPMetricsMiddlewareRecordsNon2xx(t *testing.T) {
	obs := &recordingObserver{}
	r := chi.NewRouter()
	r.Use(httpMetricsMiddleware(obs))
	r.Route("/admin/v2", func(r chi.Router) {
		// 简化版鉴权桩：无令牌直接 401 短路，语义同 adminAuthMiddleware 的未认证分支。
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("X-Test-Token") == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, req)
			})
		})
		r.Get("/config-files/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	if got := doGet(t, r, "/admin/v2/config-files/7f3a-real-uuid").Code; got != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", got)
	}
	call := obs.onlyCall(t)
	if call.status != http.StatusUnauthorized {
		t.Fatalf("状态码标签 = %d，期望 401", call.status)
	}
	if call.route != "/admin/v2/*" {
		t.Fatalf("短路请求的 route 标签 = %q，期望组通配 %q（固定值，不含真实路径）", call.route, "/admin/v2/*")
	}
}

// TestNewRouterHTTPMetricsObservesSPAPanicAs500 用**生产装配**的 NewRouter 做防回归：
// 未匹配路径落到 SPA 回退（h.Web），其 panic 由 recoverMiddleware 写成 500，
// 该 500 必须被指标中间件观测到——路由器里 r.Use 的顺序一旦被调换，本用例即失败。
func TestNewRouterHTTPMetricsObservesSPAPanicAs500(t *testing.T) {
	obs := &recordingObserver{}
	router := NewRouter(Handlers{
		HTTPMetrics: obs,
		Web: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("SPA 回退测试用 panic")
		}),
	}, "", nil, nil, nil)

	if got := doGet(t, router, "/spa-fallback-miss/7f3a").Code; got != http.StatusInternalServerError {
		t.Fatalf("SPA panic 请求状态码 = %d，期望 500", got)
	}
	call := obs.onlyCall(t)
	if call.status != http.StatusInternalServerError {
		t.Fatalf("SPA panic 请求的状态码标签 = %d，期望 500", call.status)
	}
	if call.route != unmatchedRoute {
		t.Fatalf("SPA 回退请求的 route 标签 = %q，期望兜底值 %q", call.route, unmatchedRoute)
	}
}

// noopObserver 是零开销观测桩：用于单独量化中间件包装本身的成本（不含指标写入）。
type noopObserver struct{}

func (noopObserver) ObserveHTTP(string, string, int, float64) {}

// BenchmarkHTTPMetricsMiddlewarePassthrough 量化未注入观察者时的直连开销（应接近零，即 no-op 真成立）。
func BenchmarkHTTPMetricsMiddlewarePassthrough(b *testing.B) {
	h := httpMetricsMiddleware(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/system/status", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// BenchmarkHTTPMetricsMiddleware 量化 RED 指标中间件在请求热路径上的每请求成本。
// 这个数字决定「接指标」值不值得：它必须是常数级、且不随并发与路由数增长。
// 注意此处刻意不含真实指标写入（CounterVec / HistogramVec 的原子操作）：
// 那部分由 internal/metrics 包自己的基准覆盖，此处只回答「这个中间件包装了之后多花了多少」。
func BenchmarkHTTPMetricsMiddleware(b *testing.B) {
	h := httpMetricsMiddleware(noopObserver{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/system/status", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// BenchmarkHTTPMetricsMiddlewareRealObserver 在前一条之外再含**真实指标写入**
// （CounterVec / HistogramVec 的标签查找与观察），给出「接入 RED 指标」在请求热路径上的完整成本。
// 它与前一条的差值就是指标写入本身的代价；与压测实测的每请求 CPU（约 1ms / 请求）对照，
// 回答「为了可观测性到底付了多少」——结论必须是以毫微秒计的常数，否则说明中间件写错了。
func BenchmarkHTTPMetricsMiddlewareRealObserver(b *testing.B) {
	m := metrics.New(runtime.NewRegistry())
	h := httpMetricsMiddleware(m)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/system/status", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}
