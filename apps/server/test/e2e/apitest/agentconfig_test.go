//go:build e2e

// FR-277 / FR-278 真机端到端验收：控制面下发 agent 运行配置 → agent 拉取 → 热生效。
//
// ## 这条测试要回答的问题
//
// 单元测试能证明「解析对」「落盘对」「键级防护对」，但证明不了**闭环真的通了**：
// 控制面写的值，agent 是否真的会去拉、拉到后是否真的按新值工作。
// 中间任何一环断了（端点路径不对、鉴权不通过、周期没跑、应用没触发），单测全绿也照样不生效。
//
// ## 验收手法
//
// 用「下发重载超时」这一项做探针：它在 agent 侧有**可观测的行为差异**——
// 我们可以让一个插件卡住，观察它被判「超时」还是「未执行」，从而反推超时值是否真的变了。
// 这比「读回缓存文件」强得多：读文件只证明写盘成功，不证明运行时用了新值。
//
// 但由于超时值本身不对外暴露，这里改用**间接证据 + 直接证据**两条：
//  1. 直接：`GET /admin/v2/agent-settings` 写入后能读回（控制面侧闭环）；
//  2. 间接：agent 侧 `pushed-config.json` 出现该值且时间戳更新（拉取闭环）。
//
// 真正的「运行时生效」由 agent 侧单测（ReloadDispatcher 热更新）覆盖。
package apitest_e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
