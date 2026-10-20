package service

import (
	"context"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/alert"
)

// recordingAlerter 记录扇出的告警，供断言「何时发、发什么」。
type recordingAlerter struct {
	alerts []alert.Alert
}

func (r *recordingAlerter) Dispatch(_ context.Context, a alert.Alert) {
	r.alerts = append(r.alerts, a)
}

// TestEmitDeliveryFailAlertOnFailedTarget 断言迁到 failed 时扇出 publish-fail 告警（FR-275）。
//
// 告警必须带 orderId / serverId / 失败原因，且类型是 publish-fail —— 否则运维无法定位
// 「哪张单、哪台服、为什么失败」。
func TestEmitDeliveryFailAlertOnFailedTarget(t *testing.T) {
	rec := &recordingAlerter{}
	s := &DeliveryOrchestrator{
		alerter: rec,
		now:     func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
	}
	rt := &orderRuntime{nsCode: "prod", order: &model.ChangeOrder{}}
	rt.order.ID = 42
	target := &model.ChangeTarget{ServerID: "lobby-1", Status: model.ChangeTargetStatusFailed}

	s.emitDeliveryFailAlert(rt, target, map[string]any{
		"error":         "文件已落盘，仅配置变更通知失败：1/2 插件重载失败：A: 解析失败",
		"reload_report": `{"total":2,"failedCount":1}`,
	})

	if len(rec.alerts) != 1 {
		t.Fatalf("应扇出 1 条告警，实际 %d", len(rec.alerts))
	}
	got := rec.alerts[0]
	if got.EventType() != alert.TypePublishFail {
		t.Fatalf("告警类型应为 publish-fail，实际 %q", got.EventType())
	}
	if got.Namespace != "prod" || got.ServerID != "lobby-1" {
		t.Fatalf("归属不符：%+v", got)
	}
	if got.Detail["orderId"] != uint(42) {
		t.Fatalf("detail 必须含 orderId，实际 %v", got.Detail["orderId"])
	}
	if _, ok := got.Detail["reloadReport"]; !ok {
		t.Fatal("detail 必须含逐插件重载明细，否则运维要翻交付单才知道哪个插件失败")
	}
}

// TestEmitDeliveryFailAlertSkippedWhenNotWired 断言未装配告警器时静默跳过（旁路不得阻断交付）。
func TestEmitDeliveryFailAlertSkippedWhenNotWired(t *testing.T) {
	s := &DeliveryOrchestrator{}
	rt := &orderRuntime{nsCode: "prod", order: &model.ChangeOrder{}}
	target := &model.ChangeTarget{ServerID: "lobby-1", Status: model.ChangeTargetStatusFailed}

	s.emitDeliveryFailAlert(rt, target, map[string]any{"error": "失败"}) // 不应 panic
}

// TestPublishFailNotConverged 锁死护栏：publish-fail 不得纳入 FR-232 收敛。
//
// 交付失败要逐条可见、逐条处置；一旦被收敛合并，同一台服反复交付失败只会留 1 行并被计数掩盖。
func TestPublishFailNotConverged(t *testing.T) {
	if convergesOnRecord(model.AlertEventTypePublishFail) {
		t.Fatal("publish-fail 不得纳入告警收敛")
	}
	// 对照：既有收敛类型保持收敛，确认不是把整个函数改成恒 false 蒙混过关。
	if !convergesOnRecord(model.AlertEventTypeHealthTransition) {
		t.Fatal("health-transition 必须仍参与收敛")
	}
	if !convergesOnRecord(model.AlertEventTypeIdentityConflict) {
		t.Fatal("identity-conflict 必须仍参与收敛")
	}
}
