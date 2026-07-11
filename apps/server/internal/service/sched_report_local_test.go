package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// localReport 构造一条最小合法补报。
func localReport(trace string) LocalDecisionReport {
	return LocalDecisionReport{LocalTraceID: trace, TsMs: 1_752_000_000_000, Zone: "area-1"}
}

// TestReportLocalMapsFallbackRow 补报映射为 source=local_fallback 行：trace_id=localTraceId、
// 归属取权威身份、weightsRev=0、chosenScore=-1、excluded json 文本。
func TestReportLocalMapsFallbackRow(t *testing.T) {
	svc := newSchedServiceForTest(healthview.NewStore(), 1)
	sink := &fakeSchedEnqueuer{}
	svc.SetDecisionEnqueuer(sink)

	report := LocalDecisionReport{
		LocalTraceID: "local-abc", TsMs: 1_752_000_000_000, Zone: "area-1",
		Plugin: "Lodestone", Purpose: "lobby-transfer", CandidateCount: 3,
		Excluded:       []SchedExcluded{{ServerID: "s-x", Reason: "lost"}},
		ChosenServerID: "s-a",
	}
	res, err := svc.ReportLocal(schedTestIdentity(), []LocalDecisionReport{report})
	if err != nil {
		t.Fatalf("补报不应出错: %v", err)
	}
	if res.Accepted != 1 || res.Deduplicated != 0 {
		t.Fatalf("应 accepted=1，实际 %+v", res)
	}
	if len(sink.rows) != 1 {
		t.Fatalf("应入队 1 行，实际 %d", len(sink.rows))
	}
	row := sink.rows[0]
	if row.TraceID != "local-abc" || row.Source != SchedSourceLocalFallback ||
		row.Strategy != SchedStrategyHighestScore || row.NamespaceID != 1 ||
		row.RequesterServerID != "req-1" || row.WeightsRev != 0 || row.ChosenScore != -1 ||
		row.ChosenServerID != "s-a" || row.CandidateCount != 3 || row.TsMs != 1_752_000_000_000 ||
		row.CrossNamespace || row.DurationMs != 0 {
		t.Fatalf("补报行映射不符: %+v", row)
	}
	if row.Excluded != `[{"serverId":"s-x","reason":"lost"}]` {
		t.Fatalf("excluded json 不符: %s", row.Excluded)
	}
}

// TestReportLocalNilExcludedAsEmptyArray 补报缺 excluded → 落 "[]"（不落 null 文本）。
func TestReportLocalNilExcludedAsEmptyArray(t *testing.T) {
	svc := newSchedServiceForTest(healthview.NewStore(), 1)
	sink := &fakeSchedEnqueuer{}
	svc.SetDecisionEnqueuer(sink)

	if _, err := svc.ReportLocal(schedTestIdentity(), []LocalDecisionReport{localReport("local-1")}); err != nil {
		t.Fatalf("补报不应出错: %v", err)
	}
	if sink.rows[0].Excluded != "[]" {
		t.Fatalf("excluded 应为 []，实际 %s", sink.rows[0].Excluded)
	}
}

// TestReportLocalDedup 判重：重放全判重、批内重复判重、不同 (ns, server) 判重集合互不影响。
func TestReportLocalDedup(t *testing.T) {
	svc := newSchedServiceForTest(healthview.NewStore(), 1)
	sink := &fakeSchedEnqueuer{}
	svc.SetDecisionEnqueuer(sink)
	id := schedTestIdentity()

	// 首报 2 条 + 批内重复 1 条。
	res, err := svc.ReportLocal(id, []LocalDecisionReport{
		localReport("t-1"), localReport("t-2"), localReport("t-1"),
	})
	if err != nil || res.Accepted != 2 || res.Deduplicated != 1 {
		t.Fatalf("首报应 accepted=2/dedup=1，实际 %+v err=%v", res, err)
	}
	// 重放 → 全判重。
	res, _ = svc.ReportLocal(id, []LocalDecisionReport{localReport("t-1"), localReport("t-2")})
	if res.Accepted != 0 || res.Deduplicated != 2 {
		t.Fatalf("重放应全判重，实际 %+v", res)
	}
	// 另一台服同 trace 不受影响（判重按 (namespace, server) 维度）。
	other := agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "req-2", Kind: "backend"}
	res, _ = svc.ReportLocal(other, []LocalDecisionReport{localReport("t-1")})
	if res.Accepted != 1 || res.Deduplicated != 0 {
		t.Fatalf("不同 server 判重集合应独立，实际 %+v", res)
	}
}

