//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestConfigTimelineRESTFlow per-server 有效配置变更时间线 REST 集成（FR-80）：
// 指派 lobby-1→area1/zoneA，建四层配置并对 global 项再发一版 → 查 config-timeline 应含 5 条版本、
// 按时间倒序、各条带 scope 标注；未指派 server 只含 global 层历史；缺 namespace → 400。
func TestConfigTimelineRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	cfgBase := ts.URL + "/admin/v1/configs"

	// 指派 lobby-1 → area1/zoneA，使其覆盖链含全部四层
	code, _ := doJSON(t, http.MethodPut, ts.URL+"/admin/v1/zones/assignments", map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "group": "area1", "zone": "zoneA",
	})
	if code != http.StatusOK {
		t.Fatalf("指派应 200，实际 %d", code)
	}

	// 建四层配置（同一 dataId 跨层）
	mk := func(group, scope, target, content string) int {
		code, created := doJSON(t, http.MethodPost, cfgBase, map[string]any{
			"namespace": "prod", "group": group, "dataId": "mysql.yml",
			"scopeLevel": scope, "scopeTarget": target, "format": "yaml", "content": content,
		})
		if code != http.StatusCreated {
			t.Fatalf("建 %s 层应 201，实际 %d：%v", scope, code, created)
		}
		return int(created["id"].(float64))
	}
	globalID := mk("__GLOBAL__", "global", "", "pool: 1\n")
	mk("area1", "group", "", "pool: 2\n")
	mk("area1", "zone", "zoneA", "nest:\n  a: 1\n")
	mk("area1", "server", "lobby-1", "extra: y\n")

	// global 项再发一版 → 该项两条历史
	code, _ = doJSON(t, http.MethodPut, cfgBase+"/"+itoa(globalID), map[string]any{
		"content": "pool: 9\n", "comment": "调大池",
	})
	if code != http.StatusOK {
		t.Fatalf("发布新版本应 200，实际 %d", code)
	}

	// 查 lobby-1 时间线：四层各 1 首发 + global 多 1 = 5 条
	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/lobby-1/config-timeline?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("config-timeline 应 200，实际 %d：%v", code, body)
	}
	if body["group"] != "area1" || body["zone"] != "zoneA" {
		t.Fatalf("时间线归属应 area1/zoneA，实际 group=%v zone=%v", body["group"], body["zone"])
	}
	items := asSlice(body["items"])
	if len(items) != 5 {
		t.Fatalf("时间线应含 5 条，实际 %d", len(items))
	}
	// 按时间倒序：createdAt 非递增（同刻容忍相等）；每条带非空 scopeLevel 与 dataId
	var prev string
	globalVersions := 0
	for i, raw := range items {
		m := raw.(map[string]any)
		if m["scopeLevel"].(string) == "" || m["dataId"].(string) != "mysql.yml" {
			t.Fatalf("第 %d 条缺 scope/dataId：%v", i, m)
		}
		cur := m["createdAt"].(string)
		if prev != "" && cur > prev {
			t.Fatalf("时间线未按时间倒序：第 %d 条 createdAt 晚于前一条", i)
		}
		prev = cur
		if int(m["configItemId"].(float64)) == globalID {
			globalVersions++
		}
	}
	if globalVersions != 2 {
		t.Fatalf("global 项应有 2 条版本，实际 %d", globalVersions)
	}

	// 未指派 server → 只含 global 层历史（2 条：v1 + v2）
	code, body = doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/ghost-9/config-timeline?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("未指派 server 时间线应 200，实际 %d", code)
	}
	ghostItems := asSlice(body["items"])
	if len(ghostItems) != 2 {
		t.Fatalf("未指派 server 应只含 global 项 2 条历史，实际 %d", len(ghostItems))
	}
	for _, raw := range ghostItems {
		if int(raw.(map[string]any)["configItemId"].(float64)) != globalID {
			t.Fatalf("未指派 server 时间线条目应全属 global 项")
		}
	}

	// 缺 namespace → 400
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/lobby-1/config-timeline", nil); code != http.StatusBadRequest {
		t.Fatalf("缺 namespace 应 400，实际 %d", code)
	}
}
