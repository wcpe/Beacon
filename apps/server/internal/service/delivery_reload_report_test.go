package service

import (
	"encoding/json"
	"testing"
)

// TestFailureFactsCarriesReloadReport 断言重载失败结果会落进失败事实（FR-274）。
//
// 这是「一票否决 + 失败逐条可见」的落库侧锁：agent 上报 reload 段时，
// failureFacts 必须产出 reload_report（JSON 原文），否则前端与告警都拿不到「哪个插件失败」。
func TestFailureFactsCarriesReloadReport(t *testing.T) {
	s := &DeliveryOrchestrator{}
	count := 12
	res := deliveryCmdResult{
		ChangedFileCount: 3,
		BackupPresent:    true,
		Error:            "1/2 插件重载失败：A: 解析失败",
		Reload: &deliveryReloadReport{
			Total:       2,
			OKCount:     1,
			FailedCount: 1,
			TimeoutMs:   30000,
			Plugins: []deliveryReloadPlugin{
				{
					Listener:       "top.wcpe.allin.bp.ReloadHook",
					Status:         "ok",
					AppliedMd5:     "md5-new",
					AppliedVersion: "v7",
					AppliedCount:   &count,
				},
				{
					Listener: "top.wcpe.allin.aie.ReloadHook",
					Status:   "failed",
					Reason:   "schedule.json 解析失败：unexpected token",
					Timeout:  false,
				},
			},
		},
	}

	facts := s.failureFacts("生效失败", res, false)

	raw, ok := facts["reload_report"]
	if !ok {
		t.Fatal("失败事实必须包含 reload_report，否则重载失败不可见")
	}
	var got deliveryReloadReport
	if err := json.Unmarshal([]byte(raw.(string)), &got); err != nil {
		t.Fatalf("reload_report 必须是合法 JSON：%v", err)
	}
	if got.Total != 2 || got.OKCount != 1 || got.FailedCount != 1 || got.TimeoutMs != 30000 {
		t.Fatalf("计数不符：%+v", got)
	}
	if len(got.Plugins) != 2 {
		t.Fatalf("插件条目数不符：%d", len(got.Plugins))
	}
	// 未自报 appliedCount 的失败项必须保持 nil，不能被写成 0（0 是合法值）。
	if got.Plugins[1].AppliedCount != nil {
		t.Fatalf("未自报的 appliedCount 必须为 nil，实际 %v", *got.Plugins[1].AppliedCount)
	}
	if got.Plugins[0].AppliedCount == nil || *got.Plugins[0].AppliedCount != 12 {
		t.Fatalf("自报的 appliedCount 必须保留，实际 %v", got.Plugins[0].AppliedCount)
	}
}

// TestFailureFactsOmitsReloadReportForOldAgent 断言老 agent 不上报时不写该列（保持 NULL）。
//
// 兼容性要求（behavior_fence 第四条）：老 agent + 新控制面必须行为等价改造前，
// 不能凭空写出一个空对象让前端误以为「有重载信息但全为空」。
func TestFailureFactsOmitsReloadReportForOldAgent(t *testing.T) {
	s := &DeliveryOrchestrator{}
	facts := s.failureFacts("生效失败", deliveryCmdResult{Error: "旧版回执"}, false)

	if _, ok := facts["reload_report"]; ok {
		t.Fatal("老 agent 未上报 reload 段时不得写入 reload_report")
	}
	if facts["error"] != "生效失败" {
		t.Fatalf("失败原因必须保留，实际 %v", facts["error"])
	}
}

// TestParseDeliveryCmdResultReadsReload 断言推进器能解析 agent 上报的 reload 段。
func TestParseDeliveryCmdResultReadsReload(t *testing.T) {
	raw := `{"changedFileCount":2,"skippedFileCount":0,"backupPresent":true,"error":"超时",` +
		`"reload":{"total":1,"okCount":0,"failedCount":1,"timeoutMs":30000,` +
		`"plugins":[{"listener":"A","status":"failed","reason":"重载超时（30000ms）","timeout":true}]}}`

	got := parseDeliveryCmdResult(raw)

	if got.Reload == nil {
		t.Fatal("必须解析出 reload 段")
	}
	if got.Reload.FailedCount != 1 || !got.Reload.Plugins[0].Timeout {
		t.Fatalf("reload 段解析不符：%+v", got.Reload)
	}
	if got.ChangedFileCount != 2 || !got.BackupPresent {
		t.Fatalf("既有字段解析不得被破坏：%+v", got)
	}
}

// TestParseDeliveryCmdResultToleratesMissingReload 断言旧版回执（无 reload 段）仍可解析。
func TestParseDeliveryCmdResultToleratesMissingReload(t *testing.T) {
	got := parseDeliveryCmdResult(`{"changedFileCount":1,"error":"失败"}`)

	if got.Reload != nil {
		t.Fatal("无 reload 段时必须为 nil")
	}
	if got.ChangedFileCount != 1 {
		t.Fatalf("既有字段解析不符：%+v", got)
	}
}
