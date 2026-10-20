//go:build e2e

// 拓扑订阅、玩家名册、前缀过滤订阅、admin 编排辅助的真机断言。
package apitest_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// assertTopologyWatch 验证 Discovery.watch 在拓扑真变时被回调，且回调后能重查到变化后的拓扑。
//
// ## 为什么必须用 admin 换区而不是等自然变化
//
// 拓扑只在实例上线/下线/换区时变。若只等自然变化，测试要么依赖别的用例制造的副作用，
// 要么等到超时——都不确定性。这里主动换区，用**我们刚刚制造的那个变化**作为触发源。
//
// ## 换区的真实语义（决定了断言该写成什么样）
//
// `/admin/v2/server-rezones` 是**两步流程**：先解绑该服的全部归属（ZoneID/BCClusterID/
// 默认入口全部清空）并把身份重新打回 pending，等重新审批后才落到目标小区。
// 因此换区后**立刻**去 discovery 查该服会查不到——这是正确设计：未落区的实例本就不该
// 出现在可调度发现面里。若断言「换区后立刻看到新 zone」，等于要求产品违反自己的两阶段语义。
//
// 故这里断言的是**拓扑确实变了且回调后能看到变化**：
//  1. `TOPOLOGY_CHANGED` 回调发生（推送链路通）；
//  2. 回调后的重查结果**不再包含该服的旧 zone 归属**（旧拓扑已消失）。
//
// 这两条合起来证明「回调 → 重查 → 看到新事实」这条链路真的走通了，而不依赖审批完成的时序。
func assertTopologyWatch(
	t *testing.T,
	repoRoot, base, token string,
	nsID uint,
	movedZoneID uint,
	guard *harness.GradleProc,
) {
	t.Helper()
	obsPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-discovery.log")

	// 记录换区前的观测行数：之后只认更新的观测，确保是本次换区触发而非历史残留。
	beforeIdx := len(readObservations(obsPath))

	t.Log("== 通过 admin 换区端点制造一次确定性拓扑变更 ==")
	rowID := serverRowID(t, base, token, nsID, guard)
	if _, err := harness.RequestApprovalAndWait(base, token, http.MethodPost, "/admin/v2/server-rezones", map[string]any{
		"serverIds": []uint{rowID},
		"target":    map[string]any{"kind": "zone", "id": movedZoneID},
		"reason":    "拓扑变更触发 E2E",
	}, "succeeded", pendingWait, guard); err != nil {
		t.Fatalf("发起换区失败：%v", err)
	}

	// 1) 回调必须发生。
	idx := waitObservation(t, obsPath, beforeIdx, slowObsWait, guard, func(o observation) bool {
		return o.source == "TOPOLOGY_CHANGED"
	}, "拓扑变更回调被触发")
	t.Logf("PASS 拓扑回调被触发（第 %d 行）", idx)

	// 2) 回调后重查必须反映「旧归属已消失」：该服不再带旧 zone。
	//
	// 换区第一步会解绑 ZoneID 并把身份打回 pending，故此处期望的是「本服条目不再有旧 zone」，
	// 而非「变成新 zone」——后者要等重新审批落区，属于另一个相位的事。
	waitObservation(t, obsPath, beforeIdx, slowObsWait, guard, func(o observation) bool {
		if o.source != "TOPOLOGY_AFTER" {
			return false
		}
		// 旧 zone 归属必须已从拓扑里消失。
		return !strings.Contains(o.detail, serverID+"="+zoneName)
	}, "回调后重查显示旧 zone 归属已消失")
	t.Logf("PASS 回调后 query() 反映变更：%s 的旧 zone（%s）归属已消失（换区第一步解绑生效）", serverID, zoneName)

	assertNoErrorSource(t, readObservations(obsPath), "DISCOVERY_ERROR")
}

