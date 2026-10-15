package service

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	rt "github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// dbStatsProvider 是自观测对数据库连接池统计的窄依赖：仅需 sql.DBStats。
// 由 *sql.DB（经 gorm DB() 获取，与 FR-33 同一连接池）实现，测试以替身覆盖。
type dbStatsProvider interface {
	Stats() sql.DBStats
}

// waiterCounter 是自观测对单条长轮询通道挂起数的窄依赖：仅需当前挂起 waiter 数。
// 由 *longpoll.Hub 的 WaiterCount 实现，测试以替身覆盖。
type waiterCounter interface {
	WaiterCount() int
}

// commandCounter 是自观测对命令队列深度的窄依赖：按状态分组计数。
// 由 *repository.AgentCommandRepository 的 CountByStatus 实现，测试以替身覆盖。
type commandCounter interface {
	CountByStatus() (map[string]int, error)
}

// metricWriteDiscardCounter 是自观测对异步日表写入累计丢弃行数的窄依赖（FR-144）：
// 由 *AsyncDailyWriter 的 Discarded 实现（多次重试仍失败被丢弃的行，全路由合计）。可选注入，未注入即 0。
type metricWriteDiscardCounter interface {
	Discarded() int64
}

// DBPoolStats 是数据库连接池统计快照（仅观测，FR-82）。
// 取自 sql.DBStats（database/sql 通用、非方言），字段语义与标准库一致。
type DBPoolStats struct {
	MaxOpenConnections int   // 连接池上限（0 表示无限）
	OpenConnections    int   // 当前已建连接数（使用中 + 空闲）
	InUse              int   // 使用中连接数
	Idle               int   // 空闲连接数
	WaitCount          int64 // 累计等待连接的次数
	WaitDurationMs     int64 // 累计等待连接的总时长（毫秒）
}

// LongpollStats 是长轮询四通道当前挂起 waiter 数快照（仅观测，FR-82）。
type LongpollStats struct {
	Config   int // 配置通道挂起数
	File     int // 文件树通道挂起数
	Topology int // 拓扑 watch 通道挂起数
	Command  int // 命令待办通道挂起数
	Total    int // 四通道合计
}

// Observability 是控制面内部运行态自观测快照（FR-82）：
// DB 连接池 / 长轮询挂起 / 注册表规模（按健康状态） / 命令队列深度（按状态）。
// 区别于 FR-33 页眉条（版本 / 运行时长等）与 FR-32 agent 网络负载——这里是控制面进程内部健康。
type Observability struct {
	DBPool           DBPoolStats    // 数据库连接池统计
	Longpoll         LongpollStats  // 长轮询四通道挂起数
	RegistryByStatus map[string]int // 注册表按健康状态计数（online/degraded/lost/offline，缺省键不返回）
	RegistryTotal    int            // 注册表实例总数
	CommandByStatus  map[string]int // 命令队列按状态计数（pending/fetched/... 缺省键不返回）
	// 指标异步写入累计丢弃行数（FR-144）：多次重试仍失败被丢弃的行；未注入写入池时恒 0。
	MetricWriteDiscarded int64
}

// ObservabilityService 聚合控制面内部自观测指标（FR-82）。
// 不持 GORM：DB 连接池经窄接口取 sql.DBStats；注册表规模读内存注册表；
// 长轮询挂起数读各 Hub；命令队列经命令仓库的分组计数。命令计数失败优雅降级（空 map + WARN），不阻断快照。
type ObservabilityService struct {
	dbStats        dbStatsProvider
	registry       *rt.Registry
	configHub      waiterCounter
	fileHub        waiterCounter
	topologyHub    waiterCounter
	commandHub     waiterCounter
	commandCounter commandCounter
	// 指标异步写入丢弃计数（FR-144）：可选注入（SetMetricWriteDiscardCounter），未注入即 0。
	metricDiscard metricWriteDiscardCounter
}

