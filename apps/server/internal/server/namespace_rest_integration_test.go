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
