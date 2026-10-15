package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// SelfHealth 是控制面自身健康的一份快照（FR-82 的数据面投影，见 ADR-0091）。
//
// 全部字段都是**抓取时立即可得**的值，不含需要查库的项——原因见 selfHealthCollector 的说明。
type SelfHealth struct {
	// DBPool 数据库连接池统计
	DBPoolMaxOpen      int
	DBPoolOpen         int
	DBPoolInUse        int
	DBPoolIdle         int
	DBPoolWaitCount    int64
	DBPoolWaitDuration float64 // 秒（Prometheus 惯例用基本单位，非毫秒）

	// Longpoll 长轮询四通道挂起数
	LongpollConfig   int
	LongpollFile     int
	LongpollTopology int
	LongpollCommand  int

	// CommandByStatus 命令队列按状态计数。**由后台周期刷新写入**（见 SetCommandByStatus），
	// 抓取时不查库——该值源自一次 DB GROUP BY，若放在抓取路径上，等于让外部请求速率
	// 直接驱动数据库查询（端点默认无鉴权且不限流）。
	CommandByStatus map[string]int

	// MetricWriteDiscarded 指标异步写入累计丢弃行数（FR-144）：多次重试仍失败被丢弃的行。
	MetricWriteDiscarded int64

	// WriteQueueDepth 异步日表写入通道各路由的当前队列深度（按路由种类）。
	// 这是 429 背压**出现之前**唯一的预警信号——此前运维只能在数据已被丢弃后才知道出过问题。
	WriteQueueDepth map[string]int
	// WriteQueueCapacity 各路由队列容量（各路由一致），供计算水位占比。
	WriteQueueCapacity int
}

// SelfHealthCollector 把控制面自身健康暴露为 Prometheus 指标。
//
// 设计要点（见 ADR-0091）：
//   - **抓取路径不触达 DB**：命令队列深度源自一次 DB GROUP BY，若在 Collect 里查库，
//     每次 scrape 都会打一次库。而抓取端点默认无鉴权、且不设限流，等于把数据库查询
//     暴露成「任何能访问该端口的人都能按任意频率触发」的负载源。故该值改为后台周期刷新
//     写入缓存，Collect 只读缓存。
//   - **内存来源抓取时现读**：连接池统计、长轮询挂起数、丢弃计数都是进程内立即可得的，
//     无需缓存，现读反而更准（不会滞后一个刷新周期）。
//   - **并发安全**：后台刷新与抓取并发发生，缓存字段用 RWMutex 保护。
type SelfHealthCollector struct {
	// mu 保护 cmdByStatus 与 cmdSnapshotUnix（后台刷新写、抓取读）。其余字段不在此保护范围：
	// 它们经 live 回调在抓取时现读，由各自的真源保证并发安全（sql.DBStats 与 Hub 内部均加锁）。
	mu              sync.RWMutex
	cmdByStatus     map[string]int
	cmdSnapshotUnix float64 // 命令队列缓存最后一次成功刷新的 Unix 秒；0 = 从未成功
	// live 同样受 mu 保护：它在启动期经 SetSelfHealthSource 写入，而抓取随时可能并发读。
	// 不保护会构成 data race（-race 可检出），且在启动瞬间的抓取可能读到 nil 而 panic。
	live func() SelfHealth
}

// NewSelfHealthCollector 构造 collector。live 在每次抓取时被调用，用于取内存类字段
// （连接池 / 长轮询 / 丢弃计数）；命令队列计数初始为空，由后台刷新经 SetCommandByStatus 填充。
func NewSelfHealthCollector(live func() SelfHealth) *SelfHealthCollector {
	if live == nil {
		live = func() SelfHealth { return SelfHealth{} }
	}
	return &SelfHealthCollector{cmdByStatus: map[string]int{}, live: live}
}

// SetLiveSource 替换内存类字段来源（启动期注入；并发安全）。
func (c *SelfHealthCollector) SetLiveSource(live func() SelfHealth) {
	if live == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live = live
}

// liveSnapshot 在读锁下取来源并调用，避免与 SetLiveSource 竞争。
func (c *SelfHealthCollector) liveSnapshot() SelfHealth {
	c.mu.RLock()
	live := c.live
	c.mu.RUnlock()
	if live == nil {
		return SelfHealth{}
	}
	return live()
}

