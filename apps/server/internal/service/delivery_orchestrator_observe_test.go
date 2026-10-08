package service

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestOrchestratorAutoFinishRollbackReleasesObserve 锁定 FR-265 观察窗内存释放：
// 自动收单（rolling_back → rolled_back 全自动出口）此前不释放观察窗内存，
// 控制面长跑时终态单的缓冲会无界累积。手动 / 审批路径有释放，自动收单此前漏了。
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

// TestOrchestratorFinishRollbackReleasesTerminalMemory 锁定 P2-3：人工「结束回滚」同样是终态出口，
// 也必须释放按单索引的内存（观察窗缓冲 + 停滞观测）。本组最初断言「自动收单是唯一不经释放的
// 终态出口」不成立——人工结束回滚同样不释放，故改为在所有终态出口统一释放。
func TestOrchestratorFinishRollbackReleasesTerminalMemory(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).
		Update("status", model.ChangeOrderStatusRollingBack).Error; err != nil {
		t.Fatalf("置回滚态失败: %v", err)
	}
	// 先塞入观察窗缓冲与停滞观测，验证人工收单会不会带走。
	h.orch.markObserveStarted(order.ID, 1, h.clock)
	h.orch.sampleObserve(order.ID, 1, h.f.nsID, []*model.ChangeTarget{})
	h.orch.stallMu.Lock()
	h.orch.stallByOrder[order.ID] = &deliveryStallState{kind: deliveryStallKindConfirmGate, since: h.clock}
	h.orch.stallMu.Unlock()

	if _, err := h.orch.applyFinishRollback(order.ID, "ops", "ip"); err != nil {
		t.Fatalf("人工结束回滚失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("人工结束回滚后单应 rolled_back，实际 %s", got.Status)
	}
	if _, ok := h.orch.observeByOrder[order.ID]; ok {
		t.Fatal("人工结束回滚后应释放观察窗内存缓冲")
	}
	if _, ok := h.orch.stallByOrder[order.ID]; ok {
		t.Fatal("人工结束回滚后应释放停滞观测")
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

// TestChangeOrderLegacyShortObserveWindowStillEditable 锁定 P1 回归：组合防呆**不得**把存量短窗单变成死单。
// 无条件校验会让历史上合法的「restart + 30s 观察窗」单连改个标题都被 400 拒死，
// 而错误文案只谈观察窗——运维看着标题改不动、也不知道该去改哪个字段，等于把活单变死单。
// 故只对「本次入参触及生效方式 / 健康恶化阈值 / 观察窗三者之一」的请求校验。
func TestChangeOrderLegacyShortObserveWindowStillEditable(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	detail := createDraftOrder(t, f)
	// 先落一个合法组合（窗口 120s），再绕过校验直接把库改成因历史原因带短窗的存量形态。
	if _, err := env.orders.Update(detail.ID, ChangeOrderInput{
		ObserveWindowSec: intPtr(120), UnhealthyRateThresholdPercent: intPtr(30),
	}, "ops", ""); err != nil {
		t.Fatalf("准备短窗存量单失败: %v", err)
	}
	if err := env.db.Model(&model.ChangeOrder{}).Where("id = ?", detail.ID).
		Update("observe_window_sec", 30).Error; err != nil {
		t.Fatalf("改成存量短窗失败: %v", err)
	}

	// 不触及三字段的编辑必须放行：改标题 / 改说明都不该被观察窗拦住。
	updated, err := env.orders.Update(detail.ID, ChangeOrderInput{Title: strPtr("存量短窗单改标题")}, "ops", "")
	if err != nil {
		t.Fatalf("存量短窗单应仍可编辑无关字段，实际被拒: %v", err)
	}
	if updated.Title != "存量短窗单改标题" {
		t.Fatalf("标题应改成功，实际 %q", updated.Title)
	}

	// 但一旦本次动到相关字段，冲突必须暴露（不能因为「存量豁免」而永久隐身）。
	if _, err := env.orders.Update(detail.ID, ChangeOrderInput{ObserveWindowSec: intPtr(45)}, "ops", ""); err == nil {
		t.Fatal("存量短窗单在本次改观察窗时仍应报冲突")
	}
	// 改成 restart 触发冲突 likewise 要拦住。
	if _, err := env.orders.Update(detail.ID, ChangeOrderInput{
		ActivationMethod: strPtr(model.ActivationMethodRestart),
	}, "ops", ""); err == nil {
		t.Fatal("存量短窗单改成 restart 时应报冲突")
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
