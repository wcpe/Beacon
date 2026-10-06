package service

import (
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// fakeDeclaredLabels 是自声明标签读取真源的替身：按 serverId 给出预置标签，并记下被问过的节点。
type fakeDeclaredLabels struct {
	byServer map[string]map[string]string
	asked    []string
}

func (f *fakeDeclaredLabels) DeclaredLabels(_ string, serverID string) map[string]string {
	f.asked = append(f.asked, serverID)
	return f.byServer[serverID]
}

// zoneTag 是本仓**不解释**的接入方标签键（命名空间归调用方），测试里只当一组键值用。
const zoneTag = "example.zone.lobby-a"

// TestDecideWithAdmissionScopeNarrowsBeforePick 准入作用域在**选之前**收窄：
// 分最高的那台不满足作用域，故胜出的是满足作用域的低分那台——若作用域只当"选中后校验"，
// 这里会是高分那台被选中，测试即红。
func TestDecideWithAdmissionScopeNarrowsBeforePick(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("game1201", "area-1", 95, 10, 100),
		backendView("game1202", "area-1", 60, 10, 100),
	})
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: map[string]map[string]string{
		"game1201": {"example.zone.other-a": "true"},
		"game1202": {zoneTag: "true"},
	}})

	out, err := svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "",
		[]map[string]string{{zoneTag: "true"}})
	if err != nil {
		t.Fatalf("带作用域的决策不应出错: %v", err)
	}
	if out.ChosenServerID != "game1202" {
		t.Fatalf("应选中满足作用域的那台（game1202），实际 %q（若为 game1201 说明作用域没进决策）", out.ChosenServerID)
	}
	if out.CandidateCount != 2 {
		t.Fatalf("候选总数应如实为 2（作用域只影响能否中选，不改候选计数），实际 %d", out.CandidateCount)
	}
	if len(out.Excluded) != 1 || out.Excluded[0].ServerID != "game1201" ||
		out.Excluded[0].Reason != SchedExcludedAdmissionMismatch {
		t.Fatalf("被作用域排除的节点应进 excluded 且原因可读，实际 %+v", out.Excluded)
	}
	if out.AdmissionExcludedCount != 1 {
		t.Fatalf("应如实统计「因准入作用域被排除」的台数=1，实际 %d", out.AdmissionExcludedCount)
	}
}

// TestDecideWithAdmissionScopeOrAlternatives 备选之间 OR：节点声明「不限制服务范围」（值为空串）
// 时，即便它没有那个区的标签，也照样进候选——这正是调用方真值表里「不限制」那一档。
func TestDecideWithAdmissionScopeOrAlternatives(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("game1201", "area-1", 95, 10, 100), // 不限制（默认部署形态）
		backendView("game1202", "area-1", 60, 10, 100), // 明确只服务另一个区
	})
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: map[string]map[string]string{
		"game1201": {"example.zones": ""},
		"game1202": {"example.zones": "example-other"},
	}})

	out, err := svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "",
		[]map[string]string{{"example.zones": ""}, {zoneTag: "true"}})
	if err != nil {
		t.Fatalf("带作用域的决策不应出错: %v", err)
	}
	if out.ChosenServerID != "game1201" {
		t.Fatalf("不限制服务范围的节点应进候选并胜出（95 分），实际 %q", out.ChosenServerID)
	}
	if len(out.Excluded) != 1 || out.Excluded[0].ServerID != "game1202" {
		t.Fatalf("只服务另一个区的节点应被排除，实际 %+v", out.Excluded)
	}
	if out.AdmissionExcludedCount != 1 {
		t.Fatalf("应如实统计「因准入作用域被排除」的台数=1，实际 %d", out.AdmissionExcludedCount)
	}
}

