//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestConfigDeleteRESTFlow 配置软删 REST 集成：建→软删→取详情 404→列表不含。
func TestConfigDeleteRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/configs"

	code, created := doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "del.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d：%v", code, created)
	}
	id := int(created["id"].(float64))
	itemURL := base + "/" + itoa(id)

	// 软删
	code, _ = doJSON(t, http.MethodDelete, itemURL+"?comment=clean", nil)
	if code != http.StatusOK {
		t.Fatalf("软删应 200，实际 %d", code)
	}

	// 删后取详情 → 404
	code, _ = doJSON(t, http.MethodGet, itemURL, nil)
	if code != http.StatusNotFound {
		t.Fatalf("删后取详情应 404，实际 %d", code)
	}

	// 列表不再含该配置
	code, list := doJSON(t, http.MethodGet, base+"?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", code)
	}
	for _, it := range asSlice(list["items"]) {
		m, _ := it.(map[string]any)
		if idv, _ := m["id"].(float64); int(idv) == id {
			t.Fatalf("列表不应含已删配置 id=%d", id)
		}
	}
}
