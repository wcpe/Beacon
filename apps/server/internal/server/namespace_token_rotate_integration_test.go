//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// registerAgentV2Status 以指定 token 尝试 v2 注册并**返回状态码**（不 Fatal）。
//
// 轮换测试必须断言「旧 token 被拒（401）」这一负向路径，而既有的 registerAgentV2 遇非 202
// 即 t.Fatalf，故另立此不中断版本。
func registerAgentV2Status(t *testing.T, baseURL, token, identityID, serverID string) int {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"identityId": identityID, "serverId": serverID, "kind": "backend", "bootId": "boot-" + serverID,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/beacon/v2/agent/register", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Beacon-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("注册请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// TestNamespaceTokenRotateRESTFlow 轮换 namespace 接入 token（FR-238）：
// 轮换前旧 token 可用 → 轮换返回新明文 → 旧 token **立即** 401、新 token 可用。
func TestNamespaceTokenRotateRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	nsID, oldToken := createNamespaceV2(t, ts.URL, "rotate-flow")
	if oldToken == "" {
		t.Fatal("创建应返回一次性明文 token")
	}

	// 轮换前：旧 token 可用（注册进 pending → 202）
	if got := registerAgentV2Status(t, ts.URL, oldToken, "11111111-1111-4111-8111-111111111111", "rotate-before"); got != http.StatusAccepted {
		t.Fatalf("轮换前旧 token 应可用（202），实际 %d", got)
	}

	// 轮换
	code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v2/namespaces/"+itoa(int(nsID))+"/token/rotate", nil)
	if code != http.StatusOK {
		t.Fatalf("轮换应 200，实际 %d：%v", code, body)
	}
	newToken, _ := body["accessToken"].(string)
	if newToken == "" {
		t.Fatalf("轮换响应应含一次性明文 token，实际 %v", body)
	}
	if newToken == oldToken {
		t.Fatal("轮换必须产出与旧 token 不同的新 token")
	}

	// 轮换后：旧 token 立即失效
	if got := registerAgentV2Status(t, ts.URL, oldToken, "22222222-2222-4222-8222-222222222222", "rotate-old"); got != http.StatusUnauthorized {
		t.Fatalf("轮换后旧 token 应 401，实际 %d", got)
	}
	// 轮换后：新 token 可用
	if got := registerAgentV2Status(t, ts.URL, newToken, "33333333-3333-4333-8333-333333333333", "rotate-new"); got != http.StatusAccepted {
		t.Fatalf("轮换后新 token 应可用（202），实际 %d", got)
	}
}

// TestNamespaceTokenRotateContract 轮换的契约边界：不存在 → 404；响应不回显哈希；写审计。
func TestNamespaceTokenRotateContract(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 不存在 → NAMESPACE_NOT_FOUND
	code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v2/namespaces/999999/token/rotate", nil)
	if code != http.StatusNotFound || body["code"] != "NAMESPACE_NOT_FOUND" {
		t.Fatalf("轮换不存在的 namespace 应 404 NAMESPACE_NOT_FOUND，实际 %d：%v", code, body)
	}

	// 正常轮换：明文有、哈希不出现
	nsID, _ := createNamespaceV2(t, ts.URL, "rotate-contract")
	code, rotated := doJSON(t, http.MethodPost, ts.URL+"/admin/v2/namespaces/"+itoa(int(nsID))+"/token/rotate", nil)
	if code != http.StatusOK {
		t.Fatalf("轮换应 200，实际 %d：%v", code, rotated)
	}
	if _, has := rotated["accessTokenHash"]; has {
		t.Fatal("轮换响应不得回显 token 哈希")
	}
	if _, has := rotated["access_token_hash"]; has {
		t.Fatal("轮换响应不得回显 token 哈希（snake_case 形态）")
	}

	// 写审计
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?action=namespace.token-rotate", nil)
	if code != http.StatusOK {
		t.Fatalf("查审计应 200，实际 %d：%v", code, audits)
	}
	if len(asSlice(audits["items"])) == 0 {
		t.Fatalf("轮换应写 action=namespace.token-rotate 的审计，实际 %v", audits)
	}
}
