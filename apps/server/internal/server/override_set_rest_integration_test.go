//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestOverrideSetRESTFlow 三方覆盖集 REST 集成（FR-15）：建→取→dry-run→发布→历史→回滚→列表→软删，含冲突/404/非法目标根。
func TestOverrideSetRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/override-sets"

	// 建（首版 v1）
	code, created := doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "name": "demo-set",
		"scopeLevel": "global", "targetRoot": "plugins/DeluxeMenus",
		"reloadCommand": "deluxemenus reload", "comment": "init",
	})
	if code != http.StatusCreated {
		t.Fatalf("建覆盖集应 201，实际 %d：%v", code, created)
	}
	idF, ok := created["id"].(float64)
	if !ok {
		t.Fatalf("建覆盖集响应缺 id：%v", created)
	}
	if v, _ := created["version"].(float64); v != 1 {
		t.Fatalf("首版应 version=1，实际 %v", created["version"])
	}
	if created["targetRoot"] != "plugins/DeluxeMenus" {
		t.Fatalf("targetRoot 回显错误：%v", created)
	}
	itemURL := base + "/" + itoa(int(idF))

	// 取详情
	code, got := doJSON(t, http.MethodGet, itemURL, nil)
	if code != http.StatusOK || got["name"] != "demo-set" {
		t.Fatalf("取覆盖集应 200 且 name=demo-set，实际 %d：%v", code, got)
	}

	// dry-run：无成员，命令首 token 为 deluxemenus（白名单由 agent 本地把关，控制面只展示）
	code, dry := doJSON(t, http.MethodGet, itemURL+"/dry-run", nil)
	if code != http.StatusOK {
		t.Fatalf("dry-run 应 200，实际 %d：%v", code, dry)
	}
	if dry["commandFirstToken"] != "deluxemenus" {
		t.Fatalf("dry-run commandFirstToken 应为 deluxemenus，实际 %v", dry["commandFirstToken"])
	}
	if mp := asSlice(dry["memberPaths"]); len(mp) != 0 {
		t.Fatalf("无成员时 memberPaths 应为空，实际 %v", dry["memberPaths"])
	}

	// 发布 v2（命令置空：只覆盖文件不下发命令）
	code, pub := doJSON(t, http.MethodPut, itemURL, map[string]any{
		"targetRoot": "plugins/DeluxeMenus", "reloadCommand": "", "comment": "去命令",
	})
	if code != http.StatusOK || pub["version"].(float64) != 2 {
		t.Fatalf("发布应 200 且 version=2，实际 %d：%v", code, pub)
	}

	// 历史 2 版
	code, revs := doJSON(t, http.MethodGet, itemURL+"/revisions", nil)
	if code != http.StatusOK || len(asSlice(revs["items"])) != 2 {
		t.Fatalf("历史应 2 版，实际 %d %v", code, revs["items"])
	}

	// 回滚到 v1 → v3
	code, rb := doJSON(t, http.MethodPost, itemURL+"/rollback", map[string]any{"toVersion": 1, "comment": "回滚"})
	if code != http.StatusOK || rb["version"].(float64) != 3 {
		t.Fatalf("回滚应 200 且 version=3，实际 %d：%v", code, rb)
	}

	// 列表含该集
	code, list := doJSON(t, http.MethodGet, base+"?namespace=prod", nil)
	if code != http.StatusOK || len(asSlice(list["items"])) != 1 {
		t.Fatalf("列表应 1 条，实际 %d %v", code, list["items"])
	}

	// 重复建同 identity → 409 冲突
	code, _ = doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "name": "demo-set",
		"scopeLevel": "global", "targetRoot": "plugins/DeluxeMenus",
	})
	if code != http.StatusConflict {
		t.Fatalf("重复建覆盖集应 409，实际 %d", code)
	}

	// 非法目标根（不在 plugins/ 内）→ 拒绝（4xx）
	code, _ = doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "name": "bad-root",
		"scopeLevel": "global", "targetRoot": "etc/passwd",
	})
	if code < 400 || code >= 500 {
		t.Fatalf("非法 targetRoot 建集应 4xx 拒绝，实际 %d", code)
	}

	// 软删
	code, _ = doJSON(t, http.MethodDelete, itemURL+"?comment=clean", nil)
	if code != http.StatusOK {
		t.Fatalf("软删应 200，实际 %d", code)
	}

	// 取不存在 → 404
	code, _ = doJSON(t, http.MethodGet, base+"/999999", nil)
	if code != http.StatusNotFound {
		t.Fatalf("取不存在覆盖集应 404，实际 %d", code)
	}
}
