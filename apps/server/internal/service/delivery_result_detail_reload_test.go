package service

import (
	"encoding/json"
	"testing"
)

// TestDeliveryResultDetailCarriesReload 断言回执入口把逐插件重载结果写进 result_detail（FR-273）。
//
// 这是端到端链路的**关键一环**：agent 上报的 reload 段先落到 agent_command.result_detail，
// 推进器再从那里反解、落 change_target.reload_report。若此处漏写，reload 会在回执入口被静默丢弃——
// 表现为 error 有文本、但 reload_report 恒为空，前端明细列与告警明细都渲染不出来。
// 该缺陷正是真机验收发现的，本测试防回归。
func TestDeliveryResultDetailCarriesReload(t *testing.T) {
	count := 12
	detail := deliveryResultDetail(42, DeliveryResultInput{
		Phase:            "activate",
		Status:           DeliveryResultFailed,
		ChangedFileCount: 3,
		BackupPresent:    true,
		Error:            "1/2 插件重载失败：A: 解析失败",
		Reload: &DeliveryReloadReport{
			Total:       2,
			OKCount:     1,
			FailedCount: 1,
			TimeoutMs:   30000,
			Plugins: []DeliveryReloadPlugin{
				{Listener: "A", Status: "ok", AppliedVersion: "v7", AppliedCount: &count},
				{Listener: "B", Status: "failed", Reason: "解析失败"},
			},
		},
	})

	var got struct {
		OrderID uint                  `json:"orderId"`
		Reload  *deliveryReloadReport `json:"reload"`
		Error   string                `json:"error"`
	}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("result_detail 必须是合法 JSON：%v", err)
	}
	if got.Reload == nil {
		t.Fatal("result_detail 必须包含 reload 段，否则重载明细在回执入口就被丢弃")
	}
	if got.Reload.FailedCount != 1 || len(got.Reload.Plugins) != 2 {
		t.Fatalf("reload 段内容不符：%+v", got.Reload)
	}
	if got.Reload.Plugins[1].Reason == "" {
		t.Fatal("失败项必须保留可读原因")
	}
	if got.OrderID != 42 {
		t.Fatalf("既有字段不得被破坏，orderId=%d", got.OrderID)
	}
}

// TestDeliveryResultDetailOmitsReloadForOldAgent 断言老 agent 不上报时形状完全不变。
func TestDeliveryResultDetailOmitsReloadForOldAgent(t *testing.T) {
	detail := deliveryResultDetail(7, DeliveryResultInput{
		Phase: "activate", Status: DeliveryResultSuccess,
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("result_detail 必须是合法 JSON：%v", err)
	}
	if _, has := got["reload"]; has {
		t.Fatal("老 agent 未上报时不得写 reload 键，保持既有回执形状")
	}
}
