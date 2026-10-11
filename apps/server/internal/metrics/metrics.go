// Package metrics 暴露控制面运行指标（Prometheus 文本格式），见 ADR-0020。
// 注册/健康类为 gauge，抓取时读内存注册表快照即时计数（写路径零侵入）；
// 配置发布/推送为 counter，由事件处自增；HTTP 层为 RED 指标（请求计数 + 时延直方图），由全局中间件按请求观察。
// 另含 Go 运行时（go_goroutines / go_gc_* / go_memstats_*）与进程级（process_resident_memory_bytes /
// process_cpu_seconds_total 等）指标：它们并非默认自带——client_golang 的 NewRegistry() 返回的是**空
// registry**、不会像全局 default 那样自动挂默认 collector，必须在 New() 里显式注册，否则 goroutine 泄漏、
// 堆增长、GC 停顿、RSS / CPU 增长在 Prometheus 里完全不可见，这是最容易踩的坑。
// 集中持独立 registry，不污染全局 default。
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// Metrics 持控制面全部运行指标与其独立注册表。
type Metrics struct {
	reg           *prometheus.Registry
	configPublish prometheus.Counter
	pushNotify    prometheus.Counter
	http          httpMetrics // HTTP 层 RED 指标（请求计数 + 时延直方图）
}

// New 构造指标集合：注册注册/健康 gauge collector、发布/推送 counter、HTTP RED 指标，
// 以及 Go 运行时与进程级 collector。全部注册进同一独立 registry，不污染全局 default。
func New(rt *runtime.Registry) *Metrics {
	reg := prometheus.NewRegistry()
	configPublish := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "beacon_config_publish_total",
		Help: "配置发布累计次数（含首次发布、再发布、回滚）",
	})
	pushNotify := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "beacon_push_notify_total",
		Help: "推送（长轮询唤醒）累计触发次数",
	})
	httpM := newHTTPMetrics()
	reg.MustRegister(
		configPublish, pushNotify, httpM.requests, httpM.duration, newRegistryCollector(rt),
		// Go 运行时与进程级指标默认不会随空 registry 自动挂载，必须显式注册：
		// 否则 goroutine / 堆 / GC / RSS / CPU 增长在 Prometheus 完全不可见。
		// v1.24.1 中两构造函数均只返回 prometheus.Collector、不返回 error，
		// 且都是全新 collector 注册进全新空 registry，不存在重名 / 一致性冲突，
		// 因此用 MustRegister 既安全又免去一条不可能触发的错误分支。
		collectors.NewGoCollector(),
		// ProcessCollectorOpts 零值即采集当前进程、空命名空间、ReportErrors=false：
		// 进程指标属“附带观测”，单次采集失败应被忽略而非拖垮整次 /metrics 抓取，故保持默认不报告错误。
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return &Metrics{reg: reg, configPublish: configPublish, pushNotify: pushNotify, http: httpM}
}

// Handler 返回 /metrics 的 HTTP 处理器（仅本注册表的指标）。
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// IncConfigPublish 配置发布计数 +1。
func (m *Metrics) IncConfigPublish() { m.configPublish.Inc() }

// IncPushNotify 推送计数 +1。
func (m *Metrics) IncPushNotify() { m.pushNotify.Inc() }
