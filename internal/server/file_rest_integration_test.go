//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestFileRESTFlow 文件树托管 REST 集成（通道B，与配置对称）：建→取→发布→历史→取单版本→回滚→软删→404 全流程经 HTTP。
func TestFileRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/files"

	// 建（首版 v1，含 content 回显）
	code, created := doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "path": "plugins/Demo/config.yml",
		"scopeLevel": "global", "content": "a: 1\n",
	})
	if code != http.StatusCreated {
		t.Fatalf("建文件应 201，实际 %d：%v", code, created)
	}
	idF, ok := created["id"].(float64)
	if !ok {
		t.Fatalf("建文件响应缺 id：%v", created)
	}
	if v, _ := created["version"].(float64); v != 1 {
		t.Fatalf("首版应 version=1，实际 %v", created["version"])
	}
	itemURL := base + "/" + itoa(int(idF))

	// 取详情（含 content）
	code, got := doJSON(t, http.MethodGet, itemURL, nil)
	if code != http.StatusOK || got["content"] != "a: 1\n" {
		t.Fatalf("取文件应 200 且 content 正确，实际 %d：%v", code, got)
	}

	// 发布 v2
	code, pub := doJSON(t, http.MethodPut, itemURL, map[string]any{"content": "a: 2\n", "comment": "改值"})
	if code != http.StatusOK || pub["version"].(float64) != 2 {
		t.Fatalf("发布应 200 且 version=2，实际 %d：%v", code, pub)
	}

	// 历史 2 版
	code, revs := doJSON(t, http.MethodGet, itemURL+"/revisions", nil)
	if code != http.StatusOK {
		t.Fatalf("历史应 200，实际 %d", code)
	}
	if items, _ := revs["items"].([]any); len(items) != 2 {
		t.Fatalf("历史应有 2 版，实际 %v", revs["items"])
	}

	// 取单版本 v1（含 content）
	code, rev1 := doJSON(t, http.MethodGet, itemURL+"/revisions/1", nil)
	if code != http.StatusOK || rev1["content"] != "a: 1\n" {
		t.Fatalf("取 v1 应 200 且 content=a: 1，实际 %d：%v", code, rev1)
	}

	// 回滚到 v1 → 产生 v3
	code, rb := doJSON(t, http.MethodPost, itemURL+"/rollback", map[string]any{"toVersion": 1, "comment": "回滚"})
	if code != http.StatusOK || rb["version"].(float64) != 3 {
		t.Fatalf("回滚应 200 且 version=3，实际 %d：%v", code, rb)
	}

	// 软删
	code, _ = doJSON(t, http.MethodDelete, itemURL+"?comment=clean", nil)
	if code != http.StatusOK {
		t.Fatalf("软删应 200，实际 %d", code)
	}

	// 取不存在的文件 → 404
	code, _ = doJSON(t, http.MethodGet, base+"/999999", nil)
	if code != http.StatusNotFound {
		t.Fatalf("取不存在文件应 404，实际 %d", code)
	}

	// 非法 id → 400
	code, _ = doJSON(t, http.MethodGet, base+"/abc", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 id 应 400，实际 %d", code)
	}
}
