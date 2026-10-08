package service

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// captureDeliveryLogs 把全局 slog 接到内存缓冲并把 Level 压到 Warn，返回缓冲与还原函数。
// 停滞检测的输出是 WARN 日志——「到没到点、报没报」只能靠捕获日志来验，而不是打磨打印语句。
func captureDeliveryLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	restore := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return buf, func() { slog.SetDefault(restore) }
}

// TestOrchestratorStallConfirmGateWarnsRepeatedly 锁定 FR-262「确认门超时提醒」：
// 批到 awaiting_confirm 后推进器只会空转，此前完全静默——运维看到单「还 rolling 但不动了」只能翻库猜。
// 修后：超时起按节律 WARN，点明卡在推进门等人工确认。
func TestOrchestratorStallConfirmGateWarnsRepeatedly(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 推到确认门：批 awaiting_confirm，此后推进器无事可做。
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick()
	h.advance(6 * time.Second)
	h.tick()
	batches, err := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if err != nil {
		t.Fatalf("读批次失败: %v", err)
	}
	if batches[0].Status != model.ChangeBatchStatusAwaitingConfirm {
		t.Fatalf("应已到确认门，实际 %s", batches[0].Status)
	}

	buf, restore := captureDeliveryLogs(t)
	defer restore()
	// 未到首提醒点（5 分钟）→ 不得刷屏。
	h.advance(deliveryConfirmGateRemindAfter - time.Minute)
	h.tick()
	if got := buf.String(); strings.Contains(got, "推进门") {
		t.Fatalf("未到提醒点不应告警，实际输出: %s", got)
	}
	// 过首提醒点 → 应 WARN 一次。
	h.advance(2 * time.Minute)
	h.tick()
	first := buf.String()
	if !strings.Contains(first, "推进门") || !strings.Contains(first, "等待人工确认") {
		t.Fatalf("确认门超时应告警，实际输出: %s", first)
	}
	// 节律去重：未到重复间隔前不应再报。
	buf.Reset()
	h.advance(deliveryConfirmGateRemindEvery / 2)
	h.tick()
	if got := buf.String(); strings.Contains(got, "推进门") {
		t.Fatalf("未到重复间隔不应重复告警，实际输出: %s", got)
	}
	// 过了重复间隔 → 再提醒一次（停滞没解除就得持续可见）。
	buf.Reset()
	h.advance(deliveryConfirmGateRemindEvery)
	h.tick()
	if got := buf.String(); !strings.Contains(got, "推进门") {
		t.Fatalf("停滞未解除应按节律重复提醒，实际输出: %s", got)
	}

	// 人工确认后停滞解除：再走到下个提醒点也不该报。
	buf.Reset()
	if _, err := h.orch.applyConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("末批确认后单应 completed，实际 %s", got.Status)
	}
	h.advance(2 * deliveryConfirmGateRemindEvery)
	h.tick()
	if got := buf.String(); strings.Contains(got, "推进门") {
		t.Fatalf("单已终态不应再提醒，实际输出: %s", got)
	}
	if _, stalled := h.orch.stallByOrder[order.ID]; stalled {
		t.Fatal("单终态后应清掉停滞观测，防内存无界增长")
	}
}

// TestOrchestratorStallNoActiveBatchWarns 锁定 FR-262「无活动批检测」：
// rolling 单一个活动批都没有（批被并发迁走 / 数据不一致）时推进器彻底无事可做，此前同样是静默空转。
func TestOrchestratorStallNoActiveBatchWarns(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 把唯一活动批并发迁成 completed，模拟「批不在任何活动态」的数据不一致现场。
	if err := h.env.db.Model(&model.ChangeBatch{}).Where("order_id = ?", order.ID).
		Update("status", model.ChangeBatchStatusCompleted).Error; err != nil {
		t.Fatalf("改批状态失败: %v", err)
	}
	buf, restore := captureDeliveryLogs(t)
	defer restore()

	// 未到阈值（2 分钟）→ 静默。
	h.advance(deliveryNoActiveBatchRemindAfter - time.Minute)
	h.tick()
	if got := buf.String(); strings.Contains(got, "活动批") {
		t.Fatalf("未到阈值不应告警，实际输出: %s", got)
	}
	// 过阈值 → WARN 且点明「无活动批」。
	h.advance(2 * time.Minute)
	h.tick()
	if got := buf.String(); !strings.Contains(got, "没有活动批") {
		t.Fatalf("无活动批应告警，实际输出: %s", got)
	}
}

// TestOrchestratorStallNotReportedWhenProgressing 守护 FR-262 不误报：
// 正常推进中（有 running 批 / payload 未就绪）不得被判停滞——否则每轮推进都会刷屏。
func TestOrchestratorStallNotReportedWhenProgressing(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	buf, restore := captureDeliveryLogs(t)
	defer restore()
	h.tick() // 批 running 且有目标在途
	h.advance(2 * deliveryNoActiveBatchRemindEvery)
	h.tick()
	if got := buf.String(); strings.Contains(got, "活动批") || strings.Contains(got, "推进门") {
		t.Fatalf("正常推进中不应告警，实际输出: %s", got)
	}
	if _, stalled := h.orch.stallByOrder[order.ID]; stalled {
		t.Fatal("正常推进不应留下停滞观测")
	}
}
