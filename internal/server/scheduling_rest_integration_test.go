//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestSchedulingRESTFlow 流量调度 REST 集成（FR-10）：落位候选 + drain/undrain 经 HTTP；含 undrain 404、落位缺 zone 400。
func TestSchedulingRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	drainURL := ts.URL + "/admin/v1/scheduling/drains"
	placeURL := ts.URL + "/admin/v1/scheduling/placement"

	// 先指派 zone（DB 权威），再注册实例 → 注册时解析出 ResolvedZone=zoneA，成为落位候选
	code, _ := doJSON(t, http.MethodPut, ts.URL+"/admin/v1/zones/assignments", map[string]any{
		"namespace": "prod", "serverId": "sched-1", "group": "area1", "zone": "zoneA",
	})
	if code != http.StatusOK {
		t.Fatalf("指派 zone 应 200，实际 %d", code)
	}
	code, _ = doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", map[string]any{
		"namespace": "prod", "serverId": "sched-1", "role": "bukkit", "group": "area1", "address": "10.0.0.8:25565",
	})
	if code != http.StatusOK {
		t.Fatalf("注册应 200，实际 %d", code)
	}

	// 落位候选含 sched-1（在线、未 drain）
	code, place := doJSON(t, http.MethodGet, placeURL+"?namespace=prod&zone=zoneA", nil)
	if code != http.StatusOK {
		t.Fatalf("落位查询应 200，实际 %d", code)
	}
	if !hasCandidate(place, "sched-1") {
		t.Fatalf("落位候选应含 sched-1，实际 %v", place["candidates"])
	}

	// drain sched-1 → 落位剔除
	code, d := doJSON(t, http.MethodPut, drainURL, map[string]any{
		"namespace": "prod", "serverId": "sched-1", "reason": "维护",
	})
	if code != http.StatusOK || d["serverId"] != "sched-1" {
		t.Fatalf("drain 应 200 且回显 serverId，实际 %d：%v", code, d)
	}
	code, place2 := doJSON(t, http.MethodGet, placeURL+"?namespace=prod&zone=zoneA", nil)
	if code != http.StatusOK || hasCandidate(place2, "sched-1") {
		t.Fatalf("drain 后落位不应含 sched-1，实际 %d %v", code, place2["candidates"])
	}

	// drain 列表含 sched-1
	code, dl := doJSON(t, http.MethodGet, drainURL+"?namespace=prod", nil)
	if code != http.StatusOK || len(asSlice(dl["items"])) != 1 {
		t.Fatalf("drain 列表应 1 条，实际 %d %v", code, dl["items"])
	}

	// undrain → 落位恢复
	code, _ = doJSON(t, http.MethodDelete, drainURL+"?namespace=prod&serverId=sched-1", nil)
	if code != http.StatusOK {
		t.Fatalf("undrain 应 200，实际 %d", code)
	}
	code, place3 := doJSON(t, http.MethodGet, placeURL+"?namespace=prod&zone=zoneA", nil)
	if code != http.StatusOK || !hasCandidate(place3, "sched-1") {
		t.Fatalf("undrain 后落位应恢复 sched-1，实际 %d %v", code, place3["candidates"])
	}

	// undrain 不存在 → 404
	code, _ = doJSON(t, http.MethodDelete, drainURL+"?namespace=prod&serverId=ghost", nil)
	if code != http.StatusNotFound {
		t.Fatalf("undrain 不存在应 404，实际 %d", code)
	}

	// 落位缺 zone → 400
	code, _ = doJSON(t, http.MethodGet, placeURL+"?namespace=prod", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("落位缺 zone 应 400，实际 %d", code)
	}
}

// hasCandidate 判断落位响应的 candidates 是否含某 serverId。
func hasCandidate(resp map[string]any, serverID string) bool {
	cands, _ := resp["candidates"].([]any)
	for _, c := range cands {
		if m, _ := c.(map[string]any); m["serverId"] == serverID {
			return true
		}
	}
	return false
}
