package service

// —— 第二批不变量：交付控制操作（Pause / Cancel）持有 s.mu 期间零 DB 访问 ——
//
// 生产事故（2026-10-10 02:41:05）的互等环由两半构成，已分别由前两批用例锁死：
//   - tick 侧「持 mu 等连接」   → delivery_deadlock_tick_no_db_test.go（方案 A）
//   - 审批侧「持连接等 mu」     → delivery_deadlock_approval_lock_test.go（方案 C）
//
// 但控制操作（Pause / Cancel）当时仍是「持 mu 做 DB」的形态，只是它们与审批侧构成环需要
// 「审批正持连接等 mu」同时成立、且二者抢的是同一张单的迁移权，故生产上未先炸。
// 它们**同为 HTTP 直连入口**（`POST /change-orders/{id}/pause` / `.../cancel`，requireFullRole），
// 只要有一个持锁方在等连接，这些端点就会一起挂起——正是「所有需 DB 的端点永久挂起」的放大器。
// 本文件把这两个函数纳入同一套观察窗（此前被刻意置于窗外，见 tick 用例文件头的作用域说明），
// 断言它们也不在持 mu 时发起 DB 访问。
//
// 探测机制与前批**同一个实现**（复用 registerTickMuWatch → 出厂 AttachLockDBGuard /
// lockguard.Watcher：六个处理器各自的 Before("*") 锚点 + 持有者 goroutine id 精确归属），
// 因此同样具备「先证有牙、再判零违规」的判别力：未做该校准的「零违规」无法排除「探针根本没接线」。
//
// 为什么强调「同一个实现」（P2-2）：本文件先前自造 TryLock 探针，与出厂守卫是两套判据——
// 自造探针的「零违规」无法推出出厂守卫也不会报警。改用出厂实现后，用例结论与生产行为同源。

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/pkg/lockguard"
)

// TestControlOpsNeverTouchDBWhileHoldingMutex 锁定 Pause / Cancel 的「持 mu 期间零 DB 访问」。
//
// 判别力说明（为什么本用例能识别未修复的实现）：守卫在 DB 访问发起前判定当前 goroutine 是否正是
// s.mu 的持有者；修复前的 Pause / Cancel 从进入函数到 return 全程持锁，其内部任何一次查库/事务
// 都会被记为违规。已实测反向验证：给 Pause 加回 `s.mu.Lock()` 后本用例立即失败并精确指认违规调用点。
// 用例开头先做正反校准（不持锁不误报、持锁必捕获），排除「探针没接线」的假绿。
func TestControlOpsNeverTouchDBWhileHoldingMutex(t *testing.T) {
	h := newOrchestratorHarness(t)
	w := registerTickMuWatch(t, h)

	// —— 检测器校准：与 tick 用例同款，先证明有牙 ——
	lockguard.SetEnabled(true)
	var probeRows int64
	if err := h.env.db.Model(&model.ChangeOrder{}).Count(&probeRows).Error; err != nil {
		t.Fatalf("校准查询失败: %v", err)
	}
	if w.guard.Accesses() == 0 {
		t.Fatal("探针未接线：校准查询没有触发任何处理器回调，后续「零违规」不具证明力")
	}
	if v := w.guard.Violations(); v != 0 {
		t.Fatalf("检测器误报：不持 mu 的 DB 访问被记为违规 %d 次", v)
	}
	h.orch.mu.Lock()
	probeErr := h.env.db.Model(&model.ChangeOrder{}).Count(&probeRows).Error
	h.orch.mu.Unlock()
	if probeErr != nil {
		t.Fatalf("校准确证查询失败: %v", probeErr)
	}
	if w.guard.Violations() == 0 {
		t.Fatal("检测器没有牙：持 mu 期间发起 DB 访问未被捕获，「零违规」结论不具证明力")
	}
	lockguard.SetEnabled(false)
	w.guard.Reset()

	// —— 阶段 1：Pause（rolling → paused）——
	// 观察窗刻意只包住 Pause 调用本身：窗口外是夹具装配（建单/启动），不属被测代码。
	orderA := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(orderA.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	w.phase(t, "Pause 持锁期间零 DB 访问", true, func() {
		if _, err := h.orch.Pause(orderA.ID, "ops", "ip"); err != nil {
			t.Fatalf("暂停失败: %v", err)
		}
	})
	// 前置断言：确认本阶段真的走完了迁移（否则「零违规」可能只是提前报错返回、根本没碰库）。
	if got := h.reload(orderA.ID); got.Status != model.ChangeOrderStatusPaused || got.PauseKind != model.PauseKindManual {
		t.Fatalf("前置失效：应人工暂停: %+v", got)
	}

	// —— 阶段 2：Cancel（paused → cancelled，含在途目标收口与批收尾）——
	// 从 paused 走 Cancel 可覆盖「rolling|paused → cancelled」的两个前态之一，且此时单内有
	// pending 批/目标，能触发 settleInFlightTargets 与两条批量收口更新（都是本函数内的 DB 访问）。
	w.phase(t, "Cancel 持锁期间零 DB 访问", true, func() {
		if _, err := h.orch.Cancel(orderA.ID, "误发布", "ops", "ip"); err != nil {
			t.Fatalf("终止失败: %v", err)
		}
	})
	if got := h.reload(orderA.ID); got.Status != model.ChangeOrderStatusCancelled {
		t.Fatalf("前置失效：应 cancelled: %+v", got)
	}

	// —— 阶段 3：Cancel 的在途收口路径（这层才是本函数 DB 访问最重的分支）——
	// 阶段 2 的目标还都是 pending，只走到「置 skipped」；这里先把目标推到在途，
	// 确保 settleInFlightTargets 的多轮条件批量更新真的执行（否则该分支从未被观察）。
	orderB := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(orderB.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	if c := h.targetStatuses(orderB.ID); c[model.ChangeTargetStatusPushing] != 2 {
		t.Fatalf("前置失效：首批未下发 2 目标 pushing: %v", c)
	}
	w.phase(t, "Cancel 收口在途目标期间零 DB 访问", true, func() {
		if _, err := h.orch.Cancel(orderB.ID, "收口验证", "ops", "ip"); err != nil {
			t.Fatalf("终止失败: %v", err)
		}
	})
	if c := h.targetStatuses(orderB.ID); c[model.ChangeTargetStatusFailed] != 2 {
		t.Fatalf("前置失效：在途目标应收口 failed: %v", c)
	}

	t.Logf("Pause / Cancel 合计：DB 访问 %d 次、持 mu 违规 0 次（控制操作全程锁外）", w.guard.Accesses())
}
