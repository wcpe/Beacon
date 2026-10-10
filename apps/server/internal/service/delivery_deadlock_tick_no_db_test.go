package service

// —— 方案 A 不变量：推进器 tick 持有 s.mu 期间零 DB 访问 ——
//
// 生产事故（2026-10-10 02:41:05）的互等环由两半构成：审批执行「持连接等 mu」× 推进器 tick
// 「持 mu 等连接」；事故当时的部署是单连接池（`max-open-conns: 1`），池内没有第三条连接可破环。
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
// 探测机制（**出厂守卫**，P2-2 后不再自造）：
//   - 观测面就是生产的 `AttachLockDBGuard` → `lockguard.Watcher`：在六个处理器
//     （Query / Create / Update / Delete / Row / Raw）各挂一个 Before("*") 锚点，
//     排在该处理器回调序列最前，先于取连接触发（写操作在 begin_transaction 之前、查询在 query 之前）。
//     故它捕获的是 **DB 访问意图**：既覆盖拿到连接后的 SQL，也覆盖「已持 mu、正排队等连接」
//     ——后者正是 P0 环里 tick 的那一半；事务内语句同样逐条触发。
//   - 归属判据是**持有者 goroutine id 精确比对**（非 TryLock）：回调运行在发起 DB 调用的同一
//     goroutine 上，故「本 goroutine 是否正是 s.mu 的持有者」可被精确回答。
//     此前用例自造 TryLock 探针与出厂实现判据分叉，会把「他人持锁 + 本 goroutine 做 DB」
//     误报为违规（本仓并发是常态）；改用出厂守卫后，用例的「零违规」与生产的「不报警」同源。
//   - 只读访问同样计入：不变量管的是 DB 访问本身，「持 mu 等连接池」与读还是写无关。
//
// 作用域：本文件只判推进器 tick（窗口内只跑 tick）。
//
// 控制操作的当前状态（P2-3 订正，勿沿用「它们允许持 mu 做 DB」的旧表述）：
//   - **Pause / Cancel 已不持 mu**（第二批去锁，见 delivery_orchestrator_control.go 的 Pause/Cancel
//     注释）。它们的不变量由 delivery_deadlock_control_no_db_test.go 单独锁定，**刻意置本次窗外**——
//     本文件的窗口语义是「这一轮 tick 内」，把别的入口一并圈进来会让失败信息无法指认是哪条路径回归。
//   - 仍持 mu 的是 applyStart / applyResume / applyRollback / applyFinishRollback / applyConfirmBatch
//     （生产入口全走 `*InTx` 变体、对同一把锁只持有一层；直接调用它们的只有同包测试）。它们同样在窗外。
//
// 窗口的开关即 lockguard.SetEnabled（见 phase）。

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/pkg/lockguard"
)

// tickMuWatch 是「tick 持 mu 期间零 DB 访问」的观测器（机制见文件头）。
//
// **复用出厂守卫（lockguard.Watcher）而不是自造探针**（P2-2 的处置）：
// 本组用例此前自造了一个基于 `sync.Mutex.TryLock` 的探针，与出厂的 `AttachLockDBGuard`
// 是**两套不同机制**——判据分叉会让「用例通过」与「生产不报警」同时成立：
//
//	TryLock 失败只说明锁被「某人」持有，不能说明被「自己」持有，于是「A 持锁做非 DB 工作、
//	B goroutine 做 DB 访问」会被自造探针误报为违规（本仓并发是常态）；
//	而出厂实现按持有者 goroutine id 精确比对，不会误报。
//
// 两者的**误报方向不同**，于是：自造探针的「零违规」不能推出出厂守卫也不会报警，
// 反之亦然。为消除这一分叉，本观测器改为直接读出厂 Watcher 的计数（经 orch.LockGuard()），
// 保留的只是「观察窗」（窗口语义是测试专属需求，出厂守卫按全局开关生效）。
type tickMuWatch struct {
	// guard 是出厂守卫句柄（装配后非 nil）。
	guard *lockguard.Watcher
}

// registerTickMuWatch 装上**出厂守卫**并返回观测器。
//
// 为什么不再自挂回调：出厂守卫已经把六个处理器全覆盖（由 lockguard 包自己的用例保证），
// 测试再挂一套只会新增一个「测试通过但生产漏报」的缝隙。
func registerTickMuWatch(t *testing.T, h *orchestratorHarness) *tickMuWatch {
	t.Helper()
	h.orch.AttachLockDBGuard()
	if h.orch.LockGuard() == nil {
		t.Fatal("出厂守卫未装配（AttachLockDBGuard 内部失败只记 WARN、不报错，故须显式断言）——" +
			"本用例的观测面依赖它，未装即判据空洞")
	}
	return &tickMuWatch{guard: h.orch.LockGuard()}
}

// snapshot 读当前累计计数。
func (w *tickMuWatch) snapshot() (access, violations int64) {
	return w.guard.Accesses(), w.guard.Violations()
}

// phase 在观察窗内执行一段推进（通常是 h.tick），窗口关闭后校验不变量：
//   - 零「持 mu 发起 DB 访问」违规（本用例的全部证明力所在）；
//   - expectAccess=true 时窗口内必须有 DB 访问——否则「零违规」只是判据空洞，说明场景没把该分支跑起来。
//
// 阶段前置断言（targets 状态、单状态等）是场景不空洞的主防线，此处是兜底。
func (w *tickMuWatch) phase(t *testing.T, name string, expectAccess bool, fn func()) {
	t.Helper()
	beforeAccess, beforeViolations := w.snapshot()
	// 观察窗 = 出厂守卫的全局开关：窗口外关闭，使夹具装配与控制操作不计入（它们的持锁 DB 属设计内行为）。
	lockguard.SetEnabled(true)
	func() {
		defer lockguard.SetEnabled(false)
		fn()
	}()
	afterAccess, afterViolations := w.snapshot()
	access, violations := afterAccess-beforeAccess, afterViolations-beforeViolations
	if violations > 0 {
		t.Fatalf("推进器 tick 在持有 s.mu 期间发起 DB 访问 %d 次（阶段「%s」）——P0 死锁环 tick 侧的一半复活：\n"+
			"  首个违规调用点：%v\n"+
			"  该调用发生时 tick 正持有 s.mu：连接池被占满时它随即排队，一旦存在\n"+
			"  「持连接 → 请求 mu」的路径（生产事故里是审批执行），两资源反序获取即永久互等。\n"+
			"  修复方向：把这一 DB 访问移出 s.mu——推进器全程不持本锁，互斥下沉到各写点 CAS 前态\n"+
			"  （见 delivery_orchestrator.go 中 mu 字段与 advanceActiveOrders 注释；\n"+
			"  .claude/rules/testing-and-quality.md §3「DB IO 一律在锁外」）。",
			violations, name, w.guard.Site())
	}
	if expectAccess && access == 0 {
		t.Fatalf("阶段「%s」窗口内零 DB 访问：判据空洞（没有访问自然没有违规），请复查场景是否真的跑到了该分支", name)
	}
	t.Logf("阶段「%s」：DB 访问 %d 次、持 mu 违规 0 次", name, access)
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
	// 校准完毕：关窗并清零（后续每个阶段各自取快照差值，清零让日志里的累计值可读）。
	lockguard.SetEnabled(false)
	w.guard.Reset()

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

	t.Logf("全部阶段合计：DB 访问 %d 次、持 mu 违规 0 次（tick 全程锁外）", w.guard.Accesses())
}
