//go:build integration

package server_test

import (
	"net/http"
	"sort"
	"testing"
)

// regOnline 经 agent 端点注册一台在线实例（groupHint 决定未指派时的回退大区）。
func regOnline(t *testing.T, baseURL, ns, serverID, groupHint string) {
	t.Helper()
	code, _ := doJSON(t, http.MethodPost, baseURL+"/beacon/v1/agent/register", map[string]any{
		"namespace": ns, "serverId": serverID, "role": "bukkit", "groupHint": groupHint, "address": serverID + ":25565",
	})
	if code != http.StatusOK {
		t.Fatalf("注册 %s 应 200，实际 %d", serverID, code)
	}
}

// affectedOf 调影响面端点并返回排序后的 affected 列表与 total。
func affectedOf(t *testing.T, baseURL, query string) ([]string, int) {
	t.Helper()
	code, body := doJSON(t, http.MethodGet, baseURL+"/admin/v1/configs/impact"+query, nil)
	if code != http.StatusOK {
		t.Fatalf("impact 应 200，实际 %d：%v", code, body)
	}
	raw := asSlice(body["affected"])
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	sort.Strings(out)
	return out, int(body["total"].(float64))
}

// TestConfigImpactRESTFlow 发布影响面预览 REST 集成（FR-79）：
// 注册三台在线实例 + 经 DB 指派归属 → 各 scope 查 impact 与预期受影响在线子服集合一致；非法参数 400。
func TestConfigImpactRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 三台在线实例：s1/s2 在 g1、s3 在 g2（先以 groupHint，再经 DB 指派覆盖小区）
	regOnline(t, ts.URL, "prod", "s1", "g1")
	regOnline(t, ts.URL, "prod", "s2", "g1")
	regOnline(t, ts.URL, "prod", "s3", "g2")

	// 经 admin zone 指派写 DB 归属：s1→g1/za、s2→g1/zb、s3→g2/za
	assignURL := ts.URL + "/admin/v1/zones/assignments"
	for _, a := range []struct{ sid, group, zone string }{
		{"s1", "g1", "za"}, {"s2", "g1", "zb"}, {"s3", "g2", "za"},
	} {
		code, _ := doJSON(t, http.MethodPut, assignURL, map[string]any{
			"namespace": "prod", "serverId": a.sid, "group": a.group, "zone": a.zone,
		})
		if code != http.StatusOK {
			t.Fatalf("指派 %s 应 200，实际 %d", a.sid, code)
		}
	}

	// global：覆盖全部在线
	if got, total := affectedOf(t, ts.URL, "?namespace=prod&scopeLevel=global"); total != 3 ||
		got[0] != "s1" || got[1] != "s2" || got[2] != "s3" {
		t.Fatalf("global 应命中 [s1 s2 s3]，实际 %v", got)
	}

	// group=g1：s1 + s2
	if got, total := affectedOf(t, ts.URL, "?namespace=prod&scopeLevel=group&group=g1"); total != 2 ||
		got[0] != "s1" || got[1] != "s2" {
		t.Fatalf("group=g1 应命中 [s1 s2]，实际 %v", got)
	}

	// zone=g1/za：仅 s1
	if got, total := affectedOf(t, ts.URL, "?namespace=prod&scopeLevel=zone&group=g1&scopeTarget=za"); total != 1 ||
		got[0] != "s1" {
		t.Fatalf("zone g1/za 应命中 [s1]，实际 %v", got)
	}

	// server=s2：仅 s2（在线）
	if got, total := affectedOf(t, ts.URL, "?namespace=prod&scopeLevel=server&group=g1&scopeTarget=s2"); total != 1 ||
		got[0] != "s2" {
		t.Fatalf("server s2 应命中 [s2]，实际 %v", got)
	}

	// server=s404：不在线 → 空集
	if _, total := affectedOf(t, ts.URL, "?namespace=prod&scopeLevel=server&group=g1&scopeTarget=s404"); total != 0 {
		t.Fatalf("server s404 不在线应空集，实际 total=%d", total)
	}

	// 非法 scopeLevel → 400
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/configs/impact?namespace=prod&scopeLevel=bogus", nil); code != http.StatusBadRequest {
		t.Fatalf("非法 scopeLevel 应 400，实际 %d", code)
	}
	// zone 缺 scopeTarget → 400
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/configs/impact?namespace=prod&scopeLevel=zone&group=g1", nil); code != http.StatusBadRequest {
		t.Fatalf("zone 缺 scopeTarget 应 400，实际 %d", code)
	}
}
