package service

import (
	"math"
	"testing"
	"time"

	"beacon/internal/model"
	"beacon/internal/runtime"
)

// floatEq 浮点近似相等比较（聚合均值有舍入，留小容差）。
func floatEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// onlineInst 构造一个在线 bukkit 实例样本（仅填聚合关心的负载字段）。
func onlineInst(serverID string, players int, tps float64, memUsed, memMax int64, cpu float64) *runtime.Instance {
	return onlineInstWithRole(serverID, roleBukkit, players, tps, memUsed, memMax, cpu)
}

// onlineInstWithRole 构造一个指定角色的在线实例样本（验证 bungee 不计入平均 TPS/CPU）。
func onlineInstWithRole(serverID, role string, players int, tps float64, memUsed, memMax int64, cpu float64) *runtime.Instance {
	return &runtime.Instance{
		Namespace: "prod", ServerID: serverID, Role: role, Status: runtime.StatusOnline,
		PlayerCount: players, TPS: tps, MemUsed: memUsed, MemMax: memMax, CpuLoad: cpu,
	}
}

// TestSummarizePlayersAndPerServer 验证总人数求和与每服人数分组。
func TestSummarizePlayersAndPerServer(t *testing.T) {
	insts := []*runtime.Instance{
		onlineInst("lobby-1", 42, 19.9, 128, 512, 0.3),
		onlineInst("lobby-2", 7, 20.0, 64, 256, 0.1),
	}
	sum := Summarize(insts)
	if sum.TotalPlayers != 49 {
		t.Fatalf("总人数应为 49，实际 %d", sum.TotalPlayers)
	}
	if sum.OnlineServers != 2 {
		t.Fatalf("在线服数应为 2，实际 %d", sum.OnlineServers)
	}
	byServer := map[string]int{}
	for _, s := range sum.Servers {
		byServer[s.ServerID] = s.PlayerCount
	}
	if byServer["lobby-1"] != 42 || byServer["lobby-2"] != 7 {
		t.Fatalf("每服人数分组错误：%v", byServer)
	}
}

// TestSummarizeAverages 验证平均 TPS / 内存 / CPU 计算正确。
func TestSummarizeAverages(t *testing.T) {
	insts := []*runtime.Instance{
		onlineInst("a", 10, 20.0, 100, 1000, 0.2),
		onlineInst("b", 10, 18.0, 300, 1000, 0.6),
	}
	sum := Summarize(insts)
	if !floatEq(sum.AvgTPS, 19.0) {
		t.Fatalf("平均 TPS 应为 19.0，实际 %v", sum.AvgTPS)
	}
	if sum.AvgMemUsed != 200 || sum.AvgMemMax != 1000 {
		t.Fatalf("平均内存错误：used=%d max=%d", sum.AvgMemUsed, sum.AvgMemMax)
	}
	if !floatEq(sum.AvgCPULoad, 0.4) {
		t.Fatalf("平均 CPU 应为 0.4，实际 %v", sum.AvgCPULoad)
	}
	if sum.CPUSampleCount != 2 {
		t.Fatalf("CPU 可用样本数应为 2，实际 %d", sum.CPUSampleCount)
	}
}

// TestSummarizeAvgOnlyBukkitMixed 混合 bukkit/bungee：平均 TPS/CPU 只统计 bukkit，
// 总人数/在线服数仍计全部（bungee 不进 TPS·CPU 分母，避免 bungee tps=0 拉低平均）。
func TestSummarizeAvgOnlyBukkitMixed(t *testing.T) {
	insts := []*runtime.Instance{
		onlineInstWithRole("bk-a", roleBukkit, 30, 20.0, 100, 1000, 0.4),
		onlineInstWithRole("bk-b", roleBukkit, 10, 18.0, 300, 1000, 0.6),
		// bungee：tps=0、cpu=0.9，应整体被排除出平均 TPS/CPU，但人数计入总数。
		onlineInstWithRole("bc-1", roleBungee, 5, 0.0, 200, 2000, 0.9),
	}
	sum := Summarize(insts)
	// 总人数计全部：30+10+5=45；在线服数计全部：3。
	if sum.TotalPlayers != 45 {
		t.Fatalf("总人数应计全部=45，实际 %d", sum.TotalPlayers)
	}
	if sum.OnlineServers != 3 {
		t.Fatalf("在线服数应计全部=3，实际 %d", sum.OnlineServers)
	}
	// 平均 TPS 只对两个 bukkit：(20+18)/2=19，不含 bungee 的 0。
	if !floatEq(sum.AvgTPS, 19.0) {
		t.Fatalf("平均 TPS 应仅含 bukkit=19.0，实际 %v", sum.AvgTPS)
	}
	// 平均 CPU 只对两个 bukkit：(0.4+0.6)/2=0.5，不含 bungee 的 0.9。
	if !floatEq(sum.AvgCPULoad, 0.5) {
		t.Fatalf("平均 CPU 应仅含 bukkit=0.5，实际 %v", sum.AvgCPULoad)
	}
	if sum.CPUSampleCount != 2 {
		t.Fatalf("CPU 可用样本数应仅含 bukkit=2，实际 %d", sum.CPUSampleCount)
	}
}

