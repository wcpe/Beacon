//go:build integration

package server_test

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"testing"
)

// findScanFile 在任务详情清单里取指定 path 的文件项（用于断言 ignoredByRule）。
func findScanFile(items any, path string) map[string]any {
	for _, it := range asSlice(items) {
		if m, ok := it.(map[string]any); ok && m["path"] == path {
			return m
		}
	}
	return nil
}

// driveToManifest 经审批创建受管任务并回扫描清单，把任务推到 pending-review，返回 taskID。
func driveToManifest(t *testing.T, ts *integrationTestServer, serverID, group string, files []map[string]any) int {
	t.Helper()
	task := createReverseFetchTaskForTest(t, ts, serverID, group)
	taskID := taskIDFromView(t, task)
	scanCmdID := int(task["scanCommandId"].(float64))
	// agent 拉 scan 命令并回清单
	doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId="+serverID, nil)
	code, _ := doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/scan", map[string]any{
		"commandId": scanCmdID, "files": files,
	})
	if code != http.StatusOK {
		t.Fatalf("回扫描清单应 200，实际 %d", code)
	}
	return taskID
}

// TestIgnoreRuleMarksManifest 建持久忽略规则后，扫描清单中命中规则的文件标 ignoredByRule（FR-59）。
func TestIgnoreRuleMarksManifest(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "rule-1", "area1")

	// 建 prefix 忽略规则（大区层）：ServerProbe/ 目录下全排除
	code, rule := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/reverse-fetch/ignore-rules", map[string]any{
		"namespace": "prod", "scope": "group", "group": "area1",
		"ruleType": "prefix", "pattern": "ServerProbe/", "comment": "运行时垃圾",
	})
	if code != http.StatusCreated {
		t.Fatalf("建忽略规则应 201，实际 %d：%v", code, rule)
	}
	// 列规则
	code, listed := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/ignore-rules?namespace=prod&group=area1", nil)
	if code != http.StatusOK || len(asSlice(listed["items"])) != 1 {
		t.Fatalf("应列出 1 条规则，实际 %d：%v", code, listed["items"])
	}

	taskID := driveToManifest(t, ts, "rule-1", "area1", []map[string]any{
		{"path": "AllinCore/config.yml", "size": 100, "isText": true, "overThreshold": false},
		{"path": "ServerProbe/metrics.jsonl", "size": 200, "isText": true, "overThreshold": false},
	})

	// 任务详情：命中规则的文件 ignoredByRule=true，未命中的为 false
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID), nil)
	if code != http.StatusOK {
		t.Fatalf("取任务详情应 200，实际 %d", code)
	}
	if f := findScanFile(detail["files"], "ServerProbe/metrics.jsonl"); f == nil || f["ignoredByRule"] != true {
		t.Fatalf("命中忽略规则的文件应 ignoredByRule=true，实际 %v", f)
	}
	if f := findScanFile(detail["files"], "AllinCore/config.yml"); f == nil || f["ignoredByRule"] != false {
		t.Fatalf("未命中规则的文件应 ignoredByRule=false，实际 %v", f)
	}
}

