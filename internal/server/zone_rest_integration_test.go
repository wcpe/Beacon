//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestZoneAssignmentRESTFlow zone 指派 REST 集成：指派→列表→汇总→取消→列表空，经 HTTP。
func TestZoneAssignmentRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	assignURL := ts.URL + "/admin/v1/zones/assignments"

	// 指派 z-s1 → area1/zoneA
	code, a := doJSON(t, http.MethodPut, assignURL, map[string]any{
		"namespace": "prod", "serverId": "z-s1", "group": "area1", "zone": "zoneA", "note": "n1",
	})
	if code != http.StatusOK || a["zone"] != "zoneA" {
		t.Fatalf("指派应 200 且 zone=zoneA，实际 %d：%v", code, a)
	}

	// 列表含该指派
	code, list := doJSON(t, http.MethodGet, assignURL+"?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", code)
	}
	if items, _ := list["items"].([]any); len(items) != 1 {
		t.Fatalf("应有 1 条指派，实际 %v", list["items"])
	}

	// 汇总含 area1/zoneA
	code, sum := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/zones?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("汇总应 200，实际 %d", code)
	}
	found := false
	for _, it := range asSlice(sum["items"]) {
		m, _ := it.(map[string]any)
		if m["group"] == "area1" && m["zone"] == "zoneA" {
			found = true
			if c, _ := m["serverCount"].(float64); c < 1 {
				t.Fatalf("area1/zoneA serverCount 应 >=1，实际 %v", m["serverCount"])
			}
		}
	}
	if !found {
		t.Fatalf("汇总应含 area1/zoneA，实际 %v", sum["items"])
	}

	// 取消指派
	code, _ = doJSON(t, http.MethodDelete, assignURL+"?namespace=prod&serverId=z-s1", nil)
	if code != http.StatusOK {
		t.Fatalf("取消指派应 200，实际 %d", code)
	}

	// 取消后列表为空
	code, list2 := doJSON(t, http.MethodGet, assignURL+"?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("取消后列表应 200，实际 %d", code)
	}
	if items, _ := list2["items"].([]any); len(items) != 0 {
		t.Fatalf("取消后应无指派，实际 %v", list2["items"])
	}
}

// asSlice 把 any 安全转为 []any（nil 返回空）。
func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
