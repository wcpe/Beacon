package service

import (
	"context"
	"log/slog"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	rt "github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// systemDBPingBudget 是系统状态端点探测 DB 连通性的调用侧上限。
//
// 为什么与 store 的 call 预算同数量级而不更短：本端点是排障入口，误报「DB 断开」会把排障
// 引向错误方向；正常 Ping 是毫秒级，5s 有两个数量级余量，真被耗尽时必是池枯竭或长事务占位，
// 此时报断开正是想要的结论。
const systemDBPingBudget = 5 * time.Second

// dbPinger 是系统状态对数据库连通性的窄依赖：仅需一次带预算的探测。
//
// 必须带 ctx（而不是裸 `Ping()`）：`(*sql.DB).Ping()` 内部走 `context.Background()`，
// 池被占满时**无界等待**——这正是生产事故里「所有需 DB 的端点挂起」的症状在本端点上的复现，
// 而本端点恰恰是排障时最需要可用的。生产装配注入 store.BoundedPinger（带 call 预算），
// 测试以替身覆盖连通 / 断开 / 超时三态。
type dbPinger interface {
	PingContext(ctx context.Context) error
}

// CPUSampler 是系统状态对进程 CPU 占比的窄依赖：返回 [0,100] 区间的占比与可用性。
// 真源由 gopsutilCPUSampler（后台定期采样、内存读值）实现，测试以替身覆盖可用 / 降级两态。
type CPUSampler interface {
	// Percent 返回本进程最近一次采样的 CPU 占比及其是否可用；不可用时降级返回 (0, false)。
	// 实现应读内存缓存而非每次现算——见 gopsutilCPUSampler 的说明。
	Percent() (float64, bool)
	// Stop 停止后台采样（进程退出时调用）；实现须可重复调用。测试替身可空实现。
	Stop()
}

// cpuSampleInterval 是后台 CPU 采样间隔。
// 前端页眉轮询 8 秒，采样比它更快才能保证每次请求都拿到新值；这里取 5 秒。
const cpuSampleInterval = 5 * time.Second

// gopsutilCPUSampler 基于 gopsutil 后台定期采样控制面进程 CPU，请求时只读内存缓存。
//
// 为什么不在 Percent() 里现算：gopsutil 的 Percent(0) 用「上次调用到本次的时间差」作分母。
// 若采样频率跟随外部请求（8 秒一次）且服务长时间无人访问，分母会被拉大到数小时，
// 结果把「就绪以来的平均值」当成当前占用——实测首屏出现过 100% 的失真值。
// 改为常驻后台协程固定 5 秒采样一次：分母恒定、基线永不过期，且请求路径不再做系统调用。
//
// 采集失败或尚未取到首个样本时降级返回 (0,false)，端点据此走既有的「—」路径。
type gopsutilCPUSampler struct {
	proc *process.Process

	mu    sync.RWMutex
	pct   float64
	ready bool

	// stop 用于停止后台采样协程（进程退出时由调用方关闭）。
	stop chan struct{}
	once sync.Once
}

// newGopsutilCPUSampler 创建采样器并启动后台采样协程。
// 进程句柄创建失败（极少见）时返回 nil，调用方据此判定 CPU 不可用并降级。
func newGopsutilCPUSampler() *gopsutilCPUSampler {
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		slog.Warn("创建进程句柄失败，进程 CPU% 将不可用", "错误", err)
		return nil
	}
	s := &gopsutilCPUSampler{proc: proc, stop: make(chan struct{})}
	// 建立基线：gopsutil 首调只记录起点并返回 0，不对外暴露。
	if _, err := proc.Percent(0); err != nil {
		slog.Warn("进程 CPU% 基线建立失败，后台采样将在下一轮重试", "错误", err)
	}
	go s.loop()
	return s
}

// loop 常驻采样：固定间隔读一次，写入内存缓存供请求直接读取。
func (s *gopsutilCPUSampler) loop() {
	ticker := time.NewTicker(cpuSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			pct, err := s.proc.Percent(0)
			if err != nil {
				slog.Warn("采集进程 CPU% 失败，本次保持上次值", "错误", err)
				continue
			}
			s.mu.Lock()
			s.pct, s.ready = pct, true
			s.mu.Unlock()
		}
	}
}

