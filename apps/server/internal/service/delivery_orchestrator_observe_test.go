package service

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestOrchestratorAutoFinishRollbackReleasesObserve 锁定 FR-265 观察窗内存释放：
// 自动收单（rolling_back → rolled_back 全自动出口）此前不释放观察窗内存，
// 控制面长跑时终态单的缓冲会无界累积。手动 / 审批路径有 clearObserve，唯独自动收单漏了。
func TestOrchestratorAutoFinishRollbackReleasesObserve(t *testing.T) {
	h := newOrchestratorHarness(t)
	// 目标从未推送（pushed_at 为空）→ 全是「非回滚目标」→ 走自动收单分支。
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 置成 rolling_back：目标无 pushed_at，推进器判定无实际回滚工作 → 自动收单。
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
		"status": model.ChangeOrderStatusRollingBack,
	}).Error; err != nil {
		t.Fatalf("置回滚态失败: %v", err)
	}
	// 先塞一个观察窗缓冲进去，验证自动收单会不会把它带走。
	h.orch.markObserveStarted(order.ID, 1, h.clock)
	h.orch.sampleObserve(order.ID, 1, h.f.nsID, []*model.ChangeTarget{})
	if _, ok := h.orch.observeByOrder[order.ID]; !ok {
		t.Fatalf("铺观察窗缓冲失败，实际 %+v", h.orch.observeByOrder)
	}
	h.tick()
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("无回滚工作应自动收单，实际 %s", got.Status)
	}
	if _, ok := h.orch.observeByOrder[order.ID]; ok {
		t.Fatal("自动收单后应释放观察窗内存缓冲")
	}
	if _, ok := h.orch.stallByOrder[order.ID]; ok {
		t.Fatal("自动收单后应释放停滞观测")
	}
}

// TestChangeOrderObserveWindowBelowRestartWarmupRejected 锁定 FR-265 预热口径防呆：
// restart + 健康恶化阈值开启 + 观察窗 < 90s（重启预热宽限）会让健康恶化熔断恒不触发
// （目标整窗都在预热期被排除出评估），审计上也看不出原因。组单 / 编辑时显式拒绝这种组合。
func TestChangeOrderObserveWindowBelowRestartWarmupRejected(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	detail := createDraftOrder(t, f) // 默认 restart + unhealthyRate=30
	if detail.ActivationMethod != model.ActivationMethodRestart || detail.UnhealthyRateThresholdPercent != 30 {
		t.Fatalf("夹具前提不符: %+v", detail.ChangeOrderSummaryView)
	}

	// 观察窗短于重启预热宽限 → 拒绝，且原因点明「健康恶化熔断永不触发」。
	if _, err := env.orders.Update(detail.ID, ChangeOrderInput{ObserveWindowSec: intPtr(60)}, "ops", ""); err == nil {
		t.Fatal("restart + 观察窗 60s（< 90s 预热）应被拒绝")
	}
	// 恰好等于预热宽限 → 放行（边界含等号：90s 观察窗跑完那一刻预热也已结束，仍留有评估余量）。
	updated, err := env.orders.Update(detail.ID, ChangeOrderInput{ObserveWindowSec: intPtr(90)}, "ops", "")
	if err != nil {
		t.Fatalf("观察窗等于预热宽限应放行: %v", err)
	}
	if updated.ObserveWindowSec != 90 {
		t.Fatalf("观察窗应为 90，实际 %d", updated.ObserveWindowSec)
	}

	// 关掉健康恶化阈值 → 短窗无害（熔断本就不评健康），放行。
	if _, err := env.orders.Update(detail.ID, ChangeOrderInput{
		ObserveWindowSec: intPtr(60), UnhealthyRateThresholdPercent: intPtr(0),
	}, "ops", ""); err != nil {
		t.Fatalf("关闭健康恶化阈值后短观察窗应放行: %v", err)
	}
	// 改成非 restart 生效方式 → 无预热期，短窗无害，放行。
	if _, err := env.orders.Update(detail.ID, ChangeOrderInput{
		ObserveWindowSec: intPtr(30), ActivationMethod: strPtr(model.ActivationMethodPushOnly),
	}, "ops", ""); err != nil {
		t.Fatalf("非 restart 生效方式短观察窗应放行: %v", err)
	}
}

// TestChangeOrderCreateRejectsShortObserveWindowWithRestart 守护创建路径同样过防呆（不只是编辑）。
func TestChangeOrderCreateRejectsShortObserveWindowWithRestart(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	_, err := env.orders.Create(f.nsID, ChangeOrderInput{
		Title: strPtr("短窗高危组合"), SourceServerID: strPtr("src-1"), ScanDir: strPtr("plugins/"),
		ActivationMethod: strPtr(model.ActivationMethodRestart), UnhealthyRateThresholdPercent: intPtr(30),
		ObserveWindowSec: intPtr(45),
	}, "ops", "")
	if err == nil {
		t.Fatal("创建时的 restart + 短观察窗组合应被拒绝")
	}
}
