//go:build integration

package server_test

import (
	"fmt"
	"net/http"
	"testing"
)

// TestImprintFullChain 按需拓印全链路（FR-46）：admin 触发拓印(202+审计) → agent 拉命令(200, payload 带 mode=imprint+path)
// → agent 回传整棵树 ingest(200) → 控制面取目标 path 转存、命令转 ready（不落 file_object）→ admin 拉 diff(200, 本地实际值⟷期望合并值)
// → 带正确 reviewedMd5 确认(200) → 落 server 层覆盖 + 命令 done + file.imprint 审计。
func TestImprintFullChain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-imp-1", "area1")

	// 预置组级该文件 {a:1}，使期望合并值非空（拓印源盘上是 {a:99}，构造真实 diff）
	code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/files?namespace=prod", map[string]any{
		"group": "area1", "path": "plugin-a/config.yml", "scopeLevel": "group", "content": "a: 1\n",
	})
	if code != http.StatusCreated {
		t.Fatalf("预置组级文件应 201，实际 %d", code)
	}

	// admin 触发拓印 → 202 + pending 命令
	code, cmd := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-imp-1/imprint?namespace=prod", map[string]any{
		"path": "plugin-a/config.yml",
	})
	if code != http.StatusAccepted {
		t.Fatalf("触发拓印应 202，实际 %d：%v", code, cmd)
	}
	if cmd["type"] != "ingest-plugins" || cmd["status"] != "pending" {
		t.Fatalf("命令视图应 type=ingest-plugins status=pending，实际 %v", cmd)
	}
	cmdID := int(cmd["id"].(float64))

	// 触发即写一条 file.imprint-fetch 审计
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=file.imprint-fetch", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("应有 file.imprint-fetch 审计，实际 %d：%v", code, audits["items"])
	}

	// agent 拉待办命令 → 200，payload 带 mode=imprint + path
	code, pulled := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-imp-1", nil)
	if code != http.StatusOK {
		t.Fatalf("拉命令应 200，实际 %d：%v", code, pulled)
	}
	payload, _ := pulled["payload"].(map[string]any)
	if payload["mode"] != "imprint" || payload["path"] != "plugin-a/config.yml" {
		t.Fatalf("命令 payload 应含 mode=imprint path=plugin-a/config.yml，实际 %v", payload)
	}

	// agent 回传整棵树（含目标 path 与其它文件）→ 200；控制面取目标 path 转存、命令转 ready，不落库
	code, ing := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/ingest", map[string]any{
		"commandId": cmdID,
		"files": []map[string]any{
			{"path": "plugin-a/config.yml", "content": "a: 99\n"},
			{"path": "plugin-a/lang.yml", "content": "hi: hello\n"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("拓印回传应 200，实际 %d：%v", code, ing)
	}

	// 拓印不落 file_object：server 层无该文件
	code, files := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace=prod&group=area1&scopeLevel=server&scopeTarget=src-imp-1", nil)
	if code != http.StatusOK || len(asSlice(files["items"])) != 0 {
		t.Fatalf("拓印回传不应落 server 层文件，实际 %v", files["items"])
	}

	// admin 拉 diff → 200，本地实际值 a:99、期望合并值 a:1、differs=true
	code, diff := doJSON(t, http.MethodGet,
		fmt.Sprintf("%s/admin/v1/imprints/%d/diff?scope=server&group=area1&target=src-imp-1", ts.URL, cmdID), nil)
	if code != http.StatusOK {
		t.Fatalf("拉 diff 应 200，实际 %d：%v", code, diff)
	}
	if diff["actualContent"] != "a: 99\n" {
		t.Fatalf("diff 本地实际值应 a:99，实际 %v", diff["actualContent"])
	}
	if diff["expectedContent"] != "a: 1\n" {
		t.Fatalf("diff 期望合并值应 a:1，实际 %v", diff["expectedContent"])
	}
	if diff["differs"] != true {
		t.Fatalf("a:99 与 a:1 应判有差异，实际 %v", diff["differs"])
	}
	reviewedMD5, _ := diff["actualMd5"].(string)
	if reviewedMD5 == "" {
		t.Fatal("diff 应返回 actualMd5 供确认自审")
	}

	// 带正确 reviewedMd5 确认 → 200，落 server 层覆盖（首版 version=1）
	code, conf := doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/admin/v1/imprints/%d/confirm", ts.URL, cmdID), map[string]any{
			"scope": "server", "group": "area1", "target": "src-imp-1", "reviewedMd5": reviewedMD5,
		})
	if code != http.StatusOK {
		t.Fatalf("确认拓印应 200，实际 %d：%v", code, conf)
	}
	if conf["scopeLevel"] != "server" || conf["version"].(float64) != 1 {
		t.Fatalf("确认结果应落 server 层 version=1，实际 %v", conf)
	}

	// server 层覆盖已落库（内容 = 本地实际值）
	code, files = doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace=prod&group=area1&scopeLevel=server&scopeTarget=src-imp-1", nil)
	if code != http.StatusOK || !containsFilePath(files["items"], "plugin-a/config.yml") {
		t.Fatalf("确认后 server 层应含 plugin-a/config.yml，实际 %v", files["items"])
	}

	// 确认落库写一条 file.imprint 审计
	code, audits = doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=file.imprint", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("应有 file.imprint 审计，实际 %d：%v", code, audits["items"])
	}

	// 命令已 done → 再拉无待办 204
	code, _ = doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-imp-1", nil)
	if code != http.StatusNoContent {
		t.Fatalf("命令完成后再拉应 204，实际 %d", code)
	}
}

