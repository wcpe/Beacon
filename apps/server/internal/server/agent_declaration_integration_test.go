//go:build integration

package server_test

import (
	"math/rand/v2"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// 本文件覆盖 FR-243（节点自声明的运行期刷新，见 ADR-0086）的服务端契约：
// 注册后运行期刷新 capacity / labels → 实例视图（admin 与 discovery）立即可读回；重复上报幂等；
// 两类失败可区分（401 由既有中间件保证、404 未注册、400 声明被拒）；
// 以及**负向**：声明不进任何第二真源——`?tag.*=` 过滤与调度候选 / 健康打分输入逐项不变。

// declarationPath 是节点自声明刷新端点（契约冻结，spec §3.1）。
const declarationPath = "/beacon/v1/agent/declaration"

// newDeclarationITServer 建发现用运行环境并注册一台 bukkit 实例（capacity 200 / metadata mode=survival）。
func newDeclarationITServer(t *testing.T) *integrationTestServer {
	t.Helper()
	ts := newTestServer(t)
	t.Cleanup(ts.Close)
	// 发现仅投影 lifecycle=active 的环境，先经管理面创建 prod 运行环境。
	if code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/namespaces", map[string]any{"code": "prod", "name": "生产"}); code != http.StatusCreated {
		t.Fatalf("创建发现环境应 201，实际 %d：%v", code, body)
	}
	if code, body := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "role": "bukkit",
		"address": "10.0.0.1:25565", "capacity": 200, "metadata": map[string]string{"mode": "survival"},
	}); code != http.StatusOK {
		t.Fatalf("注册应 200，实际 %d：%v", code, body)
	}
	return ts
}

// discoveryInstanceView 从发现响应取指定 serverId 的实例视图（rawQuery 为空表示不过滤），返回视图与实例总数。
func discoveryInstanceView(t *testing.T, ts *integrationTestServer, rawQuery, serverID string) (map[string]any, int) {
	t.Helper()
	code, disc := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/discovery?namespace=prod"+rawQuery, nil)
	if code != http.StatusOK {
		t.Fatalf("发现应 200，实际 %d：%v", code, disc)
	}
	insts, _ := disc["instances"].([]any)
	for _, raw := range insts {
		view, _ := raw.(map[string]any)
		if view["serverId"] == serverID {
			return view, len(insts)
		}
	}
	return nil, len(insts)
}

// adminInstanceView 取管理面实例视图（读回面之一，与 discovery 共用 toInstanceViews）。
func adminInstanceView(t *testing.T, ts *integrationTestServer, serverID string) map[string]any {
	t.Helper()
	code, view := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/"+serverID+"?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("管理面实例视图应 200，实际 %d：%v", code, view)
	}
	return view
}

// assertDeclaredView 断言实例视图上的声明字段为期望值（读回面自证）。
func assertDeclaredView(t *testing.T, where string, view map[string]any, wantCapacity int, wantLabels map[string]string) {
	t.Helper()
	if view == nil {
		t.Fatalf("%s：实例视图缺失", where)
	}
	if capacity, _ := view["capacity"].(float64); int(capacity) != wantCapacity {
		t.Fatalf("%s：capacity 应为 %d，实际 %v", where, wantCapacity, view["capacity"])
	}
	labels, _ := view["metadata"].(map[string]any)
	if len(labels) != len(wantLabels) {
		t.Fatalf("%s：labels 应为 %v，实际 %v", where, wantLabels, view["metadata"])
	}
	for k, want := range wantLabels {
		if got, _ := labels[k].(string); got != want {
			t.Fatalf("%s：labels[%s] 应为 %q，实际 %v", where, k, want, view["metadata"])
		}
	}
}

