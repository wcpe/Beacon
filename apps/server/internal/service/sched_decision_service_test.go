package service

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"regexp"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// schedTestIdentity 是决策单测的固定请求方身份。
func schedTestIdentity() agentauth.Identity {
	return agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "req-1", Kind: "backend"}
}

// newSchedServiceForTest 构造注入固定随机种子与步进时钟的决策服务（同种子结果可复现）。
// 每次 now() 前进 1ms，DurationMs 可确定断言。
func newSchedServiceForTest(store *healthview.Store, seed uint64) *SchedulingV2Service {
	svc := NewSchedulingV2Service(store, rand.New(rand.NewPCG(seed, seed)))
	base := time.UnixMilli(1_752_000_000_000).UTC()
	tick := 0
	svc.now = func() time.Time {
		t := base.Add(time.Duration(tick) * time.Millisecond)
		tick++
		return t
	}
	return svc
}

// backendView 构造一台可调度 backend 视图。
func backendView(serverID, zone string, score, online, maxOnline int) healthview.View {
	return healthview.View{
		NamespaceID: 1, Namespace: "prod", ServerID: serverID, Kind: "backend", ZoneName: zone,
		Score: score, Level: healthview.LevelHealthy, Schedulable: true,
		Reasons: []string{}, WeightsRev: 3, OnlineCount: online, MaxOnline: maxOnline,
	}
}

// excludedView 构造一台不可调度视图（携带给定原因码序列）。
func excludedView(serverID, zone string, reasons ...string) healthview.View {
	v := backendView(serverID, zone, 0, 0, 100)
	v.Schedulable = false
	v.Level = healthview.LevelUnhealthy
	v.Reasons = reasons
	return v
}

// TestDecideHighestScoreWins 分数最高者胜；候选数 / 排除数 / traceId / 耗时字段齐全。
func TestDecideHighestScoreWins(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("s-low", "area-1", 60, 10, 100),
		backendView("s-high", "area-1", 95, 50, 100),
		backendView("s-mid", "area-1", 80, 5, 100),
	})
	svc := newSchedServiceForTest(store, 1)

	out, err := svc.Decide(schedTestIdentity(), "area-1", "lobby-transfer", "Lodestone")
	if err != nil {
		t.Fatalf("决策不应出错: %v", err)
	}
	if out.ChosenServerID != "s-high" || out.ChosenScore != 95 {
		t.Fatalf("应选最高分 s-high(95)，实际 %s(%d)", out.ChosenServerID, out.ChosenScore)
	}
	if out.CandidateCount != 3 || len(out.Excluded) != 0 {
		t.Fatalf("候选应 3 台无排除，实际 %d/%d", out.CandidateCount, len(out.Excluded))
	}
	if out.WeightsRev != 3 || out.Strategy != SchedStrategyHighestScore || out.Source != SchedSourceControlPlane {
		t.Fatalf("weightsRev/strategy/source 不符: %+v", out)
	}
	if out.CrossNamespace {
		t.Fatal("cross_namespace 应恒为 false")
	}
	if out.FailReason != "" || out.DurationMs < 0 || out.TsMs != 1_752_000_000_000 {
		t.Fatalf("failReason/耗时/tsMs 不符: %+v", out)
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidRe.MatchString(out.TraceID) {
		t.Fatalf("traceId 应为 UUID v4，实际 %q", out.TraceID)
	}
}

// TestDecideTieBreakByOccupancy 同分优先容量占用率低者；maxOnline≤0 视为占满。
func TestDecideTieBreakByOccupancy(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("s-busy", "area-1", 90, 90, 100),   // 占用 0.9
		backendView("s-free", "area-1", 90, 10, 100),   // 占用 0.1 → 胜
		backendView("s-nocap", "area-1", 90, 0, 0),     // maxOnline=0 → 视为 1.0
		backendView("s-lower", "area-1", 89, 0, 10000), // 分数低一档
	})
	for seed := uint64(1); seed <= 5; seed++ {
		svc := newSchedServiceForTest(store, seed)
		out, err := svc.Decide(schedTestIdentity(), "area-1", "", "")
		if err != nil {
			t.Fatalf("seed=%d 决策不应出错: %v", seed, err)
		}
		if out.ChosenServerID != "s-free" {
			t.Fatalf("seed=%d 同分应选占用率最低的 s-free，实际 %s", seed, out.ChosenServerID)
		}
	}
}

