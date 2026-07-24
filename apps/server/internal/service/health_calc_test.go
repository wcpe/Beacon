package service

import (
	"reflect"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// factorByName 从因子明细中按名取单因子（测试断言用）。
func factorByName(t *testing.T, factors []healthview.Factor, name string) healthview.Factor {
	t.Helper()
	for _, f := range factors {
		if f.Factor == name {
			return f
		}
	}
	t.Fatalf("因子 %s 不在输出中", name)
	return healthview.Factor{}
}

// backendInputs 返回一台各因子全适用的 backend 输入基线（各用例在其上微调）。
func backendInputs() HealthFactorInputs {
	return HealthFactorInputs{
		Kind: model.ServerKindBackend,
		TPS:  19.5, CPUPct: 40, OnlineCount: 60, MaxOnline: 100, RttMs: 50, ActiveAlerts: 0,
	}
}

// proxyInputs 返回一台各因子全适用的 proxy 输入基线。
func proxyInputs() HealthFactorInputs {
	return HealthFactorInputs{
		Kind:   model.ServerKindProxy,
		CPUPct: 40, ConnCount: 1000, RttMs: 50, ActiveAlerts: 0,
	}
}

// TestFactorNormalizationBoundaries 穷举各因子 good / bad / 中点 / 越界 clamp 边界（§4.4 公式）。
func TestFactorNormalizationBoundaries(t *testing.T) {
	cfg := DefaultHealthWeightsConfig()
	cases := []struct {
		名称  string
		输入  HealthFactorInputs
		因子  string
		期望分 float64
	}{
		// tps：(tps−10)/(19.5−10)，good=100 / bad=0 / 中点≈50 / 越界 clamp
		{"tps_good", withTPS(backendInputs(), 19.5), healthFactorTPS, 100},
		{"tps_bad", withTPS(backendInputs(), 10), healthFactorTPS, 0},
		{"tps_中点", withTPS(backendInputs(), 14.75), healthFactorTPS, 50},
		{"tps_越上界clamp", withTPS(backendInputs(), 20), healthFactorTPS, 100},
		{"tps_越下界clamp", withTPS(backendInputs(), 3), healthFactorTPS, 0},
		// cpu：(90−cpu)/(90−40)
		{"cpu_good", withCPU(backendInputs(), 40), healthFactorCPU, 100},
		{"cpu_bad", withCPU(backendInputs(), 90), healthFactorCPU, 0},
		{"cpu_中点", withCPU(backendInputs(), 65), healthFactorCPU, 50},
		{"cpu_越上界clamp", withCPU(backendInputs(), 99), healthFactorCPU, 0},
		{"cpu_越下界clamp", withCPU(backendInputs(), 5), healthFactorCPU, 100},
		// capacity：r=online/max，(0.95−r)/(0.95−0.6)
		{"capacity_good", withOnline(backendInputs(), 60, 100), healthFactorCapacity, 100},
		{"capacity_bad", withOnline(backendInputs(), 95, 100), healthFactorCapacity, 0},
		{"capacity_中点", withOnline(backendInputs(), 775, 1000), healthFactorCapacity, 50},
		{"capacity_满载clamp", withOnline(backendInputs(), 100, 100), healthFactorCapacity, 0},
		{"capacity_空载clamp", withOnline(backendInputs(), 0, 100), healthFactorCapacity, 100},
		// conn：r=conn/2000，同 capacity 式
		{"conn_good", withConn(proxyInputs(), 1200), healthFactorConn, 100},
		{"conn_bad", withConn(proxyInputs(), 1900), healthFactorConn, 0},
		{"conn_中点", withConn(proxyInputs(), 1550), healthFactorConn, 50},
		{"conn_越界clamp", withConn(proxyInputs(), 3000), healthFactorConn, 0},
		// latency：(500−rtt)/(500−50)
		{"latency_good", withRtt(backendInputs(), 50), healthFactorLatency, 100},
		{"latency_bad", withRtt(backendInputs(), 500), healthFactorLatency, 0},
		{"latency_中点", withRtt(backendInputs(), 275), healthFactorLatency, 50},
		{"latency_越界clamp", withRtt(backendInputs(), 800), healthFactorLatency, 0},
		// alert：100−n×25 下限 0（P4 活跃告警恒 0 → 恒 100）
		{"alert_零告警恒满分", backendInputs(), healthFactorAlert, 100},
		{"alert_两告警", withAlerts(backendInputs(), 2), healthFactorAlert, 50},
		{"alert_下限clamp", withAlerts(backendInputs(), 5), healthFactorAlert, 0},
	}
	for _, c := range cases {
		t.Run(c.名称, func(t *testing.T) {
			f := factorByName(t, ComputeHealthFactors(c.输入, cfg), c.因子)
			if !f.Applicable {
				t.Fatalf("因子 %s 应适用", c.因子)
			}
			if !almostEq(f.Normalized, c.期望分) {
				t.Fatalf("因子 %s 归一化分期望 %v，实际 %v", c.因子, c.期望分, f.Normalized)
			}
		})
	}
}

func withTPS(in HealthFactorInputs, v float64) HealthFactorInputs { in.TPS = v; return in }
func withCPU(in HealthFactorInputs, v float64) HealthFactorInputs { in.CPUPct = v; return in }
func withRtt(in HealthFactorInputs, v float64) HealthFactorInputs { in.RttMs = v; return in }
func withAlerts(in HealthFactorInputs, n int) HealthFactorInputs  { in.ActiveAlerts = n; return in }
func withConn(in HealthFactorInputs, n int) HealthFactorInputs    { in.ConnCount = n; return in }
func withOnline(in HealthFactorInputs, online, capacity int) HealthFactorInputs {
	in.OnlineCount, in.MaxOnline = online, capacity
	return in
}

func almostEq(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

// TestFactorApplicabilityByKind 校验 proxy / backend 因子集自动适配（§4.4 适用列）。
func TestFactorApplicabilityByKind(t *testing.T) {
	cfg := DefaultHealthWeightsConfig()
	backendFactors := ComputeHealthFactors(backendInputs(), cfg)
	proxyFactors := ComputeHealthFactors(proxyInputs(), cfg)

	wantBackend := map[string]bool{
		healthFactorTPS: true, healthFactorCPU: true, healthFactorCapacity: true,
		healthFactorConn: false, healthFactorLatency: true, healthFactorAlert: true,
	}
	wantProxy := map[string]bool{
		healthFactorTPS: false, healthFactorCPU: true, healthFactorCapacity: false,
		healthFactorConn: true, healthFactorLatency: true, healthFactorAlert: true,
	}
	for name, want := range wantBackend {
		if got := factorByName(t, backendFactors, name).Applicable; got != want {
			t.Fatalf("backend 因子 %s 适用性期望 %v，实际 %v", name, want, got)
		}
	}
	for name, want := range wantProxy {
		if got := factorByName(t, proxyFactors, name).Applicable; got != want {
			t.Fatalf("proxy 因子 %s 适用性期望 %v，实际 %v", name, want, got)
		}
	}
}

// TestFactorUnavailableSentinels 校验不可用哨兵剔除：rtt<0 → latency 不适用；
// cpu<0（窗口全毛刺）→ cpu 不适用（与 latency 同构对称处理）；maxOnline≤0 → capacity 不适用。
func TestFactorUnavailableSentinels(t *testing.T) {
	cfg := DefaultHealthWeightsConfig()
	if f := factorByName(t, ComputeHealthFactors(withRtt(backendInputs(), -1), cfg), healthFactorLatency); f.Applicable {
		t.Fatal("rtt=-1 时 latency 应不适用")
	}
	if f := factorByName(t, ComputeHealthFactors(withCPU(backendInputs(), -1), cfg), healthFactorCPU); f.Applicable {
		t.Fatal("cpu=-1（窗口全不可用）时 cpu 应不适用")
	}
	if f := factorByName(t, ComputeHealthFactors(withOnline(backendInputs(), 0, 0), cfg), healthFactorCapacity); f.Applicable {
		t.Fatal("maxOnline=0 时 capacity 应不适用")
	}
	// 不适用因子归一化分强制 0、raw 保留原值供解释。
	f := factorByName(t, ComputeHealthFactors(withRtt(backendInputs(), -1), cfg), healthFactorLatency)
	if f.Normalized != 0 || f.Raw != -1 {
		t.Fatalf("不适用因子应 normalized=0 raw 保留，实际 %+v", f)
	}
}

// TestHealthScoreRenormalization 校验权重重归一：仅对适用因子求和，角色差异不失真（§4.4 综合分）。
func TestHealthScoreRenormalization(t *testing.T) {
	cfg := DefaultHealthWeightsConfig()

	// backend 全适用、全满分 → 100。
	if got := HealthScore(ComputeHealthFactors(backendInputs(), cfg)); got != 100 {
		t.Fatalf("全满分应 100，实际 %d", got)
	}
	// backend 剔除 latency（rtt=-1）后其余全满分 → 权重重归一仍 100（不因缺因子被稀释）。
	if got := HealthScore(ComputeHealthFactors(withRtt(backendInputs(), -1), cfg)); got != 100 {
		t.Fatalf("重归一后应 100，实际 %d", got)
	}
	// proxy 全适用（cpu/conn/latency/alert）全满分 → 100（无 tps/capacity 不失真）。
	if got := HealthScore(ComputeHealthFactors(proxyInputs(), cfg)); got != 100 {
		t.Fatalf("proxy 全满分应 100，实际 %d", got)
	}
	// 手工构造验证加权均值与 round：适用 tps=0（w30）+ cpu=100（w20）→ (0×30+100×20)/50 = 40。
	factors := []healthview.Factor{
		{Factor: healthFactorTPS, Normalized: 0, Weight: 30, Applicable: true},
		{Factor: healthFactorCPU, Normalized: 100, Weight: 20, Applicable: true},
		{Factor: healthFactorConn, Normalized: 100, Weight: 10, Applicable: false}, // 不适用不计
	}
	if got := HealthScore(factors); got != 40 {
		t.Fatalf("加权重归一期望 40，实际 %d", got)
	}
	// round 四舍五入：(100×30+0×20)/50 = 60；(50×30+49×20)/50 = 49.6 → 50。
	factors = []healthview.Factor{
		{Factor: healthFactorTPS, Normalized: 50, Weight: 30, Applicable: true},
		{Factor: healthFactorCPU, Normalized: 49, Weight: 20, Applicable: true},
	}
	if got := HealthScore(factors); got != 50 {
		t.Fatalf("四舍五入期望 50，实际 %d", got)
	}
	// 无任何适用因子（或权重全 0）→ 0。
	if got := HealthScore([]healthview.Factor{{Factor: healthFactorTPS, Normalized: 100, Weight: 30, Applicable: false}}); got != 0 {
		t.Fatal("无适用因子应得 0 分")
	}
	if got := HealthScore([]healthview.Factor{{Factor: healthFactorTPS, Normalized: 100, Weight: 0, Applicable: true}}); got != 0 {
		t.Fatal("权重全 0 应得 0 分")
	}
}

// TestHealthLevelBoundaries 校验等级阈值边界（≥healthyMin → healthy；≥degradedMin → degraded；否则 unhealthy）。
func TestHealthLevelBoundaries(t *testing.T) {
	levels := HealthLevelThresholds{HealthyMin: 80, DegradedMin: 50}
	cases := []struct {
		score int
		want  string
	}{
		{100, healthview.LevelHealthy},
		{80, healthview.LevelHealthy},
		{79, healthview.LevelDegraded},
		{50, healthview.LevelDegraded},
		{49, healthview.LevelUnhealthy},
		{0, healthview.LevelUnhealthy},
	}
	for _, c := range cases {
		if got := HealthLevelOf(c.score, levels); got != c.want {
			t.Fatalf("score=%d 期望 %s，实际 %s", c.score, c.want, got)
		}
	}
}

// TestSchedulableReasonsExhaustive 穷举 §4.5 七类原因逐一触发与叠加；degraded 不排除。
func TestSchedulableReasonsExhaustive(t *testing.T) {
	// 全部不成立 → schedulable（空原因）。
	ok := HealthScheduleFacts{
		Kind: model.ServerKindBackend, IdentityStatus: model.AgentIdentityStatusActive,
		Level: healthview.LevelHealthy,
	}
	if got := SchedulableReasons(ok); len(got) != 0 {
		t.Fatalf("健康 backend 应 schedulable，实际原因 %v", got)
	}

	单项 := []struct {
		名称 string
		变换 func(HealthScheduleFacts) HealthScheduleFacts
		原因 string
	}{
		{"kind非backend", func(f HealthScheduleFacts) HealthScheduleFacts {
			f.Kind = model.ServerKindProxy
			return f
		}, healthview.ReasonKindNotSchedulable},
		{"未人工确认", func(f HealthScheduleFacts) HealthScheduleFacts {
			f.IdentityStatus = model.AgentIdentityStatusPending
			return f
		}, healthview.ReasonPendingConfirm},
		{"未分配小区", func(f HealthScheduleFacts) HealthScheduleFacts {
			f.Unassigned = true
			return f
		}, healthview.ReasonUnassigned},
		{"身份禁用", func(f HealthScheduleFacts) HealthScheduleFacts {
			f.IdentityStatus = model.AgentIdentityStatusDisabled
			return f
		}, healthview.ReasonDisabled},
		{"排空中", func(f HealthScheduleFacts) HealthScheduleFacts {
			f.Draining = true
			return f
		}, healthview.ReasonDraining},
		{"失联", func(f HealthScheduleFacts) HealthScheduleFacts {
			f.Lost = true
			return f
		}, healthview.ReasonLost},
		{"不健康", func(f HealthScheduleFacts) HealthScheduleFacts {
			f.Level = healthview.LevelUnhealthy
			return f
		}, healthview.ReasonUnhealthy},
	}
	for _, c := range 单项 {
		t.Run(c.名称, func(t *testing.T) {
			got := SchedulableReasons(c.变换(ok))
			if len(got) != 1 || got[0] != c.原因 {
				t.Fatalf("应只含原因 %s，实际 %v", c.原因, got)
			}
		})
	}

	// degraded 不排除（仍 schedulable）。
	degraded := ok
	degraded.Level = healthview.LevelDegraded
	if got := SchedulableReasons(degraded); len(got) != 0 {
		t.Fatalf("degraded 不应排除，实际 %v", got)
	}

	// 原因可叠加且按 spec 表序：pending + unassigned + draining + lost + unhealthy。
	stacked := HealthScheduleFacts{
		Kind: model.ServerKindBackend, IdentityStatus: model.AgentIdentityStatusPending,
		Unassigned: true, Draining: true, Lost: true, Level: healthview.LevelUnhealthy,
	}
	want := []string{
		healthview.ReasonPendingConfirm, healthview.ReasonUnassigned,
		healthview.ReasonDraining, healthview.ReasonLost, healthview.ReasonUnhealthy,
	}
	if got := SchedulableReasons(stacked); !reflect.DeepEqual(got, want) {
		t.Fatalf("叠加原因期望 %v，实际 %v", want, got)
	}

	// proxy 的 unassigned 不叠加（proxy 不按 zone 分配，仅报 kind 原因）。
	proxyUnassigned := HealthScheduleFacts{
		Kind: model.ServerKindProxy, IdentityStatus: model.AgentIdentityStatusActive,
		Unassigned: true, Level: healthview.LevelHealthy,
	}
	if got := SchedulableReasons(proxyUnassigned); !reflect.DeepEqual(got, []string{healthview.ReasonKindNotSchedulable}) {
		t.Fatalf("proxy 未分配应只报 kind 原因，实际 %v", got)
	}
}

// TestDefaultHealthWeightsConfig 校验默认配置与 §4.4 括号内默认值一致（种子 rev=1 的真源）。
func TestDefaultHealthWeightsConfig(t *testing.T) {
	cfg := DefaultHealthWeightsConfig()
	if cfg.Weights != (HealthWeights{TPS: 30, CPU: 20, Capacity: 20, Conn: 10, Latency: 10, Alert: 10}) {
		t.Fatalf("默认权重不符: %+v", cfg.Weights)
	}
	wantNorm := HealthNormalize{
		TPSGood: 19.5, TPSBad: 10, CPUGood: 40, CPUBad: 90,
		CapGood: 0.6, CapBad: 0.95, ConnSoftLimit: 2000,
		LatGoodMs: 50, LatBadMs: 500, AlertPenalty: 25,
	}
	if cfg.Normalize != wantNorm {
		t.Fatalf("默认归一化参数不符: %+v", cfg.Normalize)
	}
	if cfg.Levels != (HealthLevelThresholds{HealthyMin: 80, DegradedMin: 50}) {
		t.Fatalf("默认阈值不符: %+v", cfg.Levels)
	}
}