// TestImprintConfirmSelfReviewGate 自审门（FR-46）：错误 reviewedMd5 → 412 IMPRINT_REVIEW_MISMATCH 且不落库；命令仍 ready。
func TestImprintConfirmSelfReviewGate(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-imp-2", "area2")

	code, cmd := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-imp-2/imprint?namespace=prod", map[string]any{
		"path": "plugin-b/config.yml",
	})
	if code != http.StatusAccepted {
		t.Fatalf("触发拓印应 202，实际 %d", code)
	}
	cmdID := int(cmd["id"].(float64))
	// 拉取转 fetched 后回传
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-imp-2", nil); code != http.StatusOK {
		t.Fatalf("拉命令应 200，实际 %d", code)
	}
	code, _ = doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/ingest", map[string]any{
		"commandId": cmdID,
		"files":     []map[string]any{{"path": "plugin-b/config.yml", "content": "x: 1\n"}},
	})
	if code != http.StatusOK {
		t.Fatalf("拓印回传应 200，实际 %d", code)
	}

	// 错误 reviewedMd5 → 412 IMPRINT_REVIEW_MISMATCH
	code, body := doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/admin/v1/imprints/%d/confirm", ts.URL, cmdID), map[string]any{
			"scope": "server", "group": "area2", "target": "src-imp-2", "reviewedMd5": "deadbeef",
		})
	if code != http.StatusPreconditionFailed || body["code"] != "IMPRINT_REVIEW_MISMATCH" {
		t.Fatalf("错误 md5 应 412 IMPRINT_REVIEW_MISMATCH，实际 %d：%v", code, body)
	}
	// 自审失败不落库
	code, files := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace=prod&group=area2&scopeLevel=server&scopeTarget=src-imp-2", nil)
	if code != http.StatusOK || len(asSlice(files["items"])) != 0 {
		t.Fatalf("自审失败不应落库，实际 %v", files["items"])
	}
}

// TestImprintDiffNotReady 命令非 ready（刚建未回传）拉 diff → 409 IMPRINT_NOT_READY。
func TestImprintDiffNotReady(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-imp-3", "area3")
	code, cmd := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-imp-3/imprint?namespace=prod", map[string]any{
		"path": "plugin-c/config.yml",
	})
	if code != http.StatusAccepted {
		t.Fatalf("触发拓印应 202，实际 %d", code)
	}
	cmdID := int(cmd["id"].(float64))
	code, body := doJSON(t, http.MethodGet,
		fmt.Sprintf("%s/admin/v1/imprints/%d/diff?scope=server&group=area3&target=src-imp-3", ts.URL, cmdID), nil)
	if code != http.StatusConflict || body["code"] != "IMPRINT_NOT_READY" {
		t.Fatalf("非 ready 拉 diff 应 409 IMPRINT_NOT_READY，实际 %d：%v", code, body)
	}
}

// TestImprintOfflineInstance 目标不在注册表（离线 / 不存在）→ 404，不建命令。
func TestImprintOfflineInstance(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/ghost/imprint?namespace=prod", map[string]any{
		"path": "plugin-a/config.yml",
	})
	if code != http.StatusNotFound || body["code"] != "INSTANCE_NOT_FOUND" {
		t.Fatalf("目标不在线应 404 INSTANCE_NOT_FOUND，实际 %d：%v", code, body)
	}
}

// TestImprintReadonlyForbidden 只读密钥触发拓印 / 确认（写操作）→ 经只读拒写中间件 403。
func TestImprintReadonlyForbidden(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	roKey, _ := createKey(t, ts.URL, "ro-imp", "readonly")
	// 触发拓印（写）
	code, body := doAPIKey(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-imp-1/imprint?namespace=prod", roKey, false, map[string]any{
		"path": "plugin-a/config.yml",
	})
	if code != http.StatusForbidden || body["code"] != "FORBIDDEN" {
		t.Fatalf("只读密钥触发拓印应 403 FORBIDDEN，实际 %d：%v", code, body)
	}
	// 确认拓印（写）
	code, body = doAPIKey(t, http.MethodPost, ts.URL+"/admin/v1/imprints/1/confirm", roKey, false, map[string]any{
		"scope": "server", "group": "area1", "target": "src-imp-1", "reviewedMd5": "x",
	})
	if code != http.StatusForbidden || body["code"] != "FORBIDDEN" {
		t.Fatalf("只读密钥确认拓印应 403 FORBIDDEN，实际 %d：%v", code, body)
	}
}
