//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// 集成测试用固定 64 位 hex sha256（仅测试值）。
const (
	assetSHAA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	assetSHAB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	assetSHAC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// activateAgent 注册并确认一台 agent（pending → active），返回后其 server 行已建、可调数据面上报端点。
func activateAgent(t *testing.T, baseURL, token, identityID, serverID string) {
	t.Helper()
	code, _ := doAgentJSON(t, http.MethodPost, baseURL+"/beacon/v2/agent/register",
		map[string]string{"X-Beacon-Token": token}, map[string]any{
			"identityId": identityID, "serverId": serverID, "kind": "backend", "bootId": "boot-1",
		})
	if code != http.StatusAccepted {
		t.Fatalf("注册 %s 应 202 pending，实际 %d", serverID, code)
	}
	code, _ = doJSON(t, http.MethodPost, baseURL+"/admin/v2/agent-identities/"+identityID+"/approve", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("确认 %s 应 200，实际 %d", serverID, code)
	}
}

// assetManifestBody 构造一次清单上报请求体。
func assetManifestBody(mode string, upserts []map[string]any, extra map[string]any) map[string]any {
	body := map[string]any{"mode": mode, "upserts": upserts, "scanDurationMs": 12, "truncated": false}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func assetFile(path, sha string, size, mtime int64) map[string]any {
	return map[string]any{"path": path, "sha256": sha, "size": size, "mtimeMs": mtime, "isText": true}
}

// TestV2AssetsManifestAndAdminFlow 端到端：接入确认 → 全量上报 → 搜索 / 概要 → 增量 → 基线失配 409。
func TestV2AssetsManifestAndAdminFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	token := createV2NamespaceToken(t, ts.URL, "assetns")
	nsID := nsIDBySelfReport(t, ts.URL, "assetns")
	identityID := "11111111-1111-4111-8111-111111111111"
	serverID := "lobby-1"
	activateAgent(t, ts.URL, token, identityID, serverID)

	manifestURL := ts.URL + "/beacon/v2/agent/assets/manifest"
	agentHeaders := map[string]string{"X-Beacon-Token": token, "X-Beacon-Identity": identityID}

	// 全量首报（2 文件）。
	code, body := doAgentJSON(t, http.MethodPost, manifestURL, agentHeaders, assetManifestBody("full",
		[]map[string]any{
			assetFile("plugins/A/config.yml", assetSHAA, 10, 100),
			assetFile("server.properties", assetSHAB, 20, 200),
		},
		map[string]any{"uploadId": "u1", "seq": 0, "eof": true}))
	if code != http.StatusOK {
		t.Fatalf("全量上报应 200，实际 %d：%v", code, body)
	}
	if fc, _ := body["fileCount"].(float64); fc != 2 {
		t.Fatalf("全量应回 fileCount=2，实际 %v", body["fileCount"])
	}
	digest, _ := body["digest"].(string)
	if digest == "" {
		t.Fatalf("全量应回非空摘要")
	}

	// 管理面搜索：整 namespace → 2 行。
	code, list := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/assets?namespaceId="+itoa(int(nsID)), nil)
	if code != http.StatusOK {
		t.Fatalf("搜索应 200，实际 %d：%v", code, list)
	}
	if total, _ := list["total"].(float64); total != 2 {
		t.Fatalf("搜索应回 2 行，实际 %v", list["total"])
	}
	items, _ := list["items"].([]any)
	first, _ := items[0].(map[string]any)
	if first["serverId"] != serverID {
		t.Fatalf("资产行 serverId 应回填业务串 %s，实际 %v", serverID, first["serverId"])
	}

	// 按扩展名过滤 → config.yml。
	code, byExt := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/assets?namespaceId="+itoa(int(nsID))+"&ext=yml", nil)
	if code != http.StatusOK {
		t.Fatalf("扩展名搜索应 200，实际 %d", code)
	}
	if total, _ := byExt["total"].(float64); total != 1 {
		t.Fatalf("ext=yml 应命中 1 行，实际 %v", byExt["total"])
	}

	// 裸 name 无索引条件 → 400。
	code, _ = doJSON(t, http.MethodGet, ts.URL+"/admin/v2/assets?namespaceId="+itoa(int(nsID))+"&name=config", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("裸 name 查询应 400，实际 %d", code)
	}

	// 概要。
	code, scan := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/assets/scan-status?namespaceId="+itoa(int(nsID)), nil)
	if code != http.StatusOK {
		t.Fatalf("概要应 200，实际 %d", code)
	}
	scanItems, _ := scan["items"].([]any)
	if len(scanItems) != 1 {
		t.Fatalf("概要应 1 行，实际 %d", len(scanItems))
	}

	// 增量：改一文件 + 删一文件。
	code, delta := doAgentJSON(t, http.MethodPost, manifestURL, agentHeaders, assetManifestBody("delta",
		[]map[string]any{assetFile("plugins/A/config.yml", assetSHAC, 11, 111)},
		map[string]any{"baseDigest": digest, "deleted": []string{"server.properties"}}))
	if code != http.StatusOK {
		t.Fatalf("增量上报应 200，实际 %d：%v", code, delta)
	}
	if fc, _ := delta["fileCount"].(float64); fc != 1 {
		t.Fatalf("增量后应剩 1 文件，实际 %v", delta["fileCount"])
	}

	// 基线失配 → 409 asset_manifest_out_of_sync。
	code, stale := doAgentJSON(t, http.MethodPost, manifestURL, agentHeaders, assetManifestBody("delta",
		[]map[string]any{assetFile("plugins/A/config.yml", assetSHAA, 1, 1)},
		map[string]any{"baseDigest": "stale-digest"}))
	if code != http.StatusConflict || stale["code"] != "asset_manifest_out_of_sync" {
		t.Fatalf("基线失配应 409 asset_manifest_out_of_sync，实际 %d：%v", code, stale)
	}

	// 未确认 agent 调上报端点 → 403（复用鉴权中间件）。
	code, _ = doAgentJSON(t, http.MethodPost, manifestURL,
		map[string]string{"X-Beacon-Token": token, "X-Beacon-Identity": "22222222-2222-4222-8222-222222222222"},
		assetManifestBody("full", nil, map[string]any{"uploadId": "u9", "seq": 0, "eof": true}))
	if code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("未知身份上报应 401/403，实际 %d", code)
	}
}