// NewObservabilityService 构造服务（启动处装配同一连接池 / 注册表 / 四条长轮询 Hub / 命令仓库）。
func NewObservabilityService(
	dbStats dbStatsProvider,
	registry *rt.Registry,
	configHub, fileHub, topologyHub, commandHub waiterCounter,
	commandCounter commandCounter,
) *ObservabilityService {
	return &ObservabilityService{
		dbStats:        dbStats,
		registry:       registry,
		configHub:      configHub,
		fileHub:        fileHub,
		topologyHub:    topologyHub,
		commandHub:     commandHub,
		commandCounter: commandCounter,
	}
}

// SetMetricWriteDiscardCounter 注入指标异步写入丢弃计数源（FR-144）：启动处把写入池接上，
// 使 /system 自观测能看见「入库多次重试仍失败被丢弃了多少行」（错误不静默，ADR-0057）。
func (s *ObservabilityService) SetMetricWriteDiscardCounter(c metricWriteDiscardCounter) {
	s.metricDiscard = c
}

// Snapshot 采集一次自观测快照。各来源均为只读：连接池统计、内存注册表计数、Hub 挂起数同步取；
// 命令队列计数走一次 DB GROUP BY（低频自观测页，非热路径），失败时降级为空 map 并告警，不阻断其余指标。
func (s *ObservabilityService) Snapshot() Observability {
	snap := s.SnapshotWithoutCommandCounts()

	cmdByStatus, err := s.commandCounter.CountByStatus()
	if err != nil {
		slog.Warn("采集命令队列计数失败，本次降级为空", "错误", err)
		cmdByStatus = map[string]int{}
	}
	snap.CommandByStatus = cmdByStatus
	return snap
}

// ScrapeSnapshot 是抓取路径专用的窄快照：只含抓取侧**真正会用到**的内存类字段。
//
// 与 Observability 的差别是**不含注册表规模**。抓取侧的 beacon_self_* 指标只需要连接池 /
// 长轮询 / 丢弃计数；注册表的实例数与健康分布另有 registryCollector 经 List 即时导出
// （指标名 beacon_instances_registered / beacon_instances_status），两边口径本就不同、
// 互不依赖。故抓取路径没有理由顺带再读一遍注册表。
type ScrapeSnapshot struct {
	DBPool               DBPoolStats   // 数据库连接池统计
	Longpoll             LongpollStats // 长轮询四通道挂起数
	MetricWriteDiscarded int64         // 指标异步写入累计丢弃行数（未注入写入池时恒 0）
}

// SnapshotForScrape 采集抓取路径需要的全部字段：**不触达 DB，也不读注册表**。
//
// 为什么单独拆出来：registry.StatusCounts() 不是 O(1) 读计数，而是一次持读锁的全表遍历
// 并新建一个 map（1000 台规模下实测 54µs，且要抢注册表的 RWMutex 读锁——心跳写路径正持写锁）。
// 抓取端点默认无鉴权、不限流，让每个 scrape 都为「本次并不输出」的注册表计数付一次遍历与分配，
// 属于纯粹的无谓开销；而注册表规模已在抓取端点由 registryCollector 按另一种口径（带 namespace /
// role / status 标签）导出，两处重复采集只会让口径来源变模糊。
//
// 本方法是抓取与自观测页共用的「内存类字段」唯一来源（见 SnapshotWithoutCommandCounts），
// 避免两处各写一份读取逻辑后漂移。
func (s *ObservabilityService) SnapshotForScrape() ScrapeSnapshot {
	st := s.dbStats.Stats()

	configN := s.configHub.WaiterCount()
	fileN := s.fileHub.WaiterCount()
	topoN := s.topologyHub.WaiterCount()
	cmdN := s.commandHub.WaiterCount()

	var metricDiscarded int64
	if s.metricDiscard != nil {
		metricDiscarded = s.metricDiscard.Discarded()
	}

	return ScrapeSnapshot{
		DBPool: DBPoolStats{
			MaxOpenConnections: st.MaxOpenConnections,
			OpenConnections:    st.OpenConnections,
			InUse:              st.InUse,
			Idle:               st.Idle,
			WaitCount:          st.WaitCount,
			WaitDurationMs:     st.WaitDuration.Milliseconds(),
		},
		Longpoll: LongpollStats{
			Config:   configN,
			File:     fileN,
			Topology: topoN,
			Command:  cmdN,
			Total:    configN + fileN + topoN + cmdN,
		},
		MetricWriteDiscarded: metricDiscarded,
	}
}

