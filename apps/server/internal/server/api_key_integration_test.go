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

// doAPIKey 用 API 密钥（X-Beacon-Api-Key 头或 Authorization: Bearer）发起请求。
func doAPIKey(t *testing.T, method, url, key string, viaBearer bool, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	if viaBearer {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("X-Beacon-Api-Key", key)
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

// createKey 经登录操作者创建一把密钥，返回明文与 id。
func createKey(t *testing.T, baseURL, name, role string) (string, int) {
	t.Helper()
	code, body := doJSON(t, http.MethodPost, baseURL+"/admin/v1/api-keys", map[string]any{
		"name": name, "role": role,
	})
	if code != http.StatusCreated {
		t.Fatalf("创建 %s 密钥应 201，实际 %d：%v", role, code, body)
	}
	plaintext, _ := body["key"].(string)
	if !strings.HasPrefix(plaintext, "bk_") {
		t.Fatalf("创建响应应含 bk_ 明文，实际 %v", body["key"])
	}
	idF, _ := body["id"].(float64)
	return plaintext, int(idF)
}

// TestAPIKeyReadonlyAllowsReadDeniesWrite 只读密钥可读、写一律 403（统一中间件裁决）。
func TestAPIKeyReadonlyAllowsReadDeniesWrite(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	roKey, roID := createKey(t, ts.URL, "ext-readonly", "readonly")

	// 读端点放行（两种请求头都认）
	if code, _ := doAPIKey(t, http.MethodGet, ts.URL+"/admin/v1/instances", roKey, false, nil); code != http.StatusOK {
		t.Fatalf("只读密钥（X-Beacon-Api-Key）读实例应 200，实际 %d", code)
	}
	if code, _ := doAPIKey(t, http.MethodGet, ts.URL+"/admin/v1/zones?namespace=prod", roKey, true, nil); code != http.StatusOK {
		t.Fatalf("只读密钥（Bearer）读 zone 应 200，实际 %d", code)
	}

	// 写端点一律 403 FORBIDDEN
	writes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/admin/v1/configs", map[string]any{
			"namespace": "prod", "group": "__GLOBAL__", "dataId": "ro.yml",
			"scopeLevel": "global", "format": "yaml", "content": "k: 1\n",
		}},
		{http.MethodPost, "/admin/v1/api-keys", map[string]any{"name": "x", "role": "readonly"}},
		{http.MethodDelete, "/admin/v1/api-keys/" + itoa(roID), nil},
	}
	for _, wcase := range writes {
		code, body := doAPIKey(t, wcase.method, ts.URL+wcase.path, roKey, false, wcase.body)
		if code != http.StatusForbidden || body["code"] != "FORBIDDEN" {
			t.Fatalf("只读密钥 %s %s 应 403 FORBIDDEN，实际 %d：%v", wcase.method, wcase.path, code, body)
		}
	}
}

// TestAPIKeyFullCanWriteAndAuditsPrincipal full 密钥可写，写审计 operator 为 apikey:<名称>。
func TestAPIKeyFullCanWriteAndAuditsPrincipal(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	fullKey, _ := createKey(t, ts.URL, "ext-full", "full")

	// full 密钥写配置 → 201
	code, created := doAPIKey(t, http.MethodPost, ts.URL+"/admin/v1/configs", fullKey, true, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "fullkey.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n",
	})
	if code != http.StatusCreated {
		t.Fatalf("full 密钥写配置应 201，实际 %d：%v", code, created)
	}

	// 该写操作审计 operator 为 apikey:ext-full（认证身份，非手填）
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=config.create", nil)
	if code != http.StatusOK {
		t.Fatalf("查审计应 200，实际 %d", code)
	}
	items, _ := audits["items"].([]any)
	if len(items) == 0 {
		t.Fatal("应有 config.create 审计")
	}
	first, _ := items[0].(map[string]any)
	if op, _ := first["operator"].(string); op != "apikey:ext-full" {
		t.Fatalf("写审计 operator 应为 apikey:ext-full，实际 %q", op)
	}
}