// TestV2AssetsCompareAndRescan 端到端：两服上报同路径不同哈希 → 比对分组 + 缺失服；批量重扫在线 / 离线拆分。
func TestV2AssetsCompareAndRescan(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	token := createV2NamespaceToken(t, ts.URL, "cmpns")
	nsID := nsIDBySelfReport(t, ts.URL, "cmpns")

	activateAgent(t, ts.URL, token, "aaaaaaaa-1111-4111-8111-111111111111", "s1")
	activateAgent(t, ts.URL, token, "bbbbbbbb-2222-4222-8222-222222222222", "s2")
	activateAgent(t, ts.URL, token, "cccccccc-3333-4333-8333-333333333333", "s3")

	path := "plugins/A/config.yml"
	report := func(identityID, serverID, sha string, size int64) {
		code, _ := doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v2/agent/assets/manifest",
			map[string]string{"X-Beacon-Token": token, "X-Beacon-Identity": identityID},
			assetManifestBody("full", []map[string]any{assetFile(path, sha, size, 100)},
				map[string]any{"uploadId": "u-" + serverID, "seq": 0, "eof": true}))
		if code != http.StatusOK {
			t.Fatalf("%s 上报应 200，实际 %d", serverID, code)
		}
	}
	report("aaaaaaaa-1111-4111-8111-111111111111", "s1", assetSHAA, 10)
	report("bbbbbbbb-2222-4222-8222-222222222222", "s2", assetSHAA, 10) // 与 s1 同哈希（多数派）
	// s3 只报别的文件、无 path → missing。
	code, _ := doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v2/agent/assets/manifest",
		map[string]string{"X-Beacon-Token": token, "X-Beacon-Identity": "cccccccc-3333-4333-8333-333333333333"},
		assetManifestBody("full", []map[string]any{assetFile("plugins/A/other.yml", assetSHAB, 5, 5)},
			map[string]any{"uploadId": "u-s3", "seq": 0, "eof": true}))
	if code != http.StatusOK {
		t.Fatalf("s3 上报应 200，实际 %d", code)
	}

	// 比对：1 组（多数派 s1/s2）+ 缺失服 s3。
	code, cmp := doJSON(t, http.MethodGet,
		ts.URL+"/admin/v2/assets/compare?namespaceId="+itoa(int(nsID))+"&path="+path, nil)
	if code != http.StatusOK {
		t.Fatalf("比对应 200，实际 %d：%v", code, cmp)
	}
	groups, _ := cmp["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("应 1 个哈希分组，实际 %d", len(groups))
	}
	g0, _ := groups[0].(map[string]any)
	servers, _ := g0["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("多数派组应 2 成员，实际 %d", len(servers))
	}
	missing, _ := cmp["missing"].([]any)
	if len(missing) != 1 || missing[0] != "s3" {
		t.Fatalf("缺失服应为 [s3]，实际 %v", cmp["missing"])
	}

	// 批量重扫：s1（在线）+ ghost（不存在，视离线）。
	code, rescan := doJSON(t, http.MethodPost, ts.URL+"/admin/v2/assets/rescan", map[string]any{
		"namespaceId": nsID, "serverIds": []string{"s1", "ghost"}, "force": true,
	})
	if code != http.StatusAccepted {
		t.Fatalf("重扫应 202，实际 %d：%v", code, rescan)
	}
	results, _ := rescan["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("应回 2 条结果，实际 %d", len(results))
	}
	byID := map[string]map[string]any{}
	for _, raw := range results {
		r, _ := raw.(map[string]any)
		byID[r["serverId"].(string)] = r
	}
	if byID["s1"]["offline"] != false || byID["s1"]["commandId"] == nil {
		t.Fatalf("s1 应下发命令、非离线，实际 %v", byID["s1"])
	}
	if byID["ghost"]["offline"] != true || byID["ghost"]["commandId"] != nil {
		t.Fatalf("ghost 应标离线、无命令，实际 %v", byID["ghost"])
	}
}
