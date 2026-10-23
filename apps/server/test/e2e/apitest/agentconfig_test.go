//go:build e2e

// FR-277 / FR-278 真机端到端验收：控制面下发 agent 运行配置 → agent 拉取 → 热生效。
//
// ## 这条测试要回答的问题
//
// 单元测试能证明「解析对」「落盘对」「键级防护对」，但证明不了**闭环真的通了**：
// 控制面写的值，agent 是否真的会去拉、拉到后是否真的按新值工作。
// 中间任何一环断了（端点路径不对、鉴权不通过、周期没跑、应用没触发），单测全绿也照样不生效。
//
// ## 验收手法（两段，缺一不可）
//
//  1. **下发闭环**（assertAgentConfigPush）：控制面写入能读回 + agent 侧
//     `pushed-config.json` 出现该值。这只证明「拉取并落盘」。
//  2. **运行时热生效**（assertReloadTimeoutHotEffect）：下发两个差异明显的重载超时，
//     各触发一次受控变更，用探针实测「插件在何时被打断」，从而把**生效中的超时预算量出来**。
//     这才是「运行时用了新值」的证据——证明「写盘成功」不等于「行为变了」。
//
// 第二段的观测原理见其函数注释；它对应 FR-278 #1/#2 的真机证据。
package apitest_e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// TestAgentConfigPushEndToEnd 验证「控制面下发 → agent 拉取落盘」的完整链路。
//
// 前置：已有在线 agent（由 TestAgentApiRemainingSurfaceE2E 的编排提供，或独立起）。
// 本测试作为子测试挂在主测试里，复用同一套真控制面 + 真 Paper。
func assertAgentConfigPush(
	t *testing.T,
	repoRoot, base, token string,
	nsID uint,
	guard *harness.GradleProc,
) {
	t.Helper()

	// 下发一个**可观测且安全**的配置项：重载超时。
	// 选它的理由：agent 侧有明确的热更新落点（ReloadDispatcher.updateTimeoutMs），
	// 且不涉及接入目标（禁区键，必须被拒）。
	const targetTimeout = "45000"
	writeAgentSetting(t, base, token, nsID, "delivery.config-reload-timeout-ms", targetTimeout, guard)

	// 控制面侧直接证据：写入后能读回。
	got := readAgentSetting(t, base, token, nsID, "delivery.config-reload-timeout-ms", guard)
	if got != targetTimeout {
		t.Fatalf("控制面未保存下发的配置：期望 %q 实际 %q", targetTimeout, got)
	}

	// agent 侧间接证据：pushed-config.json 出现该值（拉取周期 30s，给 2 分钟余量）。
	cfgPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconAgent", "pushed-config.json")
	deadline := time.Now().Add(2 * time.Minute)
	var lastRaw string
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(cfgPath); err == nil {
			lastRaw = string(raw)
			if strings.Contains(lastRaw, "delivery.config-reload-timeout-ms") &&
				strings.Contains(lastRaw, targetTimeout) {
				t.Logf("PASS agent 已拉取下发配置：%s 含 %s", cfgPath, targetTimeout)
				assertForbiddenKeysRejected(t, base, token, nsID, guard)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("agent 未在 2 分钟内拉取到下发配置；%s 当前内容：%s", cfgPath, truncate(lastRaw, 400))
}

// assertForbiddenKeysRejected 验证禁区键在**服务端**就被拒（双保险的第一层）。
//
// 这条必须单独验：若服务端放行、只靠 agent 侧拦截，那「控制面被攻破」时
// 攻击者可以下发 beacon.endpoints，而被拦下的证据只存在于 agent 日志里——
// 审计上看不到「有人试图改接入目标」。
func assertForbiddenKeysRejected(
	t *testing.T,
	base, token string,
	nsID uint,
	guard *harness.GradleProc,
) {
	t.Helper()
	body := map[string]any{
		"namespaceId": nsID,
		"values": map[string]string{
			"beacon.endpoints": "http://attacker.example.com",
		},
		"reason":            "e2e 验证禁区键被拒",
		"confirmationScope": namespace,
	}
	// 期望**未被接受**：200（直执）与 202（审批）都算接受，两种都要判失败。
	if err := doAdminJSONAllow(base, "PUT", "/admin/v2/agent-settings", token, body, 200, nil, guard); err == nil {
		t.Fatalf("禁区键 beacon.endpoints 被直接接受：控制面可被用于劫持 agent 接入目标")
	}
	// 202 表示进了审批流，同样是「接受」——审批通过后仍会下发，故也判失败。
	if err := doAdminJSONAllow(base, "PUT", "/admin/v2/agent-settings", token, body, 202, nil, guard); err == nil {
		t.Fatalf("禁区键 beacon.endpoints 被接受为审批申请：审批通过后仍会下发，等于未拦截")
	}
	t.Log("PASS 服务端拒绝了禁区键 beacon.endpoints")
}

// writeAgentSetting 写一条 agent 配置（走 admin 端点，含二次确认）。
func writeAgentSetting(
	t *testing.T,
	base, token string,
	nsID uint,
	key, value string,
	guard *harness.GradleProc,
) {
	t.Helper()
	body := map[string]any{
		"namespaceId": nsID,
		"values":      map[string]string{key: value},
		"reason":      "e2e 下发配置验收",
		// 二次确认须与作用域标识**逐字一致**（服务端据此防止「改错环境」）。
		// namespace 级的 scopeLabel 就是 namespace 编码本身。
		"confirmationScope": namespace,
	}
	// 200（直执）或 202（审批票据）都算「已受理」。
	if err := doAdminJSONAllow(base, "PUT", "/admin/v2/agent-settings", token, body, 200, nil, guard); err != nil {
		if err202 := doAdminJSONAllow(base, "PUT", "/admin/v2/agent-settings", token, body, 202, nil, guard); err202 != nil {
			t.Fatalf("下发配置既非 200 也非 202：200 分支=%v；202 分支=%v", err, err202)
		}
	}
}

// readAgentSetting 读回某键的生效值（server 级未设时回 namespace 级）。
func readAgentSetting(
	t *testing.T,
	base, token string,
	nsID uint,
	key string,
	guard *harness.GradleProc,
) string {
	t.Helper()
	var out struct {
		Items []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"items"`
	}
	path := "/admin/v2/agent-settings?namespaceId=" + itoa(int(nsID))
	if err := doAdminJSONAllow(base, "GET", path, token, nil, 200, &out, guard); err != nil {
		t.Fatalf("读取下发配置失败：%v", err)
	}
	for _, it := range out.Items {
		if it.Key == key {
			return it.Value
		}
	}
	return ""
}

// truncate 截断过长日志，避免测试失败时把整份文件打进报告。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ============================ FR-278 热生效（运行时行为） ============================
//
// ## 这一节补的是什么
//
// 上面 assertAgentConfigPush 只证明「控制面写的值被 agent 拉下来落进了 pushed-config.json」。
// 那是**文件层面的闭环**，与「运行时真的按新值工作」是两件事：协调器完全可能把值写进缓存、
// 却忘了推给派发器（PushedConfigCoordinator 里 timeout 与 pool-size 各有独立落点，
// 少推一处就是这类形态），此时缓存文件的断言全绿、行为却一点没变。
//
// ## 观测手法：把「生效中的超时预算」量出来
//
// `delivery.config-reload-timeout-ms` 是**单插件重载超时**：派发器判超时时会 cancel 打断
// 执行线程（见 ReloadOutcomeCollector.onTimeout）。探针的计时路径因此可以：
//  1. 在回调里睡满 60s（远超任何被测超时）；
//  2. 记录「实际睡了多久、有没有被中断」。
//
// 于是 elapsed 就是**生效中的超时预算的实测值**——这是运行时行为，不是读缓存文件。
// 用例下发两个差异明显的值（2000 与 12000），各触发一次变更并各量一次：
//   - 若热生效没接线，第一次会量到此前生效的旧值（45000，由「下发配置闭环」阶段写入）；
//   - 若只生效了一次，第二次仍会量到第一次的值。
//
// 两个方向都能红，故不是「读一眼文件就通过」的假断言。
//
// ## 触发变更为什么走 V1 配置中心
//
// 只需「让一份受控配置变更」即可，不必造交付单（那要种子版本 + 审批 + 回执，重得多）。
// V1 Create 是直写并 notify，长轮询会立刻把变更推给 agent（与前缀订阅相位同一手法）。
// 探针为计时路径额外接受裸 dataId 作为别名，正是为这条链路准备的。

// timedWindow 描述一次「超时预算实测」的期望区间。
type timedWindow struct {
	// wantMs 是本轮下发的超时值（用于报错文案）。
	wantMs int64
	// minMs / maxMs 是允许的实测区间：下界防「按旧的小值跑」，上界防「按旧的大值跑」。
	minMs int64
	maxMs int64
}

// assertReloadTimeoutHotEffect 验证下发新的重载超时后，agent **运行时**按新值工作。
//
// 前置：agent 已在线，且「下发配置闭环」阶段已把超时设为 45000。
// 该值就是本用例的「旧值」基线——第一次实测若落在 45000 附近即说明新值没生效。
func assertReloadTimeoutHotEffect(
	t *testing.T,
	repoRoot, base, token string,
	nsID uint,
	guard *harness.GradleProc,
) {
	t.Helper()
	const key = "delivery.config-reload-timeout-ms"

	// 两轮：先 2000，再 12000。两者相差 6 倍，远大于调度噪声，
	// 因此「量到的值」能唯一指认哪一份预算在生效。
	first := timedWindow{wantMs: 2000, minMs: 1500, maxMs: 8000}
	second := timedWindow{wantMs: 12000, minMs: 9000, maxMs: 20000}

	measureTimedReload(t, repoRoot, base, token, nsID, key, "delivery-reload-timed-a.yml", first, guard)
	measureTimedReload(t, repoRoot, base, token, nsID, key, "delivery-reload-timed-b.yml", second, guard)

	t.Log("PASS FR-278 热生效成立：两次下发得到两个不同的实测超时预算，agent 运行时确实按新值工作")
}

// measureTimedReload 下发一次超时值，触发一次受控变更，并断言实测值落在期望区间。
func measureTimedReload(
	t *testing.T,
	repoRoot, base, token string,
	nsID uint,
	key, dataID string,
	window timedWindow,
	guard *harness.GradleProc,
) {
	t.Helper()
	// 1) 下发新超时值，并等 agent 真的拉到（缓存文件是「拉取」的证据，不是「生效」的证据）。
	writeAgentSetting(t, base, token, nsID, key, utoa64(window.wantMs), guard)
	waitPushedSetting(t, repoRoot, key, utoa64(window.wantMs), guard)

	// 2) 触发一次受控变更：新建一份 V1 配置项（直写并 notify，长轮询立刻推送）。
	obsPath := filepath.Join(harness.ProbeDataDir(harness.BackendRunDir(repoRoot)), "e2e-delivery-hot-reload.log")
	before := countTimedObservations(obsPath, dataID)
	triggerReloadForDataID(t, base, token, dataID, guard)

	// 3) 等探针记下这一轮的实测行，并断言实测值。
	got := waitTimedObservation(t, obsPath, dataID, before, obsWait, guard)
	if !got.interrupted {
		t.Fatalf("下发超时 %dms 后探针**没有被中断**（elapsed=%dms）：说明超时预算没有作用到运行时，"+
			"最可能是协调器把值写进了缓存却没推给派发器", window.wantMs, got.elapsedMs)
	}
	if got.elapsedMs < window.minMs || got.elapsedMs > window.maxMs {
		t.Fatalf("下发超时 %dms，但实测打断发生在 %dms（期望 %d~%dms）：运行时用的不是这次下发的值。"+
			"若实测≈45000 说明压根没热生效；若≈上一轮的值说明只有第一次生效",
			window.wantMs, got.elapsedMs, window.minMs, window.maxMs)
	}
	t.Logf("PASS 下发 %dms → 实测打断于 %dms（区间 %d~%dms）",
		window.wantMs, got.elapsedMs, window.minMs, window.maxMs)
}

// timedObservation 是一行 RELOAD_TIMED 观测的解析结果。
type timedObservation struct {
	path        string
	elapsedMs   int64
	interrupted bool
}

// countTimedObservations 统计某路径已有的实测行数（用于「只认本轮新增」）。
func countTimedObservations(path, dataID string) int {
	n := 0
	for _, o := range readObservations(path) {
		if o.source != "RELOAD_TIMED" || !strings.HasSuffix(o.field3, dataID) {
			continue
		}
		n++
	}
	return n
}

// waitTimedObservation 等待该路径**新增**一行实测观测，并解析出 elapsed / interrupted。
//
// 只认「索引 >= fromIdx 且路径匹配」的行：观测文件跨轮累积，
// 认旧行会让第二轮直接拿着第一轮的数字通过（假通过）。
func waitTimedObservation(
	t *testing.T,
	path, dataID string,
	fromIdx int,
	timeout time.Duration,
	guard *harness.GradleProc,
) timedObservation {
	t.Helper()
	var got timedObservation
	var last []observation
	err := waitUntil(timeout, guard, func(context.Context) bool {
		last = readObservations(path)
		for i := fromIdx; i < len(last); i++ {
			o := last[i]
			if o.source != "RELOAD_TIMED" || !strings.HasSuffix(o.field3, dataID) {
				continue
			}
			parsed, ok := parseTimedDetail(o)
			if !ok {
				continue
			}
			got = parsed
			return true
		}
		return false
	})
	if err != nil {
		t.Fatalf("等待受控变更 %s 的超时实测观测失败（%s）；文件=%s，当前 %d 行：%v",
			dataID, timeout, path, len(last), err)
	}
	return got
}

// parseTimedDetail 解析探针写下的明细 `elapsed=<ms>ms interrupted=<true|false>`。
func parseTimedDetail(o observation) (timedObservation, bool) {
	out := timedObservation{path: o.field3}
	var haveElapsed, haveInterrupted bool
	for _, field := range strings.Fields(o.detail) {
		switch {
		case strings.HasPrefix(field, "elapsed="):
			n := atoiSafe(strings.TrimSuffix(strings.TrimPrefix(field, "elapsed="), "ms"))
			out.elapsedMs = int64(n)
			haveElapsed = true
		case strings.HasPrefix(field, "interrupted="):
			out.interrupted = strings.TrimPrefix(field, "interrupted=") == "true"
			haveInterrupted = true
		}
	}
	return out, haveElapsed && haveInterrupted
}

// waitPushedSetting 等 agent 的下发缓存里出现「该键=该值」。
//
// 解析 JSON 而不是子串匹配：缓存里同时存在别的键，子串比对会被
// 例如 `reload-pool-size=2000` 这类巧合命中，那会让断言在错误的地方变绿。
func waitPushedSetting(
	t *testing.T,
	repoRoot, key, want string,
	guard *harness.GradleProc,
) {
	t.Helper()
	path := harness.AgentPushedConfigPath(harness.BackendRunDir(repoRoot))
	var lastRaw string
	// 下发协调器周期 30s，给 3 分钟余量；拉取 + 应用在同一次 pass 内完成。
	err := waitUntil(3*time.Minute, guard, func(context.Context) bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		lastRaw = string(raw)
		var parsed struct {
			Values map[string]string `json:"values"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return false
		}
		return parsed.Values[key] == want
	})
	if err != nil {
		t.Fatalf("agent 未在 3 分钟内拉到下发值 %s=%s；%s 当前内容：%s",
			key, want, path, truncate(lastRaw, 400))
	}
	// 缓存写入与「推给派发器」在同一次 pass 内紧邻完成（PushedConfigStore.update → applyIfChanged），
	// 这里再让出一小段时间，避免在两步之间抢跑触发变更。
	time.Sleep(2 * time.Second)
}

