package service

import (
	"log/slog"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	rt "beacon/internal/runtime"
)

// dbPinger 是系统状态对数据库连通性的窄依赖：仅需一次 Ping。
// 由 *sql.DB（经 gorm DB() 获取）实现，便于以测试替身验证连通 / 断开两态。
type dbPinger interface {
	Ping() error
}

// cpuSampler 是系统状态对进程 CPU 占比的窄依赖：返回 [0,100] 区间的占比与可用性。
// 真源由 gopsutilCPUSampler（基于 gopsutil process）实现，测试以替身覆盖可用 / 降级两态。
type cpuSampler interface {
	// Percent 返回自上次调用以来本进程的 CPU 占比及其是否可用；不可用时降级返回 (0, false)。
	Percent() (float64, bool)
}

// gopsutilCPUSampler 基于 gopsutil 采集控制面进程自身 CPU 占比。
// 持有进程句柄并在构造时预热一次（gopsutil Percent(0) 首调返回 0，需先建立基线）；
// 之后每次 Percent(0) 返回自上次调用以来的占比。采集失败优雅降级（返回不可用），不 panic。
type gopsutilCPUSampler struct {
	proc *process.Process
	mu   sync.Mutex // 串行化 Percent 调用：gopsutil 内部按上次采样时刻算增量，并发会扰乱基线
}

// newGopsutilCPUSampler 创建基于当前进程的 CPU 采样器并预热一次基线。
// 进程句柄创建失败（极少见）时返回 nil，调用方据此判定 CPU 不可用并降级。
func newGopsutilCPUSampler() *gopsutilCPUSampler {
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		slog.Warn("创建进程句柄失败，进程 CPU% 将不可用", "错误", err)
		return nil
	}
	// 预热：首调 Percent(0) 仅建立基线、返回 0，丢弃其结果。
	if _, err := proc.Percent(0); err != nil {
		slog.Warn("进程 CPU% 预热失败，将在后续采集时重试", "错误", err)
	}
	return &gopsutilCPUSampler{proc: proc}
}

// Percent 取自上次调用以来的进程 CPU 占比；句柄缺失或采集出错时降级返回 (0, false)。
func (s *gopsutilCPUSampler) Percent() (float64, bool) {
	if s == nil || s.proc == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pct, err := s.proc.Percent(0)
	if err != nil {
		slog.Warn("采集进程 CPU% 失败，本次降级为不可用", "错误", err)
		return 0, false
	}
	return pct, true
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
	CPUAvailable    bool         // 进程 CPU% 是否可用；采集失败时降级为 false
	CPUPercent      float64      // 进程 CPU 占比（[0,100]）；CPUAvailable=false 时无意义、恒 0
}

// SystemService 汇集控制面自身状态：版本 / 运行时长 / DB 连通 / 在线实例数 / 采样器状态 / Go 运行时资源。
// 不持 GORM：DB 连通经窄接口 dbPinger 探测；在线实例数读内存注册表（List 返回深拷贝、锁内取）。
type SystemService struct {
	version        string
	startedAt      time.Time
	pinger         dbPinger
	registry       *rt.Registry
	samplerEnabled bool
	cpu            cpuSampler
	now            func() time.Time // 便于测试注入时钟；默认 UTC now
}

// NewSystemService 构造服务。startedAt 由调用方在进程启动处记录并传入（统一为 UTC）。
// cpu 为进程 CPU% 采样器（生产由 NewGopsutilCPUSampler 提供并已预热，测试以替身注入）。
func NewSystemService(version string, startedAt time.Time, pinger dbPinger, registry *rt.Registry, samplerEnabled bool, cpu cpuSampler) *SystemService {
	return &SystemService{
		version:        version,
		startedAt:      startedAt.UTC(),
		pinger:         pinger,
		registry:       registry,
		samplerEnabled: samplerEnabled,
		cpu:            cpu,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// NewGopsutilCPUSampler 暴露生产用 CPU 采样器构造，供进程启动处装配并预热基线。
// 句柄创建失败时返回的采样器其 Percent 恒降级为 (0,false)，端点据此置 CPUAvailable=false。
func NewGopsutilCPUSampler() cpuSampler {
	return newGopsutilCPUSampler()
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

	// 进程 CPU%：采样器不可用 / 采集失败时降级（cpuAvailable=false、占比恒 0）。
	cpuPercent, cpuAvailable := 0.0, false
	if s.cpu != nil {
		if pct, ok := s.cpu.Percent(); ok {
			cpuPercent, cpuAvailable = roundCPUPercent(pct), true
		}
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
		CPUAvailable: cpuAvailable,
		CPUPercent:   cpuPercent,
	}
}

// roundCPUPercent 把 gopsutil 原始进程占比规整为展示值：钳到 [0,100] 并四舍五入到 1 位小数。
// gopsutil 进程占比不按核心数归一，多核满载可能 >100%，故上界钳到 100。
func roundCPUPercent(pct float64) float64 {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return math.Round(pct*10) / 10
}
