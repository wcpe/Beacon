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

// TestReverseFetchManagedTaskFullChain 受管任务两段式全链路（FR-58，见 ADR-0037）：
// admin 触发建扫描任务(202, scanning) → agent 拉 scan 命令(mode=scan) → agent 回扫描清单(/files/scan)
// → 任务 pending-review → admin 提交选定集(202, fetching) → agent 拉 submit 命令(mode=submit, selectedPaths)
// → agent 回选定内容(/files/ingest) → 仅选定落库、任务 done。
func TestReverseFetchManagedTaskFullChain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-1", "area1")

	// admin 触发反向抓取（组级）→ 202 + scanning 任务
	code, task := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-1/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area1",
	})
	if code != http.StatusAccepted {
		t.Fatalf("触发反向抓取应 202，实际 %d：%v", code, task)
	}
	if task["status"] != "scanning" {
		t.Fatalf("任务视图应 status=scanning，实际 %v", task)
	}
	taskID := int(task["id"].(float64))
	scanCmdID := int(task["scanCommandId"].(float64))

	// 触发即写一条 file.reverse-fetch-scan 审计
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=file.reverse-fetch-scan", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("应有 file.reverse-fetch-scan 审计，实际 %d：%v", code, audits["items"])
	}

	// agent 拉 scan 命令 → 200，payload 含 mode=scan
	code, pulled := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-1", nil)
	if code != http.StatusOK || int(pulled["id"].(float64)) != scanCmdID {
		t.Fatalf("拉 scan 命令应 200 且匹配 id，实际 %d：%v", code, pulled)
	}
	payload, _ := pulled["payload"].(map[string]any)
	if payload["mode"] != "scan" || payload["scope"] != "group" || payload["group"] != "area1" {
		t.Fatalf("scan 命令 payload 应含 mode=scan/scope/group，实际 %v", payload)
	}

	// agent 回扫描清单（含一个超阈值运行时垃圾文件，应被列出而非整批失败）→ 200
	code, scanResp := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/scan", map[string]any{
		"commandId": scanCmdID,
		"files": []map[string]any{
			{"path": "plugin-a/config.yml", "size": 100, "isText": true, "overThreshold": false},
			{"path": "plugin-a/metrics.jsonl", "size": 5000000, "isText": true, "overThreshold": true},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("回扫描清单应 200，实际 %d：%v", code, scanResp)
	}

	// 任务转 pending-review、清单含两文件、超阈值计数 1
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID), nil)
	if code != http.StatusOK || detail["status"] != "pending-review" {
		t.Fatalf("清单到后任务应 pending-review，实际 %d：%v", code, detail)
	}
	if int(detail["totalFiles"].(float64)) != 2 || int(detail["overThresholdCount"].(float64)) != 1 {
		t.Fatalf("任务计数应 total=2 over=1，实际 %v", detail)
	}

	// admin 提交选定集（仅小配置，不含超阈值文件）→ 202 fetching
	code, submitted := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/submit", map[string]any{
		"selectedPaths": []string{"plugin-a/config.yml"},
	})
	if code != http.StatusAccepted || submitted["status"] != "fetching" {
		t.Fatalf("提交应 202 fetching，实际 %d：%v", code, submitted)
	}
	submitCmdID := int(submitted["submitCommandId"].(float64))

	// agent 拉 submit 命令 → payload 含 mode=submit + selectedPaths
	code, pulled2 := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-1", nil)
	if code != http.StatusOK || int(pulled2["id"].(float64)) != submitCmdID {
		t.Fatalf("拉 submit 命令应 200 且匹配 id，实际 %d：%v", code, pulled2)
	}
	payload2, _ := pulled2["payload"].(map[string]any)
	if payload2["mode"] != "submit" {
		t.Fatalf("submit 命令 payload 应含 mode=submit，实际 %v", payload2)
	}

	// agent 回选定内容（复用 /files/ingest）→ 200，仅选定落库
	code, ing := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/ingest", map[string]any{
		"commandId": submitCmdID,
		"files":     []map[string]any{{"path": "plugin-a/config.yml", "content": "k: 1\n"}},
	})
	if code != http.StatusOK || ing["created"].(float64) != 1 {
		t.Fatalf("submit ingest 应 200 落 1 文件，实际 %d：%v", code, ing)
	}

	// 任务 done、文件树落组覆盖含选定文件、不含超阈值未选文件
	code, doneTask := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID), nil)
	if code != http.StatusOK || doneTask["status"] != "done" {
		t.Fatalf("入库后任务应 done，实际 %d：%v", code, doneTask)
	}
	code, files := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace=prod&group=area1&scopeLevel=group", nil)
	if code != http.StatusOK {
		t.Fatalf("列文件应 200，实际 %d", code)
	}
	if !containsFilePath(files["items"], "plugin-a/config.yml") {
		t.Fatalf("文件树应含选定的 plugin-a/config.yml，实际 %v", files["items"])
	}
	if containsFilePath(files["items"], "plugin-a/metrics.jsonl") {
		t.Fatalf("未选定的超阈值文件不应落库，实际 %v", files["items"])
	}
}

// TestReverseFetchMutex 同实例已有活跃任务再触发 → 409 REVERSE_FETCH_TASK_ACTIVE。
func TestReverseFetchMutex(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-m", "area1")
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-m/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area1",
	}); code != http.StatusAccepted {
		t.Fatalf("首次触发应 202，实际 %d", code)
	}
	code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-m/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area1",
	})
	if code != http.StatusConflict || body["code"] != "REVERSE_FETCH_TASK_ACTIVE" {
		t.Fatalf("已有活跃任务应 409 REVERSE_FETCH_TASK_ACTIVE，实际 %d：%v", code, body)
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

// TestReverseFetchOfflineInstance 目标不在注册表（离线 / 不存在）→ 404，不建任务。
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

// TestReverseFetchSubmitIngestRejectsJar 提交后 agent 回传含 .jar → 400 INVALID_PATH 且无文件落库、任务 failed。
func TestReverseFetchSubmitIngestRejectsJar(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-2", "area2")
	// 建任务 → 回扫描清单 → 提交选定 → agent 回传含 jar
	_, task := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-2/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area2",
	})
	taskID := int(task["id"].(float64))
	scanCmdID := int(task["scanCommandId"].(float64))
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-2", nil); code != http.StatusOK {
		t.Fatalf("拉 scan 命令应 200，实际 %d", code)
	}
	doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/scan", map[string]any{
		"commandId": scanCmdID,
		"files":     []map[string]any{{"path": "evil/plugin.yml", "size": 10, "isText": true, "overThreshold": false}},
	})
	_, submitted := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/submit", map[string]any{
		"selectedPaths": []string{"evil/plugin.yml"},
	})
	submitCmdID := int(submitted["submitCommandId"].(float64))
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-2", nil); code != http.StatusOK {
		t.Fatalf("拉 submit 命令应 200，实际 %d", code)
	}
	// 回传含 .jar → 400 INVALID_PATH（双保险，整批拒）
	code, body := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/ingest", map[string]any{
		"commandId": submitCmdID,
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
