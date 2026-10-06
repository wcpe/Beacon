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
	assertKeys(t, body, "generatedAtMs", "lobby", "zones")
	lobby, ok := body["lobby"].(map[string]any)
	if !ok {
		t.Fatalf("缺少大厅成员时 lobby 仍应为对象，实际 %v", body["lobby"])
	}
	assertKeys(t, lobby, "candidates", "clusterId", "ready")
	if lobby["ready"] != false {
		t.Fatalf("无大厅候选时 ready 应为 false，实际 %v", lobby)
	}
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

// schedHandlerWithLabels 构造挂真实服务、且**装配了**自声明标签真源的调度处理器（FR-244）。
// 未装配那一档由 newSchedHandlerForTest 覆盖（它同时是"旧装配"的向后兼容形态）。
func schedHandlerWithLabels(views []healthview.View, labels map[string]map[string]string) *V2SchedHandler {
	h := newSchedHandlerForTest(views)
	h.svc.SetDeclarationLabels(schedFakeLabels{byServer: labels})
	return h
}

// schedFakeLabels 是自声明标签真源的替身（键 = serverId）。
type schedFakeLabels struct {
	byServer map[string]map[string]string
}

func (f schedFakeLabels) DeclaredLabels(_ string, serverID string) map[string]string {
	return f.byServer[serverID]
}

// TestSchedCandidatesCarryLabelsWhenDeclarationWired 装配了标签真源时，候选随带 labels 字段；
// 没声明过标签的节点发空对象（= 稳定事实），与"真源未装配"（整个键不发）分开。
func TestSchedCandidatesCarryLabelsWhenDeclarationWired(t *testing.T) {
	h := schedHandlerWithLabels(schedTestViews(), map[string]map[string]string{
		"s-a": {"example.zone.lobby-a": "true"},
	})
	rec := httptest.NewRecorder()
	h.Candidates(rec, schedAgentRequest(http.MethodGet, "/beacon/v2/agent/schedule/candidates", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应应为 json：%v", err)
	}
	zones, _ := body["zones"].([]any)
	zone, _ := zones[0].(map[string]any)
	candidates, _ := zone["candidates"].([]any)
	byID := map[string]map[string]any{}
	for _, raw := range candidates {
		c := raw.(map[string]any)
		byID[c["serverId"].(string)] = c
	}
	first := byID["s-a"]
	assertKeys(t, first, "serverId", "score", "level", "schedulable", "onlineCount", "maxOnline", "labels")
	labels, _ := first["labels"].(map[string]any)
	if labels["example.zone.lobby-a"] != "true" {
		t.Fatalf("候选应带上节点自声明的标签，实际 %v", first["labels"])
	}
	second := byID["s-b"]
	if empty, ok := second["labels"].(map[string]any); !ok || len(empty) != 0 {
		t.Fatalf("没声明过标签的节点应发空对象（稳定事实），实际 %v", second["labels"])
	}
}

// TestSchedDecideAdmissionScopeNarrowsThroughHandler 请求体里的 admissionScope 一路传到服务层：
// 高分节点不满足作用域 → 选低分满足者；全被滤掉 → 200 + no_candidate_in_scope（稳定事实）。
func TestSchedDecideAdmissionScopeNarrowsThroughHandler(t *testing.T) {
	views := []healthview.View{
		func() healthview.View {
			v := schedTestViews()[0]
			v.ServerID, v.Score = "s-high", 99
			return v
		}(),
		schedTestViews()[1], // s-b(80)
	}
	h := schedHandlerWithLabels(views, map[string]map[string]string{
		"s-high": {"example.zone.other-a": "true"},
		"s-b":    {"example.zone.lobby-a": "true"},
	})
	rec := httptest.NewRecorder()
	h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide", map[string]any{
		"zone": "area-1", "admissionScope": []map[string]string{{"example.zone.lobby-a": "true"}}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	chosen, _ := body["chosen"].(map[string]any)
	if chosen == nil || chosen["serverId"] != "s-b" {
		t.Fatalf("应选中满足作用域的 s-b，实际 %v（选中 s-high 说明作用域没进决策）", body["chosen"])
	}
	if body["admissionExcludedCount"] != float64(1) {
		t.Fatalf("响应应如实带出「因作用域被排除」的台数=1，实际 %v", body["admissionExcludedCount"])
	}

	// 换一个谁都不满足的作用域：稳定空结果，原因码与"没候选"分开。
	rec = httptest.NewRecorder()
	h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide", map[string]any{
		"zone": "area-1", "admissionScope": []map[string]string{{"example.zone.nobody": "true"}}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("全被滤掉是稳定结论、应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	body = map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["failReason"] != "no_candidate_in_scope" {
		t.Fatalf("失败原因应为 no_candidate_in_scope，实际 %v", body["failReason"])
	}
	if body["chosen"] != nil {
		t.Fatalf("不应给出目标，实际 %v", body["chosen"])
	}
}

// TestSchedDecideAdmissionScopeWithoutLabelSource 未装配标签真源 + 非空作用域 → 503
// （当前状态、可重试），**不**静默忽略作用域照旧全量决策。
func TestSchedDecideAdmissionScopeWithoutLabelSource(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	rec := httptest.NewRecorder()
	h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide", map[string]any{
		"zone": "area-1", "admissionScope": []map[string]string{{"example.zone.lobby-a": "true"}}}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("应 503，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "admission_unavailable" {
		t.Fatalf("业务码应为 admission_unavailable，实际 %v", body["code"])
	}
}