// Stop 停止后台采样协程；可重复调用。
func (s *gopsutilCPUSampler) Stop() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.stop != nil {
			close(s.stop)
		}
	})
}

// Percent 读取最近一次后台采样值，不做系统调用；尚未取到样本时降级返回 (0, false)。
func (s *gopsutilCPUSampler) Percent() (float64, bool) {
	if s == nil || s.proc == nil {
		return 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.ready {
		return 0, false
	}
	return s.pct, true
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
// 不持 GORM：DB 连通经窄接口 dbPinger 探测；在线实例数读内存注册表（StatusCounts 只计数、不深拷贝）。
type SystemService struct {
	version        string
	startedAt      time.Time
	pinger         dbPinger
	registry       *rt.Registry
	samplerEnabled func() bool // 采样器是否启用：从设置 store 读、热生效（FR-61），反映 metric.enabled 当前值
	cpu            CPUSampler
	now            func() time.Time // 便于测试注入时钟；默认 UTC now
}

// NewSystemService 构造服务。startedAt 由调用方在进程启动处记录并传入（统一为 UTC）。
// samplerEnabled 为读取采样器启用状态的回调（FR-61：从设置 store 读 metric.enabled，热生效；nil 视为禁用）。
// cpu 为进程 CPU% 采样器（生产由 NewGopsutilCPUSampler 提供并已预热，测试以替身注入）。
func NewSystemService(version string, startedAt time.Time, pinger dbPinger, registry *rt.Registry, samplerEnabled func() bool, cpu CPUSampler) *SystemService {
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
func NewGopsutilCPUSampler() CPUSampler {
	return newGopsutilCPUSampler()
}

// Status 采集一次控制面自身状态快照。
// DB Ping 同步执行（连接池上的轻量探测，带预算：池耗尽时在预算内快速失败而非挂起）；
// 在线实例数读内存注册表，Go 运行时资源读 runtime。
func (s *SystemService) Status() SystemStatus {
	now := s.now()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	dbStatus := DBStatus{Connected: true}
	// 预算 ctx：探测本身已在包装层限额（store.BoundedPinger），此处再给一层调用侧上限。
	// 为什么不只依赖包装层：本服务的 pinger 是窄接口，测试替身与其它实现不保证自带期限；
	// 调用侧给期限后，无论注入的是哪种实现，「本端点永不挂起」都由本处独立成立。
	// 取值与包装层同源（CallTimeoutMs 默认 5s）——两层同值不冲突：同值下先到点的一方即预算本身。
	ctx, cancel := context.WithTimeout(context.Background(), systemDBPingBudget)
	defer cancel()
	if err := s.pinger.PingContext(ctx); err != nil {
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
		OnlineInstances: s.onlineInstanceCount(),
		SamplerEnabled:  s.samplerEnabled != nil && s.samplerEnabled(),
		Runtime: RuntimeStats{
			Goroutines: runtime.NumGoroutine(),
			HeapAlloc:  ms.HeapAlloc,
			HeapSys:    ms.HeapSys,
		},
		CPUAvailable: cpuAvailable,
		CPUPercent:   cpuPercent,
	}
}

// onlineInstanceCount 取在线实例数（只计数，不深拷贝注册表）。
//
// 此前用 len(registry.List(Filter{Status: online}))：为了拿一个计数就把每个命中实例深拷贝一份
// （Metadata map 与 Backends 切片），深拷贝发生在读锁临界区内、临界区随实例数线性变长。
// 规模口径（实测采样，两数是**不同规模**而非改前/改后）：64 台 p50 0.44ms → 1000 台 0.75ms。
// 本函数自身的收益是把上述深拷贝移出临界区，不是锁模式变化——List 与 StatusCounts 同为 RLock。
// StatusCounts 持读锁一次遍历、只累加计数（同文件已注明「不深拷贝、不做 DB IO」），正是此处所需。
func (s *SystemService) onlineInstanceCount() int {
	if s.registry == nil {
		return 0
	}
	return s.registry.StatusCounts()[rt.StatusOnline]
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
