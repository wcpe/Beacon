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
	// 守护 FR-58 真机暴露的缺陷：agent 命令响应必须把 selectedPaths（JSON 数组）原文透传，
	// 不可经 map[string]string 反序列化致数组字段被静默丢弃——否则 agent 收不到选定集、回退整树读内容走超限整批失败。
	selPaths := asSlice(payload2["selectedPaths"])
	if len(selPaths) != 1 || selPaths[0] != "plugin-a/config.yml" {
		t.Fatalf("submit 命令 payload 应含 selectedPaths=[plugin-a/config.yml]（数组原文透传），实际 %v", payload2["selectedPaths"])
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

// TestReverseFetchScanErrorReport agent scan 读盘失败回传错误（FR-87）：
// 建任务(scanning) → agent 拉 scan 命令 → agent 回传 /files/error → 任务 failed + lastError 落库、命令 failed、审计；
// 任务视图含 elapsedSec（≥0）；任务终结后互斥解除可再建。
func TestReverseFetchScanErrorReport(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-e", "area3")

	code0, task := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-e/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area3",
	})
	if code0 != http.StatusAccepted {
		t.Fatalf("建任务应 202，实际 %d：%v", code0, task)
	}
	taskID := int(task["id"].(float64))
	scanCmdID := int(task["scanCommandId"].(float64))
	// 视图含 elapsedSec 字段（派生、≥0）
	if _, ok := task["elapsedSec"]; !ok {
		t.Fatalf("任务视图应含 elapsedSec，实际 %v", task)
	}

	// agent 拉 scan 命令（迁 fetched）
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-e", nil); code != http.StatusOK {
		t.Fatalf("拉 scan 命令应 200，实际 %d", code)
	}

	// agent 回传执行错误 → 200
	code, errResp := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/error", map[string]any{
		"commandId": scanCmdID,
		"reason":    "扫描 plugins 目录元信息失败：IOException: permission denied",
	})
	if code != http.StatusOK {
		t.Fatalf("回传错误应 200，实际 %d：%v", code, errResp)
	}

	// 任务转 failed 且 lastError 落库可查
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID), nil)
	if code != http.StatusOK || detail["status"] != "failed" {
		t.Fatalf("回传错误后任务应 failed，实际 %d：%v", code, detail)
	}
	if le, _ := detail["lastError"].(string); le == "" {
		t.Fatalf("failed 任务应记 lastError，实际 %v", detail["lastError"])
	}
	if ev, ok := detail["elapsedSec"].(float64); !ok || ev < 0 {
		t.Fatalf("任务视图 elapsedSec 应为 ≥0 数字，实际 %v", detail["elapsedSec"])
	}

	// 记一条 file.reverse-fetch-error 审计
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=file.reverse-fetch-error", nil)
	if code != http.StatusOK || len(asSlice(audits["items"])) == 0 {
		t.Fatalf("应有 file.reverse-fetch-error 审计，实际 %d：%v", code, audits["items"])
	}

	// 互斥解除：同实例可再建任务
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-e/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area3",
	}); code != http.StatusAccepted {
		t.Fatalf("失败终结后同实例应可再建，实际 %d", code)
	}
}

// TestReverseFetchErrorRejectsMismatch 错误回传幂等守卫（FR-87）：命令不存在 → 404；终态任务回传 → 409。
func TestReverseFetchErrorRejectsMismatch(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "src-em", "area4")

	// 命令不存在 → 404 COMMAND_NOT_FOUND
	code, body := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/error", map[string]any{
		"commandId": 999999, "reason": "x",
	})
	if code != http.StatusNotFound || body["code"] != "COMMAND_NOT_FOUND" {
		t.Fatalf("不存在命令应 404 COMMAND_NOT_FOUND，实际 %d：%v", code, body)
	}

	// 建任务 → 拉 scan 命令 → 取消任务（终态）后再回传错误 → 409 STATE
	_, task := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/instances/src-em/reverse-fetch?namespace=prod", map[string]any{
		"scope": "group", "group": "area4",
	})
	taskID := int(task["id"].(float64))
	scanCmdID := int(task["scanCommandId"].(float64))
	doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=src-em", nil)
	if code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/cancel", nil); code != http.StatusOK {
		t.Fatalf("取消应 200，实际 %d", code)
	}
	code, body2 := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/error", map[string]any{
		"commandId": scanCmdID, "reason": "x",
	})
	if code != http.StatusConflict || body2["code"] != "REVERSE_FETCH_TASK_STATE" {
		t.Fatalf("终态任务回传错误应 409 REVERSE_FETCH_TASK_STATE，实际 %d：%v", code, body2)
	}
}
