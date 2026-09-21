//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// FR-222 机器注册通道的真实 HTTP 链路（见 docs/specs/internal-trust-channel.md §6 验收）。
//
// 落点：受信内部调用方 = 请求命中 agentTokenMiddleware 的共享 token 分支（X-Beacon-Token == 部署 agent-token），
// 该判定由中间件完成并注入 context，handler 透传给 service；调用方无法用请求体伪造。
// 机器注册分支落在 v1 注册端点（共享 token 唯一可达的注册端点），由它把身份直落 active 并绑定 serverId。

const (
	machineRegisterIdentityServerA = "mr-1"
	machineRegisterIdentityServerB = "mr-2"
)

// registerV1Raw 以指定 token 调用 v1 注册端点，返回状态码与响应体。
func registerV1Raw(t *testing.T, baseURL, token, namespace, serverID string) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"namespace": namespace, "serverId": serverID, "role": "bukkit",
		"address": "10.0.0.7:25565", "version": "1.0.0", "capacity": 100, "weight": 100,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/beacon/v1/agent/register", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Beacon-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("注册请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &parsed)
	}
	return resp.StatusCode, parsed
}

// TestFR222MachineRegisterDisabledKeepsPending 验收 1/6：开关关闭（默认）时，携共享 token 的注册与现状一致
// （不建活跃身份，仅留「已提交待审批」审计）。
func TestFR222MachineRegisterDisabledKeepsPending(t *testing.T) {
	ts := newTestServer(t) // 未开启 allow-machine-register
	defer ts.Close()
	createV2NamespaceToken(t, ts.URL, "prod")

	code, res := registerV1Raw(t, ts.URL, testAgentToken, "prod", machineRegisterIdentityServerA)
	if code != http.StatusOK {
		t.Fatalf("开关关闭时共享 token 注册应 200，实际 %d：%v", code, res)
	}
	if res["machineRegistered"] == true {
		t.Fatalf("开关关闭时不得走机器注册通道，实际 %v", res)
	}
	// 机器注册意图审计：记为已提交待审批。
	if outcome := machineRegisterAuditField(t, ts, machineRegisterIdentityServerA, "outcome"); outcome != "pending" {
		t.Fatalf("开关关闭时机器注册意图应记为 pending，实际 %q", outcome)
	}
}

// TestFR222MachineRegisterEnabledActivatesDirectly 验收 2/5：开关开启时，共享 token 注册直接 active 且绑定 serverId。
func TestFR222MachineRegisterEnabledActivatesDirectly(t *testing.T) {
	ts := newTestServerWithOptions(t, testAgentToken, true)
	defer ts.Close()
	createV2NamespaceToken(t, ts.URL, "prod")

	code, res := registerV1Raw(t, ts.URL, testAgentToken, "prod", machineRegisterIdentityServerB)
	if code != http.StatusOK {
		t.Fatalf("开关开启时共享 token 注册应 200，实际 %d：%v", code, res)
	}
	if res["machineRegistered"] != true || res["identityId"] == nil || res["boundAt"] == nil {
		t.Fatalf("机器注册应回带权威绑定事实，实际 %v", res)
	}
	identityID, _ := res["identityId"].(string)
	if identityID == "" {
		t.Fatalf("机器注册应回带 identityId，实际 %v", res)
	}
	// 绑定结果在管理面可见：该 server 的身份为 active 且归属该 serverId。
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/agent-identities/"+identityID, nil)
	if code != http.StatusOK {
		t.Fatalf("查身份详情应 200，实际 %d", code)
	}
	if detail["status"] != "active" || detail["serverId"] != machineRegisterIdentityServerB {
		t.Fatalf("机器注册身份应为 active 且绑定 serverId，实际 %v", detail)
	}
	// 审计可查，含 serverId 与来源 IP。
	if outcome := machineRegisterAuditField(t, ts, identityID, "outcome"); outcome != "active" {
		t.Fatalf("审计应记 active，实际 %q", outcome)
	}
}

