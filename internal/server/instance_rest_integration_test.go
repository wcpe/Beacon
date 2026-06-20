//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestInstanceQueryRESTFlow 实例查询 REST 集成：agent 注册后 List 命中、role 过滤、Get 详情、不存在 404、缺 namespace 400。
func TestInstanceQueryRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// agent 注册一个实例（写内存注册表）
	code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", map[string]any{
		"namespace": "prod", "serverId": "inst-1", "role": "bukkit", "group": "area1", "address": "10.0.0.5:25565",
	})
	if code != http.StatusOK {
		t.Fatalf("注册应 200，实际 %d", code)
	}

	// List 命中且字段正确
	code, list := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", code)
	}
	items := asSlice(list["items"])
	if len(items) != 1 {
		t.Fatalf("应有 1 个实例，实际 %v", list["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["serverId"] != "inst-1" || first["status"] != "online" {
		t.Fatalf("实例字段错误：%v", first)
	}

	// role=bukkit 命中、role=bungee 为空
	code, byRole := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances?namespace=prod&role=bukkit", nil)
	if code != http.StatusOK || len(asSlice(byRole["items"])) != 1 {
		t.Fatalf("role=bukkit 应命中 1 个，实际 %d %v", code, byRole["items"])
	}
	code, byRole2 := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances?namespace=prod&role=bungee", nil)
	if code != http.StatusOK || len(asSlice(byRole2["items"])) != 0 {
		t.Fatalf("role=bungee 应为空，实际 %d %v", code, byRole2["items"])
	}

	// Get 单实例
	code, got := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/inst-1?namespace=prod", nil)
	if code != http.StatusOK || got["serverId"] != "inst-1" {
		t.Fatalf("取实例应 200 且 serverId=inst-1，实际 %d：%v", code, got)
	}

	// Get 不存在 → 404
	code, _ = doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/nope?namespace=prod", nil)
	if code != http.StatusNotFound {
		t.Fatalf("取不存在实例应 404，实际 %d", code)
	}

	// Get 缺 namespace → 400
	code, _ = doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances/inst-1", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("缺 namespace 应 400，实际 %d", code)
	}
}
