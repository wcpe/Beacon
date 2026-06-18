// Package metrics 暴露控制面运行指标（Prometheus 文本格式），见 ADR-0020。
// 注册/健康类为 gauge，抓取时读内存注册表快照即时计数（写路径零侵入）；
// 配置发布/推送为 counter，由事件处自增。集中持独立 registry，不污染全局 default。
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"beacon/internal/runtime"
)

// Metrics 持控制面全部运行指标与其独立注册表。
type Metrics struct {
	reg           *prometheus.Registry
	configPublish prometheus.Counter
	pushNotify    prometheus.Counter
}

// New 构造指标集合：注册注册/健康 gauge collector 与发布/推送 counter。
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
	reg.MustRegister(configPublish, pushNotify, newRegistryCollector(rt))
	return &Metrics{reg: reg, configPublish: configPublish, pushNotify: pushNotify}
}

// Handler 返回 /metrics 的 HTTP 处理器（仅本注册表的指标）。
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// IncConfigPublish 配置发布计数 +1。
func (m *Metrics) IncConfigPublish() { m.configPublish.Inc() }

// IncPushNotify 推送计数 +1。
func (m *Metrics) IncPushNotify() { m.pushNotify.Inc() }
