//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// createBatchConfig 建一个配置项并返回其 id（供批量用例铺底）。
func createBatchConfig(t *testing.T, base, dataID string) int {
	t.Helper()
	code, created := doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": dataID,
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置 %s 应 201，实际 %d：%v", dataID, code, created)
	}
	return int(created["id"].(float64))
}

// createBatchFile 建一个文件对象并返回其 id（供批量用例铺底）。
func createBatchFile(t *testing.T, base, path string) int {
	t.Helper()
	code, created := doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "bw", "path": path,
		"scopeLevel": "group", "content": "x: 1\n",
	})
	if code != http.StatusCreated {
		t.Fatalf("建文件 %s 应 201，实际 %d：%v", path, code, created)
	}
	return int(created["id"].(float64))
}

// TestConfigBatchDeleteRESTFlow 配置批量软删 REST 集成：建多条 → 一次批删 → 列表均不含。
func TestConfigBatchDeleteRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/configs"

	id1 := createBatchConfig(t, base, "bd1.yml")
	id2 := createBatchConfig(t, base, "bd2.yml")

	code, resp := doJSON(t, http.MethodPost, base+"/batch", map[string]any{
		"action": "delete", "ids": []int{id1, id2},
	})
	if code != http.StatusOK {
		t.Fatalf("批量软删应 200，实际 %d：%v", code, resp)
	}
	if int(resp["count"].(float64)) != 2 {
		t.Fatalf("批量软删 count 应 2，实际 %v", resp["count"])
	}

	// 两条均删后取详情 → 404
	for _, id := range []int{id1, id2} {
		code, _ = doJSON(t, http.MethodGet, base+"/"+itoa(id), nil)
		if code != http.StatusNotFound {
			t.Fatalf("删后取详情 id=%d 应 404，实际 %d", id, code)
		}
	}
}

// TestConfigBatchDisableEnableRESTFlow 配置批量禁用 / 启用 REST 集成：批禁 enabled=false → 批启 enabled=true。
func TestConfigBatchDisableEnableRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/configs"

	id1 := createBatchConfig(t, base, "be1.yml")
	id2 := createBatchConfig(t, base, "be2.yml")

	// 批量禁用
	code, _ := doJSON(t, http.MethodPost, base+"/batch", map[string]any{
		"action": "disable", "ids": []int{id1, id2},
	})
	if code != http.StatusOK {
		t.Fatalf("批量禁用应 200，实际 %d", code)
	}
	for _, id := range []int{id1, id2} {
		_, item := doJSON(t, http.MethodGet, base+"/"+itoa(id), nil)
		if item["enabled"].(bool) {
			t.Fatalf("禁用后 id=%d enabled 应 false", id)
		}
	}

	// 批量启用
	code, _ = doJSON(t, http.MethodPost, base+"/batch", map[string]any{
		"action": "enable", "ids": []int{id1, id2},
	})
	if code != http.StatusOK {
		t.Fatalf("批量启用应 200，实际 %d", code)
	}
	for _, id := range []int{id1, id2} {
		_, item := doJSON(t, http.MethodGet, base+"/"+itoa(id), nil)
		if !item["enabled"].(bool) {
			t.Fatalf("启用后 id=%d enabled 应 true", id)
		}
	}
}

// TestConfigBatchInvalidRequests 配置批量端点入参校验：空 ids / 非法 action 一律 400。
func TestConfigBatchInvalidRequests(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	url := ts.URL + "/admin/v1/configs/batch"

	// 空 ids → 400
	code, _ := doJSON(t, http.MethodPost, url, map[string]any{"action": "delete", "ids": []int{}})
	if code != http.StatusBadRequest {
		t.Fatalf("空 ids 应 400，实际 %d", code)
	}
	// 非法 action → 400
	code, _ = doJSON(t, http.MethodPost, url, map[string]any{"action": "frobnicate", "ids": []int{1}})
	if code != http.StatusBadRequest {
		t.Fatalf("非法 action 应 400，实际 %d", code)
	}
}

// TestConfigBatchDeleteAtomicRollback 配置批量软删原子性：批中含不存在 id 时整批回滚，已存在项仍在。
func TestConfigBatchDeleteAtomicRollback(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/configs"

	id1 := createBatchConfig(t, base, "atomic.yml")

	// 批中混入不存在 id（999999）→ 整批 404、id1 不应被删
	code, _ := doJSON(t, http.MethodPost, base+"/batch", map[string]any{
		"action": "delete", "ids": []int{id1, 999999},
	})
	if code != http.StatusNotFound {
		t.Fatalf("批中含不存在 id 应 404，实际 %d", code)
	}
	code, _ = doJSON(t, http.MethodGet, base+"/"+itoa(id1), nil)
	if code != http.StatusOK {
		t.Fatalf("整批回滚后 id1 应仍在（200），实际 %d", code)
	}
}

// TestFileBatchDeleteRESTFlow 文件批量软删 REST 集成：建多条 → 一次批删 → 详情均 404。
func TestFileBatchDeleteRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/files"

	id1 := createBatchFile(t, base, "a/x.yml")
	id2 := createBatchFile(t, base, "a/y.yml")

	code, resp := doJSON(t, http.MethodPost, base+"/batch", map[string]any{
		"action": "delete", "ids": []int{id1, id2},
	})
	if code != http.StatusOK {
		t.Fatalf("文件批量软删应 200，实际 %d：%v", code, resp)
	}
	for _, id := range []int{id1, id2} {
		code, _ = doJSON(t, http.MethodGet, base+"/"+itoa(id), nil)
		if code != http.StatusNotFound {
			t.Fatalf("删后取文件详情 id=%d 应 404，实际 %d", id, code)
		}
	}
}

// TestFileBatchDisableEnableRESTFlow 文件批量禁用 / 启用 REST 集成。
func TestFileBatchDisableEnableRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/files"

	id1 := createBatchFile(t, base, "b/x.yml")

	code, _ := doJSON(t, http.MethodPost, base+"/batch", map[string]any{
		"action": "disable", "ids": []int{id1},
	})
	if code != http.StatusOK {
		t.Fatalf("文件批量禁用应 200，实际 %d", code)
	}
	_, item := doJSON(t, http.MethodGet, base+"/"+itoa(id1), nil)
	if item["enabled"].(bool) {
		t.Fatalf("禁用后文件 id=%d enabled 应 false", id1)
	}

	code, _ = doJSON(t, http.MethodPost, base+"/batch", map[string]any{
		"action": "enable", "ids": []int{id1},
	})
	if code != http.StatusOK {
		t.Fatalf("文件批量启用应 200，实际 %d", code)
	}
	_, item = doJSON(t, http.MethodGet, base+"/"+itoa(id1), nil)
	if !item["enabled"].(bool) {
		t.Fatalf("启用后文件 id=%d enabled 应 true", id1)
	}
}