// assertRosterWithInjection 验证连接注入后名册出现该玩家（与空名册降级形成一对互补断言）。
//
// 空名册证明降级形态；注入后非空证明「名册真的被连接事件驱动写进来了」。
// 只做前者会漏掉「名册存储从未被写入」的缺陷。
func assertRosterWithInjection(t *testing.T, obs []observation, playerName string) bool {
	t.Helper()
	for i, o := range filterBySource(obs, "ROSTER_ALL") {
		if strings.Contains(o.detail, playerName+"=") {
			t.Logf("PASS 名册含注入玩家：第 %d 条 ROSTER_ALL 观测（%s）", i+1, o.detail)
			return true
		}
	}
	return false
}

// assertPrefixSubscription 验证前缀过滤订阅。
//
// ## 判定逻辑
//
// 控制面下发的 dataId 里只有 `beacon-e2e.yml` 系（前缀 `beacon-e2e`），没有 `beacon-*` 外的
// 其它前缀。故：
//   - `beacon-e2e` 前缀的监听器**应当**收到回调；
//   - `beacon-other` 前缀的监听器**绝不应当**收到回调。
//
// 第二条是本探针的核心价值：只断言「该命中的命中了」无法排除「根本没做过滤、所有监听器都被唤醒」，
// 必须同时断言「不该命中的一次都没被唤醒」。
//
// ## 前置条件
//
// 需要外部先让一个 `beacon-e2e` 前缀的 dataId 真的变更过（由编排侧建/改配置项触发）。
// 本函数只负责读观测与断言，不负责造变更。
func assertPrefixSubscription(t *testing.T, repoRoot string, guard *harness.GradleProc) {
	t.Helper()
	obsPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-prefix-subscription.log")

	// 两个前缀的监听器都必须注册成功，否则后续断言没有意义。
	for _, prefix := range []string{"beacon-e2e", "beacon-other"} {
		waitObservation(t, obsPath, 0, obsWait, guard, func(o observation) bool {
			return o.source == "PREFIX_REGISTERED" && o.field3 == prefix
		}, "前缀 "+prefix+" 的监听器已注册")
	}

	// 该命中的必须命中。
	waitObservation(t, obsPath, 0, slowObsWait, guard, func(o observation) bool {
		return o.source == "PREFIX_CALLBACK" && o.field3 == "beacon-e2e"
	}, "beacon-e2e 前缀的监听器收到回调")

	// 不该命中的必须一次都没有。
	obs := readObservations(obsPath)
	for i, o := range obs {
		if o.source == "PREFIX_CALLBACK" && o.field3 == "beacon-other" {
			t.Fatalf("第 %d 行观测显示 beacon-other 前缀的监听器被唤醒（%s）："+
				"前缀过滤未生效——不相关的监听器不应收到任何回调", i+1, o.detail)
		}
	}
	t.Log("PASS 前缀过滤生效：beacon-e2e 被唤醒，beacon-other 一次都未被唤醒")

	assertNoErrorSource(t, obs, "PREFIX_OUTCOME")
}

// ---- admin 编排辅助 ----