// TestReportLocalQueueFullNotMarked 队列满：本批不入库也不登记判重，重试仍可接收（数据不静默丢）。
func TestReportLocalQueueFullNotMarked(t *testing.T) {
	svc := newSchedServiceForTest(healthview.NewStore(), 1)
	sink := &fakeSchedEnqueuer{full: true}
	svc.SetDecisionEnqueuer(sink)
	id := schedTestIdentity()

	res, err := svc.ReportLocal(id, []LocalDecisionReport{localReport("t-1")})
	if err != nil || res.Accepted != 0 || res.Deduplicated != 0 {
		t.Fatalf("队列满应 accepted=0 且无错，实际 %+v err=%v", res, err)
	}
	// 队列恢复后重报同 trace → 正常接收（未被误标已补报）。
	sink.full = false
	res, _ = svc.ReportLocal(id, []LocalDecisionReport{localReport("t-1")})
	if res.Accepted != 1 {
		t.Fatalf("恢复后重报应接收，实际 %+v", res)
	}
}

// TestReportLocalValidation 单批超限与字段校验：非法即 400 整批拒绝。
func TestReportLocalValidation(t *testing.T) {
	svc := newSchedServiceForTest(healthview.NewStore(), 1)
	id := schedTestIdentity()

	over := make([]LocalDecisionReport, 101)
	for i := range over {
		over[i] = localReport(fmt.Sprintf("t-%d", i))
	}
	cases := map[string][]LocalDecisionReport{
		"超 100 条":          over,
		"localTraceId 为空":  {localReport("")},
		"localTraceId 超长":  {localReport(strings.Repeat("a", 37))},
		"zone 为空":          {{LocalTraceID: "t-1", TsMs: 1}},
		"tsMs 非正":          {{LocalTraceID: "t-1", Zone: "area-1"}},
		"candidateCount 负": {{LocalTraceID: "t-1", TsMs: 1, Zone: "area-1", CandidateCount: -1}},
	}
	for name, decisions := range cases {
		if _, err := svc.ReportLocal(id, decisions); !errors.Is(err, apperr.ErrInvalidParam) {
			t.Fatalf("%s 应 ErrInvalidParam，实际 %v", name, err)
		}
	}
	// 空批为安全空操作。
	if res, err := svc.ReportLocal(id, nil); err != nil || res.Accepted != 0 {
		t.Fatalf("空批应无错空结果，实际 %+v err=%v", res, err)
	}
}

// TestBoundedTraceSetFIFOEviction 判重集合容量满按 FIFO 淘汰最旧，容量恒不超限。
func TestBoundedTraceSetFIFOEviction(t *testing.T) {
	set := newBoundedTraceSet(3)
	for _, tr := range []string{"a", "b", "c"} {
		set.Add(tr)
	}
	if !set.Has("a") || !set.Has("b") || !set.Has("c") {
		t.Fatal("容量内应全部命中")
	}
	set.Add("d") // 淘汰最旧 a
	if set.Has("a") {
		t.Fatal("容量满应淘汰最旧的 a")
	}
	if !set.Has("b") || !set.Has("c") || !set.Has("d") {
		t.Fatal("b/c/d 应仍在集合")
	}
	set.Add("d") // 重复登记为幂等空操作，不挤占槽位
	set.Add("e") // 淘汰 b
	if set.Has("b") || !set.Has("c") || !set.Has("d") || !set.Has("e") {
		t.Fatal("重复 Add 不应挤占槽位；e 应淘汰 b")
	}
	if len(set.seen) != 3 {
		t.Fatalf("集合大小应恒 ≤3，实际 %d", len(set.seen))
	}
}

// TestCandidatesSnapshot 候选快照：按 ns 圈定、仅可调度、仅有候选的 zone、排序确定。
func TestCandidatesSnapshot(t *testing.T) {
	store := healthview.NewStore()
	otherNs := backendView("s-other", "area-1", 99, 0, 100)
	otherNs.NamespaceID = 2
	unassigned := backendView("s-unassigned", "", 88, 0, 100)
	store.ReplaceAll([]healthview.View{
		backendView("s-b", "area-1", 80, 0, 100),
		backendView("s-a", "area-1", 90, 0, 100),
		backendView("s-z2", "zone-b", 70, 0, 100),
		excludedView("s-x", "area-2", healthview.ReasonUnhealthy),
		otherNs, unassigned,
	})
	svc := newSchedServiceForTest(store, 1)

	res := svc.Candidates(schedTestIdentity())
	if res.GeneratedAtMs != 1_752_000_000_000 {
		t.Fatalf("generatedAtMs 应取注入时钟，实际 %d", res.GeneratedAtMs)
	}
	if len(res.Zones) != 2 || res.Zones[0].Zone != "area-1" || res.Zones[1].Zone != "zone-b" {
		t.Fatalf("应仅 area-1 / zone-b 两个有候选 zone（按名排序），实际 %+v", res.Zones)
	}
	area1 := res.Zones[0].Candidates
	if len(area1) != 2 || area1[0].ServerID != "s-a" || area1[1].ServerID != "s-b" {
		t.Fatalf("area-1 候选应按分数降序 [s-a, s-b]，实际 %+v", area1)
	}
	if area1[0].Score != 90 || area1[0].Level != healthview.LevelHealthy || !area1[0].Schedulable ||
		area1[0].OnlineCount != 0 || area1[0].MaxOnline != 100 {
		t.Fatalf("候选字段不符: %+v", area1[0])
	}
}
