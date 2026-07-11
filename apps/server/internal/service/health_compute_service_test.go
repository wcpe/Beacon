package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
)

// backendFact 返回一台健康 backend 的 DB 事实基线。
func backendFact() repository.HealthFact {
	return repository.HealthFact{
		NamespaceID: 1, Namespace: "prod", ServerID: "s1", Kind: model.ServerKindBackend,
		ZoneName: "area-1", IdentityStatus: model.AgentIdentityStatusActive,
	}
}

// healthSampleAt 构造一批窗口样本（接收时刻 recvMs，各因子健康值）。
func healthSampleAt(recvMs int64, cpu float64) metricwindow.Sample {
	return metricwindow.Sample{
		NamespaceID: 1, ServerID: "s1", Kind: model.ServerKindBackend,
		BucketStartMs: recvMs - 1000, SampleCount: 5,
		CPUPctAvg: cpu, TPSAvg: 19.5, OnlineMax: 60, MaxOnline: 100, ReportRttMs: 50,
		ReceivedAtMs: recvMs,
	}
}

// TestAggregateFactorInputsWindow 校验窗口聚合口径：cpu/tps 取批 avg 均值、在线 / 连接 / 容量取 max、
// latency 按 kind 取 reportRttMs / backendRttMsAvg 有效均值。
func TestAggregateFactorInputsWindow(t *testing.T) {
	samples := []metricwindow.Sample{
		{CPUPctAvg: 30, TPSAvg: 20, OnlineMax: 50, MaxOnline: 100, ConnMax: 0, ReportRttMs: 40},
		{CPUPctAvg: 50, TPSAvg: 18, OnlineMax: 70, MaxOnline: 100, ConnMax: 0, ReportRttMs: 60},
	}
	in, online, maxOnline := aggregateFactorInputs(model.ServerKindBackend, samples)
	if !almostEq(in.CPUPct, 40) || !almostEq(in.TPS, 19) {
		t.Fatalf("cpu/tps 应取均值 40/19，实际 %v/%v", in.CPUPct, in.TPS)
	}
	if in.OnlineCount != 70 || in.MaxOnline != 100 || online != 70 || maxOnline != 100 {
		t.Fatalf("在线/容量应取 max 70/100，实际 %d/%d", in.OnlineCount, in.MaxOnline)
	}
	if !almostEq(in.RttMs, 50) {
		t.Fatalf("backend rtt 应取 reportRttMs 均值 50，实际 %v", in.RttMs)
	}

	// proxy：conn 取 max、rtt 取 backendRttMsAvg；-1 无效批剔除。
	proxySamples := []metricwindow.Sample{
		{CPUPctAvg: 30, ConnMax: 800, BackendRttMsAvg: 20, ReportRttMs: 10},
		{CPUPctAvg: 30, ConnMax: 1200, BackendRttMsAvg: -1, ReportRttMs: 10},
	}
	pin, _, _ := aggregateFactorInputs(model.ServerKindProxy, proxySamples)
	if pin.ConnCount != 1200 {
		t.Fatalf("conn 应取 max 1200，实际 %d", pin.ConnCount)
	}
	if !almostEq(pin.RttMs, 20) {
		t.Fatalf("proxy rtt 应只对有效 backendRttMsAvg 求均值 20，实际 %v", pin.RttMs)
	}
	// 全部 rtt 无效 → -1（latency 不适用）。
	pin2, _, _ := aggregateFactorInputs(model.ServerKindProxy, []metricwindow.Sample{{BackendRttMsAvg: -1}})
	if pin2.RttMs >= 0 {
		t.Fatalf("全无效 rtt 应为 -1，实际 %v", pin2.RttMs)
	}
}

// TestAggregateCPUGlitchSymmetry 校验 cpu 毛刺对称处理三情形（JVM getProcessCpuLoad 偶发 -1）：
// 部分 -1 只对 ≥0 批求均值；全 -1 置 -1（因子不适用并权重重归一）；全正常照常均值。
func TestAggregateCPUGlitchSymmetry(t *testing.T) {
	// 全正常：均值。
	in, _, _ := aggregateFactorInputs(model.ServerKindBackend, []metricwindow.Sample{
		{CPUPctAvg: 30}, {CPUPctAvg: 50},
	})
	if !almostEq(in.CPUPct, 40) {
		t.Fatalf("全正常应均值 40，实际 %v", in.CPUPct)
	}
	// 部分 -1：只对 ≥0 批求均值（毛刺不拉低均值）。
	in, _, _ = aggregateFactorInputs(model.ServerKindBackend, []metricwindow.Sample{
		{CPUPctAvg: 30}, {CPUPctAvg: -1}, {CPUPctAvg: 50},
	})
	if !almostEq(in.CPUPct, 40) {
		t.Fatalf("部分 -1 应剔除后均值 40，实际 %v", in.CPUPct)
	}
	// 全 -1：置 -1 → cpu 因子不适用、权重重归一（与 latency rtt=-1 同构）。
	in, _, _ = aggregateFactorInputs(model.ServerKindBackend, []metricwindow.Sample{
		{CPUPctAvg: -1}, {CPUPctAvg: -1},
	})
	if in.CPUPct >= 0 {
		t.Fatalf("全 -1 应置 -1，实际 %v", in.CPUPct)
	}
	factors := ComputeHealthFactors(HealthFactorInputs{
		Kind: model.ServerKindBackend, CPUPct: in.CPUPct,
		TPS: 19.5, OnlineCount: 10, MaxOnline: 100, RttMs: 50,
	}, DefaultHealthWeightsConfig())
	if factorByName(t, factors, healthFactorCPU).Applicable {
		t.Fatal("窗口全 -1 时 cpu 因子应不适用")
	}
	if got := HealthScore(factors); got != 100 {
		t.Fatalf("cpu 剔除后其余满分应重归一为 100，实际 %d", got)
	}
}