// TestAPIKeyCreateAuditAndListNoSecret 创建审计 operator 为登录身份；列表不泄露明文/哈希。
func TestAPIKeyCreateAuditAndListNoSecret(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	_, _ = createKey(t, ts.URL, "audited-key", "readonly")

	// 创建审计 operator = 登录操作者（admin），target_type=apikey
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?action=apikey.create", nil)
	if code != http.StatusOK {
		t.Fatalf("查密钥审计应 200，实际 %d", code)
	}
	items, _ := audits["items"].([]any)
	if len(items) == 0 {
		t.Fatal("应有 apikey.create 审计")
	}
	first, _ := items[0].(map[string]any)
	if op, _ := first["operator"].(string); op != testAuthUser {
		t.Fatalf("创建密钥审计 operator 应为登录身份 %q，实际 %q", testAuthUser, op)
	}
	if detail, _ := first["detail"].(string); strings.Contains(detail, "bk_") {
		t.Fatalf("密钥审计 detail 绝不应含明文：%s", detail)
	}

	// 列表无任何明文 / 哈希字段
	code, list := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/api-keys", nil)
	if code != http.StatusOK {
		t.Fatalf("列密钥应 200，实际 %d", code)
	}
	keys, _ := list["items"].([]any)
	if len(keys) == 0 {
		t.Fatal("应有至少 1 把密钥")
	}
	k0, _ := keys[0].(map[string]any)
	if _, hasKey := k0["key"]; hasKey {
		t.Fatal("列表绝不应返回明文 key")
	}
	if _, hasHash := k0["keyHash"]; hasHash {
		t.Fatal("列表绝不应返回 keyHash")
	}
	if _, hasPrefix := k0["keyPrefix"]; !hasPrefix {
		t.Fatal("列表应含非机密 keyPrefix 供识别")
	}
}

// TestAPIKeyRevokeThenUnauthorized 吊销后该密钥访问一律 401；重置后旧明文 401、新明文可用。
func TestAPIKeyRevokeThenUnauthorized(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 吊销
	revKey, revID := createKey(t, ts.URL, "to-revoke", "readonly")
	if code, _ := doJSON(t, http.MethodDelete, ts.URL+"/admin/v1/api-keys/"+itoa(revID), nil); code != http.StatusOK {
		t.Fatalf("吊销密钥应 200，实际 %d", code)
	}
	if code, body := doAPIKey(t, http.MethodGet, ts.URL+"/admin/v1/instances", revKey, false, nil); code != http.StatusUnauthorized || body["code"] != "ADMIN_UNAUTHORIZED" {
		t.Fatalf("吊销后访问应 401 ADMIN_UNAUTHORIZED，实际 %d：%v", code, body)
	}

	// 重置（轮换明文）
	resetKey, resetID := createKey(t, ts.URL, "to-reset", "full")
	code, fresh := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/api-keys/"+itoa(resetID)+"/reset", nil)
	if code != http.StatusOK {
		t.Fatalf("重置密钥应 200，实际 %d：%v", code, fresh)
	}
	newKey, _ := fresh["key"].(string)
	if newKey == "" || newKey == resetKey {
		t.Fatal("重置应返回新明文且不同于旧明文")
	}
	if code, _ := doAPIKey(t, http.MethodGet, ts.URL+"/admin/v1/instances", resetKey, false, nil); code != http.StatusUnauthorized {
		t.Fatalf("重置后旧明文应 401，实际 %d", code)
	}
	if code, _ := doAPIKey(t, http.MethodGet, ts.URL+"/admin/v1/instances", newKey, false, nil); code != http.StatusOK {
		t.Fatalf("重置后新明文应 200，实际 %d", code)
	}
}

// TestAPIKeyUnknownRejected 未知 / 伪造密钥一律 401。
func TestAPIKeyUnknownRejected(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	if code, body := doAPIKey(t, http.MethodGet, ts.URL+"/admin/v1/instances", "bk_forged-nonexistent", false, nil); code != http.StatusUnauthorized || body["code"] != "ADMIN_UNAUTHORIZED" {
		t.Fatalf("未知密钥应 401 ADMIN_UNAUTHORIZED，实际 %d：%v", code, body)
	}
}