// findNodeID 在列表端点里按名字找 id（仅用于确有 GET 的端点）。
func findNodeID(t *testing.T, base, token, path, name string, guard *harness.GradleProc) uint {
	t.Helper()
	var out struct {
		Items []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := doAdminJSONAllow(base, http.MethodGet, path, token, nil, http.StatusOK, &out, guard); err != nil {
		t.Fatalf("查询 %s 失败：%v", path, err)
	}
	for _, it := range out.Items {
		if it.Name == name {
			return it.ID
		}
	}
	t.Fatalf("在 %s 里找不到名为 %q 的记录", path, name)
	return 0
}

// createNode 建一个区服结构节点，返回其 id。
func createNode(t *testing.T, base, token, path string, body map[string]any, guard harness.ProcessGuard) uint {
	t.Helper()
	var out struct {
		ID uint `json:"id"`
	}
	if err := doAdminJSONAllow(base, http.MethodPost, "/admin/v2"+path, token, body, http.StatusCreated, &out, guard); err != nil {
		t.Fatalf("建 %s 失败：%v", path, err)
	}
	if out.ID == 0 {
		t.Fatalf("建 %s 应返回数字 id，实际 %+v", path, out)
	}
	return out.ID
}

// serverRowID 取该 server 在 admin 里的行记录 id。
func serverRowID(t *testing.T, base, token string, nsID uint, guard harness.ProcessGuard) uint {
	t.Helper()
	var out struct {
		Items []struct {
			ID       uint   `json:"id"`
			ServerID string `json:"serverId"`
		} `json:"items"`
	}
	path := "/admin/v2/servers?namespaceId=" + utoa(nsID)
	if err := doAdminJSONAllow(base, http.MethodGet, path, token, nil, http.StatusOK, &out, guard); err != nil {
		t.Fatalf("查询 servers 失败：%v", err)
	}
	for _, it := range out.Items {
		if it.ServerID == serverID {
			return it.ID
		}
	}
	t.Fatalf("未找到 server %s 的行记录", serverID)
	return 0
}

// waitSchedulable 等健康计算轮吸收分配事实：/admin/v2/health/{serverId} 转 schedulable=true。
func waitSchedulable(t *testing.T, base, token string, guard *harness.GradleProc) {
	t.Helper()
	var detail struct {
		Schedulable bool `json:"schedulable"`
	}
	err := waitUntil(schedulableWait, guard, func(ctx context.Context) bool {
		detail.Schedulable = false
		if !tryGet(ctx, base+"/admin/v2/health/"+url.PathEscape(serverID), token, &detail, guard) {
			return false
		}
		return detail.Schedulable
	})
	if err != nil {
		t.Fatalf("等待 %s 分配后转 schedulable=true 失败（%s）：%v", serverID, schedulableWait, err)
	}
}

// createNamespace 建一个 v2 namespace。
func createNamespace(t *testing.T, base, token string) namespaceView {
	t.Helper()
	id, accessToken, err := harness.CreateV2Namespace(base, token, namespace, "agent-api 剩余能力面 E2E")
	if err != nil {
		t.Fatalf("建 namespace 失败：%v", err)
	}
	return namespaceView{ID: id, AccessToken: accessToken}
}

// namespaceView 是 namespace 建好后的 id 与接入 token。
type namespaceView struct {
	ID          uint   `json:"id"`
	AccessToken string `json:"accessToken"`
}

// doAdminJSONAllow 发一个带 Bearer 的 admin 请求并要求指定状态码。
//
// 与 harness.doAdminJSON 的差别：它是包内私有，本包无法直接调用，故在此做一份等价实现。
func doAdminJSONAllow(
	base, method, path, token string,
	body any,
	wantStatus int,
	out any,
	guard harness.ProcessGuard,
) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := harness.DoRequestWithGuard(req, 0, guard)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errStatus(method, path, wantStatus, resp.StatusCode, string(raw))
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// errStatus 构造带响应片段的状态码错误，便于定位。
func errStatus(method, path string, want, got int, body string) error {
	return &statusError{method: method, path: path, want: want, got: got, body: body}
}

// statusError 是 admin 请求状态码不符的错误类型。
type statusError struct {
	method string
	path   string
	want   int
	got    int
	body   string
}

// Error 实现 error 接口。
func (e *statusError) Error() string {
	return e.method + " " + e.path + " 期望 HTTP " + itoa(e.want) + "，得 " + itoa(e.got) + "，响应=" + e.body
}

// tryGet 发一个带 Bearer 的 admin GET，仅在 200 且能解析时返回 true（轮询用，不报错）。
func tryGet(ctx context.Context, u, token string, out any, guard harness.ProcessGuard) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := harness.DoRequestWithGuard(req, 0, guard)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	raw, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(raw, out) == nil
}