// triggerReloadForDataID 用 V1 配置中心建一份 dataId，制造一次真实的有效配置变更。
//
// 走 V1 的理由与 triggerPrefixDataIdChange 相同：agent 的 config() 长轮询的是 V1 effective 通道，
// Create 即直写并 notify；V2 的文件+版本链语义不会唤醒该通道，用它会变成「靠探针周期轮询偶发通过」。
func triggerReloadForDataID(t *testing.T, base, token, dataID string, guard *harness.GradleProc) {
	t.Helper()
	body := map[string]any{
		"namespace":  namespace,
		"group":      model.GlobalGroupCode,
		"dataId":     dataID,
		"scopeLevel": model.ScopeGlobal,
		"format":     "yaml",
		"content":    "e2e-reload-marker: " + time.Now().UTC().Format("20060102T150405.000000"),
		"comment":    "FR-278 重载超时热生效 E2E 触发配置",
	}
	if err := doAdminJSONAllow(base, http.MethodPost, "/admin/v1/configs", token, body, http.StatusCreated, nil, guard); err != nil {
		t.Fatalf("建 V1 配置项 %s 失败（超时热生效断言依赖它触发变更）：%v", dataID, err)
	}
	t.Logf("已建 V1 global 层配置 dataId=%s（应经长轮询触发一次受控重载）", dataID)
}

// utoa64 把 int64 转十进制字符串（配置值一律以字符串下发）。
func utoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