// TestDecideWithAdmissionScopeAllFilteredIsStableEmpty 候选全被作用域滤掉 → 成功响应 +
// failReason=no_candidate_in_scope（**稳定事实**），与"这个区本来就没候选"分开诊断。
func TestDecideWithAdmissionScopeAllFilteredIsStableEmpty(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{backendView("game1201", "area-1", 95, 10, 100)})
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: map[string]map[string]string{
		"game1201": {"example.zone.other-a": "true"},
	}})

	out, err := svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "",
		[]map[string]string{{zoneTag: "true"}})
	if err != nil {
		t.Fatalf("全被滤掉是稳定结论，不应是错误: %v", err)
	}
	if out.ChosenServerID != "" {
		t.Fatalf("不应选出任何候选，实际 %q", out.ChosenServerID)
	}
	if out.FailReason != SchedFailNoCandidateInScope {
		t.Fatalf("失败原因应为 %s（稳定事实），实际 %q", SchedFailNoCandidateInScope, out.FailReason)
	}
	if out.AdmissionExcludedCount != 1 {
		t.Fatalf("全部候选都因作用域被排除时，台数应等于候选数 1，实际 %d", out.AdmissionExcludedCount)
	}
	if len(out.Excluded) != 1 || out.Excluded[0].Reason != SchedExcludedAdmissionMismatch {
		t.Fatalf("排除明细应为作用域不符，实际 %+v", out.Excluded)
	}
}

// TestDecideWithAdmissionScopeAllExcludedByScope 全部候选（多台）都因作用域被排除：
// 这才是"作用域排空"的判据——作用域排除台数 == 候选台数，与健康无关。
func TestDecideWithAdmissionScopeAllExcludedByScope(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("game1201", "area-1", 95, 10, 100),
		backendView("game1202", "area-1", 60, 10, 100),
	})
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: map[string]map[string]string{
		"game1201": {"example.zone.other-a": "true"},
		"game1202": {"example.zone.other-b": "true"},
	}})

	out, err := svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "",
		[]map[string]string{{zoneTag: "true"}})
	if err != nil {
		t.Fatalf("全被滤掉是稳定结论，不应是错误: %v", err)
	}
	if out.FailReason != SchedFailNoCandidateInScope {
		t.Fatalf("失败原因应为 %s，实际 %q", SchedFailNoCandidateInScope, out.FailReason)
	}
	if out.AdmissionExcludedCount != 2 {
		t.Fatalf("作用域排除台数应等于候选台数 2，实际 %d", out.AdmissionExcludedCount)
	}
}

// TestDecideWithAdmissionScopeWithoutLabelSourceIsUnavailable 作用域非空而判定真源未装配 → 503
// （当前状态、可重试）：**不**忽略作用域照旧全量决策，也**不**报成"没有候选"。
func TestDecideWithAdmissionScopeWithoutLabelSourceIsUnavailable(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{backendView("game1201", "area-1", 95, 10, 100)})
	svc := newSchedServiceForTest(store, 1) // 刻意不装配 SetDeclarationLabels

	out, err := svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "",
		[]map[string]string{{zoneTag: "true"}})
	if err != apperr.ErrSchedAdmissionUnavailable {
		t.Fatalf("应报「准入作用域此刻判不了」，实际 %v", err)
	}
	if out.ChosenServerID != "" {
		t.Fatalf("判不了时不得给出任何目标，实际 %q", out.ChosenServerID)
	}
}

// TestDecideWithEmptyAdmissionScopeNeverReadsLabels 空作用域（= 旧请求路径）一次都不读标签真源，
// 判定与改动前逐位一致——这是向后兼容的关键一条。
func TestDecideWithEmptyAdmissionScopeNeverReadsLabels(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("game1201", "area-1", 60, 10, 100),
		backendView("game1202", "area-1", 95, 10, 100),
	})
	svc := newSchedServiceForTest(store, 1)
	reader := &fakeDeclaredLabels{byServer: map[string]map[string]string{}}
	svc.SetDeclarationLabels(reader)

	out, err := svc.DecideScoped(schedTestIdentity(), SchedScopeZone, "area-1", "", "")
	if err != nil {
		t.Fatalf("决策不应出错: %v", err)
	}
	if out.ChosenServerID != "game1202" {
		t.Fatalf("空作用域下仍是最高分者胜出，实际 %q", out.ChosenServerID)
	}
	if len(reader.asked) != 0 {
		t.Fatalf("空作用域不应读标签真源（零额外开销、零行为变化），实际问了 %v", reader.asked)
	}
	if out.AdmissionExcludedCount != 0 {
		t.Fatalf("空作用域不应有任何作用域排除，实际 %d", out.AdmissionExcludedCount)
	}
}