// TestDecideTieBreakRandomSeeded 同分同容量随机决胜：固定种子结果可复现，且不同种子覆盖多台。
func TestDecideTieBreakRandomSeeded(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("s-a", "area-1", 90, 10, 100),
		backendView("s-b", "area-1", 90, 10, 100),
		backendView("s-c", "area-1", 90, 10, 100),
	})
	first := ""
	for i := 0; i < 3; i++ {
		svc := newSchedServiceForTest(store, 42)
		out, err := svc.Decide(schedTestIdentity(), "area-1", "", "")
		if err != nil {
			t.Fatalf("决策不应出错: %v", err)
		}
		if first == "" {
			first = out.ChosenServerID
		} else if out.ChosenServerID != first {
			t.Fatalf("同种子应可复现，首次 %s 本次 %s", first, out.ChosenServerID)
		}
	}
	seen := map[string]bool{}
	for seed := uint64(1); seed <= 32; seed++ {
		svc := newSchedServiceForTest(store, seed)
		out, _ := svc.Decide(schedTestIdentity(), "area-1", "", "")
		seen[out.ChosenServerID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("多种子下应覆盖多台候选，实际仅 %v", seen)
	}
}

// TestDecideExcludedReasons 逐台排除：不可调度者按第一条原因码记入 excluded，degraded 可调度不排除。
func TestDecideExcludedReasons(t *testing.T) {
	store := healthview.NewStore()
	degraded := backendView("s-degraded", "area-1", 55, 10, 100)
	degraded.Level = healthview.LevelDegraded
	store.ReplaceAll([]healthview.View{
		excludedView("s-drain", "area-1", healthview.ReasonDraining),
		excludedView("s-lost", "area-1", healthview.ReasonLost, healthview.ReasonUnhealthy),
		excludedView("s-pending", "area-1", healthview.ReasonPendingConfirm),
		degraded,
	})
	svc := newSchedServiceForTest(store, 7)

	out, err := svc.Decide(schedTestIdentity(), "area-1", "", "")
	if err != nil {
		t.Fatalf("决策不应出错: %v", err)
	}
	if out.ChosenServerID != "s-degraded" {
		t.Fatalf("degraded 仍可调度、应被选中，实际 %s", out.ChosenServerID)
	}
	want := []SchedExcluded{
		{ServerID: "s-drain", Reason: healthview.ReasonDraining},
		{ServerID: "s-lost", Reason: healthview.ReasonLost},
		{ServerID: "s-pending", Reason: healthview.ReasonPendingConfirm},
	}
	if len(out.Excluded) != len(want) {
		t.Fatalf("排除应 %d 台，实际 %d：%+v", len(want), len(out.Excluded), out.Excluded)
	}
	for i, w := range want {
		if out.Excluded[i] != w {
			t.Fatalf("排除[%d] 应 %+v，实际 %+v（多原因取第一条）", i, w, out.Excluded[i])
		}
	}
	if out.CandidateCount != 4 {
		t.Fatalf("进入评估候选应 4 台，实际 %d", out.CandidateCount)
	}
}

