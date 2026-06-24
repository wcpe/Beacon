package service

import (
	"database/sql"
	"log/slog"

	rt "github.com/wcpe/Beacon/internal/runtime"
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

// Snapshot 采集一次自观测快照。各来源均为只读：连接池统计、内存注册表计数、Hub 挂起数同步取；
// 命令队列计数走一次 DB GROUP BY（低频自观测页，非热路径），失败时降级为空 map 并告警，不阻断其余指标。
func (s *ObservabilityService) Snapshot() Observability {
	st := s.dbStats.Stats()

	configN := s.configHub.WaiterCount()
	fileN := s.fileHub.WaiterCount()
	topoN := s.topologyHub.WaiterCount()
	cmdN := s.commandHub.WaiterCount()

	byStatus := s.registry.StatusCounts()
	total := 0
	for _, n := range byStatus {
		total += n
	}

	cmdByStatus, err := s.commandCounter.CountByStatus()
	if err != nil {
		slog.Warn("采集命令队列计数失败，本次降级为空", "错误", err)
		cmdByStatus = map[string]int{}
	}

	return Observability{
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
		RegistryByStatus: byStatus,
		RegistryTotal:    total,
		CommandByStatus:  cmdByStatus,
	}
}