// assertSameDecision 断言两次决策的**判定结果**逐位一致（traceId / 时间戳 / 耗时天然不同，不参与比较）。
func assertSameDecision(t *testing.T, want, got SchedDecisionOutcome, admission []map[string]string) {
	t.Helper()
	if got.ChosenServerID != want.ChosenServerID || got.ChosenScore != want.ChosenScore ||
		got.CandidateCount != want.CandidateCount || got.FailReason != want.FailReason ||
		got.AdmissionExcludedCount != want.AdmissionExcludedCount || got.WeightsRev != want.WeightsRev ||
		len(got.Excluded) != len(want.Excluded) {
		t.Fatalf("作用域 %+v 应与缺键逐位一致：\n期望 %+v\n实际 %+v", admission, want, got)
	}
	for i := range want.Excluded {
		if got.Excluded[i] != want.Excluded[i] {
			t.Fatalf("作用域 %+v 的排除明细应与缺键逐位一致：期望 %+v 实际 %+v", admission, want.Excluded, got.Excluded)
		}
	}
}

// TestDecideWithUnconstrainedAdmissionScopeDegenerates 备选全为空对象（`[{}]` / `[{},{}]`）是**无约束**
// 作用域：任何候选都满足、一台都滤不掉，故与缺键逐位一致——不读标签真源、不因真源未装配报 503、
// 也不会被印成 no_candidate_in_scope。
func TestDecideWithUnconstrainedAdmissionScopeDegenerates(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		backendView("game1201", "area-1", 95, 10, 100),
		backendView("game1202", "area-1", 60, 10, 100),
	})
	// 基线：不带作用域（= 旧客户端路径）。
	want, err := newSchedServiceForTest(store, 1).
		DecideScoped(schedTestIdentity(), SchedScopeZone, "area-1", "", "")
	if err != nil {
		t.Fatalf("基线决策不应出错: %v", err)
	}

	for _, admission := range [][]map[string]string{{{}}, {{}, {}}} {
		// ① 真源**未装配**：归一为缺键后绝不能走 503 那条路。
		got, err := newSchedServiceForTest(store, 1).
			DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "", admission)
		if err != nil {
			t.Fatalf("无约束作用域 %+v 与缺键同效、不应出错: %v", admission, err)
		}
		assertSameDecision(t, want, got, admission)

		// ② 真源**已装配**：归一为缺键后一次都不该读它（零额外开销）。
		svc := newSchedServiceForTest(store, 1)
		reader := &fakeDeclaredLabels{byServer: map[string]map[string]string{}}
		svc.SetDeclarationLabels(reader)
		got, err = svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "", admission)
		if err != nil {
			t.Fatalf("无约束作用域 %+v 与缺键同效、不应出错: %v", admission, err)
		}
		if len(reader.asked) != 0 {
			t.Fatalf("无约束作用域不该读标签真源，实际问了 %v", reader.asked)
		}
		assertSameDecision(t, want, got, admission)
	}
}

