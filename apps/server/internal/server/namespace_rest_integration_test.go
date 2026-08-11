//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// TestNamespaceCreateRESTFlow 环境 REST 集成：建环境→列表含新环境→重复建冲突。
func TestNamespaceCreateRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/namespaces"

	// 建新环境（回显 code/name）
	code, created := doJSON(t, http.MethodPost, base, map[string]any{"code": "staging", "name": "预发布"})
	if code != http.StatusCreated || created["code"] != "staging" || created["name"] != "预发布" {
		t.Fatalf("建环境应 201 且回显字段，实际 %d：%v", code, created)
	}

	// 列表含新环境
	code, list := doJSON(t, http.MethodGet, base, nil)
	if code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", code)
	}
	has := false
	for _, it := range asSlice(list["items"]) {
		if m, _ := it.(map[string]any); m["code"] == "staging" {
			has = true
		}
	}
	if !has {
		t.Fatalf("列表应含 staging，实际 %v", list["items"])
	}

	// 重复建同 code → 冲突（4xx）
	code, _ = doJSON(t, http.MethodPost, base, map[string]any{"code": "staging", "name": "again"})
	if code < 400 {
		t.Fatalf("重复建环境应失败（4xx），实际 %d", code)
	}
}

// TestLegacyNamespaceDeleteMigrated 锁定旧 V1/V2 删除端点已迁移：full 角色收到 410 且不删除、不审计。
func TestLegacyNamespaceDeleteMigrated(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	v1Code := "delete-migrated-v1"
	code, created := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/namespaces", map[string]any{
		"code": v1Code, "name": "旧删除迁移 V1",
	})
	if code != http.StatusCreated {
		t.Fatalf("创建 V1 环境应 201，实际 %d：%v", code, created)
	}
	code, body := doJSON(t, http.MethodDelete, ts.URL+"/admin/v1/namespaces/"+v1Code, nil)
	if code != http.StatusGone || body["code"] != "namespace_delete_migrated" {
		t.Fatalf("V1 旧删除应 410 namespace_delete_migrated，实际 %d：%v", code, body)
	}
	assertNamespaceListed(t, ts.URL, v1Code)

	code, created = doJSON(t, http.MethodPost, ts.URL+"/admin/v2/namespaces", map[string]any{
		"name": "旧删除迁移 V2",
	})
	if code != http.StatusCreated {
		t.Fatalf("创建 V2 环境应 201，实际 %d：%v", code, created)
	}
	id, ok := created["id"].(float64)
	if !ok || id == 0 {
		t.Fatalf("创建 V2 环境应返回 id，实际 %v", created)
	}
	code, body = doJSON(t, http.MethodDelete, ts.URL+"/admin/v2/namespaces/"+itoa(int(id)), nil)
	if code != http.StatusGone || body["code"] != "namespace_delete_migrated" {
		t.Fatalf("V2 旧删除应 410 namespace_delete_migrated，实际 %d：%v", code, body)
	}
	assertV2NamespaceListed(t, ts.URL, id)

	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?action=namespace.delete", nil)
	if code != http.StatusOK {
		t.Fatalf("查询删除审计应 200，实际 %d", code)
	}
	if total, _ := audits["total"].(float64); total != 0 {
		t.Fatalf("旧删除迁移不应产生审计，实际 %v", audits["items"])
	}
}

func TestLegacyNamespaceDeleteReadonlyDenied(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	readonlyKey, _ := createKey(t, ts, "namespace-delete-readonly", "readonly")

	for _, path := range []string{
		"/admin/v1/namespaces/any-code",
		"/admin/v2/namespaces/1",
	} {
		code, body := doAPIKey(t, http.MethodDelete, ts.URL+path, readonlyKey, false, nil)
		if code != http.StatusForbidden || body["code"] != "FORBIDDEN" {
			t.Fatalf("只读密钥 DELETE %s 应 403 FORBIDDEN，实际 %d：%v", path, code, body)
		}
	}
}

func assertNamespaceListed(t *testing.T, baseURL, wantCode string) {
	t.Helper()
	code, body := doJSON(t, http.MethodGet, baseURL+"/admin/v1/namespaces", nil)
	if code != http.StatusOK {
		t.Fatalf("查询 V1 环境应 200，实际 %d", code)
	}
	for _, item := range asSlice(body["items"]) {
		if namespace, _ := item.(map[string]any); namespace["code"] == wantCode {
			return
		}
	}
	t.Fatalf("旧删除后 V1 环境 %q 应仍存在，实际 %v", wantCode, body["items"])
}

func assertV2NamespaceListed(t *testing.T, baseURL string, wantID float64) {
	t.Helper()
	code, body := doJSON(t, http.MethodGet, baseURL+"/admin/v2/namespaces", nil)
	if code != http.StatusOK {
		t.Fatalf("查询 V2 环境应 200，实际 %d", code)
	}
	for _, item := range asSlice(body["items"]) {
		if namespace, _ := item.(map[string]any); namespace["id"] == wantID {
			return
		}
	}
	t.Fatalf("旧删除后 V2 环境 %v 应仍存在，实际 %v", wantID, body["items"])
}

// TestNamespaceCreateAudited 守护 FR-7/FR-30：建环境必产一条 namespace.create 审计，
// operator 为认证身份、detail 不含敏感数据、来源 IP 入库。
func TestNamespaceCreateAudited(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	const wantIP = "203.0.113.9"

	// 经 X-Forwarded-For 指定来源 IP 建一个新环境。
	raw, _ := json.Marshal(map[string]any{"code": "audited-ns", "name": "被审计环境"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/v1/namespaces", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", wantIP)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("建环境请求失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建环境应 201，实际 %d", resp.StatusCode)
	}

	// 应查到一条 namespace.create 审计。
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=audited-ns&action=namespace.create", nil)
	if code != http.StatusOK {
		t.Fatalf("查审计应 200，实际 %d", code)
	}
	items := asSlice(audits["items"])
	if len(items) == 0 {
		t.Fatal("应有 namespace.create 审计，实际无")
	}
	first, _ := items[0].(map[string]any)
	if op, _ := first["operator"].(string); op != testAuthUser {
		t.Fatalf("审计 operator 应为认证身份 %q，实际 %q", testAuthUser, op)
	}
	if tt, _ := first["targetType"].(string); tt != "namespace" {
		t.Fatalf("审计 targetType 应为 namespace，实际 %q", tt)
	}
	if got, _ := first["clientIp"].(string); got != wantIP {
		t.Fatalf("审计 clientIp 应为 %q，实际 %q", wantIP, got)
	}
}
