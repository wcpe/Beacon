//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestAgentLogTailFullChain 取 agent 日志命令-回传全链路（FR-88，见 ADR-0040）：
// admin 触发取日志(202, pending) → 写一条 instance.tail-logs 审计 → agent 拉 tail-logs 命令(200)
// → agent 回传脱敏日志(/beacon/v1/agent/logs, 200) → admin 查询(200, done) 得脱敏日志行。
func TestAgentLogTailFullChain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "log-1", "area1")

	// admin 触发取日志 → 202 + pending 命令视图
	code, cmd := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-1/logs?namespace=prod", nil)
	if code != http.StatusAccepted {
		t.Fatalf("触发取日志应 202，实际 %d：%v", code, cmd)
	}
	if cmd["status"] != "pending" {
		t.Fatalf("命令视图应 status=pending，实际 %v", cmd)
	}
	cmdID := int(cmd["commandId"].(float64))

	// 触发即写一条 instance.tail-logs 审计（detail 不含日志内容）
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=instance.tail-logs", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("应有 instance.tail-logs 审计，实际 %d：%v", code, audits["items"])
	}

	// agent 拉 tail-logs 命令 → 200，type=tail-logs
	code, pulled := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=log-1", nil)
	if code != http.StatusOK || int(pulled["id"].(float64)) != cmdID {
		t.Fatalf("拉 tail-logs 命令应 200 且匹配 id，实际 %d：%v", code, pulled)
	}
	if pulled["type"] != "tail-logs" {
		t.Fatalf("命令 type 应 tail-logs，实际 %v", pulled["type"])
	}

	// agent 回传脱敏日志快照 → 200
	code, recv := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/logs", map[string]any{
		"commandId": cmdID,
		"lines": []map[string]any{
			{"level": "INFO", "text": "已应用有效配置 md5=abc"},
			{"level": "WARN", "text": "bootstrap-token=***"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("回传日志应 200，实际 %d：%v", code, recv)
	}

	// admin 查询 → 200 done，得脱敏日志行
	code, view := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/log-1/logs?namespace=prod", nil)
	if code != http.StatusOK || view["status"] != "done" {
		t.Fatalf("查询取日志应 200 done，实际 %d：%v", code, view)
	}
	lines := asSlice(view["lines"])
	if len(lines) != 2 {
		t.Fatalf("应得 2 行日志，实际 %v", view["lines"])
	}
	first := lines[0].(map[string]any)
	if first["level"] != "INFO" || first["text"] != "已应用有效配置 md5=abc" {
		t.Fatalf("首行日志不符，实际 %v", first)
	}
}

// TestAgentLogTailSingleActive 单活跃限速（FR-88）：已有进行中取日志命令再触发 → 409 AGENT_LOG_ACTIVE。
func TestAgentLogTailSingleActive(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "log-2", "area1")

	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-2/logs?namespace=prod", nil); code != http.StatusAccepted {
		t.Fatalf("首次触发应 202，实际 %d", code)
	}
	// 第一条仍 pending（未被 agent 拉走 / 回传），再触发应被单活跃限速拒。
	code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-2/logs?namespace=prod", nil)
	if code != http.StatusConflict || body["code"] != "AGENT_LOG_ACTIVE" {
		t.Fatalf("已有活跃取日志命令再触发应 409 AGENT_LOG_ACTIVE，实际 %d：%v", code, body)
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

// TestAgentLogTailReadonlyForbidden 只读密钥触发取日志（写操作）→ 经只读拒写中间件 403。
func TestAgentLogTailReadonlyForbidden(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	roKey, _ := createKey(t, ts.URL, "ro-log", "readonly")
	code, body := doAPIKey(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-1/logs?namespace=prod", roKey, false, nil)
	if code != http.StatusForbidden || body["code"] != "FORBIDDEN" {
		t.Fatalf("只读密钥触发取日志应 403 FORBIDDEN，实际 %d：%v", code, body)
	}
}

// TestAgentLogGetNoCommand 从未触发取日志 → 查询 204（前端据此显示「点按钮拉取」）。
func TestAgentLogGetNoCommand(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "log-3", "area1")
	code, _ := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/log-3/logs?namespace=prod", nil)
	if code != http.StatusNoContent {
		t.Fatalf("从无取日志命令查询应 204，实际 %d", code)
	}
}
