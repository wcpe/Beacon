package service

import (
	"sort"
	"time"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/runtime"
)

// cpuLoadUnavailable 是 CPU 不可用哨兵（与 agent / registry 约定一致：取不到为 -1.0）。
// 聚合均值时该哨兵被剔除；若一组内无任何可用样本，均值回退为本哨兵以示"不可用"。
const cpuLoadUnavailable = -1.0

// 实例角色编码（与 registry / agent 约定一致）。平均 TPS·CPU 只统计 bukkit：
// bungee 作纯代理 tps 恒为 0，计入会拉低平均失真，故排除出这两个平均的分母（人数 / 在线服数仍计全部）。
const (
	roleBukkit = "bukkit"
	roleBungee = "bungee"
)

// countsInAvg 判定某角色是否计入平均 TPS·CPU：仅 bukkit 计入，bungee 排除。
func countsInAvg(role string) bool { return role == roleBukkit }

// latencyUnavailable 是后端平均延迟不可用哨兵（与 agent / registry 约定一致：无可达后端为 -1.0）。
const latencyUnavailable = -1.0

// ServerPlayers 是每服人数明细（聚合端点用，仅负载数字，不含名单）。
type ServerPlayers struct {
	ServerID    string
	Role        string // 实例角色（bukkit / bungee），供前端按角色分组明细（FR-43）
	PlayerCount int
}

// BCSummary 是 bc（bungee 代理）维度的当前快照聚合（FR-34，仅对 role=bungee 在线实例统计）。
// 仅负载计数事实（代理数 / 连接 / 线程 / 后端可达性·延迟），不含玩家名单 / 身份。
type BCSummary struct {
	ProxyCount          int     // 在线 bc 代理数
	TotalConnections    int     // 全部 bc 在线连接数合计
	AvgThreadCount      float64 // bc 平均 JVM 线程数
	BackendUp           int     // 全部 bc 可达后端数合计
	BackendTotal        int     // 全部 bc 配置后端总数合计
	AvgBackendLatencyMs float64 // bc 平均后端延迟（剔除 -1.0 不可用样本；无可用样本时为 -1.0）
}

// Summary 是当前快照聚合结果（实时从内存注册表算，仅展示负载事实，FR-32）。
type Summary struct {
	TotalPlayers   int             // 全集群总在线人数
	OnlineServers  int             // 在线子服数
	Servers        []ServerPlayers // 每服人数明细（按 serverId 升序，确定性）
	AvgTPS         float64         // 平均 TPS
	AvgMemUsed     int64           // 平均 JVM 已用堆字节
	AvgMemMax      int64           // 平均 JVM 最大堆字节
	AvgCPULoad     float64         // 平均 CPU 负载（剔除 -1.0 不可用样本；无可用样本时为 -1.0）
	CPUSampleCount int             // 参与 CPU 平均的可用样本数（cpuLoad>=0）
	BC             BCSummary       // bc（bungee 代理）专属维度聚合（FR-34，仅 role=bungee 实例统计）
}

// Summarize 对一组在线实例做当前快照聚合（纯函数，可穷举单测）：
// 总人数求和、每服人数分组、平均 TPS / 内存；CPU 平均剔除 -1.0 不可用样本。
func Summarize(insts []*runtime.Instance) Summary {
	s := Summary{Servers: make([]ServerPlayers, 0, len(insts))}
	if len(insts) == 0 {
		s.AvgCPULoad = cpuLoadUnavailable
		return s
	}

	var sumTPS, sumCPU float64
	var sumMemUsed, sumMemMax int64
	// bukkit 计数：平均 TPS·内存共用同一分母（bungee 不进分母，与平均口径一致，FR-43）。
	bukkitCount := 0
	cpuCount := 0 // 计入平均 CPU 的 bukkit 且可用样本数
	for _, in := range insts {
		s.TotalPlayers += in.PlayerCount
		s.Servers = append(s.Servers, ServerPlayers{ServerID: in.ServerID, Role: in.Role, PlayerCount: in.PlayerCount})
		if !countsInAvg(in.Role) { // bungee 不计入平均 TPS·内存·CPU
			continue
		}
		sumTPS += in.TPS
		// 内存均值仅算 bukkit（与平均 TPS·CPU 同口径，避免 bc 堆字节混入子服内存均值，FR-43）。
		sumMemUsed += in.MemUsed
		sumMemMax += in.MemMax
		bukkitCount++
		if in.CPULoad >= 0 { // 剔除 -1.0 不可用样本
			sumCPU += in.CPULoad
			cpuCount++
		}
	}
	s.OnlineServers = len(insts)
	s.AvgTPS = avgFloat(sumTPS, bukkitCount)
	s.AvgMemUsed = avgInt64(sumMemUsed, bukkitCount)
	s.AvgMemMax = avgInt64(sumMemMax, bukkitCount)
	s.CPUSampleCount = cpuCount
	s.AvgCPULoad = avgCPU(sumCPU, cpuCount)
	s.BC = summarizeBC(insts)

	sort.Slice(s.Servers, func(i, j int) bool { return s.Servers[i].ServerID < s.Servers[j].ServerID })
	return s
}

