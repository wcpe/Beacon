//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// registerOnline 经 agent 注册端点写一个在线实例到内存注册表（供反向抓取在线校验命中）。
func registerOnline(t *testing.T, baseURL, ns, serverID, group string) {
	t.Helper()
	code, _ := doJSON(t, http.MethodPost, baseURL+"/beacon/v1/agent/register", map[string]any{
		"namespace": ns, "serverId": serverID, "role": "bukkit", "group": group, "address": "10.0.0.7:25565",
	})
	if code != http.StatusOK {
		t.Fatalf("注册实例 %s 应 200，实际 %d", serverID, code)
	}
}

// containsFilePath 判断文件列表项里是否含指定相对 path。
func containsFilePath(items any, path string) bool {
	for _, it := range asSlice(items) {
		if m, ok := it.(map[string]any); ok && m["path"] == path {
			return true
		}
	}
	return false
}

// TestReverseFetchFullChain 反向抓取全链路（FR-39）：admin 触发(202+审计) → agent 拉命令(200, payload)
// → agent 回传 ingest(200) → 落组覆盖（文件树命中）→ 命令完成后再拉无待办(204)。
func TestReverseFetchFullChain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-1", "area1")

	// admin 触发反向抓取（组级）→ 202 + pending 命令
	code, cmd := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-1/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area1",
	})
	if code != http.StatusAccepted {
		t.Fatalf("触发反向抓取应 202，实际 %d：%v", code, cmd)
	}
	if cmd["type"] != "ingest-plugins" || cmd["status"] != "pending" {
		t.Fatalf("命令视图应 type=ingest-plugins status=pending，实际 %v", cmd)
	}
	cmdID := int(cmd["id"].(float64))

	// 触发即写一条 file.reverse-fetch 审计
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=file.reverse-fetch", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("应有 file.reverse-fetch 审计，实际 %d：%v", code, audits["items"])
	}

	// agent 拉待办命令 → 200，payload 含目标层 / 组
	code, pulled := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-1", nil)
	if code != http.StatusOK {
		t.Fatalf("拉命令应 200，实际 %d：%v", code, pulled)
	}
	if int(pulled["id"].(float64)) != cmdID || pulled["type"] != "ingest-plugins" {
		t.Fatalf("拉到的命令应匹配 id/type，实际 %v", pulled)
	}
	payload, _ := pulled["payload"].(map[string]any)
	if payload["scope"] != "group" || payload["group"] != "area1" {
		t.Fatalf("命令 payload 应含 scope=group group=area1，实际 %v", payload)
	}

	// agent 回传 ingest → 200，落 1 个新文件
	code, ing := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/ingest", map[string]any{
		"commandId": cmdID,
		"files":     []map[string]any{{"path": "plugin-a/config.yml", "content": "k: 1\n"}},
	})
	if code != http.StatusOK {
		t.Fatalf("ingest 应 200，实际 %d：%v", code, ing)
	}
	if ing["created"].(float64) != 1 {
		t.Fatalf("应新建 1 个文件，实际 %v", ing)
	}

	// 文件树落组覆盖命中
	code, files := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace=prod&group=area1&scopeLevel=group", nil)
	if code != http.StatusOK {
		t.Fatalf("列文件应 200，实际 %d", code)
	}
	if !containsFilePath(files["items"], "plugin-a/config.yml") {
		t.Fatalf("文件树应含 ingest 的 plugin-a/config.yml，实际 %v", files["items"])
	}

	// 命令已 done（非 pending）→ 再拉无待办 204
	code, _ = doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-1", nil)
	if code != http.StatusNoContent {
		t.Fatalf("命令完成后再拉应 204，实际 %d", code)
	}
}

// TestReverseFetchMissingNamespace 缺 namespace 查询参数 → 400。
func TestReverseFetchMissingNamespace(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-1", "area1")
	code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-1/reverse-fetch", map[string]any{
		"scope": "group", "group": "area1",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 namespace 应 400，实际 %d", code)
	}
}

// TestReverseFetchOfflineInstance 目标不在注册表（离线 / 不存在）→ 404，不建命令。
func TestReverseFetchOfflineInstance(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/ghost/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area1",
	})
	if code != http.StatusNotFound || body["code"] != "INSTANCE_NOT_FOUND" {
		t.Fatalf("目标不在线应 404 INSTANCE_NOT_FOUND，实际 %d：%v", code, body)
	}
}

// TestReverseFetchReadonlyForbidden 只读密钥触发反向抓取（写操作）→ 经只读拒写中间件 403。
func TestReverseFetchReadonlyForbidden(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	roKey, _ := createKey(t, ts.URL, "ro-rf", "readonly")
	code, body := doAPIKey(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-1/reverse-fetch?namespace=prod", roKey, false, map[string]any{
		"scope": "group", "group": "area1",
	})
	if code != http.StatusForbidden || body["code"] != "FORBIDDEN" {
		t.Fatalf("只读密钥触发反向抓取应 403 FORBIDDEN，实际 %d：%v", code, body)
	}
}

// TestReverseFetchIngestRejectsJarOverHTTP 控制面入库前双校验经 HTTP 生效：回传含 .jar → 400 INVALID_PATH 且无文件落库。
func TestReverseFetchIngestRejectsJarOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-2", "area2")
	code, cmd := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-2/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area2",
	})
	if code != http.StatusAccepted {
		t.Fatalf("触发应 202，实际 %d", code)
	}
	cmdID := int(cmd["id"].(float64))
	// 先拉取转 fetched 才能回传
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-2", nil); code != http.StatusOK {
		t.Fatalf("拉命令应 200，实际 %d", code)
	}
	// 回传含 .jar → 400 INVALID_PATH（整批拒绝）
	code, body := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/ingest", map[string]any{
		"commandId": cmdID,
		"files":     []map[string]any{{"path": "evil/plugin.jar", "content": "MZ"}},
	})
	if code != http.StatusBadRequest || body["code"] != "INVALID_PATH" {
		t.Fatalf("含 jar 应 400 INVALID_PATH，实际 %d：%v", code, body)
	}
	// jar 被拒后 area2 组无任何文件落库
	code, files := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace=prod&group=area2&scopeLevel=group", nil)
	if code != http.StatusOK || len(asSlice(files["items"])) != 0 {
		t.Fatalf("jar 被拒后 area2 应无文件，实际 %v", files["items"])
	}
}
