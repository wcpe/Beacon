//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// v1 注册端点的归属校验（ADR-0094 决策 5）。
//
// 注册同时是数据面挂载入口：不校验归属时，任一持有 namespace token 的实例都能把自己注册进
// **任意环境**的实例列表与拓扑，并自定 address / role / capacity / weight / backends，
// 污染 /discovery 与落位建议；按 < TTL 周期重注册他人 serverId 还会让真 agent 永久注册失败。
//
// 共享 token 的机器注册通道是引导入口（此时可能尚无身份），保持放行；
// 经 v2 身份分支到达的请求必然已持有完整权威身份，自报归属必须与 DB 权威绑定一致。
func TestRegisterRejectsIdentityBindingMismatch(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 为 prod/lobby-1 建立已确认身份，之后所有请求都以这套头发起。
	headers := bindAgentForTest(t, ts, "prod", "lobby-1")

	// 正向：自报归属与身份一致，应放行。
	status, body := doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", headers,
		map[string]any{"namespace": "prod", "serverId": "lobby-1", "role": "bukkit", "address": "10.0.0.7:25565"})
	if status != http.StatusOK {
		t.Fatalf("自报归属与身份一致应 200，实际 %d：%v", status, body)
	}

	// 负向一：冒充同环境内的另一台实例。
	status, body = doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", headers,
		map[string]any{"namespace": "prod", "serverId": "ghost", "role": "bukkit", "address": "10.0.0.8:25565"})
	if status != http.StatusConflict {
		t.Fatalf("冒充同环境他服应 409，实际 %d：%v", status, body)
	}
	if body["code"] != "identity_binding_mismatch" {
		t.Fatalf("应返回 identity_binding_mismatch，实际 %v", body["code"])
	}

	// 负向二：跨环境注册（这正是本 PR 要关闭的横向冒充面）。
	status, body = doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", headers,
		map[string]any{"namespace": "other", "serverId": "lobby-1", "role": "bukkit", "address": "10.0.0.9:25565"})
	if status != http.StatusConflict {
		t.Fatalf("跨环境注册应 409，实际 %d：%v", status, body)
	}
}