// TestConflictReviewFullChain 冲突审核全链路（FR-59）：制造冲突（同 path 已追踪 + 再抓）→ 进 conflict-review
// → 取冲突清单 + diff → resolve（overwrite 自审）→ 落库 done。
func TestConflictReviewFullChain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	registerOnline(t, ts.URL, "prod", "conf-1", "area1")

	// 预置目标 group 层已有 A/config.yml（制造冲突）。
	createFileForTest(t, ts, "prod", "area1", "A/config.yml", "group", "old: 1\n")

	taskID := driveToManifest(t, ts, "conf-1", "area1", []map[string]any{
		{"path": "A/config.yml", "size": 10, "isText": true, "overThreshold": false},
		{"path": "B/new.yml", "size": 10, "isText": true, "overThreshold": false},
	})

	// 提交两文件（A 冲突、B 非冲突）并取 submit 审批链创建的一次性授权。
	submitTicket := requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/submit", t.Name()+"-reverse-submit", map[string]any{
		"selectedPaths": []string{"A/config.yml", "B/new.yml"}, "reason": "集成测试提交反向抓取文件",
	})
	code, submitted := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID), nil)
	if code != http.StatusOK || submitted["status"] != "fetching" {
		t.Fatalf("提交审批执行后任务应 fetching，实际 %d：%v", code, submitted)
	}
	submitCmdID := int(submitted["submitCommandId"].(float64))
	// agent 拉 submit 命令并回选定内容（A 新内容与已有冲突）
	doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/commands?namespace=prod&serverId=conf-1", nil)
	code, _ = doJSON(t, http.MethodPost, ts.URL+"/beacon/v1/agent/files/ingest", map[string]any{
		"commandId": submitCmdID,
		"files": []map[string]any{
			{"path": "A/config.yml", "content": "new: 2\n"},
			{"path": "B/new.yml", "content": "added: yes\n"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("回选定内容应 200（进冲突审核），实际 %d", code)
	}

	// 任务进 conflict-review
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID), nil)
	if code != http.StatusOK || detail["status"] != "conflict-review" {
		t.Fatalf("有冲突应进 conflict-review，实际 %d：%v", code, detail["status"])
	}

	// 冲突清单含 A/config.yml
	code, conflicts := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/conflicts", nil)
	if code != http.StatusOK {
		t.Fatalf("取冲突清单应 200，实际 %d", code)
	}
	cs := asSlice(conflicts["conflicts"])
	if len(cs) != 1 || cs[0] != "A/config.yml" {
		t.Fatalf("冲突清单应含 A/config.yml，实际 %v", cs)
	}

	// 抓取正文属于敏感内容；旧 diff 入口必须失败关闭，不能绕过 permit 直接读取。
	code, diff := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/conflicts/diff?path=A/config.yml", nil)
	if code != http.StatusConflict || diff["code"] != "operation_requires_approval" {
		t.Fatalf("未持 permit 读取冲突正文应 409 operation_requires_approval，实际 %d：%v", code, diff)
	}
	grantID := sensitiveGrantIDForTest(t, ts, submitTicket["approvalRequestId"].(string))
	code, bundle := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/conflicts/grants/"+grantID+"/consume", nil)
	items := asSlice(bundle["items"])
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("原申请主体一次消费冲突正文应 200 且只含冲突路径，实际 %d：%v", code, bundle)
	}
	item, _ := items[0].(map[string]any)
	if item["path"] != "A/config.yml" || item["fetchedContent"] != "new: 2\n" || item["existingContent"] != "old: 1\n" {
		t.Fatalf("冲突正文包不符：%v", item)
	}
	sum := md5.Sum([]byte("new: 2\n"))
	fetchedMD5 := hex.EncodeToString(sum[:])

	// 盲确认（无 reviewedMd5）→ 412
	code, _ = doJSONWithHeaders(t, http.MethodPost, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/resolve", map[string]any{
		"decisions": []map[string]any{{"path": "A/config.yml", "action": "overwrite"}},
		"reason":    "集成测试验证盲确认",
	}, map[string]string{"Idempotency-Key": compactIdempotencyKey(t.Name() + "-blind-resolve")})
	if code != http.StatusPreconditionFailed {
		t.Fatalf("盲确认应 412，实际 %d", code)
	}

	// 带正确 reviewedMd5 overwrite → 先创建审批，批准 worker 后落库 done。
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/reverse-fetch/tasks/"+itoa(taskID)+"/resolve", t.Name()+"-reverse-resolve", map[string]any{
		"decisions": []map[string]any{{"path": "A/config.yml", "action": "overwrite", "reviewedMd5": fetchedMD5}},
		"reason":    "集成测试确认冲突覆盖",
	})
	code, done := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/reverse-fetch/tasks/"+itoa(taskID), nil)
	if code != http.StatusOK || done["status"] != "done" {
		t.Fatalf("resolve 后应 done，实际 %d：%v", code, done["status"])
	}
	// A 覆盖为抓取内容、B 落库
	code, files := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace=prod&group=area1&scopeLevel=group", nil)
	if code != http.StatusOK || !containsFilePath(files["items"], "B/new.yml") {
		t.Fatalf("非冲突文件 B/new.yml 应落库，实际 %v", files["items"])
	}
}