// SnapshotWithoutCommandCounts 采集除命令队列外的全部自观测项，**不触达 DB**。
//
// 为什么需要这个变体（见 ADR-0091）：命令队列计数源自一次 DB GROUP BY，而抓取端点
// 默认无鉴权且不设限流——若把它放进抓取路径，等于任何能访问该端口的人按任意频率
// 驱动数据库查询。故抓取侧只取本方法（纯内存 / 标准库统计），命令队列计数改由后台
// 周期刷新器（CommandCountRefresher）单独采集后写入指标缓存。
//
// 注意本方法**不用于抓取路径**：它含注册表规模，而抓取侧不用（见 SnapshotForScrape）。
// 它的调用方只有管理台自观测页那条链（Snapshot）。
func (s *ObservabilityService) SnapshotWithoutCommandCounts() Observability {
	base := s.SnapshotForScrape()

	byStatus := s.registry.StatusCounts()
	total := 0
	for _, n := range byStatus {
		total += n
	}

	return Observability{
		DBPool:               base.DBPool,
		Longpoll:             base.Longpoll,
		RegistryByStatus:     byStatus,
		RegistryTotal:        total,
		CommandByStatus:      map[string]int{},
		MetricWriteDiscarded: base.MetricWriteDiscarded,
	}
}

// CommandCountRefresher 是命令队列计数刷新器（见 ADR-0091）：后台周期把队列深度写入指标缓存。
//
// 存在的理由：抓取路径不能查库（见 SnapshotWithoutCommandCounts），但命令队列深度又必须
// 可见——它是「429 出现前唯一的积压信号」（OPERATIONS 已记录该缺口）。折中是：DB 查询留在
// 后台，抓取只读缓存。周期取 15s：与 Prometheus 默认抓取间隔同量级，既不加重 DB 负担，
// 又足以在积压发展成 429 之前被看到。
type CommandCountRefresher struct {
	counter commandCounter
	sink    commandCountSink
}

// commandCountSink 是刷新器对指标缓存的窄依赖（由 *metrics.Metrics 实现，见其 SetCommandStatusSnapshot）。
type commandCountSink interface {
	SetCommandStatusSnapshot(byStatus map[string]int, nowUnix float64)
}

// commandCountRefreshInterval 是命令队列计数的后台刷新周期。
const commandCountRefreshInterval = 15 * time.Second

// NewCommandCountRefresher 构造刷新器。
func NewCommandCountRefresher(counter commandCounter, sink commandCountSink) *CommandCountRefresher {
	return &CommandCountRefresher{counter: counter, sink: sink}
}

// Run 周期刷新命令队列计数，直到 ctx 取消。
//
// 首轮**立即**刷新（不等第一个 tick）：否则进程启动后的前 15s 抓取只能看到空缓存，
// 那段时间的看板会把「还没采过」误读成「队列为空」。
func (r *CommandCountRefresher) Run(ctx context.Context) {
	refresh := func() {
		byStatus, err := r.counter.CountByStatus()
		if err != nil {
			// 采集失败不写缓存（sink 侧对 nil 亦为保留旧值），避免把"查不到"写成"队列空"。
			slog.Warn("刷新命令队列计数失败，保留上一轮值", "错误", err)
			return
		}
		r.sink.SetCommandStatusSnapshot(byStatus, float64(time.Now().Unix()))
	}
	refresh()

	ticker := time.NewTicker(commandCountRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
