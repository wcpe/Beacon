package handler

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// newSchedHandlerForTest 构造挂真实服务的调度处理器：健康视图直填、入库通道接空 flusher（不碰 DB）。
func newSchedHandlerForTest(views []healthview.View) *V2SchedHandler {
	store := healthview.NewStore()
	store.ReplaceAll(views)
	svc := service.NewSchedulingV2Service(store, rand.New(rand.NewPCG(1, 1)))
	writer := service.NewAsyncDailyWriter()
	service.RegisterFlusher(writer, service.RouteKindSchedDecision,
		func(_ []model.SchedDecisionV2) (int, error) { return 0, nil })
	svc.SetDecisionEnqueuer(service.SchedDecisionEnqueuer{Writer: writer})
	return NewV2SchedHandler(svc)
}

// schedAgentRequest 构造带已鉴权身份 context 的请求（模拟 agentV2ReportMiddleware 注入）。
func schedAgentRequest(method, target string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	id := agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "req-1", Kind: "backend"}
	return req.WithContext(agentauth.WithIdentity(req.Context(), id))
}

// schedTestViews 返回两 zone 的健康视图：area-1 两台可调度 + 一台排除，area-2 全排除，另 ns 一台。
func schedTestViews() []healthview.View {
	mk := func(ns uint, server, zone string, score int, schedulable bool) healthview.View {
		v := healthview.View{
			NamespaceID: ns, Namespace: "prod", ServerID: server, Kind: "backend", ZoneName: zone,
			Score: score, Level: healthview.LevelHealthy, Schedulable: schedulable,
			Reasons: []string{}, WeightsRev: 2, OnlineCount: 10, MaxOnline: 100,
		}
		if !schedulable {
			v.Level = healthview.LevelUnhealthy
			v.Reasons = []string{healthview.ReasonUnhealthy}
		}
		return v
	}
	return []healthview.View{
		mk(1, "s-a", "area-1", 90, true),
		mk(1, "s-b", "area-1", 80, true),
		mk(1, "s-x", "area-1", 0, false),
		mk(1, "s-y", "area-2", 0, false),
		mk(2, "s-other", "area-9", 99, true),
	}
}

// keysOf 返回 json 对象的键集合（排序后），供逐键契约断言。
func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertKeys 断言 json 对象的键集合与期望完全一致（不多不少）。
func assertKeys(t *testing.T, m map[string]any, want ...string) {
	t.Helper()
	sort.Strings(want)
	got := keysOf(m)
	if len(got) != len(want) {
		t.Fatalf("键集合应 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("键集合应 %v，实际 %v", want, got)
		}
	}
}