// TestDecideWithAdmissionScopeHealthExclusionIsNotScopeExclusion 回归用例：候选**全部因健康原因**
// 不可调度、而每台都满足作用域时，失败原因必须仍是 no_candidate（健康排除是当前状态），
// 作用域排除台数为 0。判据若写成"excluded 台数 == 候选台数"，这条路径会恒真并误印成稳定事实。
func TestDecideWithAdmissionScopeHealthExclusionIsNotScopeExclusion(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		excludedView("game1201", "area-1", healthview.ReasonUnhealthy),
		excludedView("game1202", "area-1", healthview.ReasonDraining),
	})
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: map[string]map[string]string{
		"game1201": {zoneTag: "true"},
		"game1202": {zoneTag: "true"},
	}})

	out, err := svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "",
		[]map[string]string{{zoneTag: "true"}})
	if err != nil {
		t.Fatalf("决策不应出错: %v", err)
	}
	if out.FailReason != SchedFailNoCandidate {
		t.Fatalf("健康排除是等一等会过去的当前状态，失败原因应为 %s，实际 %q",
			SchedFailNoCandidate, out.FailReason)
	}
	if out.AdmissionExcludedCount != 0 {
		t.Fatalf("每台都满足作用域，作用域排除台数应为 0，实际 %d", out.AdmissionExcludedCount)
	}
	if len(out.Excluded) != 2 {
		t.Fatalf("健康排除明细应如实落 2 条，实际 %+v", out.Excluded)
	}
	for _, item := range out.Excluded {
		if item.Reason == SchedExcludedAdmissionMismatch {
			t.Fatalf("健康排除的原因码不得是作用域不符，实际 %+v", out.Excluded)
		}
	}
}

// TestDecideWithAdmissionScopeMixedReasonsStayNoCandidate 混合原因（部分因作用域排除 + 部分因健康排除）
// 仍报 no_candidate（不是稳定事实），作用域排除台数如实为 1，且 excluded 明细把作用域原因排在前。
func TestDecideWithAdmissionScopeMixedReasonsStayNoCandidate(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{
		excludedView("game1201", "area-1", healthview.ReasonUnhealthy),
		backendView("game1202", "area-1", 95, 10, 100),
	})
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: map[string]map[string]string{
		"game1201": {zoneTag: "true"},
		"game1202": {"example.zone.other-a": "true"},
	}})

	out, err := svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "",
		[]map[string]string{{zoneTag: "true"}})
	if err != nil {
		t.Fatalf("决策不应出错: %v", err)
	}
	if out.FailReason != SchedFailNoCandidate {
		t.Fatalf("混合原因里有当前状态（健康），失败原因应为 %s，实际 %q", SchedFailNoCandidate, out.FailReason)
	}
	if out.AdmissionExcludedCount != 1 {
		t.Fatalf("作用域排除台数应如实为 1，实际 %d", out.AdmissionExcludedCount)
	}
	if len(out.Excluded) != 2 ||
		out.Excluded[0].ServerID != "game1202" || out.Excluded[0].Reason != SchedExcludedAdmissionMismatch ||
		out.Excluded[1].ServerID != "game1201" || out.Excluded[1].Reason != healthview.ReasonUnhealthy {
		t.Fatalf("明细应作用域原因在前、健康原因在后，实际 %+v", out.Excluded)
	}
}

// TestCandidatesCarryDeclaredLabels 候选快照随带节点自声明标签（决策侧与读侧同一真源）。
func TestCandidatesCarryDeclaredLabels(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{backendView("game1202", "area-1", 60, 10, 100)})
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: map[string]map[string]string{
		"game1202": {zoneTag: "true"},
	}})

	result := svc.Candidates(schedTestIdentity())
	if len(result.Zones) != 1 || len(result.Zones[0].Candidates) != 1 {
		t.Fatalf("候选快照形状不符: %+v", result.Zones)
	}
	labels := result.Zones[0].Candidates[0].Labels
	if labels[zoneTag] != "true" {
		t.Fatalf("候选应带上节点自声明的标签，实际 %+v", labels)
	}

	// 真源未装配：Labels 为 nil（= "看不到声明"），**不是**空 map（= "没声明过标签"）——
	// 调用方据此把"看不到"归入不可用，而不是读成"没有符合条件的候选"。
	svc.SetDeclarationLabels(nil)
	if got := svc.Candidates(schedTestIdentity()).Zones[0].Candidates[0].Labels; got != nil {
		t.Fatalf("真源未装配时标签应为 nil 以区分「看不到」，实际 %+v", got)
	}
}