// SetCommandByStatus 由后台刷新器周期调用，替换命令队列计数缓存；nowUnix 为本轮刷新时刻（Unix 秒）。
//
// 传 nil 表示本轮采集失败（如查库出错）：**保留上一轮的值而不是清空**。
// 理由：清空会让看板上的 series 突然消失，被误读成"队列空了"（假好消息）；
// 保留旧值虽有过时风险，但 beacon_self_commands_snapshot_timestamp_seconds 会停在旧时刻，
// 可用 `time() - ...` 直接判断数据新鲜度，比 series 消失更容易发现异常。
func (c *SelfHealthCollector) SetCommandByStatus(byStatus map[string]int, nowUnix float64) {
	if byStatus == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cmdByStatus = byStatus
	c.cmdSnapshotUnix = nowUnix
}

// Snapshot 返回当前快照（内存类字段现读，命令队列取缓存）。
func (c *SelfHealthCollector) Snapshot() SelfHealth {
	s := c.liveSnapshot()
	byStatus, _ := c.commandCache()
	s.CommandByStatus = byStatus
	return s
}

// commandCache 在**同一把读锁**下返回命令队列计数与最后一次成功刷新的 Unix 秒。
//
// 两者必须一起取：若分两次取锁，后台刷新可能恰好插在中间，导致输出的计数与时间戳
// 来自不同轮次（时间戳比计数新），让「数据陈旧度」这个判据失真。
// 返回的 map 是副本，调用方可安全持有。
func (c *SelfHealthCollector) commandCache() (map[string]int, float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]int, len(c.cmdByStatus))
	for k, v := range c.cmdByStatus {
		out[k] = v
	}
	return out, c.cmdSnapshotUnix
}

// 自身健康指标名。统一 beacon_self_ 前缀，与业务指标（beacon_instances_*）区分：
// 前者描述「控制面这个进程自己怎么样」，后者描述「被它管理的实例怎么样」。
var (
	selfDBPoolDesc = prometheus.NewDesc(
		"beacon_self_db_pool_connections",
		"控制面数据库连接池连接数（按 state 维度：max_open/open/in_use/idle）",
		[]string{"state"}, nil,
	)
	selfDBWaitCountDesc = prometheus.NewDesc(
		"beacon_self_db_pool_wait_total",
		"控制面累计等待数据库连接的次数（池被占满即增长，是池枯竭的直接信号）",
		nil, nil,
	)
	selfDBWaitDurationDesc = prometheus.NewDesc(
		"beacon_self_db_pool_wait_duration_seconds_total",
		"控制面累计等待数据库连接的总时长（秒）",
		nil, nil,
	)
	selfLongpollDesc = prometheus.NewDesc(
		"beacon_self_longpoll_waiters",
		"控制面长轮询各通道当前挂起的 waiter 数（按 channel 维度）",
		[]string{"channel"}, nil,
	)
	selfCommandDesc = prometheus.NewDesc(
		"beacon_self_commands",
		"控制面命令队列深度（按 status 维度）。由后台周期刷新，非抓取时查库",
		[]string{"status"}, nil,
	)
	selfMetricDiscardedDesc = prometheus.NewDesc(
		"beacon_self_metric_write_discarded_total",
		"控制面指标异步写入累计丢弃行数（多次重试仍失败）。非零即代表数据已丢失",
		nil, nil,
	)
	selfCmdSnapshotDesc = prometheus.NewDesc(
		"beacon_self_commands_snapshot_timestamp_seconds",
		"命令队列计数缓存最后一次成功刷新的 Unix 秒。0 表示从未成功；"+
			"`time() - 本值` 即数据陈旧度，用于区分「队列真的空了」与「采集失败导致值不更新」",
		nil, nil,
	)
	selfWriteQueueDepthDesc = prometheus.NewDesc(
		"beacon_self_write_queue_depth",
		"控制面异步日表写入通道各路由的当前队列深度（按 route 维度）。"+
			"接近容量即说明消费跟不上生产，是 429 背压出现前的预警信号",
		[]string{"route"}, nil,
	)
	selfWriteQueueCapacityDesc = prometheus.NewDesc(
		"beacon_self_write_queue_capacity",
		"控制面异步日表写入通道各路由的队列容量（各路由一致，见 AsyncDailyWriter.RegisterFlusher："+
			"所有路由共用同一个 queueCapacity，故无需 route 维度）。与 depth 相除即水位占比",
		nil, nil,
	)
)

