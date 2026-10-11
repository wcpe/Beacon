package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// httpRequestLabels 请求计数指标（beacon_http_requests_total）的标签顺序：方法 / 路由模板 / 状态码。
// 路由模板取 chi 的 RoutePattern（如 /admin/v2/config-files/{id}）而**非**真实路径，
// 否则 id / serverId 等高基数取值会瞬间打爆 Prometheus 时序数。
var httpRequestLabels = []string{"method", "route", "status"}

// httpDurationLabels 时延直方图（beacon_http_request_duration_seconds）的标签顺序：方法 / 路由模板。
var httpDurationLabels = []string{"method", "route"}

// httpDurationBuckets 请求时延直方图分桶（秒），覆盖 1ms → 60s。
// 必须显式给全：控制面有最长 30s 的长轮询与更长的流式传输，若沿用 Prometheus 默认分桶（止于 10s），
// 这些请求会全部堆进 +Inf 桶，histogram_quantile 算出的 p95 / p99 会被上界拉平、完全失真。
var httpDurationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// httpMetrics 持 HTTP 层 RED 指标：请求计数与请求时延分布（直方图，供算分位数）。
type httpMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// newHTTPMetrics 构造 HTTP RED 指标对象；注册由 New 统一完成，保持单一独立 registry。
func newHTTPMetrics() httpMetrics {
	return httpMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "beacon_http_requests_total",
			Help: "HTTP 请求累计数（按方法 / 路由模板 / 状态码分维）",
		}, httpRequestLabels),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "beacon_http_request_duration_seconds",
			Help:    "HTTP 请求耗时分布（按方法 / 路由模板分维，单位秒）",
			Buckets: httpDurationBuckets,
		}, httpDurationLabels),
	}
}

// ObserveHTTP 记录一次 HTTP 请求的方法、路由模板、状态码与耗时（HTTP RED 指标）。
// route 必须是 chi 路由模板或固定兜底值（未匹配时为 unmatched），不得传真实请求路径。
func (m *Metrics) ObserveHTTP(method, route string, status int, seconds float64) {
	m.http.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.http.duration.WithLabelValues(method, route).Observe(seconds)
}