// TestCandidatesDoNotShareRegistryMap 候选快照里的标签是拷贝：改动真源不影响已取出的快照。
func TestCandidatesDoNotShareRegistryMap(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{backendView("game1202", "area-1", 60, 10, 100)})
	source := map[string]map[string]string{"game1202": {zoneTag: "true"}}
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: source})

	snapshot := svc.Candidates(schedTestIdentity())
	// 模拟声明刷新：真源整体换了一份 map。
	source["game1202"] = map[string]string{zoneTag: "false"}
	if snapshot.Zones[0].Candidates[0].Labels[zoneTag] != "true" {
		t.Fatalf("已取出的候选快照不应随真源变化，实际 %+v", snapshot.Zones[0].Candidates[0].Labels)
	}
}

// TestSatisfiesAdmissionSemantics AND 语义 + 精确相等（不做 trim / 大小写折叠）；空作用域恒满足。
func TestSatisfiesAdmissionSemantics(t *testing.T) {
	declared := map[string]string{"a": "1", "b": "x"}
	cases := []struct {
		name      string
		admission []map[string]string
		want      bool
	}{
		{"空作用域恒满足（哪怕节点没有标签）", nil, true},
		{"单键命中", []map[string]string{{"a": "1"}}, true},
		{"多键全命中", []map[string]string{{"a": "1", "b": "x"}}, true},
		{"缺一键即不满足", []map[string]string{{"a": "1", "c": "1"}}, false},
		{"值不同即不满足", []map[string]string{{"a": "2"}}, false},
		{"不做 trim（空白不折叠）", []map[string]string{{"a": " 1 "}}, false},
		{"不做大小写折叠", []map[string]string{{"b": "X"}}, false},
		{"备选之间 OR：第二个备选成立即准入", []map[string]string{{"a": "2"}, {"b": "x"}}, true},
		{"备选之间 OR：都不成立才拒绝", []map[string]string{{"a": "2"}, {"b": "y"}}, false},
		{"备选之内 AND：同一备选缺一键即不成立", []map[string]string{{"a": "1", "b": "y"}, {"c": "1"}}, false},
	}
	for _, c := range cases {
		if got := satisfiesAdmission(declared, c.admission); got != c.want {
			t.Fatalf("%s：应 %v，实际 %v", c.name, c.want, got)
		}
	}
	if satisfiesAdmission(nil, []map[string]string{{"a": "1"}}) {
		t.Fatal("节点没有任何自声明时，非空作用域一律不满足（不得把「读不到」读成「满足」）")
	}
}

// TestValidateAdmissionScopeRejectsBadShape 作用域形状校验：沿用 FR-227 同一套约束，超界 400。
func TestValidateAdmissionScopeRejectsBadShape(t *testing.T) {
	if err := validateAdmissionScope(nil); err != nil {
		t.Fatalf("空作用域合法: %v", err)
	}
	if err := validateAdmissionScope([]map[string]string{{zoneTag: "true"}}); err != nil {
		t.Fatalf("正常作用域应通过: %v", err)
	}
	bad := [][]map[string]string{
		{{"bad key": "v"}},
		{{strings.Repeat("k", 33): "v"}},
		{{"k": strings.Repeat("v", 129)}},
	}
	for _, admission := range bad {
		if err := validateAdmissionScope(admission); err != apperr.ErrInvalidParam {
			t.Fatalf("非法形状应 400，实际 %v（%+v）", err, admission)
		}
	}
	tooMany := map[string]string{}
	for i := 0; i <= 20; i++ {
		tooMany["k"+string(rune('a'+i))] = "v"
	}
	if err := validateAdmissionScope([]map[string]string{tooMany}); err != apperr.ErrInvalidParam {
		t.Fatalf("超单节点标签数上限应 400，实际 %v", err)
	}
	tooManyAlternatives := make([]map[string]string, 0, schedAdmissionMaxAlternatives+1)
	for i := 0; i <= schedAdmissionMaxAlternatives; i++ {
		tooManyAlternatives = append(tooManyAlternatives, map[string]string{"k": "v"})
	}
	if err := validateAdmissionScope(tooManyAlternatives); err != apperr.ErrInvalidParam {
		t.Fatalf("超备选个数上限应 400，实际 %v", err)
	}
}

