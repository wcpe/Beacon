package service

import (
	"runtime"
	"time"

	rt "beacon/internal/runtime"
)

// dbPinger 是系统状态对数据库连通性的窄依赖：仅需一次 Ping。
// 由 *sql.DB（经 gorm DB() 获取）实现，便于以测试替身验证连通 / 断开两态。
type dbPinger interface {
	Ping() error
}

// DBStatus 是数据库连通性的判定结果（不含底层细节，仅展示用）。
type DBStatus struct {
	Connected bool   // Ping 成功为 true
	Error     string // Ping 失败时的简短错误说明；连通时为空
}

// RuntimeStats 是 Go 运行时资源快照（仅展示）。
type RuntimeStats struct {
	Goroutines int    // 当前 goroutine 数（runtime.NumGoroutine）
	HeapAlloc  uint64 // Go 堆已分配且仍在用的字节数（MemStats.HeapAlloc）
	HeapSys    uint64 // Go 从系统申请的堆字节数（MemStats.HeapSys）
}

// SystemStatus 是控制面自身状态（区别于 agent 网络聚合指标，FR-33）。
type SystemStatus struct {
	Version         string       // 控制面版本号
	StartedAt       time.Time    // 进程启动时间（UTC）
	UptimeSeconds   int64        // 运行时长（秒）
	DB              DBStatus     // 数据库连通性
	OnlineInstances int          // 在线实例数（读内存注册表）
	SamplerEnabled  bool         // 负载指标采样器是否启用（FR-32）
	Runtime         RuntimeStats // Go 运行时资源
	CPUAvailable    bool         // 进程 CPU% 是否可用；当前 dep-free 跨平台无法取得，恒 false（占位）
}

// SystemService 汇集控制面自身状态：版本 / 运行时长 / DB 连通 / 在线实例数 / 采样器状态 / Go 运行时资源。
// 不持 GORM：DB 连通经窄接口 dbPinger 探测；在线实例数读内存注册表（List 返回深拷贝、锁内取）。
type SystemService struct {
	version        string
	startedAt      time.Time
	pinger         dbPinger
	registry       *rt.Registry
	samplerEnabled bool
	now            func() time.Time // 便于测试注入时钟；默认 UTC now
}

// NewSystemService 构造服务。startedAt 由调用方在进程启动处记录并传入（统一为 UTC）。
func NewSystemService(version string, startedAt time.Time, pinger dbPinger, registry *rt.Registry, samplerEnabled bool) *SystemService {
	return &SystemService{
		version:        version,
		startedAt:      startedAt.UTC(),
		pinger:         pinger,
		registry:       registry,
		samplerEnabled: samplerEnabled,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// Status 采集一次控制面自身状态快照。
// DB Ping 同步执行（连接池上的轻量探测）；在线实例数读内存注册表；Go 运行时资源读 runtime。
func (s *SystemService) Status() SystemStatus {
	now := s.now()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	dbStatus := DBStatus{Connected: true}
	if err := s.pinger.Ping(); err != nil {
		dbStatus = DBStatus{Connected: false, Error: err.Error()}
	}

	return SystemStatus{
		Version:         s.version,
		StartedAt:       s.startedAt,
		UptimeSeconds:   int64(now.Sub(s.startedAt).Seconds()),
		DB:              dbStatus,
		OnlineInstances: len(s.registry.List(rt.Filter{Status: rt.StatusOnline})),
		SamplerEnabled:  s.samplerEnabled,
		Runtime: RuntimeStats{
			Goroutines: runtime.NumGoroutine(),
			HeapAlloc:  ms.HeapAlloc,
			HeapSys:    ms.HeapSys,
		},
		// 进程 CPU% 无 dep-free 跨平台办法，置不可用（见 FR-33 决策：不为此引入 gopsutil 等依赖）。
		CPUAvailable: false,
	}
}