// TestComputeInstanceViewFreshLostCarry 校验单实例视图三态流转：
// 30s 内有批正常计算 → 最近一批超 30s 判 lost → 窗口空但上一轮 ≤30s 沿用、超 30s 判 lost。
func TestComputeInstanceViewFreshLostCarry(t *testing.T) {
	weights := HealthWeightsSnapshot{Rev: 3, Config: DefaultHealthWeightsConfig()}
	fact := backendFact()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC).UnixMilli()

	// 正常：30s 内有批 → 正常计算（各因子健康 → healthy、schedulable）。
	fresh := computeInstanceView(fact, []metricwindow.Sample{healthSampleAt(base, 30)}, nil, base+5_000, weights)
	if fresh.Level != healthview.LevelHealthy || !fresh.Schedulable || fresh.WeightsRev != 3 {
		t.Fatalf("活性正常应 healthy 且可调度，实际 %+v", fresh)
	}
	if fresh.ZoneName != "area-1" || fresh.Namespace != "prod" {
		t.Fatalf("视图应携带 zone / namespace，实际 %+v", fresh)
	}

	// 边界：恰 30s 仍正常；30s+1ms 判 lost（score=0、unhealthy、reasons 含 lost、无因子）。
	edge := computeInstanceView(fact, []metricwindow.Sample{healthSampleAt(base, 30)}, nil, base+30_000, weights)
	if containsReason(edge.Reasons, healthview.ReasonLost) {
		t.Fatalf("恰 30s 不应判 lost，实际 %+v", edge.Reasons)
	}
	lost := computeInstanceView(fact, []metricwindow.Sample{healthSampleAt(base, 30)}, &fresh, base+30_001, weights)
	if lost.Score != 0 || lost.Level != healthview.LevelUnhealthy || lost.Schedulable {
		t.Fatalf("超 30s 应 lost：score=0 unhealthy 不可调度，实际 %+v", lost)
	}
	if !containsReason(lost.Reasons, healthview.ReasonLost) || !containsReason(lost.Reasons, healthview.ReasonUnhealthy) {
		t.Fatalf("lost 视图 reasons 应含 lost+unhealthy，实际 %v", lost.Reasons)
	}
	if len(lost.Factors) != 0 {
		t.Fatalf("lost 视图不应输出因子，实际 %v", lost.Factors)
	}

	// 沿用：窗口无数据、上一轮 ≤30s 且非 lost → 保留分数 / 因子 / 计算时刻，按当前事实重判 schedulable。
	drainingFact := fact
	drainingFact.Draining = true
	carried := computeInstanceView(drainingFact, nil, &fresh, fresh.ComputedAtMs+20_000, weights)
	if carried.Score != fresh.Score || carried.Level != fresh.Level || carried.ComputedAtMs != fresh.ComputedAtMs {
		t.Fatalf("沿用应保留上一轮分数与计算时刻，实际 %+v", carried)
	}
	if carried.Schedulable || !containsReason(carried.Reasons, healthview.ReasonDraining) {
		t.Fatalf("沿用期间事实变化（draining）应重判 schedulable，实际 %+v", carried)
	}

	// 沿用超 30s → lost（ComputedAtMs 沿用不刷新，保证自然滑入 lost）。
	expired := computeInstanceView(fact, nil, &carried, fresh.ComputedAtMs+30_001, weights)
	if !containsReason(expired.Reasons, healthview.ReasonLost) {
		t.Fatalf("沿用超 30s 应判 lost，实际 %+v", expired.Reasons)
	}
	// 上一轮已 lost 不再沿用（直接维持 lost）。
	stillLost := computeInstanceView(fact, nil, &lost, lost.ComputedAtMs+5_000, weights)
	if !containsReason(stillLost.Reasons, healthview.ReasonLost) {
		t.Fatalf("上一轮 lost 不应被沿用回健康，实际 %+v", stillLost.Reasons)
	}
	// 从未上报（窗口空且无上一轮）→ lost。
	never := computeInstanceView(fact, nil, nil, base, weights)
	if !containsReason(never.Reasons, healthview.ReasonLost) {
		t.Fatalf("从未上报应判 lost，实际 %+v", never.Reasons)
	}
}