// TestValidateAdmissionScopeBoundaries 边界：备选数**恰好** 8、单备选**恰好** 20 键都合法（界本身不算越界）。
func TestValidateAdmissionScopeBoundaries(t *testing.T) {
	exactAlternatives := make([]map[string]string, 0, schedAdmissionMaxAlternatives)
	for i := 0; i < schedAdmissionMaxAlternatives; i++ {
		exactAlternatives = append(exactAlternatives, map[string]string{"k": "v"})
	}
	if err := validateAdmissionScope(exactAlternatives); err != nil {
		t.Fatalf("备选数恰好 %d 应合法: %v", schedAdmissionMaxAlternatives, err)
	}
	exactKeys := map[string]string{}
	for i := 0; i < model.ServerTagMaxPerServer; i++ {
		exactKeys["k"+string(rune('a'+i))] = "v"
	}
	if err := validateAdmissionScope([]map[string]string{exactKeys}); err != nil {
		t.Fatalf("单备选恰好 %d 键应合法: %v", model.ServerTagMaxPerServer, err)
	}
}

// TestValidateAdmissionScopeRequiresNormalizedKey 判定侧要求 key **已经是规整形态**：带首尾空白的 key
// 在真源里永远命中不了，那是坏请求，当场 400（而不是"校验放过、判定永不命中"）。
// value 不归一化：空白是值的一部分（判定侧逐字符精确相等）。
func TestValidateAdmissionScopeRequiresNormalizedKey(t *testing.T) {
	for _, admission := range [][]map[string]string{
		{{" " + zoneTag: "true"}},
		{{zoneTag + " ": "true"}},
		{{zoneTag + "\t": "true"}},
	} {
		if err := validateAdmissionScope(admission); err != apperr.ErrInvalidParam {
			t.Fatalf("未规整 key 应 400，实际 %v（%+v）", err, admission)
		}
	}
	if err := validateAdmissionScope([]map[string]string{{zoneTag: "true"}}); err != nil {
		t.Fatalf("规整 key 应合法: %v", err)
	}
	if err := validateAdmissionScope([]map[string]string{{zoneTag: " true "}}); err != nil {
		t.Fatalf("value 不归一化、空白值应合法: %v", err)
	}
}

// TestDecisionRowRecordsScopeExclusion 被作用域排除这件事要落进决策行（可事后核对），
// 且失败原因码与"没候选"分开。
func TestDecisionRowRecordsScopeExclusion(t *testing.T) {
	store := healthview.NewStore()
	store.ReplaceAll([]healthview.View{backendView("game1201", "area-1", 95, 10, 100)})
	svc := newSchedServiceForTest(store, 1)
	svc.SetDeclarationLabels(&fakeDeclaredLabels{byServer: map[string]map[string]string{
		"game1201": {"example.zone.other-a": "true"},
	}})
	sink := &fakeSchedEnqueuer{}
	svc.SetDecisionEnqueuer(sink)

	if _, err := svc.DecideScopedWithAdmission(schedTestIdentity(), SchedScopeZone, "area-1", "", "",
		[]map[string]string{{zoneTag: "true"}}); err != nil {
		t.Fatalf("决策不应出错: %v", err)
	}
	if len(sink.rows) != 1 {
		t.Fatalf("应落一行决策记录，实际 %d", len(sink.rows))
	}
	row := sink.rows[0]
	if !strings.Contains(row.Excluded, SchedExcludedAdmissionMismatch) {
		t.Fatalf("排除明细应落库（原因码 %s），实际 %q", SchedExcludedAdmissionMismatch, row.Excluded)
	}
	if row.FailReason != SchedFailNoCandidateInScope {
		t.Fatalf("决策行失败原因应为 %s，实际 %q", SchedFailNoCandidateInScope, row.FailReason)
	}
}