// TestSummarizeAvgAllBungee 全 bungee：无 bukkit 参与平均 → 平均 TPS=0、CPU 为不可用哨兵；
// 但总人数/在线服数仍按全部统计。
func TestSummarizeAvgAllBungee(t *testing.T) {
	insts := []*runtime.Instance{
		onlineInstWithRole("bc-1", roleBungee, 5, 0.0, 200, 2000, 0.9),
		onlineInstWithRole("bc-2", roleBungee, 7, 0.0, 100, 2000, 0.8),
	}
	sum := Summarize(insts)
	if sum.TotalPlayers != 12 || sum.OnlineServers != 2 {
		t.Fatalf("总人数/在线服数应计全部 bungee：players=%d servers=%d", sum.TotalPlayers, sum.OnlineServers)
	}
	if sum.AvgTPS != 0 {
		t.Fatalf("无 bukkit 时平均 TPS 应为 0，实际 %v", sum.AvgTPS)
	}
	if sum.CPUSampleCount != 0 {
		t.Fatalf("无 bukkit 时 CPU 可用样本数应为 0，实际 %d", sum.CPUSampleCount)
	}
	if sum.AvgCPULoad != cpuLoadUnavailable {
		t.Fatalf("无 bukkit 时平均 CPU 应为不可用哨兵 -1.0，实际 %v", sum.AvgCPULoad)
	}
}

// TestSummarizeAvgAllBukkit 全 bukkit：平均 TPS/CPU 对全部实例求（与角色过滤前行为一致）。
func TestSummarizeAvgAllBukkit(t *testing.T) {
	insts := []*runtime.Instance{
		onlineInstWithRole("bk-a", roleBukkit, 10, 20.0, 100, 1000, 0.2),
		onlineInstWithRole("bk-b", roleBukkit, 10, 18.0, 300, 1000, 0.6),
	}
	sum := Summarize(insts)
	if !floatEq(sum.AvgTPS, 19.0) {
		t.Fatalf("全 bukkit 平均 TPS 应为 19.0，实际 %v", sum.AvgTPS)
	}
	if !floatEq(sum.AvgCPULoad, 0.4) || sum.CPUSampleCount != 2 {
		t.Fatalf("全 bukkit 平均 CPU 应为 0.4 / 计数 2，实际 %v / %d", sum.AvgCPULoad, sum.CPUSampleCount)
	}
}

// TestSummarizeExcludesUnavailableCPU 验证 cpuLoad=-1.0（不可用）被剔除出平均，只对可用样本求均值。
func TestSummarizeExcludesUnavailableCPU(t *testing.T) {
	insts := []*runtime.Instance{
		onlineInst("a", 10, 20.0, 100, 1000, 0.4),
		onlineInst("b", 10, 20.0, 100, 1000, -1.0), // 不可用，剔除
		onlineInst("c", 10, 20.0, 100, 1000, 0.6),
	}
	sum := Summarize(insts)
	// 平均只对 a、c 两个可用样本求：(0.4+0.6)/2 = 0.5
	if !floatEq(sum.AvgCPULoad, 0.5) {
		t.Fatalf("平均 CPU 应剔除 -1.0 后为 0.5，实际 %v", sum.AvgCPULoad)
	}
	if sum.CPUSampleCount != 2 {
		t.Fatalf("CPU 可用样本数应为 2（剔除 1 个不可用），实际 %d", sum.CPUSampleCount)
	}
}

// TestSummarizeAllCPUUnavailable 全部 CPU 不可用时平均为不可用哨兵 -1.0、可用样本数为 0。
func TestSummarizeAllCPUUnavailable(t *testing.T) {
	insts := []*runtime.Instance{
		onlineInst("a", 1, 20.0, 1, 1, -1.0),
		onlineInst("b", 1, 20.0, 1, 1, -1.0),
	}
	sum := Summarize(insts)
	if sum.CPUSampleCount != 0 {
		t.Fatalf("无可用 CPU 样本时计数应为 0，实际 %d", sum.CPUSampleCount)
	}
	if sum.AvgCPULoad != cpuLoadUnavailable {
		t.Fatalf("无可用 CPU 样本时平均应为不可用哨兵 -1.0，实际 %v", sum.AvgCPULoad)
	}
}

