//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// doRaw 发起一次不自动携带令牌的请求（用于测试缺/错令牌场景），返回状态码与解析体。
func doRaw(t *testing.T, method, url, bearer string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &parsed)
	}
	return resp.StatusCode, parsed
}

// TestAdminAuthRequired 无令牌访问 admin 端点应 401 ADMIN_UNAUTHORIZED。
func TestAdminAuthRequired(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	code, body := doRaw(t, http.MethodGet, ts.URL+"/admin/v1/namespaces", "", nil)
	if code != http.StatusUnauthorized || body["code"] != "ADMIN_UNAUTHORIZED" {
		t.Fatalf("无令牌读端点应 401 ADMIN_UNAUTHORIZED，实际 %d：%v", code, body)
	}

	code, body = doRaw(t, http.MethodPost, ts.URL+"/admin/v1/configs", "", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "noauth.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n",
	})
	if code != http.StatusUnauthorized || body["code"] != "ADMIN_UNAUTHORIZED" {
		t.Fatalf("无令牌写端点应 401 ADMIN_UNAUTHORIZED，实际 %d：%v", code, body)
	}
}

// TestAdminAuthBadToken 非法令牌应 401。
func TestAdminAuthBadToken(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	code, body := doRaw(t, http.MethodGet, ts.URL+"/admin/v1/namespaces", "garbage.token", nil)
	if code != http.StatusUnauthorized || body["code"] != "ADMIN_UNAUTHORIZED" {
		t.Fatalf("非法令牌应 401 ADMIN_UNAUTHORIZED，实际 %d：%v", code, body)
	}
}

// TestLoginWrongCredentials 错误凭据登录应 401 BAD_CREDENTIALS。
func TestLoginWrongCredentials(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	code, body := doRaw(t, http.MethodPost, ts.URL+"/admin/v1/auth/login", "", map[string]any{
		"username": testAuthUser, "password": "wrong-pass",
	})
	if code != http.StatusUnauthorized || body["code"] != "BAD_CREDENTIALS" {
		t.Fatalf("错误凭据应 401 BAD_CREDENTIALS，实际 %d：%v", code, body)
	}
}

// TestLoginThenAuthorized 正确登录得到令牌，携带令牌可访问 admin 端点。
func TestLoginThenAuthorized(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	code, body := doRaw(t, http.MethodPost, ts.URL+"/admin/v1/auth/login", "", map[string]any{
		"username": testAuthUser, "password": testAuthPass,
	})
	if code != http.StatusOK {
		t.Fatalf("正确凭据登录应 200，实际 %d：%v", code, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatal("登录响应缺 token")
	}

	code, _ = doRaw(t, http.MethodGet, ts.URL+"/admin/v1/namespaces", token, nil)
	if code != http.StatusOK {
		t.Fatalf("携带有效令牌读端点应 200，实际 %d", code)
	}
}

// TestWriteAuditUsesAuthenticatedOperator 写操作审计的 operator 必须是认证身份，
// 而非请求体里手填的 operator（后端以认证身份为准）。
func TestWriteAuditUsesAuthenticatedOperator(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 请求体故意手填一个伪造 operator；应被认证身份覆盖。
	code, created := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "auth-operator.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "forged-user",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d：%v", code, created)
	}

	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=config.create", nil)
	if code != http.StatusOK {
		t.Fatalf("查审计应 200，实际 %d", code)
	}
	items, _ := audits["items"].([]any)
	if len(items) == 0 {
		t.Fatal("应有 config.create 审计")
	}
	first, _ := items[0].(map[string]any)
	if op, _ := first["operator"].(string); op != testAuthUser {
		t.Fatalf("审计 operator 应为认证身份 %q，实际 %q（手填 operator 未被覆盖）", testAuthUser, op)
	}
}