// assertDeclarationRejected 断言声明端点按期望业务码拒绝。
func assertDeclarationRejected(t *testing.T, ts *integrationTestServer, body any, wantStatus int, wantCode string) {
	t.Helper()
	code, res := doJSON(t, http.MethodPost, ts.URL+declarationPath, body)
	if code != wantStatus || res["code"] != wantCode {
		t.Fatalf("应 %d %s，实际 %d：%v", wantStatus, wantCode, code, res)
	}
}

// TestAgentDeclarationRefreshFlow 覆盖验收项：运行期刷新 + 可选刷新 + 幂等 + 两类结论可区分。
func TestAgentDeclarationRefreshFlow(t *testing.T) {
	ts := newDeclarationITServer(t)

	// ① 注册值先经两个读回面确认（基线）
	assertDeclaredView(t, "注册后 discovery", mustDiscoveryView(t, ts, "lobby-1"), 200, map[string]string{"mode": "survival"})
	assertDeclaredView(t, "注册后 admin", adminInstanceView(t, ts, "lobby-1"), 200, map[string]string{"mode": "survival"})

	// ② 只给 capacity：labels 不刷新（部分刷新）
	code, res := doJSON(t, http.MethodPost, ts.URL+declarationPath, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "capacity": 350})
	if code != http.StatusOK || res["ok"] != true {
		t.Fatalf("声明应 200 ok=true，实际 %d：%v", code, res)
	}
	if capacity, _ := res["capacity"].(float64); int(capacity) != 350 {
		t.Fatalf("响应应回带生效后 capacity=350，实际 %v", res)
	}
	assertDeclaredView(t, "只给 capacity 后 admin", adminInstanceView(t, ts, "lobby-1"), 350, map[string]string{"mode": "survival"})
	assertDeclaredView(t, "只给 capacity 后 discovery", mustDiscoveryView(t, ts, "lobby-1"), 350, map[string]string{"mode": "survival"})

	// ③ 只给 labels：capacity 不刷新，标签整体替换（旧 key mode 消失）
	code, res = doJSON(t, http.MethodPost, ts.URL+declarationPath, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "labels": map[string]string{"tier": "core", "mode": "creative"}})
	if code != http.StatusOK {
		t.Fatalf("只给 labels 应 200，实际 %d：%v", code, res)
	}
	if labels, _ := res["labels"].(map[string]any); len(labels) != 2 {
		t.Fatalf("响应应回带生效后的 2 个标签，实际 %v", res["labels"])
	}
	assertDeclaredView(t, "只给 labels 后 admin", adminInstanceView(t, ts, "lobby-1"), 350,
		map[string]string{"tier": "core", "mode": "creative"})

	// ④ capacity=0 生效（不与「缺键」混淆：上一步为 350）
	if code, res := doJSON(t, http.MethodPost, ts.URL+declarationPath, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "capacity": 0}); code != http.StatusOK {
		t.Fatalf("capacity=0 应 200，实际 %d：%v", code, res)
	}
	assertDeclaredView(t, "capacity=0 后 admin", adminInstanceView(t, ts, "lobby-1"), 0,
		map[string]string{"tier": "core", "mode": "creative"})

	// ⑤ 空标签对象 = 清空全部标签（与「缺键不刷新」区分）
	if code, res := doJSON(t, http.MethodPost, ts.URL+declarationPath, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "labels": map[string]string{}}); code != http.StatusOK {
		t.Fatalf("空标签集应 200（= 清空），实际 %d：%v", code, res)
	}
	assertDeclaredView(t, "清空标签后 admin", adminInstanceView(t, ts, "lobby-1"), 0, map[string]string{})

	// ⑥ 幂等：同一请求连续上报 3 次 → 结果一致、实例数不变、标签不叠加
	for i := 0; i < 3; i++ {
		code, res := doJSON(t, http.MethodPost, ts.URL+declarationPath, map[string]any{
			"namespace": "prod", "serverId": "lobby-1", "capacity": 500,
			"labels": map[string]string{"mode": "creative", "tier": "core"}})
		if code != http.StatusOK {
			t.Fatalf("第 %d 次声明应 200，实际 %d：%v", i+1, code, res)
		}
	}
	view, total := discoveryInstanceView(t, ts, "", "lobby-1")
	if total != 1 {
		t.Fatalf("重复声明不得新增实例，发现应仅 1 个实例，实际 %d", total)
	}
	assertDeclaredView(t, "重复声明后 discovery", view, 500, map[string]string{"mode": "creative", "tier": "core"})

	// ⑦ 未注册（未挂载数据面 / 不在册）→ 404 NOT_REGISTERED
	assertDeclarationRejected(t, ts, map[string]any{
		"namespace": "prod", "serverId": "ghost", "capacity": 1}, http.StatusNotFound, "NOT_REGISTERED")
	// 归属不符（写他人）：合法格式但不在册，同样按在册判定拒绝。
	assertDeclarationRejected(t, ts, map[string]any{
		"namespace": "prod", "serverId": "lobby-2", "labels": map[string]string{"k": "v"}}, http.StatusNotFound, "NOT_REGISTERED")

	// ⑧ 声明被拒（400 INVALID_PARAM）：两字段全缺 / capacity<0 / key 非法 / 标签超界 / 请求体非对象
	assertDeclarationRejected(t, ts, map[string]any{"namespace": "prod", "serverId": "lobby-1"}, http.StatusBadRequest, "INVALID_PARAM")
	assertDeclarationRejected(t, ts, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "capacity": -1}, http.StatusBadRequest, "INVALID_PARAM")
	assertDeclarationRejected(t, ts, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "labels": map[string]string{"bad key": "v"}}, http.StatusBadRequest, "INVALID_PARAM")
	assertDeclarationRejected(t, ts, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "labels": manyLabelsForTest(21)}, http.StatusBadRequest, "INVALID_PARAM")
	// 请求体格式非法：JSON 数组不是请求体契约里的对象形态
	assertDeclarationRejected(t, ts, []int{1, 2}, http.StatusBadRequest, "INVALID_PARAM")
	// 身份缺失同样是 400（复用注册路径的 IDENTITY_REQUIRED 码）
	assertDeclarationRejected(t, ts, map[string]any{"capacity": 10}, http.StatusBadRequest, "IDENTITY_REQUIRED")

	// 被拒请求一律不改内存真源（最后仍为 ⑥ 的生效值）
	assertDeclaredView(t, "被拒声明后 admin", adminInstanceView(t, ts, "lobby-1"), 500,
		map[string]string{"mode": "creative", "tier": "core"})
}

