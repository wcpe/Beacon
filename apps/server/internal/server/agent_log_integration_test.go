//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestAgentLogTailFullChain 验证日志命令直执、Agent 回传、正文查询与元数据审计闭环。
func TestAgentLogTailFullChain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// 回传要求权威身份：与生产一致地建立 v2 身份并用其请求头回传。
	const identityID = "e1000000-0000-4000-8000-000000000001"
	const otherIdentityID = "e1000000-0000-4000-8000-000000000002"
	v2Token := createV2NamespaceToken(t, ts.URL, "prod")
	activateAgent(t, ts, v2Token, identityID, "log-1")
	activateAgent(t, ts, v2Token, otherIdentityID, "log-other")
	registerOnline(t, ts.URL, "prod", "log-1", "area1")
	agentHeaders := map[string]string{"X-Beacon-Token": v2Token, "X-Beacon-Identity": identityID, "X-Beacon-Boot": "boot-1"}

	code, created := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-1/logs?namespace=prod", map[string]any{"reason": "集成测试读取脱敏日志"})
	if code != http.StatusAccepted || created["commandId"] == nil {
		t.Fatalf("日志读取应直接创建命令，实际 %d：%v", code, created)
	}
	cmdID := int(created["commandId"].(float64))

	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=instance.tail-logs", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("应有 instance.tail-logs 审计，实际 %d：%v", code, audits["items"])
	}
	// 拉命令要求权威身份（issue #146）：沿用本用例自己建立的那套身份，
	// 不能另建一套——同一 serverId 的第二套身份会与该用例的身份审批幂等键冲突。
	code, pulled := doAgentJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=log-1", agentHeaders, nil)
	if code != http.StatusOK || pulled["type"] != "tail-logs" || int(pulled["id"].(float64)) != cmdID {
		t.Fatalf("agent 应拉到指定 tail-logs 命令，实际 %d：%v", code, pulled)
	}

	// 身份不符（另一台 agent 猜中 commandId）必须被拒，且不得改写命令内容。
	// 这一步必须放在命令仍处 fetched 时进行：若放在合法回传之后，命令已是 done，
	// 状态校验本身就会拒绝，断言将无法鉴别归属校验是否还存在（会空过）。
	code, forged := doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/logs",
		map[string]string{"X-Beacon-Token": v2Token, "X-Beacon-Identity": otherIdentityID, "X-Beacon-Boot": "boot-1"},
		map[string]any{"commandId": cmdID, "lines": []map[string]any{{"level": "INFO", "text": "越权写入"}}})
	if code != http.StatusNotFound {
		t.Fatalf("非归属 agent 回传应 404，实际 %d：%v", code, forged)
	}

	code, recv := doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/logs", agentHeaders, map[string]any{
		"commandId": cmdID,
		"lines": []map[string]any{
			{"level": "INFO", "text": "已应用有效配置 md5=abc"},
			{"level": "WARN", "text": "bootstrap-token=***"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("回传日志应 200，实际 %d：%v", code, recv)
	}

	code, view := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-1/logs/grants/legacy/consume", map[string]any{"commandId": cmdID})
	if code != http.StatusOK || view["status"] != "done" {
		t.Fatalf("direct 查询取日志应 200 done，实际 %d：%v", code, view)
	}
	lines := asSlice(view["lines"])
	if len(lines) != 2 {
		t.Fatalf("应得 2 行日志，实际 %v", view["lines"])
	}
	first := lines[0].(map[string]any)
	if first["level"] != "INFO" || first["text"] != "已应用有效配置 md5=abc" {
		t.Fatalf("首行日志不符，实际 %v", first)
	}
	code, latest := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/log-1/logs?namespace=prod", nil)
	if code != http.StatusOK || len(asSlice(latest["lines"])) != 2 {
		t.Fatalf("GET 日志应直读最近结果，实际 %d：%v", code, latest)
	}
}

// TestAgentLogTailSingleActive 验证 direct 日志请求仍受单活跃命令限速。
func TestAgentLogTailSingleActive(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "log-2", "area1")

	code, first := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-2/logs?namespace=prod", map[string]any{"reason": "集成测试读取脱敏日志"})
	if code != http.StatusAccepted || first["commandId"] == nil {
		t.Fatalf("首次日志读取应创建命令，实际 %d：%v", code, first)
	}
	code, second := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-2/logs?namespace=prod", map[string]any{"reason": "集成测试重复读取日志"})
	if code != http.StatusConflict || second["code"] != "AGENT_LOG_ACTIVE" {
		t.Fatalf("重复取日志应被单活跃限制拒绝，实际 %d：%v", code, second)
	}
}

// TestAgentLogTailOfflineInstance 目标不在注册表（离线 / 不存在）→ 404，不建命令。
func TestAgentLogTailOfflineInstance(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/ghost/logs?namespace=prod", nil)
	if code != http.StatusNotFound || body["code"] != "INSTANCE_NOT_FOUND" {
		t.Fatalf("离线实例触发取日志应 404 INSTANCE_NOT_FOUND，实际 %d：%v", code, body)
	}
}

// TestAgentLogTailReadonlyForbidden 验证具有 management.read 的只读密钥可 direct 触发日志读取。
func TestAgentLogTailReadonlyForbidden(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "log-ro", "area1")
	roKey, _ := createKey(t, ts, "ro-log", "readonly")
	code, body := doAPIKey(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-ro/logs?namespace=prod", roKey, false, nil)
	if code != http.StatusAccepted || body["commandId"] == nil {
		t.Fatalf("只读密钥具备 management.read，应允许 direct 读取，实际 %d：%v", code, body)
	}
}

// TestAgentLogGetNoCommand 验证没有历史日志时 direct 查询返回空摘要并写审计。
func TestAgentLogGetNoCommand(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "log-3", "area1")
	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/log-3/logs?namespace=prod", nil)
	if code != http.StatusOK || len(asSlice(body["lines"])) != 0 {
		t.Fatalf("无历史日志应返回空摘要，实际 %d：%v", code, body)
	}
	// 读取使用独立动作名，便于与「下发命令」区分检索。
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=instance.tail-logs-read", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("无结果查询也应留下 instance.tail-logs-read 审计，实际 %d：%v", code, audits)
	}
}