// TestSchedDecideEmptyAdmissionScopeIsBackwardCompatible 空 / 缺键的 admissionScope 逐位等于旧行为。
func TestSchedDecideEmptyAdmissionScopeIsBackwardCompatible(t *testing.T) {
	h := schedHandlerWithLabels(schedTestViews(), map[string]map[string]string{
		"s-a": {"example.zone.other-a": "true"}, // 即使最高分者不满足，空作用域也不该排除它
	})
	for _, payload := range []map[string]any{
		{"zone": "area-1"},
		{"zone": "area-1", "admissionScope": []map[string]string{}},
	} {
		rec := httptest.NewRecorder()
		h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide", payload))
		if rec.Code != http.StatusOK {
			t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		chosen, _ := body["chosen"].(map[string]any)
		if chosen == nil || chosen["serverId"] != "s-a" {
			t.Fatalf("空作用域下仍应选最高分 s-a，实际 %v（请求 %v）", body["chosen"], payload)
		}
		if _, present := body["admissionExcludedCount"]; present {
			t.Fatalf("没被作用域收窄时不应下发该键（旧响应逐字不变），实际 %v", body)
		}
	}
}

// TestSchedDecideUnconstrainedAdmissionScopeIsBackwardCompatible 备选全为空对象（`[{}]`）是无约束作用域：
// 即便本进程**没装配**标签真源也照常决策（不 503），响应键集合与不带作用域时逐字一致。
func TestSchedDecideUnconstrainedAdmissionScopeIsBackwardCompatible(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews()) // 刻意不装配标签真源
	rec := httptest.NewRecorder()
	h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide",
		map[string]any{"zone": "area-1", "admissionScope": []map[string]string{{}}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("无约束作用域与缺键同效、应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	assertKeys(t, body, "traceId", "chosen", "candidateCount", "excludedCount", "failReason")
	chosen, _ := body["chosen"].(map[string]any)
	if chosen == nil || chosen["serverId"] != "s-a" {
		t.Fatalf("无约束作用域下仍应选最高分 s-a，实际 %v", body["chosen"])
	}
}

// TestSchedDecideLobbyRequestShape 锁定 scope=lobby 不带 zone 的兼容扩展与空大厅 no_candidate 形状。
func TestSchedDecideLobbyRequestShape(t *testing.T) {
	h := newSchedHandlerForTest(schedTestViews())
	rec := httptest.NewRecorder()
	h.Decide(rec, schedAgentRequest(http.MethodPost, "/beacon/v2/agent/schedule/decide",
		map[string]any{"scope": "lobby", "purpose": "proxy-initial-entry"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("lobby decide 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["chosen"] != nil || body["failReason"] != "no_candidate" {
		t.Fatalf("空大厅应 no_candidate，实际 %v", body)
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
