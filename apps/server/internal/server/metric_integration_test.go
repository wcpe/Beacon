//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestMetricSummaryRequiresAdminAuth 验证聚合端点挂管理台鉴权：无令牌应 401。
func TestMetricSummaryRequiresAdminAuth(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 临时清空令牌，验证未鉴权被拒。
	saved := adminToken
	adminToken = ""
	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/metrics/summary?namespace=prod", nil)
	adminToken = saved
	if code != http.StatusUnauthorized {
		t.Fatalf("无令牌访问聚合端点应 401，实际 %d：%v", code, body)
	}
}

// TestMetricSummaryStructure 注册并上报后，聚合端点返回总人数、每服明细与平均值，且不含玩家名单字段。
func TestMetricSummaryStructure(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 经 agent 端注册两台子服。
	for _, s := range []map[string]any{
		{"namespace": "prod", "serverId": "m-s1", "role": "bukkit", "address": "10.0.0.1:25565"},
		{"namespace": "prod", "serverId": "m-s2", "role": "bukkit", "address": "10.0.0.2:25565"},
	} {
		if code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", s); code != http.StatusOK {
			t.Fatalf("注册 %v 应 200，实际 %d", s["serverId"], code)
		}
	}
	// 上报真实负载（含一个 CPU 不可用）。
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/report", map[string]any{
		"namespace": "prod", "serverId": "m-s1", "playerCount": 42, "tps": 19.9, "memUsed": 128, "memMax": 512, "cpuLoad": 0.4,
	}); code != http.StatusOK {
		t.Fatalf("上报 m-s1 应 200，实际 %d", code)
	}
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/report", map[string]any{
		"namespace": "prod", "serverId": "m-s2", "playerCount": 8, "tps": 20.0, "memUsed": 64, "memMax": 256, "cpuLoad": -1.0,
	}); code != http.StatusOK {
		t.Fatalf("上报 m-s2 应 200，实际 %d", code)
	}

	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/metrics/summary?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("聚合端点应 200，实际 %d：%v", code, body)
	}
	if total, _ := body["totalPlayers"].(float64); total != 50 {
		t.Fatalf("总人数应为 50，实际 %v", body["totalPlayers"])
	}
	if servers, _ := body["servers"].([]any); len(servers) != 2 {
		t.Fatalf("每服明细应有 2 条，实际 %v", body["servers"])
	}
	// CPU 平均只对可用样本（m-s1=0.4）求 → 0.4；可用样本数 1。
	if avg, _ := body["avgCpuLoad"].(float64); avg < 0.39 || avg > 0.41 {
		t.Fatalf("平均 CPU 应剔除 -1.0 后约 0.4，实际 %v", body["avgCpuLoad"])
	}
	if cnt, _ := body["cpuSampleCount"].(float64); cnt != 1 {
		t.Fatalf("CPU 可用样本数应为 1，实际 %v", body["cpuSampleCount"])
	}
	// 边界守护：响应体不得含任何玩家名单 / 身份字段。
	assertNoRosterFields(t, body)
}

// TestMetricSummaryBCDimension 注册 bc 代理并上报 proxy 指标后，聚合端点 bc 子对象反映 BC 维度（FR-34），
// 且不含玩家名单字段。
func TestMetricSummaryBCDimension(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 注册一台 bukkit 子服与一台 bc 代理。
	for _, s := range []map[string]any{
		{"namespace": "prod", "serverId": "bc-bk1", "role": "bukkit", "address": "10.0.1.1:25565"},
		{"namespace": "prod", "serverId": "bc-px1", "role": "bungee", "address": "10.0.1.9:25577"},
	} {
		if code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/register", s); code != http.StatusOK {
			t.Fatalf("注册 %v 应 200，实际 %d", s["serverId"], code)
		}
	}
	// bukkit 上报（无 proxy 段）。
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/report", map[string]any{
		"namespace": "prod", "serverId": "bc-bk1", "playerCount": 20, "tps": 19.9, "cpuLoad": 0.3,
	}); code != http.StatusOK {
		t.Fatalf("上报 bc-bk1 应 200，实际 %d", code)
	}
	// bc 上报含 proxy 子对象。
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/report", map[string]any{
		"namespace": "prod", "serverId": "bc-px1", "playerCount": 35, "tps": 0,
		"proxy": map[string]any{
			"onlineConnections": 35, "threadCount": 48, "uptimeMs": 600000,
			"backendUp": 1, "backendTotal": 1, "backendAvgLatencyMs": 9.0,
		},
	}); code != http.StatusOK {
		t.Fatalf("上报 bc-px1 应 200，实际 %d", code)
	}

	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/metrics/summary?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("聚合端点应 200，实际 %d：%v", code, body)
	}
	bc, ok := body["bc"].(map[string]any)
	if !ok {
		t.Fatalf("聚合响应应含 bc 子对象，实际 %v", body)
	}
	if pc, _ := bc["proxyCount"].(float64); pc != 1 {
		t.Fatalf("bc 代理数应为 1，实际 %v", bc["proxyCount"])
	}
	if conn, _ := bc["totalConnections"].(float64); conn != 35 {
		t.Fatalf("bc 连接合计应为 35，实际 %v", bc["totalConnections"])
	}
	if up, _ := bc["backendUp"].(float64); up != 1 {
		t.Fatalf("bc 可达后端应为 1，实际 %v", bc["backendUp"])
	}
	if lat, _ := bc["avgBackendLatencyMs"].(float64); lat < 8.9 || lat > 9.1 {
		t.Fatalf("bc 平均延迟应约 9.0，实际 %v", bc["avgBackendLatencyMs"])
	}
	// 边界守护：bc 子对象与顶层都不得含玩家名单 / 身份字段。
	assertNoRosterFields(t, body)
	assertNoRosterFields(t, bc)
}

// TestMetricTrendStructure 趋势端点返回时间序列点，按时间窗查询，且不含玩家名单字段。
func TestMetricTrendStructure(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 趋势读 metric_sample 表；空窗口应返回空序列（无样本）。
	code, body := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/metrics/trend?namespace=prod&window=1h", nil)
	if code != http.StatusOK {
		t.Fatalf("趋势端点应 200，实际 %d：%v", code, body)
	}
	points, ok := body["points"].([]any)
	if !ok {
		t.Fatalf("趋势响应应含 points 数组，实际 %v", body)
	}
	if len(points) != 0 {
		t.Fatalf("无样本时趋势应为空序列，实际 %d 点", len(points))
	}
	assertNoRosterFields(t, body)
}

// TestMetricTrendRequiresAdminAuth 趋势端点挂管理台鉴权：无令牌应 401。
func TestMetricTrendRequiresAdminAuth(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	saved := adminToken
	adminToken = ""
	code, _ := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/metrics/trend?namespace=prod&window=1h", nil)
	adminToken = saved
	if code != http.StatusUnauthorized {
		t.Fatalf("无令牌访问趋势端点应 401，实际 %d", code)
	}
}

// assertNoRosterFields 递归断言响应体不含任何玩家名单 / 身份相关字段（守 ADR-0023 边界：只指标不名单）。
func assertNoRosterFields(t *testing.T, body map[string]any) {
	t.Helper()
	banned := []string{"players", "roster", "playerNames", "names", "uuids", "playerList"}
	for _, k := range banned {
		if _, ok := body[k]; ok {
			t.Fatalf("响应体不得含玩家名单 / 身份字段 %q（越界），实际 %v", k, body)
		}
	}
}
