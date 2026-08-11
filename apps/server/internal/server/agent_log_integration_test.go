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

	// admin 提审后由 worker 创建 pending 命令。
	ticket := requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/instances/log-1/logs?namespace=prod", t.Name()+"-tail-logs", map[string]any{
		"reason": "集成测试读取脱敏日志",
	})
	requestID, _ := ticket["approvalRequestId"].(string)

	// 触发即写一条 instance.tail-logs 审计（detail 不含日志内容）
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=instance.tail-logs", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("应有 instance.tail-logs 审计，实际 %d：%v", code, audits["items"])
	}

	// agent 拉 tail-logs 命令 → 200，type=tail-logs
	code, pulled := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=log-1", nil)
	if code != http.StatusOK {
		t.Fatalf("拉 tail-logs 命令应 200，实际 %d：%v", code, pulled)
	}
	if pulled["type"] != "tail-logs" {
		t.Fatalf("命令 type 应 tail-logs，实际 %v", pulled["type"])
	}
	cmdID := int(pulled["id"].(float64))

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

	// 原申请人从审批详情取得一次性授权，再消费脱敏日志正文。
	grantID := sensitiveGrantIDForTest(t, ts, requestID)
	code, view := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-1/logs/grants/"+grantID+"/consume", map[string]any{"commandId": cmdID})
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

// TestAgentLogTailSingleActive 单活跃限速（FR-88）：重复申请可进入审批，但执行时不得创建第二条活跃命令。
func TestAgentLogTailSingleActive(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "log-2", "area1")

	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/instances/log-2/logs?namespace=prod", t.Name()+"-tail-logs-first", map[string]any{"reason": "集成测试读取脱敏日志"})
	code, ticket := doJSONWithHeaders(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-2/logs?namespace=prod", map[string]any{"reason": "集成测试重复读取日志"}, map[string]string{"Idempotency-Key": t.Name() + "-tail-logs-second"})
	if code != http.StatusAccepted {
		t.Fatalf("重复取日志应先返回审批票据，实际 %d：%v", code, ticket)
	}
	applyApprovalTicket(t, ts, ticket)
	requestID, _ := ticket["approvalRequestId"].(string)
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/approval-requests/"+requestID, nil)
	if code != http.StatusOK || detail["status"] != "failed" {
		t.Fatalf("第二条取日志执行应因单活跃限制失败，实际 %d：%v", code, detail)
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
	roKey, _ := createKey(t, ts, "ro-log", "readonly")
	code, body := doAPIKey(t, http.MethodPost, ts.URL+"/admin/v1/instances/log-1/logs?namespace=prod", roKey, false, nil)
	if code != http.StatusForbidden || body["code"] != "FORBIDDEN" {
		t.Fatalf("只读密钥触发取日志应 403 FORBIDDEN，实际 %d：%v", code, body)
	}
}

// TestAgentLogGetNoCommand 旧查询入口不返回日志正文，必须经审批授权消费。
func TestAgentLogGetNoCommand(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "log-3", "area1")
	code, _ := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/log-3/logs?namespace=prod", nil)
	if code != http.StatusConflict {
		t.Fatalf("旧日志查询入口应拒绝并要求审批，实际 %d", code)
	}
}
