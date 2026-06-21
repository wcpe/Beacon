package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/wcpe/Beacon/internal/runtime"
)

// registryCollector 在抓取时读取内存注册表快照，即时导出注册数与健康状态分布。
// 采用 pull 模型：不在注册/心跳/扫描写路径埋点，运行态写路径零侵入（见 ADR-0020）。
type registryCollector struct {
	rt           *runtime.Registry
	registeredVa *prometheus.Desc // 注册实例数（namespace, role）
	statusVa     *prometheus.Desc // 健康状态分布（status）
}

// newRegistryCollector 构造注册/健康 gauge collector。
func newRegistryCollector(rt *runtime.Registry) *registryCollector {
	return &registryCollector{
		rt: rt,
		registeredVa: prometheus.NewDesc(
			"beacon_instances_registered",
			"当前注册实例数（按 namespace、role 维度）",
			[]string{"namespace", "role"}, nil,
		),
		statusVa: prometheus.NewDesc(
			"beacon_instances_status",
			"当前实例健康状态分布（online/degraded/lost/offline 各计数）",
			[]string{"status"}, nil,
		),
	}
}

// Describe 输出本 collector 的指标描述。
func (c *registryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.registeredVa
	ch <- c.statusVa
}

// Collect 抓取时读取注册表快照并按维度聚合导出。
func (c *registryCollector) Collect(ch chan<- prometheus.Metric) {
	insts := c.rt.List(runtime.Filter{})
	// 注册数：按 (namespace, role) 聚合
	byNsRole := map[[2]string]int{}
	// 状态分布：四态均输出（含 0），便于监控侧看到完整曲线
	byStatus := map[string]int{
		runtime.StatusOnline:   0,
		runtime.StatusDegraded: 0,
		runtime.StatusLost:     0,
		runtime.StatusOffline:  0,
	}
	for _, i := range insts {
		byNsRole[[2]string{i.Namespace, i.Role}]++
		byStatus[i.Status]++
	}
	for k, v := range byNsRole {
		ch <- prometheus.MustNewConstMetric(c.registeredVa, prometheus.GaugeValue, float64(v), k[0], k[1])
	}
	for status, v := range byStatus {
		ch <- prometheus.MustNewConstMetric(c.statusVa, prometheus.GaugeValue, float64(v), status)
	}
}
