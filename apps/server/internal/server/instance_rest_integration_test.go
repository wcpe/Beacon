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

// register 经 agent 端点注册一台实例并返回状态码（供下线流程测试复用）。
func register(t *testing.T, baseURL, ns, serverID string) int {
	t.Helper()
	code, _ := doJSON(t, http.MethodPost, baseURL+"/beacon/v1/agent/register", map[string]any{
		"namespace": ns, "serverId": serverID, "role": "bukkit", "groupHint": "area1", "address": "10.0.0.9:25565",
	})
	return code
}

// TestInstanceActiveOfflineRESTFlow 主动下线 REST 集成（FR-49）：
// 注册 → 下线（落 DB 拒绝态 + 移出可用集）→ 重注册被 INSTANCE_OFFLINE_REJECTED(403) 拒 → 取消下线 → 可重新注册。
func TestInstanceActiveOfflineRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 注册成功并在册
	if code := register(t, ts.URL, "prod", "off-1"); code != http.StatusOK {
		t.Fatalf("首次注册应 200，实际 %d", code)
	}

	// 主动下线（带 reason）→ 200
	code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/off-1/offline?namespace=prod",
		map[string]any{"reason": "故障下架"})
	if code != http.StatusOK {
		t.Fatalf("下线应 200，实际 %d", code)
	}

	// 下线后列表不再含该实例（已移出可用集）
	code, list := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/instances?namespace=prod", nil)
	if code != http.StatusOK || len(asSlice(list["items"])) != 0 {
		t.Fatalf("下线后列表应为空，实际 %d %v", code, list["items"])
	}

	// 重注册被专门错误码拒绝 → 403 INSTANCE_OFFLINE_REJECTED
	code, rej := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", map[string]any{
		"namespace": "prod", "serverId": "off-1", "role": "bukkit", "groupHint": "area1", "address": "10.0.0.9:25565",
	})
	if code != http.StatusForbidden || rej["code"] != "INSTANCE_OFFLINE_REJECTED" {
		t.Fatalf("下线后重注册应 403 INSTANCE_OFFLINE_REJECTED，实际 %d：%v", code, rej)
	}

	// 取消未下线实例 → 404 OFFLINE_NOT_FOUND
	code, nf := doJSON(t, http.MethodDelete, ts.URL+"/admin/v1/instances/ghost/offline?namespace=prod", nil)
	if code != http.StatusNotFound || nf["code"] != "OFFLINE_NOT_FOUND" {
		t.Fatalf("取消不存在下线应 404 OFFLINE_NOT_FOUND，实际 %d：%v", code, nf)
	}

	// 取消下线 → 200
	code, _ = doJSON(t, http.MethodDelete, ts.URL+"/admin/v1/instances/off-1/offline?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("取消下线应 200，实际 %d", code)
	}

	// 取消下线后可重新注册 → 200
	if code := register(t, ts.URL, "prod", "off-1"); code != http.StatusOK {
		t.Fatalf("取消下线后重注册应 200，实际 %d", code)
	}
}