// TestSchedCandidatesResponseShape 候选快照：键逐字对齐 §5.1，仅含可调度候选、仅列有候选的 zone。
func TestSchedCandidatesResponseShape(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	rec := httptest.NewRecorder()
	h.Candidates(rec, schedAgentRequest(http.MethodGet, "/beacon/v2/agent/schedule/candidates", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 json: %v", err)
	}
	assertKeys(t, body, "generatedAtMs", "zones")
	zones, _ := body["zones"].([]any)
	if len(zones) != 1 {
		t.Fatalf("仅 area-1 有候选、应 1 个 zone，实际 %d：%v", len(zones), zones)
	}
	zone, _ := zones[0].(map[string]any)
	assertKeys(t, zone, "zone", "candidates")
	if zone["zone"] != "area-1" {
		t.Fatalf("zone 应 area-1，实际 %v", zone["zone"])
	}
	candidates, _ := zone["candidates"].([]any)
	if len(candidates) != 2 {
		t.Fatalf("area-1 应 2 台可调度候选，实际 %d", len(candidates))
	}
	first, _ := candidates[0].(map[string]any)
	assertKeys(t, first, "serverId", "score", "level", "schedulable", "onlineCount", "maxOnline")
	if first["serverId"] != "s-a" || first["score"] != float64(90) || first["schedulable"] != true {
		t.Fatalf("候选按分数降序、首台应 s-a(90)，实际 %v", first)
	}
}

// TestSchedDecideResponseShapeSuccess decide 成功：五键齐全、chosen 对象两键、failReason 为 null。
func TestSchedDecideResponseShapeSuccess(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	rec := httptest.NewRecorder()
	h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide",
		map[string]any{"zone": "area-1", "purpose": "lobby-transfer", "plugin": "Lodestone"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	assertKeys(t, body, "traceId", "chosen", "candidateCount", "excludedCount", "failReason")
	chosen, ok := body["chosen"].(map[string]any)
	if !ok {
		t.Fatalf("chosen 应为对象，实际 %v", body["chosen"])
	}
	assertKeys(t, chosen, "serverId", "score")
	if chosen["serverId"] != "s-a" || chosen["score"] != float64(90) {
		t.Fatalf("应选 s-a(90)，实际 %v", chosen)
	}
	if body["candidateCount"] != float64(3) || body["excludedCount"] != float64(1) {
		t.Fatalf("candidateCount/excludedCount 应 3/1，实际 %v/%v", body["candidateCount"], body["excludedCount"])
	}
	if body["failReason"] != nil {
		t.Fatalf("成功时 failReason 应为 null，实际 %v", body["failReason"])
	}
}

// TestSchedDecideNoCandidate 全排除 zone：200 + chosen null + failReason=no_candidate。
func TestSchedDecideNoCandidate(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	rec := httptest.NewRecorder()
	h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide",
		map[string]any{"zone": "area-2"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("no_candidate 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["chosen"] != nil {
		t.Fatalf("无候选 chosen 应为 null，实际 %v", body["chosen"])
	}
	if body["failReason"] != "no_candidate" {
		t.Fatalf("failReason 应 no_candidate，实际 %v", body["failReason"])
	}
}

// TestSchedDecideZoneNotFound zone 不存在（含他 ns 同名 zone 不可见）：404 zone_not_found。
func TestSchedDecideZoneNotFound(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	rec := httptest.NewRecorder()
	h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide",
		map[string]any{"zone": "area-9"}))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "zone_not_found" {
		t.Fatalf("错误码应 zone_not_found，实际 %v", body["code"])
	}
}

// TestSchedDecideBadBody 非法请求体 / 缺 zone：400 参数错误。
func TestSchedDecideBadBody(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	for name, body := range map[string]any{"缺 zone": map[string]any{}, "zone 为空": map[string]any{"zone": ""}} {
		rec := httptest.NewRecorder()
		h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide", body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d", name, rec.Code)
		}
	}
}

// TestSchedReportLocalResponseShape 补报：202 {accepted, deduplicated}，重放判重。
func TestSchedReportLocalResponseShape(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	payload := map[string]any{"decisions": []map[string]any{
		{"localTraceId": "local-1", "tsMs": 1_752_000_000_000, "zone": "area-1",
			"plugin": "Lodestone", "purpose": "lobby-transfer", "candidateCount": 2,
			"excluded":       []map[string]any{{"serverId": "s-x", "reason": "unhealthy"}},
			"chosenServerId": "s-a"},
		{"localTraceId": "local-2", "tsMs": 1_752_000_001_000, "zone": "area-1",
			"candidateCount": 0, "excluded": []map[string]any{}, "failReason": "no_candidate"},
	}}
	rec := httptest.NewRecorder()
	h.ReportLocal(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/report-local", payload))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("应 202，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	assertKeys(t, body, "accepted", "deduplicated")
	if body["accepted"] != float64(2) || body["deduplicated"] != float64(0) {
		t.Fatalf("首报应 accepted=2/deduplicated=0，实际 %v", body)
	}

	// 重放同批 → 全部判重。
	rec = httptest.NewRecorder()
	h.ReportLocal(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/report-local", payload))
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusAccepted || body["accepted"] != float64(0) || body["deduplicated"] != float64(2) {
		t.Fatalf("重放应 accepted=0/deduplicated=2，实际 %d %v", rec.Code, body)
	}
}

// TestSchedReportLocalOverBatchLimit 单批 >100 条 → 400。
func TestSchedReportLocalOverBatchLimit(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	decisions := make([]map[string]any, 0, 101)
	for i := 0; i < 101; i++ {
		decisions = append(decisions, map[string]any{
			"localTraceId": string(rune('a'+i%26)) + "-trace", "tsMs": 1, "zone": "area-1"})
	}
	rec := httptest.NewRecorder()
	h.ReportLocal(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/report-local",
		map[string]any{"decisions": decisions}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超 100 条应 400，实际 %d", rec.Code)
	}
}