// captureSnapshotEnqueuer 捕获快照批的替身。
type captureSnapshotEnqueuer struct {
	batches [][]model.HealthSnapshot
	full    bool
}

func (c *captureSnapshotEnqueuer) Enqueue(rows []model.HealthSnapshot) bool {
	if c.full {
		return false
	}
	c.batches = append(c.batches, rows)
	return true
}

// newComputeFixture 装配一个用内存 sqlite 权重服务 + 替身事实源的计算轮（单测快路）。
func newComputeFixture(t *testing.T, facts []repository.HealthFact) (*HealthComputeService, *metricwindow.Store, *healthview.Store, *captureSnapshotEnqueuer) {
	t.Helper()
	weights, _ := newTestHealthWeightsService(t)
	window := metricwindow.New(metricwindow.DefaultCapacity)
	views := healthview.NewStore()
	capture := &captureSnapshotEnqueuer{}
	svc := NewHealthComputeService(fakeFactsSource(facts), window, views, weights, capture)
	return svc, window, views, capture
}

// fakeFactsSource 是固定事实的替身源。
type fakeFactsSource []repository.HealthFact

func (f fakeFactsSource) ListAll() ([]repository.HealthFact, error) { return f, nil }

// TestHealthComputeRoundAndSnapshotCadence 校验整轮：视图整批替换 + 每 6 轮产出一次全量快照
// （reasons/factors 序列化 json、ts_ms=本轮时刻、weights_rev 随行）。
func TestHealthComputeRoundAndSnapshotCadence(t *testing.T) {
	proxyFact := repository.HealthFact{
		NamespaceID: 1, Namespace: "prod", ServerID: "p1", Kind: model.ServerKindProxy,
		IdentityStatus: model.AgentIdentityStatusActive,
	}
	svc, window, views, capture := newComputeFixture(t, []repository.HealthFact{backendFact(), proxyFact})

	base := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC).UnixMilli()
	nowMs := base
	svc.now = func() time.Time { return time.UnixMilli(nowMs).UTC() }
	window.Upsert(healthSampleAt(base-2_000, 35))

	// 前 5 轮：只更新视图不产快照。
	for i := 0; i < 5; i++ {
		svc.runRound()
	}
	if views.Count() != 2 {
		t.Fatalf("应输出全部在册实例视图 2 台，实际 %d", views.Count())
	}
	if len(capture.batches) != 0 {
		t.Fatalf("未满 6 轮不应产快照，实际 %d 批", len(capture.batches))
	}
	s1, ok := views.Get(1, "s1")
	if !ok || s1.Level != healthview.LevelHealthy || !s1.Schedulable {
		t.Fatalf("s1 应 healthy 可调度，实际 %+v", s1)
	}
	p1, ok := views.Get(1, "p1")
	if !ok || containsReason(p1.Reasons, healthview.ReasonLost) == false || p1.Schedulable {
		t.Fatalf("p1 从未上报应 lost 且不可调度，实际 %+v", p1)
	}

	// 第 6 轮：产出一批全量快照。
	svc.runRound()
	if len(capture.batches) != 1 || len(capture.batches[0]) != 2 {
		t.Fatalf("第 6 轮应产 1 批 2 行快照，实际 %+v", capture.batches)
	}
	var s1Row *model.HealthSnapshot
	for i := range capture.batches[0] {
		if capture.batches[0][i].ServerID == "s1" {
			s1Row = &capture.batches[0][i]
		}
	}
	if s1Row == nil || s1Row.TsMs != nowMs || s1Row.WeightsRev != 1 || !s1Row.Schedulable {
		t.Fatalf("s1 快照行不符，实际 %+v", s1Row)
	}
	var factors []HealthFactorView
	if err := json.Unmarshal([]byte(s1Row.Factors), &factors); err != nil || len(factors) != 6 {
		t.Fatalf("factors 应为 6 因子 json 数组: %v %v", err, s1Row.Factors)
	}
	var reasons []string
	if err := json.Unmarshal([]byte(s1Row.Reasons), &reasons); err != nil || len(reasons) != 0 {
		t.Fatalf("可调度实例 reasons 应为空数组: %v %q", err, s1Row.Reasons)
	}

	// 快照队列满：丢弃本轮快照但视图照常更新（只 WARN 不阻塞）。
	capture.full = true
	for i := 0; i < healthSnapshotEveryRounds; i++ {
		svc.runRound()
	}
	if len(capture.batches) != 1 {
		t.Fatalf("队列满不应再捕获批，实际 %d", len(capture.batches))
	}
	if views.Count() != 2 {
		t.Fatal("队列满不应影响视图更新")
	}
}