// TestAgentDeclarationDoesNotFeedOtherTruthSources 负向断言（spec §3.5；**FR-244 已重新裁定其中一条**）：
// 声明刷新后，`?tag.*=` 标签过滤（真源 = 控制面 server_tag）与**健康打分**（真源 = 健康视图内存快照）
// 逐项不变——控制面只存；自声明标签**仅在准入判定处被读**，不参与健康打分、不参与候选排序、
// 也不进 `?tag.*=` 过滤真源。
//
// **口径变更（FR-244，2026-10-06）**：本用例原先把「调度候选逐项不变」也算在负向里。FR-244 明文重新
// 裁定那一条：节点自声明**进入调度决策**（作为准入作用域的判定输入），并随候选带出 `labels` 字段。
// 但它**仍不**参与健康打分、**仍不**参与候选排序、**仍不**进入 `?tag.*=` 过滤真源。
// 故这里把候选那一档改成两条一起断：**调度字段**逐项不变，而 `labels` 正是本次声明的那一份——
// 少了前者是"声明改动了调度语义"（越界），少了后者是"声明没接进决策"（FR-244 未达）。
func TestAgentDeclarationDoesNotFeedOtherTruthSources(t *testing.T) {
	ts := newDeclarationITServer(t)
	// 落新真源归属 + 经管理面写控制面标签 env=beta（FR-227，`?tag.*=` 过滤的唯一真源）。
	seedServerZoneForTest(t, ts, "prod", "lobby-1", "area1", "zoneA")
	if code, body := doJSON(t, http.MethodPut, ts.URL+"/admin/v2/servers/lobby-1/tags", map[string]any{
		"tags": map[string]string{"env": "beta"}}); code != http.StatusOK {
		t.Fatalf("写控制面标签应 200，实际 %d：%v", code, body)
	}

	// 基线：命中控制面标签的过滤能看到实例；声明里将出现的 env=gamma 此时不命中。
	if _, total := discoveryInstanceView(t, ts, "&tag.env=beta", "lobby-1"); total != 1 {
		t.Fatalf("基线：tag.env=beta 应命中 1 个实例，实际 %d", total)
	}
	if _, total := discoveryInstanceView(t, ts, "&tag.env=gamma", "lobby-1"); total != 0 {
		t.Fatalf("基线：tag.env=gamma 不应命中任何实例，实际 %d", total)
	}

	// 健康视图真源（调度候选与健康打分的共同输入）：预置一台可调度实例后取候选快照。
	ns := ensureNamespaceRowForTest(t, ts, "prod")
	testHealthViews.ReplaceAll([]healthview.View{{
		NamespaceID: ns.ID, Namespace: "prod", ServerID: "lobby-1", Kind: "backend",
		ZoneName: "zoneA", Score: 88, Level: healthview.LevelHealthy, Schedulable: true,
		OnlineCount: 7, MaxOnline: 200,
	}})
	schedSvc := service.NewSchedulingV2Service(testHealthViews, rand.New(rand.NewPCG(1, 2)))
	// 按**生产同款**装配自声明标签真源（main.go 里那一行同源）：调度侧的准入作用域读的就是它。
	schedSvc.SetDeclarationLabels(service.RegistryDeclarationLabels{Registry: ts.registry})
	id := agentIdentityForTest(ns.ID, "lobby-1")
	before := schedSvc.Candidates(id)
	beforeView, ok := testHealthViews.Get(ns.ID, "lobby-1")
	if !ok {
		t.Fatal("预置的健康视图应可读回")
	}
	beforeAdmin := adminInstanceView(t, ts, "lobby-1")

	// 声明刷新：容量与标签都换成本地声明值（其中 env=gamma 与过滤真源 env=beta 故意不同）。
	if code, res := doJSON(t, http.MethodPost, ts.URL+declarationPath, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "capacity": 999,
		"labels": map[string]string{"env": "gamma", "tier": "core"}}); code != http.StatusOK {
		t.Fatalf("声明应 200，实际 %d：%v", code, res)
	}

	// 声明值确实生效（否则下面的负向断言会因「声明没生效」而假通过）。
	assertDeclaredView(t, "声明后 admin", adminInstanceView(t, ts, "lobby-1"), 999,
		map[string]string{"env": "gamma", "tier": "core"})

	// ① 控制面标签过滤逐项不变：env=beta 仍命中；声明的 env=gamma 不被过滤真源采纳。
	if _, total := discoveryInstanceView(t, ts, "&tag.env=beta", "lobby-1"); total != 1 {
		t.Fatalf("声明后 tag.env=beta 应仍命中 1 个实例，实际 %d", total)
	}
	if _, total := discoveryInstanceView(t, ts, "&tag.env=gamma", "lobby-1"); total != 0 {
		t.Fatalf("声明标签不得进入 ?tag.*= 过滤真源，实际命中 %d 个实例", total)
	}

	// ② 调度候选：**调度字段**逐项不变（声明不得改变排序 / 健康字段），而 labels 如实是本次声明的那一份。
	after := schedSvc.Candidates(id)
	afterLabels := after.Zones[0].Candidates[0].Labels
	beforeLabels := before.Zones[0].Candidates[0].Labels
	before.GeneratedAtMs, after.GeneratedAtMs = 0, 0
	before.Zones[0].Candidates[0].Labels, after.Zones[0].Candidates[0].Labels = nil, nil
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("声明刷新不得改变候选的调度字段：\n前 %+v\n后 %+v", before, after)
	}
	if beforeLabels["env"] != "" {
		t.Fatalf("声明之前候选不应带 env 标签，实际 %v", beforeLabels)
	}
	if afterLabels["env"] != "gamma" || afterLabels["tier"] != "core" {
		t.Fatalf("候选应带出本次声明的标签（FR-244 重新裁定的那一处），实际 %v", afterLabels)
	}
	// ②b 决策侧按作用域收窄：声明的 env=gamma 是判据，未声明该标签的节点进不了候选。
	narrowed, err := schedSvc.DecideScopedWithAdmission(id, service.SchedScopeZone, "zoneA", "", "",
		[]map[string]string{{"env": "gamma"}})
	if err != nil {
		t.Fatalf("带作用域的决策不应出错: %v", err)
	}
	if narrowed.ChosenServerID != "lobby-1" {
		t.Fatalf("声明了 env=gamma 的节点应被选中，实际 %q", narrowed.ChosenServerID)
	}
	empty, err := schedSvc.DecideScopedWithAdmission(id, service.SchedScopeZone, "zoneA", "", "",
		[]map[string]string{{"env": "beta"}})
	if err != nil {
		t.Fatalf("全被滤掉是稳定结论、不应出错: %v", err)
	}
	if empty.FailReason != service.SchedFailNoCandidateInScope || empty.ChosenServerID != "" {
		t.Fatalf("未声明 env=beta 时应为空结果（no_candidate_in_scope），实际 %+v", empty)
	}

	// ③ 健康打分输入（健康视图真源）逐项不变。
	afterView, _ := testHealthViews.Get(ns.ID, "lobby-1")
	if !reflect.DeepEqual(beforeView, afterView) {
		t.Fatalf("声明刷新不得改动健康视图真源：\n前 %+v\n后 %+v", beforeView, afterView)
	}

	// ④ 实例视图上声明之外的字段（健康 / 指标 / 注册事实）逐项不变。
	afterAdmin := adminInstanceView(t, ts, "lobby-1")
	for _, key := range []string{"status", "lastHeartbeat", "registeredAt", "appliedMd5", "playerCount", "tps", "zone", "group", "role"} {
		if !reflect.DeepEqual(beforeAdmin[key], afterAdmin[key]) {
			t.Fatalf("声明刷新不得改动字段 %s：前 %v 后 %v", key, beforeAdmin[key], afterAdmin[key])
		}
	}
}

// mustDiscoveryView 取发现侧实例视图，缺失即失败。
func mustDiscoveryView(t *testing.T, ts *integrationTestServer, serverID string) map[string]any {
	t.Helper()
	view, total := discoveryInstanceView(t, ts, "", serverID)
	if view == nil {
		t.Fatalf("发现侧应能读到实例 %s（当前 %d 个实例）", serverID, total)
	}
	return view
}

// manyLabelsForTest 造 n 个合法且互不相同的标签（用于触发单节点标签数上限）。
func manyLabelsForTest(n int) map[string]string {
	out := make(map[string]string, n)
	for i := 0; i < n; i++ {
		out["k"+strings.Repeat("x", i%8)+string(rune('a'+i/8))+string(rune('a'+i%8))] = "v"
	}
	return out
}

// agentIdentityForTest 造一个与该 namespace 匹配的 agent 权威身份（供调度候选按 namespace 圈定）。
func agentIdentityForTest(namespaceID uint, serverID string) agentauth.Identity {
	return agentauth.Identity{NamespaceID: namespaceID, Namespace: "prod", ServerID: serverID, Kind: "backend"}
}
