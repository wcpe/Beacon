package service

// —— 方案 A 不变量：推进器 tick 持有 s.mu 期间零 DB 访问 ——
//
// 生产事故（2026-10-10 02:41:05）的互等环由两半构成：审批执行「持连接等 mu」× 推进器 tick
// 「持 mu 等连接」；`max-open-conns: 1`（config.example.yml）下没有第三条连接可破环。
// 本组（方案 A）修掉的是后一半：`advanceActiveOrders` 整轮不请求 s.mu（见该函数与 mu 字段的注释）。
//
// 三份用例各锁一段，缺一不可：
//   - delivery_deadlock_repro_test.go         —— 环本体（生产配置单连接池；任一半落地即转绿，故它判不了单侧）；
//   - delivery_deadlock_approval_lock_test.go —— C 半：审批执行不请求 mu；
//   - 本文件                                   —— A 半：tick 持有 s.mu 时零 DB 访问。
//
// 为什么单侧必须独立锁死：复现用例判的是「环」，只修一半它就转绿——若 A 半日后回归（有人给 tick
// 重新加锁，或在 tick 路径里新增持 mu 的调用），只要再出现任何「持连接 → 请求 mu」的路径，
// 环就重新闭合，而复现用例无法指认回归发生在哪一半。本用例把 A 半写成与调度时序无关的硬约束。
//
// 探测机制（gorm 回调，已实测其锚点先于「取连接」触发）：
//   - 六个处理器（Query / Create / Update / Delete / Row / Raw）各挂一个 Before("*") 锚点，
//     排在该处理器回调序列最前，先于取连接触发（写操作在 begin_transaction 之前、查询在 query 之前）。
//     故它捕获的是 **DB 访问意图**：既覆盖拿到连接后的 SQL，也覆盖「已持 mu、正排队等连接」
//     ——后者正是 P0 环里 tick 的那一半；事务内语句同样逐条触发。
//   - 回调运行在发起 DB 调用的同一 goroutine 上，而 sync.Mutex 不可重入，故 TryLock 失败
//     ⟺「这个 goroutine 此刻正持有 s.mu」。TryLock 成功时探针必须立刻释放，避免扰动外层语义。
//   - 只读访问同样计入：不变量管的是 DB 访问本身，「持 mu 等连接池」与读还是写无关。
//
// 作用域：只判推进器 tick（窗口内只跑 tick）。控制操作（Pause / Resume / Cancel / Rollback /
// FinishRollback / ConfirmBatch / applyStart）允许持 mu 做 DB，属设计内行为——它们与审批执行
// 路径的互等已由方案 C 消除「持连接请求 mu」而解除，故这些调用**刻意置于观察窗之外**。

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// tickMuWatch 是「tick 持 mu 期间零 DB 访问」的观测器（机制见文件头）。
type tickMuWatch struct {
	orch *DeliveryOrchestrator
	// active 是观察窗开关：只有窗口内的 DB 访问计入（窗外有夹具装配、控制操作与测试侧断言，不属 tick）。
	active atomic.Bool
	// access 是探针观测到的 DB 访问次数（六处理器合计）。
	access atomic.Int64
	// violations 是「当前 goroutine 持有 s.mu 时发起 DB 访问」的次数，即不变量破坏计数。
	violations atomic.Int64
	// site 记录首个违规的调用点（tick 侧发起该 DB 访问的函数），失败时用于指认回归位置。
	site atomic.Value
}

