//go:build integration

package server_test

import (
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
