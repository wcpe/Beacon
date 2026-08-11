//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// FR-155 区服权威缺口端点 REST 集成：换区工单多表事务 + 默认入口 409。
// 需真实 MySQL（BEACON_TEST_DSN）；未设则由 testsupport.OpenTestDB 跳过。

// createNamespaceV2 经管理端建 namespace，返回其 id 与一次性明文接入 token。
func createNamespaceV2(t *testing.T, baseURL, name string) (uint, string) {
	t.Helper()
	code, body := doJSON(t, http.MethodPost, baseURL+"/admin/v2/namespaces", map[string]any{"name": name, "description": ""})
	if code != http.StatusCreated {
		t.Fatalf("建 namespace 应 201，实际 %d：%v", code, body)
	}
	return uint(body["id"].(float64)), body["accessToken"].(string)
}

// registerAgentV2 以 namespace token 注册一台 agent，期望进入 pending（202）。
func registerAgentV2(t *testing.T, baseURL, token, identityID, serverID, kind string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"identityId": identityID, "serverId": serverID, "kind": kind, "bootId": "boot-" + serverID,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/beacon/v2/agent/register", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Beacon-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("注册 %s 失败: %v", serverID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("注册 %s 应 202 pending，实际 %d：%s", serverID, resp.StatusCode, string(data))
	}
}

// serverRowID 取某 namespace 下指定 serverId 的 server 行数字 id。
func serverRowID(t *testing.T, baseURL string, namespaceID uint, serverID string) uint {
	t.Helper()
	code, body := doJSON(t, http.MethodGet, baseURL+"/admin/v2/servers?namespaceId="+itoa(int(namespaceID)), nil)
	if code != http.StatusOK {
		t.Fatalf("列 server 应 200，实际 %d：%v", code, body)
	}
	for _, item := range asSlice(body["items"]) {
		row := item.(map[string]any)
		if row["serverId"] == serverID {
			return uint(row["id"].(float64))
		}
	}
	t.Fatalf("未找到 server %s：%v", serverID, body["items"])
	return 0
}

func createAuthorityNode(t *testing.T, baseURL, path string, body map[string]any) uint {
	t.Helper()
	code, resp := doJSON(t, http.MethodPost, baseURL+path, body)
	if code != http.StatusCreated {
		t.Fatalf("建 %s 应 201，实际 %d：%v", path, code, resp)
	}
	return uint(resp["id"].(float64))
}

// TestV2RezoneRestMultiTableTransaction 换区工单经 HTTP：解绑清归属 + 写预填 + 身份重入 pending，再重确认落区。
func TestV2RezoneRestMultiTableTransaction(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	nsID, token := createNamespaceV2(t, ts.URL, "prod")
	const identityID = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	registerAgentV2(t, ts.URL, token, identityID, "lobby-1", "backend")
	approveAgentIdentityForTest(t, ts, identityID, "lobby-1")

	clusterID := createAuthorityNode(t, ts.URL, "/admin/v2/bc-clusters", map[string]any{"namespaceId": nsID, "name": "bc-a"})
	regionID := createAuthorityNode(t, ts.URL, "/admin/v2/regions", map[string]any{"bcClusterId": clusterID, "name": "r1"})
	zoneA := createAuthorityNode(t, ts.URL, "/admin/v2/zones", map[string]any{"regionId": regionID, "name": "z-a"})
	zoneB := createAuthorityNode(t, ts.URL, "/admin/v2/zones", map[string]any{"regionId": regionID, "name": "z-b"})

	rowID := serverRowID(t, ts.URL, nsID, "lobby-1")
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v2/server-assignments", t.Name()+"-assign", map[string]any{
		"serverIds": []uint{rowID}, "target": map[string]any{"kind": "zone", "id": zoneA}, "isDefaultEntry": true, "reason": "首次",
	})

	// 发起换区工单 → zoneB
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v2/server-rezones", t.Name()+"-rezone", map[string]any{
		"serverIds": []uint{rowID}, "target": map[string]any{"kind": "zone", "id": zoneB}, "reason": "扩容换区",
	})

	// server 已解绑清归属 + 记预填目标
	code, servers := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/servers?namespaceId="+itoa(int(nsID)), nil)
	if code != http.StatusOK {
		t.Fatalf("列 server 应 200，实际 %d", code)
	}
	row := asSlice(servers["items"])[0].(map[string]any)
	if row["zoneId"] != nil || row["assigned"] != false || row["pendingZoneId"] != float64(zoneB) {
		t.Fatalf("换区后 server 应未分配且预填 zoneB，实际 %v", row)
	}

	// 身份重入 pending，详情带预填目标
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/agent-identities/"+identityID, nil)
	if code != http.StatusOK || detail["status"] != "pending" {
		t.Fatalf("换区后身份应 pending，实际 %d：%v", code, detail)
	}
	prefill, _ := detail["rezonePrefill"].(map[string]any)
	if prefill == nil || prefill["targetId"] != float64(zoneB) {
		t.Fatalf("身份详情应带预填目标 zoneB，实际 %v", detail["rezonePrefill"])
	}

	// 重确认（缺省取预填）落区 zoneB
	approveAgentIdentityWithKeyForTest(t, ts, identityID, "lobby-1", t.Name()+"-identity-reconfirm")
	code, servers = doJSON(t, http.MethodGet, ts.URL+"/admin/v2/servers?namespaceId="+itoa(int(nsID)), nil)
	if code != http.StatusOK {
		t.Fatalf("列 server 应 200，实际 %d", code)
	}
	row = asSlice(servers["items"])[0].(map[string]any)
	if row["zoneId"] != float64(zoneB) || row["pendingZoneId"] != nil {
		t.Fatalf("重确认后 server 应落区 zoneB 且清 pending，实际 %v", row)
	}
}

// TestV2DefaultEntryRestFailure 未分配小区的 server 置默认入口须经审批；执行时因未分配而失败。
func TestV2DefaultEntryRestFailure(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	nsID, token := createNamespaceV2(t, ts.URL, "prod")
	const identityID = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	registerAgentV2(t, ts.URL, token, identityID, "lobby-9", "backend")
	approveAgentIdentityForTest(t, ts, identityID, "lobby-9")
	rowID := serverRowID(t, ts.URL, nsID, "lobby-9")

	code, ticket := doJSONWithHeaders(t, http.MethodPut, ts.URL+"/admin/v2/servers/"+itoa(int(rowID))+"/default-entry", map[string]any{"value": true, "reason": "测试默认入口"}, map[string]string{"Idempotency-Key": t.Name() + "-default-entry"})
	if code != http.StatusAccepted {
		t.Fatalf("未分配 server 置默认入口应先返回审批票据，实际 %d：%v", code, ticket)
	}
	applyApprovalTicket(t, ts, ticket)
	requestID, _ := ticket["approvalRequestId"].(string)
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/approval-requests/"+requestID, nil)
	if code != http.StatusOK || detail["status"] != "failed" {
		t.Fatalf("未分配 server 的审批执行应失败，实际 %d：%v", code, detail)
	}
}