// registerTickMuWatch 在六个处理器上各挂一个 Before("*") 探针，返回观测器。
func registerTickMuWatch(t *testing.T, h *orchestratorHarness) *tickMuWatch {
	t.Helper()
	w := &tickMuWatch{orch: h.orch}
	probe := func(*gorm.DB) {
		if !w.active.Load() {
			return
		}
		w.access.Add(1)
		if !h.orch.mu.TryLock() {
			// 锁被当前 goroutine 持有（sync.Mutex 不可重入）→ 不变量破坏。
			// 此时**不得**尝试解锁：锁属于外层持有者，代为释放会破坏其解锁语义。
			if w.violations.Add(1) == 1 {
				w.site.Store(tickMuWatchCallSite())
			}
			return
		}
		// 未持锁：探针自己拿到了锁，立即放回，不引入可观测的状态变化。
		h.orch.mu.Unlock()
	}
	if err := h.env.db.Callback().Query().Before("*").Register("tick-mu-watch:query", probe); err != nil {
		t.Fatalf("注册 query 探针失败: %v", err)
	}
	if err := h.env.db.Callback().Create().Before("*").Register("tick-mu-watch:create", probe); err != nil {
		t.Fatalf("注册 create 探针失败: %v", err)
	}
	if err := h.env.db.Callback().Update().Before("*").Register("tick-mu-watch:update", probe); err != nil {
		t.Fatalf("注册 update 探针失败: %v", err)
	}
	if err := h.env.db.Callback().Delete().Before("*").Register("tick-mu-watch:delete", probe); err != nil {
		t.Fatalf("注册 delete 探针失败: %v", err)
	}
	if err := h.env.db.Callback().Row().Before("*").Register("tick-mu-watch:row", probe); err != nil {
		t.Fatalf("注册 row 探针失败: %v", err)
	}
	if err := h.env.db.Callback().Raw().Before("*").Register("tick-mu-watch:raw", probe); err != nil {
		t.Fatalf("注册 raw 探针失败: %v", err)
	}
	return w
}

// phase 在观察窗内执行一段推进（通常是 h.tick），窗口关闭后校验不变量：
//   - 零「持 mu 发起 DB 访问」违规（本用例的全部证明力所在）；
//   - expectAccess=true 时窗口内必须有 DB 访问——否则「零违规」只是判据空洞，说明场景没把该分支跑起来。
//
// 阶段前置断言（targets 状态、单状态等）是场景不空洞的主防线，此处是兜底。
func (w *tickMuWatch) phase(t *testing.T, name string, expectAccess bool, fn func()) {
	t.Helper()
	beforeAccess, beforeViolations := w.access.Load(), w.violations.Load()
	w.active.Store(true)
	func() {
		defer w.active.Store(false)
		fn()
	}()
	access, violations := w.access.Load()-beforeAccess, w.violations.Load()-beforeViolations
	if violations > 0 {
		t.Fatalf("推进器 tick 在持有 s.mu 期间发起 DB 访问 %d 次（阶段「%s」）——P0 死锁环 tick 侧的一半复活：\n"+
			"  首个违规调用点：%v\n"+
			"  该调用发生时 tick 正持有 s.mu：max-open-conns=1 下它随即在连接池排队，一旦存在\n"+
			"  「持连接 → 请求 mu」的路径（生产事故里是审批执行），两资源反序获取即永久互等。\n"+
			"  修复方向：把这一 DB 访问移出 s.mu——推进器全程不持本锁，互斥下沉到各写点 CAS 前态\n"+
			"  （见 delivery_orchestrator.go 中 mu 字段与 advanceActiveOrders 注释；\n"+
			"  .claude/rules/testing-and-quality.md §3「DB IO 一律在锁外」）。",
			violations, name, w.site.Load())
	}
	if expectAccess && access == 0 {
		t.Fatalf("阶段「%s」窗口内零 DB 访问：判据空洞（没有访问自然没有违规），请复查场景是否真的跑到了该分支", name)
	}
	t.Logf("阶段「%s」：DB 访问 %d 次、持 mu 违规 0 次", name, access)
}

// tickMuWatchCallSite 从回调栈里取首个属于推进代码（非测试文件）的调用帧，指认违规发生处。
func tickMuWatchCallSite() string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs) // 跳过 Callers 与探针闭包自身
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if strings.Contains(f.File, "/internal/service/") && !strings.HasSuffix(f.File, "_test.go") {
			return fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Function)
		}
		if !more {
			return "未识别（栈内无 internal/service 非测试帧）"
		}
	}
}

