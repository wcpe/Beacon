package alert

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// captureSink 记录落库的告警事件，并对角色查询返回固定值。
type captureSink struct {
	events []*model.AlertEvent
}

func (c *captureSink) Record(e *model.AlertEvent) error {
	c.events = append(c.events, e)
	return nil
}

func (c *captureSink) ResolveAlertRole(_, _ string) string { return "backend" }

// TestPersistAlerterWritesPublishFailByType 断言 publish-fail 按类型分派落库（FR-275）。
//
// 关键点：非健康类型**不得**被写成 health-transition、不得带 PrevStatus→Status 语义，
// 且必须固定 warning 级（工作量的分级矩阵只适用于健康流转）。
func TestPersistAlerterWritesPublishFailByType(t *testing.T) {
	sink := &captureSink{}
	p := NewPersistAlerter(sink)

	err := p.Notify(context.Background(), Alert{
		Type:      TypePublishFail,
		Namespace: "prod",
		ServerID:  "lobby-1",
		Message:   "交付失败：1/2 插件重载失败",
		Detail:    map[string]any{"orderId": 42},
	})
	if err != nil {
		t.Fatalf("落库不应失败：%v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("应落 1 行，实际 %d", len(sink.events))
	}
	got := sink.events[0]
	if got.Type != model.AlertEventTypePublishFail {
		t.Fatalf("类型应为 publish-fail，实际 %q", got.Type)
	}
	if got.Level != model.AlertLevelWarning {
		t.Fatalf("级别应为 warning，实际 %q", got.Level)
	}
	if got.ToStatus != "" {
		t.Fatalf("非健康类型不得写 to_status（方向维度只服务健康流转），实际 %q", got.ToStatus)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(got.Detail), &detail); err != nil {
		t.Fatalf("detail 必须是合法 JSON：%v", err)
	}
	if detail["orderId"] != float64(42) {
		t.Fatalf("detail 必须保留 orderId，实际 %v", detail["orderId"])
	}
}

// TestPersistAlerterKeepsHealthTransitionBehavior 断言健康路径行为未被本次扩展改变。
func TestPersistAlerterKeepsHealthTransitionBehavior(t *testing.T) {
	sink := &captureSink{}
	p := NewPersistAlerter(sink)

	// 不设 Type（老调用方零改动路径）：必须仍按健康流转落库。
	err := p.Notify(context.Background(), Alert{
		Namespace:  "prod",
		ServerID:   "lobby-1",
		PrevStatus: "online",
		Status:     "lost",
	})
	if err != nil {
		t.Fatalf("落库不应失败：%v", err)
	}
	got := sink.events[0]
	if got.Type != model.AlertEventTypeHealthTransition {
		t.Fatalf("空 Type 必须归一为 health-transition，实际 %q", got.Type)
	}
	if got.ToStatus != "lost" {
		t.Fatalf("健康路径必须仍写 to_status，实际 %q", got.ToStatus)
	}
	if got.Message != "lobby-1 online → lost" {
		t.Fatalf("健康路径 message 口径不得变，实际 %q", got.Message)
	}
}