// summarizeBC 对一组在线实例做 bc（bungee 代理）维度聚合（纯函数，FR-34）：
// 只统计 role=bungee 实例，连接 / 后端可达性求和、线程取均值；平均延迟剔除 -1.0 不可用样本。
// 无 bc 实例时返回零值且平均延迟为不可用哨兵 -1.0。
func summarizeBC(insts []*runtime.Instance) BCSummary {
	bc := BCSummary{AvgBackendLatencyMs: latencyUnavailable}
	var sumThreads, sumLatency float64
	latencyCount := 0 // 计入平均延迟的可用样本数（latency>=0）
	for _, in := range insts {
		if in.Role != roleBungee {
			continue
		}
		bc.ProxyCount++
		bc.TotalConnections += in.Proxy.OnlineConnections
		bc.BackendUp += in.Proxy.BackendUp
		bc.BackendTotal += in.Proxy.BackendTotal
		sumThreads += float64(in.Proxy.ThreadCount)
		if in.Proxy.BackendAvgLatencyMs >= 0 { // 剔除 -1.0 不可用样本
			sumLatency += in.Proxy.BackendAvgLatencyMs
			latencyCount++
		}
	}
	if bc.ProxyCount == 0 {
		return bc // 无 bc 实例：零值 + 延迟不可用
	}
	bc.AvgThreadCount = sumThreads / float64(bc.ProxyCount)
	bc.AvgBackendLatencyMs = avgLatency(sumLatency, latencyCount)
	return bc
}

// avgLatency 计算后端延迟均值：可用样本数为 0 时回退不可用哨兵 -1.0。
func avgLatency(sum float64, count int) float64 {
	if count == 0 {
		return latencyUnavailable
	}
	return sum / float64(count)
}

// TrendPoint 是一个降采样后的时间序列点（趋势端点用，仅聚合数字，FR-32）。
type TrendPoint struct {
	SampledAt    time.Time // 桶起点时刻（对齐到 bucket 边界）
	TotalPlayers int       // 桶内人数求和
	AvgTPS       float64   // 桶内平均 TPS
	AvgMemUsed   int64     // 桶内平均已用堆字节
	AvgMemMax    int64     // 桶内平均最大堆字节
	AvgCPULoad   float64   // 桶内平均 CPU（剔除 -1.0；无可用样本时为 -1.0）
}

// Downsample 把样本按固定时长 bucket 分桶降采样为时间序列（纯函数，可穷举单测）：
// 同桶人数求和、TPS / 内存取均值、CPU 均值剔除 -1.0；桶按时间升序，桶时间对齐到桶起点。
// bucket<=0 时退化为每条样本独立成桶（防除零）。输入样本顺序不要求，内部自行按时间排序成桶。
func Downsample(samples []model.MetricSample, bucket time.Duration) []TrendPoint {
	if len(samples) == 0 {
		return []TrendPoint{}
	}

	// 按桶起点（UTC Unix 纳秒对齐）聚合。
	type acc struct {
		at         time.Time
		players    int
		sumTPS     float64
		sumMemUsed int64
		sumMemMax  int64
		sumCPU     float64
		// 桶内 bukkit 样本数：平均 TPS·内存共用同一分母（bungee 不进分母，FR-43）。
		bukkitCount int
		cpuCount    int // 计入平均 CPU 的 bukkit 且可用样本数
	}
	buckets := make(map[int64]*acc)
	order := make([]int64, 0)
	for i := range samples {
		key, at := bucketKey(samples[i].SampledAt, bucket)
		a := buckets[key]
		if a == nil {
			a = &acc{at: at}
			buckets[key] = a
			order = append(order, key)
		}
		a.players += samples[i].PlayerCount
		if !countsInAvg(samples[i].Role) { // bungee 不计入平均 TPS·内存·CPU
			continue
		}
		a.sumTPS += samples[i].TPS
		// 内存均值仅算 bukkit（与 Summarize 同口径，避免 bc 堆字节混入桶内内存均值，FR-43）。
		a.sumMemUsed += samples[i].MemUsed
		a.sumMemMax += samples[i].MemMax
		a.bukkitCount++
		if samples[i].CPULoad >= 0 {
			a.sumCPU += samples[i].CPULoad
			a.cpuCount++
		}
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	pts := make([]TrendPoint, 0, len(order))
	for _, key := range order {
		a := buckets[key]
		pts = append(pts, TrendPoint{
			SampledAt:    a.at,
			TotalPlayers: a.players,
			AvgTPS:       avgFloat(a.sumTPS, a.bukkitCount),
			AvgMemUsed:   avgInt64(a.sumMemUsed, a.bukkitCount),
			AvgMemMax:    avgInt64(a.sumMemMax, a.bukkitCount),
			AvgCPULoad:   avgCPU(a.sumCPU, a.cpuCount),
		})
	}
	return pts
}

// bucketKey 返回样本所属桶的键与桶起点时刻；bucket<=0 时每条样本各自成桶（键用纳秒时间戳）。
func bucketKey(at time.Time, bucket time.Duration) (int64, time.Time) {
	if bucket <= 0 {
		ns := at.UTC().UnixNano()
		return ns, at.UTC()
	}
	utc := at.UTC()
	b := bucket.Nanoseconds()
	startNs := (utc.UnixNano() / b) * b
	return startNs, time.Unix(0, startNs).UTC()
}

// avgFloat 计算浮点均值：分母为 0 时返回 0（无 bukkit 参与平均 TPS 时取 0，不污染图表）。
func avgFloat(sum float64, count int) float64 {
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// avgInt64 计算整型均值：分母为 0 时返回 0（无 bukkit 参与平均内存时取 0，与 avgFloat 同口径，防除零）。
func avgInt64(sum int64, count int) int64 {
	if count == 0 {
		return 0
	}
	return sum / int64(count)
}

// avgCPU 计算 CPU 均值：可用样本数为 0 时回退为不可用哨兵 -1.0。
func avgCPU(sum float64, count int) float64 {
	if count == 0 {
		return cpuLoadUnavailable
	}
	return sum / float64(count)
}