// TestOrchestratorTickNeverTouchesDBWhileHoldingMutex 锁定方案 A 不变量，并驱动 tick 的主要
// 分支逐段校验：rolling 下发 / 回执收口 / 观察窗到点 / 暂停期收口 / 失败率熔断 / 整单回滚 /
// 目标级（子集）回滚 / payload 准备。每个阶段都要求窗口内确有 DB 访问，确保校验不是空转。
//
// 开头先做检测器校准（不持 mu 不误报、持 mu 必捕获）：没有校准，「零违规」无法排除
// 「探针根本没接线」这种假绿。
func TestOrchestratorTickNeverTouchesDBWhileHoldingMutex(t *testing.T) {
	h := newOrchestratorHarness(t)
	w := registerTickMuWatch(t, h)

	// —— 检测器校准：先证明它有牙，其「零违规」结论才可信 ——
	w.active.Store(true)
	var probeRows int64
	if err := h.env.db.Model(&model.ChangeOrder{}).Count(&probeRows).Error; err != nil {
		t.Fatalf("校准查询失败: %v", err)
	}
	if w.access.Load() == 0 {
		t.Fatal("探针未接线：校准查询没有触发任何处理器回调，后续「零违规」不具证明力")
	}
	if v := w.violations.Load(); v != 0 {
		t.Fatalf("检测器误报：不持 mu 的 DB 访问被记为违规 %d 次", v)
	}
	h.orch.mu.Lock()
	probeErr := h.env.db.Model(&model.ChangeOrder{}).Count(&probeRows).Error
	h.orch.mu.Unlock()
	if probeErr != nil {
		t.Fatalf("校准确证查询失败: %v", probeErr)
	}
	if w.violations.Load() == 0 {
		t.Fatal("检测器没有牙：持 mu 期间发起 DB 访问未被捕获，「零违规」结论不具证明力")
	}
	w.active.Store(false)
	w.access.Store(0)
	w.violations.Store(0)

	// —— 阶段 1：rolling 单全链（下发 → 回执收口 → 观察窗到点）——
	orderA := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(orderA.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	w.phase(t, "rolling 批下发（dispatchPending 事务）", true, func() { h.tick() })
	if c := h.targetStatuses(orderA.ID); c[model.ChangeTargetStatusPushing] != 2 {
		t.Fatalf("前置失效：首批未下发 2 目标 pushing: %v", c)
	}
	h.completeAllPushes(t, orderA.ID)
	w.phase(t, "推送回执收口（reconcile + 计数 + 批转 observing）", true, func() { h.tick() })
	if c := h.targetStatuses(orderA.ID); c[model.ChangeTargetStatusActivated] != 2 {
		t.Fatalf("前置失效：回执未收口 2 目标 activated: %v", c)
	}
	h.advance(6 * time.Second)
	w.phase(t, "观察窗到点转 awaiting_confirm", true, func() { h.tick() })
	// 控制操作放观察窗外：确认末批 → 单 completed。
	if _, err := h.orch.applyConfirmBatch(orderA.ID, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认末批失败（前置失效：未到确认门?）: %v", err)
	}
	if got := h.reload(orderA.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("前置失效：末批确认后单应 completed: %s", got.Status)
	}

	// —— 阶段 2：暂停期在途收口（advanceOrder 的 paused 分支：只收口、不下发）——
	orderB := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(orderB.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	w.phase(t, "暂停前批下发", true, func() { h.tick() })
	if _, err := h.orch.Pause(orderB.ID, "ops", "ip"); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	h.completeAllPushes(t, orderB.ID)
	w.phase(t, "暂停期在途目标收口", true, func() { h.tick() })
	if c := h.targetStatuses(orderB.ID); c[model.ChangeTargetStatusActivated] != 2 {
		t.Fatalf("前置失效：暂停期在途目标未收口 activated: %v", c)
	}
	// 释放目标：paused 单仍占着启动冲突守卫的活动位（deliveryStartConflictStatuses），
	// 不终止它后续阶段无法复用同一批夹具目标。控制操作放在观察窗外（见文件头作用域说明）。
	if _, err := h.orch.Cancel(orderB.ID, "阶段结束释放目标", "ops", "ip"); err != nil {
		t.Fatalf("终止失败: %v", err)
	}

	// —— 阶段 3：失败率熔断（tripBreaker 事务：批 CAS + 单 CAS + 未下发目标 skipped + 审计）——
	orderC := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 50)
	if _, err := h.orch.applyStart(orderC.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	w.phase(t, "熔断前批下发", true, func() { h.tick() })
	completeDeliveryCommand(t, h.env.db, orderC.ID, "t-1", model.CommandTypeDeliveryPush, model.CommandStatusDone, pushResult)
	completeDeliveryCommand(t, h.env.db, orderC.ID, "t-2", model.CommandTypeDeliveryPush, model.CommandStatusFailed, `{"error":"落盘失败"}`)
	w.phase(t, "失败率熔断（tripBreaker 事务）", true, func() { h.tick() })
	gotC := h.reload(orderC.ID)
	if gotC.Status != model.ChangeOrderStatusPaused || gotC.PauseKind != model.PauseKindCircuitBreak {
		t.Fatalf("前置失效：应熔断暂停（circuit_break）: %+v", gotC)
	}
	if _, err := h.orch.Cancel(orderC.ID, "阶段结束释放目标", "ops", "ip"); err != nil {
		t.Fatalf("终止失败: %v", err)
	}

	// —— 阶段 4：整单回滚（rolling_back 分支：下发 → 收口 → autoFinish）——
	orderD := h.completedPushOnlyOrder(t)
	if _, err := h.orch.applyRollback(orderD.ID, "回退变更", "ops", "ip"); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	w.phase(t, "整单回滚命令下发（dispatchRollback 事务）", true, func() { h.tick() })
	if got := h.rollbackTargetsByServer(orderD.ID)["t-1"].RollbackStatus; got != model.RollbackStatusRunning {
		t.Fatalf("前置失效：整单回滚命令未下发: %s", got)
	}
	h.completeAllRollbacks(t, orderD.ID, model.CommandStatusDone)
	w.phase(t, "整单回滚收口与自动完成", true, func() { h.tick() })
	if got := h.reload(orderD.ID); got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("前置失效：整单回滚应 rolled_back: %s", got.Status)
	}

	// —— 阶段 5：目标级（子集）回滚（advanceTargetRollbacks 独立扫描，不改单主状态）——
	orderE := h.completedPushOnlyOrder(t)
	if err := h.runRollbackTargets(t, orderE.ID, []string{"t-1"}, "单台回滚"); err != nil {
		t.Fatalf("目标级回滚失败: %v", err)
	}
	w.phase(t, "目标级回滚下发", true, func() { h.tick() })
	h.completeAllRollbacks(t, orderE.ID, model.CommandStatusDone)
	w.phase(t, "目标级回滚收口与记录写回", true, func() { h.tick() })
	if got := h.rollbackTargetsByServer(orderE.ID)["t-1"].RollbackStatus; got != model.RollbackStatusRolledBack {
		t.Fatalf("前置失效：目标级回滚应收口 rolled_back: %s", got)
	}

	// —— 阶段 6：payload 准备分支（spec §4.4.2：缺失 blob 等上传 / 上传终态仍缺 → prepare_failed）——
	detail := createDraftOrder(t, h.f)
	sha := repeatHex("bc", 64)
	action := model.ChangeItemActionAdd
	p, s, size := "plugins/x.jar", sha, int64(64)
	mustCreate(t, h.env.db, &model.ChangeOrderItem{
		OrderID: detail.ID, Kind: model.ChangeItemKindFileDiff, Path: &p, Action: &action, SHA256: &s, SizeBytes: &size,
	})
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", detail.ID).
		Updates(map[string]any{"status": model.ChangeOrderStatusApproved, "batch_sizes": encodeBatchSizes([]int{100}),
			"activation_method": model.ActivationMethodPushOnly}).Error; err != nil {
		t.Fatalf("置单为 approved 失败: %v", err)
	}
	if _, err := h.orch.applyStart(detail.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	w.phase(t, "payload 缺失、上传在途（只读等待分支）", true, func() { h.tick() })
	if got := h.reload(detail.ID); got.PayloadState != model.PayloadStateUploading {
		t.Fatalf("前置失效：缺失 blob 应 uploading: %s", got.PayloadState)
	}
	completeDeliveryCommand(t, h.env.db, detail.ID, "src-1", model.CommandTypeDeliveryUpload, model.CommandStatusDone, "")
	w.phase(t, "上传终态仍缺 blob → prepare_failed 暂停", true, func() { h.tick() })
	gotF := h.reload(detail.ID)
	if gotF.Status != model.ChangeOrderStatusPaused || gotF.PauseKind != model.PauseKindPrepareFailed {
		t.Fatalf("前置失效：应准备失败暂停: %+v", gotF)
	}

	t.Logf("全部阶段合计：DB 访问 %d 次、持 mu 违规 0 次（tick 全程锁外）", w.access.Load())
}