// Describe 输出本 collector 的全部指标描述。
func (c *SelfHealthCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- selfDBPoolDesc
	ch <- selfDBWaitCountDesc
	ch <- selfDBWaitDurationDesc
	ch <- selfLongpollDesc
	ch <- selfCommandDesc
	ch <- selfMetricDiscardedDesc
	ch <- selfCmdSnapshotDesc
	ch <- selfWriteQueueDepthDesc
	ch <- selfWriteQueueCapacityDesc
}

// Collect 输出自身健康指标。注意：**本方法不查库**（见类型注释）。
func (c *SelfHealthCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.liveSnapshot()
	// 命令队列计数与时间戳一起取，保证两者同轮次（见 commandCache）。
	byStatus, snapshotUnix := c.commandCache()

	// 连接池四个状态都输出（含 0）：便于监控侧看到完整曲线，也避免某状态归零时 series 消失。
	for state, v := range map[string]int{
		"max_open": s.DBPoolMaxOpen,
		"open":     s.DBPoolOpen,
		"in_use":   s.DBPoolInUse,
		"idle":     s.DBPoolIdle,
	} {
		ch <- prometheus.MustNewConstMetric(selfDBPoolDesc, prometheus.GaugeValue, float64(v), state)
	}
	ch <- prometheus.MustNewConstMetric(selfDBWaitCountDesc, prometheus.CounterValue, float64(s.DBPoolWaitCount))
	ch <- prometheus.MustNewConstMetric(selfDBWaitDurationDesc, prometheus.CounterValue, s.DBPoolWaitDuration)

	for channel, v := range map[string]int{
		"config":   s.LongpollConfig,
		"file":     s.LongpollFile,
		"topology": s.LongpollTopology,
		"command":  s.LongpollCommand,
	} {
		ch <- prometheus.MustNewConstMetric(selfLongpollDesc, prometheus.GaugeValue, float64(v), channel)
	}

	// 命令队列按状态输出。已声明但当前为 0 的状态也输出，使曲线连续（同连接池口径）。
	for _, status := range commandStatuses {
		ch <- prometheus.MustNewConstMetric(selfCommandDesc, prometheus.GaugeValue, float64(byStatus[status]), status)
	}
	// 不在已知枚举内的状态**照常输出**：未知状态意味着出现了代码未预期的取值，
	// 静默丢弃会让它永远不被发现；输出后看板上会直接多出一条 series。
	for status, v := range byStatus {
		if !isKnownCommandStatus(status) {
			ch <- prometheus.MustNewConstMetric(selfCommandDesc, prometheus.GaugeValue, float64(v), status)
		}
	}

	ch <- prometheus.MustNewConstMetric(selfMetricDiscardedDesc, prometheus.CounterValue, float64(s.MetricWriteDiscarded))
	ch <- prometheus.MustNewConstMetric(selfCmdSnapshotDesc, prometheus.GaugeValue, snapshotUnix)

	// 写入队列：逐路由输出深度，并输出容量（便于算水位占比）。
	// 未注册任何路由时两者都不输出（容量为 0 时输出 0 无意义、且会误导成"队列已满"）。
	if s.WriteQueueCapacity > 0 {
		for route, depth := range s.WriteQueueDepth {
			ch <- prometheus.MustNewConstMetric(selfWriteQueueDepthDesc, prometheus.GaugeValue, float64(depth), route)
		}
		ch <- prometheus.MustNewConstMetric(selfWriteQueueCapacityDesc, prometheus.GaugeValue, float64(s.WriteQueueCapacity))
	}
}

// commandStatuses 是命令生命周期的已知状态（与 model.CommandStatus* 常量同源）。
//
// 此处**刻意复制字面量而非 import model**：metrics 包当前只依赖 runtime（见包注释的
// 分层约定），为一个枚举引入 model 依赖会让这个底层观测包耦合到数据模型层。
// 有单测锁定该清单与 model 的常量一致，防止两边漂移（漂移会让某状态静默缺失）。
var commandStatuses = []string{"pending", "fetched", "ready", "done", "failed", "expired"}

func isKnownCommandStatus(s string) bool {
	for _, k := range commandStatuses {
		if k == s {
			return true
		}
	}
	return false
}