// TestDecideZoneNotFound ns 内无该 zone 名 → 404 错误，但决策行仍产出（failReason 可查）。
func TestDecideZoneNotFound(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("s-1", "area-1", 90, 0, 100),
		// 其他 namespace 的同名 zone 不可见（跨 ns 默认拒绝语境下按不存在处理）
		func() healthview.View {
			v := backendView("other-ns", "area-2", 99, 0, 100)
			v.NamespaceID = 2
			return v
		}(),
	})
	svc := newSchedServiceForTest(store, 1)

	out, err := svc.Decide(schedTestIdentity(), "area-2", "", "")
	if !errors.Is(err, apperr.ErrSchedZoneNotFound) {
		t.Fatalf("应返回 ErrSchedZoneNotFound，实际 %v", err)
	}
	if out.FailReason != SchedFailZoneNotFound || out.TraceID == "" || out.CandidateCount != 0 {
		t.Fatalf("zone_not_found 决策行不符: %+v", out)
	}
	if out.ChosenServerID != "" || out.ChosenScore != -1 {
		t.Fatalf("失败决策 chosen 应为空/-1，实际 %s/%d", out.ChosenServerID, out.ChosenScore)
	}
}

// TestDecideNoCandidate 候选全被排除 → 成功返回（非 HTTP 错误）但 failReason=no_candidate。
func TestDecideNoCandidate(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		excludedView("s-1", "area-1", healthview.ReasonUnhealthy),
		excludedView("s-2", "area-1", healthview.ReasonDraining),
	})
	svc := newSchedServiceForTest(store, 1)

	out, err := svc.Decide(schedTestIdentity(), "area-1", "", "")
	if err != nil {
		t.Fatalf("no_candidate 不是 HTTP 错误，实际 %v", err)
	}
	if out.FailReason != SchedFailNoCandidate || out.ChosenServerID != "" || out.ChosenScore != -1 {
		t.Fatalf("no_candidate 决策不符: %+v", out)
	}
	if out.CandidateCount != 2 || len(out.Excluded) != 2 {
		t.Fatalf("候选/排除计数不符: %+v", out)
	}
	if out.WeightsRev != 3 {
		t.Fatalf("无选中时 weightsRev 应取候选视图值 3，实际 %d", out.WeightsRev)
	}
}

// TestDecideDurationFromClock 耗时字段来自注入时钟差（步进时钟下为确定值）。
func TestDecideDurationFromClock(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{backendView("s-1", "area-1", 90, 0, 100)})
	svc := newSchedServiceForTest(store, 1)

	out, err := svc.Decide(schedTestIdentity(), "area-1", "", "")
	if err != nil {
		t.Fatalf("决策不应出错: %v", err)
	}
	// 步进时钟：Decide 内调 now() 两次（起点 + 结算），差恒为 1ms。
	if out.DurationMs != 1 {
		t.Fatalf("步进时钟下 durationMs 应为 1，实际 %d", out.DurationMs)
	}
}

// TestDecideParamValidation zone 必填、字段超日表列宽 → 400 参数错误。
func TestDecideParamValidation(t *testing.T) {
	store := healthview.NewStore()
	svc := newSchedServiceForTest(store, 1)
	long := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}
	cases := []struct {
		name                  string
		zone, purpose, plugin string
	}{
		{"zone 为空", "", "", ""},
		{"zone 超长", long(65), "", ""},
		{"purpose 超长", "area-1", long(129), ""},
		{"plugin 超长", "area-1", "", long(65)},
	}
	for _, c := range cases {
		if _, err := svc.Decide(schedTestIdentity(), c.zone, c.purpose, c.plugin); !errors.Is(err, apperr.ErrInvalidParam) {
			t.Fatalf("%s 应 ErrInvalidParam，实际 %v", c.name, err)
		}
	}
}

// TestSchedExcludedJSONShape excluded 序列化形状与 spec §3.4 json 数组一致（camelCase 键）。
func TestSchedExcludedJSONShape(t *testing.T) {
	raw, err := json.Marshal([]SchedExcluded{{ServerID: "s-1", Reason: "draining"}})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	want := `[{"serverId":"s-1","reason":"draining"}]`
	if string(raw) != want {
		t.Fatalf("excluded json 形状应为 %s，实际 %s", want, raw)
	}
}