// TestSummarizeEmpty 空在线集合 → 全 0、CPU 平均为不可用哨兵、不 panic。
func TestSummarizeEmpty(t *testing.T) {
	sum := Summarize(nil)
	if sum.TotalPlayers != 0 || sum.OnlineServers != 0 || len(sum.Servers) != 0 {
		t.Fatalf("空集合应全 0，实际 %+v", sum)
	}
	if sum.AvgTPS != 0 || sum.AvgMemUsed != 0 || sum.AvgMemMax != 0 {
		t.Fatalf("空集合平均应为 0，实际 %+v", sum)
	}
	if sum.AvgCPULoad != cpuLoadUnavailable {
		t.Fatalf("空集合 CPU 平均应为不可用哨兵 -1.0，实际 %v", sum.AvgCPULoad)
	}
}

// sampleAt 构造一条 bukkit 样本（聚合降采样只看 sampledAt 与各负载值）。
func sampleAt(serverID string, at time.Time, players int, tps float64, memUsed, memMax int64, cpu float64) model.MetricSample {
	return sampleAtWithRole(serverID, roleBukkit, at, players, tps, memUsed, memMax, cpu)
}

// sampleAtWithRole 构造一条指定角色的样本（验证 bungee 不计入桶内平均 TPS/CPU）。
func sampleAtWithRole(serverID, role string, at time.Time, players int, tps float64, memUsed, memMax int64, cpu float64) model.MetricSample {
	return model.MetricSample{
		Namespace: "prod", ServerID: serverID, Role: role, SampledAt: at,
		PlayerCount: players, TPS: tps, MemUsed: memUsed, MemMax: memMax, CpuLoad: cpu,
	}
}

// TestDownsampleBucketsByTime 验证按时间桶降采样：同桶样本聚合为一个点，桶按时间升序。
func TestDownsampleBucketsByTime(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	bucket := time.Minute
	samples := []model.MetricSample{
		// 第 0 桶 [10:00,10:01)：两条 → 聚合
		sampleAt("a", base.Add(10*time.Second), 10, 20.0, 100, 1000, 0.2),
		sampleAt("b", base.Add(40*time.Second), 20, 18.0, 300, 1000, 0.6),
		// 第 2 桶 [10:02,10:03)：一条
		sampleAt("a", base.Add(2*time.Minute+5*time.Second), 5, 19.0, 200, 1000, 0.4),
	}
	pts := Downsample(samples, bucket)
	if len(pts) != 2 {
		t.Fatalf("应聚合为 2 个时间桶，实际 %d", len(pts))
	}
	// 桶按时间升序。
	if !pts[0].SampledAt.Before(pts[1].SampledAt) {
		t.Fatalf("时间桶应升序，实际 %v / %v", pts[0].SampledAt, pts[1].SampledAt)
	}
	// 第 0 桶：人数求和 30、平均 TPS=(20+18)/2=19、平均内存 used=(100+300)/2=200、CPU=(0.2+0.6)/2=0.4
	b0 := pts[0]
	if b0.TotalPlayers != 30 {
		t.Fatalf("第 0 桶总人数应为 30，实际 %d", b0.TotalPlayers)
	}
	if !floatEq(b0.AvgTPS, 19.0) {
		t.Fatalf("第 0 桶平均 TPS 应为 19.0，实际 %v", b0.AvgTPS)
	}
	if b0.AvgMemUsed != 200 {
		t.Fatalf("第 0 桶平均内存 used 应为 200，实际 %d", b0.AvgMemUsed)
	}
	if !floatEq(b0.AvgCPULoad, 0.4) {
		t.Fatalf("第 0 桶平均 CPU 应为 0.4，实际 %v", b0.AvgCPULoad)
	}
	// 桶时间对齐到桶起点。
	if !b0.SampledAt.Equal(base) {
		t.Fatalf("第 0 桶时间应对齐到 %v，实际 %v", base, b0.SampledAt)
	}
}

