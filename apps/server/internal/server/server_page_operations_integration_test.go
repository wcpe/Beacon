//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestServerPageResyncCommandVisible 服务器页重同步闭环：
// admin 下发 resync → agent 拉命令并回传结果 → 命令记录端点可按 serverId 查到 done，且不带瞬态载荷。
func TestServerPageResyncCommandVisible(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "srv-resync-1", "area1")

	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/instances/srv-resync-1/resync?namespace=prod", t.Name()+"-resync", map[string]any{"reason": "集成测试重同步"})

	code, pulled := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=srv-resync-1", nil)
	if code != http.StatusOK || pulled["type"] != "resync-config" {
		t.Fatalf("agent 应拉到 resync-config 命令，实际 %d：%v", code, pulled)
	}
	cmdID := int(pulled["id"].(float64))

	code, _ = doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/commands/result", map[string]any{
		"commandId": cmdID,
		"ok":        true,
	})
	if code != http.StatusOK {
		t.Fatalf("回传重同步结果应 200，实际 %d", code)
	}

	code, page := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/commands?namespace=prod&serverId=srv-resync-1&type=resync-config", nil)
	if code != http.StatusOK {
		t.Fatalf("命令记录查询应 200，实际 %d：%v", code, page)
	}
	items := asSlice(page["items"])
	if len(items) != 1 {
		t.Fatalf("命令记录应有 1 条，实际 %v", page["items"])
	}
	first := items[0].(map[string]any)
	if first["status"] != "done" || first["type"] != "resync-config" {
		t.Fatalf("命令记录应显示 done resync-config，实际 %v", first)
	}
	if _, ok := first["payload"]; ok {
		t.Fatalf("命令记录不得带 payload：%v", first)
	}
}

// TestServerPageBrowseFullChain 守护旧文件浏览入口的 permit 边界：
// 公开入口固定失败关闭，不能直连 Agent 读取。
func TestServerPageBrowseFullChain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "srv-browse-1", "area1")

	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/srv-browse-1/browse?namespace=prod&op=list&path=&limit=50", nil)
	if code != http.StatusConflict || body["code"] != "operation_requires_approval" {
		t.Fatalf("未持 permit 浏览文件内容应 409 operation_requires_approval，实际 %d：%v", code, body)
	}

	roKey, _ := createKey(t, ts, "ro-browse", "readonly")
	code, body = doAPIKey(t, http.MethodGet, ts.URL+"/admin/v1/instances/srv-browse-1/browse?namespace=prod&op=list", roKey, false, nil)
	if code != http.StatusConflict || body["code"] != "operation_requires_approval" {
		t.Fatalf("readonly 调旧浏览 GET 也应保持 409，实际 %d：%v", code, body)
	}
}

// TestServerPageBrowseApprovalFlow 验证浏览命令必须经申请、审批、worker、回传和一次性授权消费闭环。
func TestServerPageBrowseApprovalFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	const identityID = "f1000000-0000-4000-8000-000000000001"
	v2Token := createV2NamespaceToken(t, ts.URL, "prod")
	activateAgent(t, ts, v2Token, identityID, "srv-browse-approval")
	registerOnline(t, ts.URL, "prod", "srv-browse-approval", "area1")
	agentHeaders := map[string]string{"X-Beacon-Token": v2Token, "X-Beacon-Identity": identityID, "X-Beacon-Boot": "boot-1"}

	code, ticket := doJSONWithHeaders(t, http.MethodPost, ts.URL+"/admin/v1/instances/srv-browse-approval/browse", map[string]any{
		"namespace": "prod", "op": "file", "path": "Demo/config.yml", "reason": "核对线上文件",
	}, map[string]string{"Idempotency-Key": t.Name() + "-browse"})
	if code != http.StatusAccepted {
		t.Fatalf("提交浏览审批应 202，实际 %d：%v", code, ticket)
	}
	applyApprovalTicket(t, ts, ticket)

	code, pulled := doAgentJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=srv-browse-approval", agentHeaders, nil)
	if code != http.StatusOK || pulled["type"] != "fs-browse" {
		t.Fatalf("批准后 agent 应拉到 fs-browse，实际 %d：%v", code, pulled)
	}
	commandID := int(pulled["id"].(float64))
	code, body := doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/browse-result", agentHeaders, map[string]any{
		"namespace": "prod", "serverId": "srv-browse-approval", "commandId": commandID, "ok": true,
		"result": map[string]any{"path": "Demo/config.yml", "content": "enabled: true\n"},
	})
	if code != http.StatusOK {
		t.Fatalf("agent 回传浏览结果应 200，实际 %d：%v", code, body)
	}
	grantID := sensitiveGrantIDForTest(t, ts, ticket["approvalRequestId"].(string))
	code, consumed := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/srv-browse-approval/browse/grants/"+grantID+"/consume", map[string]any{"commandId": commandID})
	if code != http.StatusOK || consumed["content"] != "enabled: true\n" {
		t.Fatalf("原申请主体应一次消费浏览结果，实际 %d：%v", code, consumed)
	}
	code, second := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/srv-browse-approval/browse/grants/"+grantID+"/consume", map[string]any{"commandId": commandID})
	if code != http.StatusGone || second["code"] != "sensitive_access_consumed" {
		t.Fatalf("浏览授权第二次消费应失败，实际 %d：%v", code, second)
	}
}