// TestFR222MachineRegisterRejectsUntrustedCallerEvenWhenEnabled 验收 4：缺 / 错 token 一律 401（与开关无关）。
func TestFR222MachineRegisterRejectsUntrustedCallerEvenWhenEnabled(t *testing.T) {
	ts := newTestServerWithOptions(t, testAgentToken, true)
	defer ts.Close()
	createV2NamespaceToken(t, ts.URL, "prod")

	for name, token := range map[string]string{"缺 token": "", "错 token": "wrong-token"} {
		code, _ := registerV1Raw(t, ts.URL, token, "prod", "mr-unt")
		if code != http.StatusUnauthorized {
			t.Fatalf("%s：应 401（与开关无关），实际 %d", name, code)
		}
	}
}

// TestFR222MachineRegisterAuditSearchable 验收 5：审计页可按动作 + 关键词检索到机器注册记录。
func TestFR222MachineRegisterAuditSearchable(t *testing.T) {
	ts := newTestServerWithOptions(t, testAgentToken, true)
	defer ts.Close()
	createV2NamespaceToken(t, ts.URL, "prod")
	if code, res := registerV1Raw(t, ts.URL, testAgentToken, "prod", "mr-3"); code != http.StatusOK {
		t.Fatalf("机器注册应 200，实际 %d：%v", code, res)
	}

	code, audits := doJSON(t, http.MethodGet,
		ts.URL+"/admin/v1/audits?action=identity.machine_registered&keyword=mr-3", nil)
	if code != http.StatusOK {
		t.Fatalf("查机器注册审计应 200，实际 %d", code)
	}
	items, _ := audits["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("审计检索应命中机器注册记录，实际 %v", audits)
	}
	first, _ := items[0].(map[string]any)
	if first["operator"] != "system:machine-register" {
		t.Fatalf("审计操作者应为 system:machine-register，实际 %v", first["operator"])
	}
	detail, _ := first["detail"].(string)
	if !strings.Contains(detail, "mr-3") || !strings.Contains(detail, "clientIp") {
		t.Fatalf("审计 detail 应含 serverId 与来源 IP，实际 %q", detail)
	}
}

// TestFR222MachineRegisterLeavesAssignmentUntouched 验收 8：机器注册不落 zone / 默认入口，分配仍走审批。
func TestFR222MachineRegisterLeavesAssignmentUntouched(t *testing.T) {
	ts := newTestServerWithOptions(t, testAgentToken, true)
	defer ts.Close()
	createV2NamespaceToken(t, ts.URL, "prod")
	if code, res := registerV1Raw(t, ts.URL, testAgentToken, "prod", "mr-4"); code != http.StatusOK {
		t.Fatalf("机器注册应 200，实际 %d：%v", code, res)
	}

	code, servers := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/servers?keyword=mr-4", nil)
	if code != http.StatusOK {
		t.Fatalf("查 server 应 200，实际 %d", code)
	}
	items, _ := servers["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("机器注册应创建 server 行，实际 %v", servers)
	}
	first, _ := items[0].(map[string]any)
	if first["zoneId"] != nil || first["isDefaultEntry"] == true {
		t.Fatalf("机器注册不得顺手落区 / 设默认入口，实际 %v", first)
	}
}

// machineRegisterAuditField 读取机器注册审计 detail 里的指定字符串字段（无记录返回空串）。
func machineRegisterAuditField(t *testing.T, ts *integrationTestServer, targetRef, key string) string {
	t.Helper()
	code, audits := doJSON(t, http.MethodGet,
		ts.URL+"/admin/v1/audits?action=identity.machine_registered&targetRef="+targetRef, nil)
	if code != http.StatusOK {
		t.Fatalf("查机器注册审计应 200，实际 %d", code)
	}
	items, _ := audits["items"].([]any)
	if len(items) == 0 {
		return ""
	}
	first, _ := items[0].(map[string]any)
	detail, _ := first["detail"].(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(detail), &parsed); err != nil {
		return ""
	}
	value, _ := parsed[key].(string)
	return value
}
