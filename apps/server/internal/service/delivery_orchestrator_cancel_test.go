package service

import (
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// setTargetStatus 直接改目标主状态（铺各阶段起点：pushing / pushed / activating）。
func setTargetStatus(t *testing.T, db *gorm.DB, orderID uint, serverID, status string) {
	t.Helper()
	if err := db.Model(&model.ChangeTarget{}).Where("order_id = ? AND server_id = ?", orderID, serverID).
		Update("status", status).Error; err != nil {
		t.Fatalf("置目标状态失败: %v", err)
	}
}

// TestOrchestratorCancelSettlesInFlightTargets 锁定 FR-260：紧急终止必须把在途目标收口到终态。
//
// cancelled 不在推进器装载集内——终止后推进器再也不看这张单，留在 pushing/pushed/activating 的目标
// 永远等不到回执处理能力，成为「既不推进也不收尸」的孤儿目标：
// 状态墙长期停在中间态、目标计数不再变化，运维分不清「这台到底动没动盘」。
func TestOrchestratorCancelSettlesInFlightTargets(t *testing.T) {
	h := newOrchestratorHarness(t)
	// 补第三台目标，使 pushing / pushed / activating 三种在途态各有一台可断言。
	seedDeliveryServer(t, h.env.db, h.f.nsID, "t-3", model.ServerKindBackend, &h.f.zone2ID, model.AgentIdentityStatusActive)
	markDeliveryOnline(h.env.health, h.f.nsID, "t-3")
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0)
	// 单的目标由 selector 固化：把 t-3 并进来，使三种在途态各有一台可断言。
	selector := ChangeSelector{Servers: []string{"t-1", "t-2", "t-3"}}
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).
		Update("selector", encodeSelector(selector)).Error; err != nil {
		t.Fatalf("改目标选择器失败: %v", err)
	}
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// tick 下发全部三台 → pushing；再手工把其中两台铺成 pushed / activating。
	h.tick()
	targets, err := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	if err != nil {
		t.Fatalf("读目标失败: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("应有 3 台目标，实际 %d", len(targets))
	}
	pushed, activating := "", ""
	for _, tg := range targets {
		if tg.ServerID == "t-2" {
			pushed = tg.ServerID
		}
		if tg.ServerID == "t-3" {
			activating = tg.ServerID
		}
	}
	if pushed == "" || activating == "" {
		t.Fatalf("未铺齐 pushed / activating 目标: %+v", targets)
	}
	setTargetStatus(t, h.env.db, order.ID, pushed, model.ChangeTargetStatusPushed)
	setTargetStatus(t, h.env.db, order.ID, activating, model.ChangeTargetStatusActivating)

	if _, err := h.orch.Cancel(order.ID, "误发布需止损", "ops", "ip"); err != nil {
		t.Fatalf("终止失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCancelled {
		t.Fatalf("终止后单应 cancelled，实际 %s", got.Status)
	}
	counts := h.targetStatuses(order.ID)
	// 三台在途目标全部收口为 failed（不是留在在途态），且没有任何目标残留在中间态。
	if counts[model.ChangeTargetStatusFailed] != 3 {
		t.Fatalf("在途目标应收口为 failed，实际 %v", counts)
	}
	for _, lingering := range cancelInFlightStatuses {
		if counts[lingering] != 0 {
			t.Fatalf("终止后不应仍有 %s 目标，实际 %v", lingering, counts)
		}
	}
	// 收口后必须留下可读原因：运维据此判断这台动过盘、要不要回滚。
	var rows []model.ChangeTarget
	if err := h.env.db.Where("order_id = ?", order.ID).Find(&rows).Error; err != nil {
		t.Fatalf("回读目标失败: %v", err)
	}
	for _, row := range rows {
		if row.Status == model.ChangeTargetStatusFailed && row.Error != inFlightTargetCancelReason {
			t.Fatalf("收口目标应带终止原因，实际 %q", row.Error)
		}
	}

	// 批也要收终态：活动批（running）不能停在非终态，否则 finished_at 恒空、终态批事件永不派生。
	batches, err := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if err != nil {
		t.Fatalf("读批次失败: %v", err)
	}
	for _, b := range batches {
		if b.Status != model.ChangeBatchStatusSkipped || b.FinishedAt == nil {
			t.Fatalf("终止后批应 skipped 且带 finished_at，实际 %+v", b)
		}
	}

	// 终止后推进器不再装载该单：再 tick 两轮目标状态不该被改回去（收口是一次性终态）。
	h.tick()
	h.tick()
	if after := h.targetStatuses(order.ID); after[model.ChangeTargetStatusFailed] != 3 {
		t.Fatalf("终止后推进器不应再改目标态，实际 %v", after)
	}
}

// TestOrchestratorCancelKeepsPendingSkipped 守护 FR-260 不误伤既有语义：
// 从未开始的 pending 目标仍按旧口径置 skipped（不是 failed）——skipped 的语义是「没动过盘」。
func TestOrchestratorCancelKeepsPendingSkipped(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if _, err := h.orch.Cancel(order.ID, "误发布", "ops", "ip"); err != nil {
		t.Fatalf("终止失败: %v", err)
	}
	counts := h.targetStatuses(order.ID)
	if counts[model.ChangeTargetStatusSkipped] != 2 || counts[model.ChangeTargetStatusFailed] != 0 {
		t.Fatalf("未开始目标应 skipped 而非 failed，实际 %v", counts)
	}
}
