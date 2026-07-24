//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestAgentRESTFlow REST 集成：注册→心跳→重复守卫→发现→指派回填→下线。
func TestAgentRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	reg := ts.URL + "/beacon/v1/agent/register"

	// 注册（未指派 → resolvedZone null、assigned false）
	code, res := doJSON(t, http.MethodPost, reg, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "role": "bukkit",
		"groupHint": "area1", "address": "10.0.0.1:25565", "capacity": 200, "weight": 100,
	})
	if code != http.StatusOK {
		t.Fatalf("注册应 200，实际 %d：%v", code, res)
	}
	if res["resolvedZone"] != nil || res["assigned"] != false {
		t.Fatalf("未指派应 resolvedZone=null assigned=false，实际 %v", res)
	}

	// 心跳
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/heartbeat", map[string]any{
		"namespace": "prod", "serverId": "lobby-1"}); code != http.StatusOK {
		t.Fatalf("心跳应 200，实际 %d", code)
	}

	// 重复 serverId 守卫：仍新鲜的不同地址 → 409
	code, dup := doJSON(t, http.MethodPost, reg, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "address": "10.0.0.2:25565"})
	if code != http.StatusConflict || dup["code"] != "DUPLICATE_SERVER_ID" {
		t.Fatalf("重复 serverId 应 409 DUPLICATE_SERVER_ID，实际 %d：%v", code, dup)
	}

	// 同地址重连幂等
	if code, _ := doJSON(t, http.MethodPost, reg, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "address": "10.0.0.1:25565"}); code != http.StatusOK {
		t.Fatalf("同地址重连应 200，实际 %d", code)
	}

	// 心跳未注册 → 404
	if code, nr := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/heartbeat", map[string]any{
		"namespace": "prod", "serverId": "ghost"}); code != http.StatusNotFound || nr["code"] != "NOT_REGISTERED" {
		t.Fatalf("未注册心跳应 404 NOT_REGISTERED，实际 %d：%v", code, nr)
	}

	// 发现：在线实例含 lobby-1
	code, disc := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/discovery?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("发现应 200，实际 %d", code)
	}
	if insts, _ := disc["instances"].([]any); len(insts) != 1 {
		t.Fatalf("发现应返回 1 个在线实例，实际 %v", disc["instances"])
	}

	// 指派 zone 后重新注册 → 回填 zoneA、assigned true
	if code, _ := doJSON(t, http.MethodPut, ts.URL+"/admin/v1/zones/assignments", map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "group": "area1", "zone": "zoneA", "operator": "admin",
	}); code != http.StatusOK {
		t.Fatalf("指派应 200，实际 %d", code)
	}
	code, re := doJSON(t, http.MethodPost, reg, map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "address": "10.0.0.1:25565"})
	if code != http.StatusOK || re["resolvedZone"] != "zoneA" || re["assigned"] != true {
		t.Fatalf("指派后注册应回填 zoneA/assigned，实际 %d：%v", code, re)
	}

	// 手动下线 → 之后发现为空
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/lobby-1/offline?namespace=prod&operator=admin", nil); code != http.StatusOK {
		t.Fatalf("下线应 200，实际 %d", code)
	}
	code, disc2 := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/discovery?namespace=prod", nil)
	if insts, _ := disc2["instances"].([]any); code != http.StatusOK || len(insts) != 0 {
		t.Fatalf("下线后发现应为空，实际 %v", disc2["instances"])
	}
}

// TestEffectiveRESTFlow REST 集成：首拉 200、无变更 304、未注册 404。
func TestEffectiveRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 建一个 global 配置
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "app.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "admin",
	}); code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d", code)
	}
	// 注册 s1
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", map[string]any{
		"namespace": "prod", "serverId": "s1", "address": "10.0.0.1:1"}); code != http.StatusOK {
		t.Fatalf("注册应 200，实际 %d", code)
	}

	// 首拉（md5 空）→ 200 带 items 与 md5
	code, eff := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/config/effective?namespace=prod&serverId=s1&md5=", nil)
	if code != http.StatusOK {
		t.Fatalf("首拉应 200，实际 %d", code)
	}
	md5, _ := eff["md5"].(string)
	if items, _ := eff["items"].([]any); len(items) != 1 || md5 == "" {
		t.Fatalf("首拉应返回 1 个 dataId 与 md5，实际 %v", eff)
	}

	// 带当前 md5 + 短超时 → 304
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/config/effective?namespace=prod&serverId=s1&md5="+md5+"&timeoutMs=150", nil); code != http.StatusNotModified {
		t.Fatalf("无变更应 304，实际 %d", code)
	}

	// 未注册 → 404 NOT_REGISTERED
	code, nr := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/config/effective?namespace=prod&serverId=ghost&md5=", nil)
	if code != http.StatusNotFound || nr["code"] != "NOT_REGISTERED" {
		t.Fatalf("未注册应 404 NOT_REGISTERED，实际 %d：%v", code, nr)
	}
}

// TestAgentTokenGuard token 中间件：错误 token → 401。
func TestAgentTokenGuard(t *testing.T) {
	ts := newTestServerWithToken(t, "secret-token")
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/beacon/v1/agent/discovery?namespace=prod", nil)
	req.Header.Set("X-Beacon-Token", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误 token 应 401，实际 %d", resp.StatusCode)
	}
}
