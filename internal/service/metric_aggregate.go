package service

import (
	"sort"
	"time"

	"beacon/internal/model"
	"beacon/internal/runtime"
)

// cpuLoadUnavailable 是 CPU 不可用哨兵（与 agent / registry 约定一致：取不到为 -1.0）。
// 聚合均值时该哨兵被剔除；若一组内无任何可用样本，均值回退为本哨兵以示"不可用"。
const cpuLoadUnavailable = -1.0

// ServerPlayers 是每服人数明细（聚合端点用，仅负载数字，不含名单）。
type ServerPlayers struct {
	ServerID    string
	PlayerCount int
}

// Summary 是当前快照聚合结果（实时从内存注册表算，仅展示负载事实，FR-32）。
type Summary struct {
	TotalPlayers  int             // 全集群总在线人数
	OnlineServers int             // 在线子服数
	Servers       []ServerPlayers // 每服人数明细（按 serverId 升序，确定性）
	AvgTPS        float64         // 平均 TPS
	AvgMemUsed    int64           // 平均 JVM 已用堆字节
	AvgMemMax     int64           // 平均 JVM 最大堆字节
	AvgCPULoad    float64         // 平均 CPU 负载（剔除 -1.0 不可用样本；无可用样本时为 -1.0）
	CPUSampleCount int            // 参与 CPU 平均的可用样本数（cpuLoad>=0）
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
	cpuCount := 0
	for _, in := range insts {
		s.TotalPlayers += in.PlayerCount
		s.Servers = append(s.Servers, ServerPlayers{ServerID: in.ServerID, PlayerCount: in.PlayerCount})
		sumTPS += in.TPS
		sumMemUsed += in.MemUsed
		sumMemMax += in.MemMax
		if in.CpuLoad >= 0 { // 剔除 -1.0 不可用样本
			sumCPU += in.CpuLoad
			cpuCount++
		}
	}
	s.OnlineServers = len(insts)
	n := float64(len(insts))
	s.AvgTPS = sumTPS / n
	s.AvgMemUsed = sumMemUsed / int64(len(insts))
	s.AvgMemMax = sumMemMax / int64(len(insts))
	s.CPUSampleCount = cpuCount
	s.AvgCPULoad = avgCPU(sumCPU, cpuCount)

	sort.Slice(s.Servers, func(i, j int) bool { return s.Servers[i].ServerID < s.Servers[j].ServerID })
	return s
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
		count      int
		cpuCount   int
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
		a.sumTPS += samples[i].TPS
		a.sumMemUsed += samples[i].MemUsed
		a.sumMemMax += samples[i].MemMax
		a.count++
		if samples[i].CpuLoad >= 0 {
			a.sumCPU += samples[i].CpuLoad
			a.cpuCount++
		}
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	pts := make([]TrendPoint, 0, len(order))
	for _, key := range order {
		a := buckets[key]
		n := int64(a.count)
		pts = append(pts, TrendPoint{
			SampledAt:    a.at,
			TotalPlayers: a.players,
			AvgTPS:       a.sumTPS / float64(a.count),
			AvgMemUsed:   a.sumMemUsed / n,
			AvgMemMax:    a.sumMemMax / n,
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

// avgCPU 计算 CPU 均值：可用样本数为 0 时回退为不可用哨兵 -1.0。
func avgCPU(sum float64, count int) float64 {
	if count == 0 {
		return cpuLoadUnavailable
	}
	return sum / float64(count)
}