// TestDownsampleAvgOnlyBukkitMixed 桶内混合 bukkit/bungee：平均 TPS/CPU 只统计 bukkit，
// 人数仍按全部求和（bungee tps=0 不拉低桶内平均）。
func TestDownsampleAvgOnlyBukkitMixed(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		sampleAtWithRole("bk-a", roleBukkit, base.Add(1*time.Second), 30, 20.0, 100, 1000, 0.4),
		sampleAtWithRole("bk-b", roleBukkit, base.Add(2*time.Second), 10, 18.0, 300, 1000, 0.6),
		// bungee：tps=0、cpu=0.9，整体排除出平均 TPS/CPU；人数计入。
		sampleAtWithRole("bc-1", roleBungee, base.Add(3*time.Second), 5, 0.0, 200, 2000, 0.9),
	}
	pts := Downsample(samples, time.Minute)
	if len(pts) != 1 {
		t.Fatalf("应聚合为 1 个桶，实际 %d", len(pts))
	}
	p := pts[0]
	// 人数计全部：30+10+5=45。
	if p.TotalPlayers != 45 {
		t.Fatalf("桶内总人数应计全部=45，实际 %d", p.TotalPlayers)
	}
	// 平均 TPS 只对两个 bukkit：(20+18)/2=19。
	if !floatEq(p.AvgTPS, 19.0) {
		t.Fatalf("桶内平均 TPS 应仅含 bukkit=19.0，实际 %v", p.AvgTPS)
	}
	// 平均 CPU 只对两个 bukkit：(0.4+0.6)/2=0.5。
	if !floatEq(p.AvgCPULoad, 0.5) {
		t.Fatalf("桶内平均 CPU 应仅含 bukkit=0.5，实际 %v", p.AvgCPULoad)
	}
}

// TestDownsampleAvgAllBungee 桶内全 bungee：平均 TPS=0、CPU 为不可用哨兵；人数仍求和。
func TestDownsampleAvgAllBungee(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		sampleAtWithRole("bc-1", roleBungee, base.Add(1*time.Second), 5, 0.0, 200, 2000, 0.9),
		sampleAtWithRole("bc-2", roleBungee, base.Add(2*time.Second), 7, 0.0, 100, 2000, 0.8),
	}
	pts := Downsample(samples, time.Minute)
	if len(pts) != 1 {
		t.Fatalf("应聚合为 1 个桶，实际 %d", len(pts))
	}
	p := pts[0]
	if p.TotalPlayers != 12 {
		t.Fatalf("桶内总人数应计全部 bungee=12，实际 %d", p.TotalPlayers)
	}
	if p.AvgTPS != 0 {
		t.Fatalf("桶内无 bukkit 时平均 TPS 应为 0，实际 %v", p.AvgTPS)
	}
	if p.AvgCPULoad != cpuLoadUnavailable {
		t.Fatalf("桶内无 bukkit 时平均 CPU 应为不可用哨兵 -1.0，实际 %v", p.AvgCPULoad)
	}
}

// TestDownsampleExcludesUnavailableCPU 桶内 cpuLoad=-1.0 被剔除出该桶平均。
func TestDownsampleExcludesUnavailableCPU(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		sampleAt("a", base.Add(1*time.Second), 1, 20.0, 1, 1, 0.4),
		sampleAt("b", base.Add(2*time.Second), 1, 20.0, 1, 1, -1.0), // 剔除
		sampleAt("c", base.Add(3*time.Second), 1, 20.0, 1, 1, 0.6),
	}
	pts := Downsample(samples, time.Minute)
	if len(pts) != 1 {
		t.Fatalf("应为 1 个桶，实际 %d", len(pts))
	}
	if !floatEq(pts[0].AvgCPULoad, 0.5) {
		t.Fatalf("桶平均 CPU 应剔除 -1.0 后为 0.5，实际 %v", pts[0].AvgCPULoad)
	}
}

// TestDownsampleAllCPUUnavailable 桶内全部 CPU 不可用 → 该桶 CPU 平均为不可用哨兵 -1.0。
func TestDownsampleAllCPUUnavailable(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		sampleAt("a", base.Add(1*time.Second), 1, 20.0, 1, 1, -1.0),
		sampleAt("b", base.Add(2*time.Second), 1, 20.0, 1, 1, -1.0),
	}
	pts := Downsample(samples, time.Minute)
	if len(pts) != 1 || pts[0].AvgCPULoad != cpuLoadUnavailable {
		t.Fatalf("桶内全不可用 CPU 时平均应为 -1.0，实际 %+v", pts)
	}
}

// TestDownsampleEmpty 空样本集 → 空序列、不 panic。
func TestDownsampleEmpty(t *testing.T) {
	if pts := Downsample(nil, time.Minute); len(pts) != 0 {
		t.Fatalf("空样本应返回空序列，实际 %d", len(pts))
	}
	if pts := Downsample([]model.MetricSample{}, time.Minute); len(pts) != 0 {
		t.Fatalf("空切片应返回空序列，实际 %d", len(pts))
	}
}

// TestDownsampleZeroBucketFallback 桶大小 <=0 时退化为每条样本独立成桶（防除零、防无意义聚合）。
func TestDownsampleZeroBucketFallback(t *testing.T) {
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		sampleAt("a", base, 1, 20.0, 1, 1, 0.1),
		sampleAt("b", base.Add(time.Second), 2, 20.0, 1, 1, 0.2),
	}
	pts := Downsample(samples, 0)
	if len(pts) != 2 {
		t.Fatalf("桶大小<=0 应每条独立成点，实际 %d", len(pts))
	}
}
