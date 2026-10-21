package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/alert"
)

// recordingAlerter 记录扇出的告警，供断言「何时发、发什么」。
//
// 必须支持异步收集：交付编排改走 DispatchAsync（告警是旁路，不得阻塞推进器主循环），
// 因此断言要等 goroutine 落定，否则测试会因时序竞争变成 flaky——这种「有时过有时不过」
// 的测试比没有更糟，它会把真回归淹掉。
type recordingAlerter struct {
	mu     sync.Mutex
	alerts []alert.Alert
	ch     chan alert.Alert
}

// newRecordingAlerter 构造带通知通道的记录器（通道容量足够，不会阻塞被测代码）。
func newRecordingAlerter() *recordingAlerter {
	return &recordingAlerter{ch: make(chan alert.Alert, 16)}
}

// DispatchAsync 异步扇出：记下并通知等待方（与生产实现同语义）。
func (r *recordingAlerter) DispatchAsync(_ context.Context, a alert.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
	select {
	case r.ch <- a:
	default: // 容量足够时不会走到这里；真满了也只是少一次通知，不阻塞被测代码
	}
}

// snapshot 返回当前已收集的告警副本（并发安全）。
func (r *recordingAlerter) snapshot() []alert.Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]alert.Alert, len(r.alerts))
	copy(out, r.alerts)
	return out
}

// waitOne 等一条告警落定；超时即失败（异步扇出必须真的发生，不能靠「稍后自然出现」）。
func (r *recordingAlerter) waitOne(t *testing.T) alert.Alert {
	t.Helper()
	select {
	case a := <-r.ch:
		return a
	case <-time.After(2 * time.Second):
		t.Fatalf("等待告警扇出超时（2s），已收集 %d 条", len(r.snapshot()))
		return alert.Alert{}
	}
}

// assertNone 断言在给定窗口内没有任何告警（用于「不得扇出」的负向断言）。
func (r *recordingAlerter) assertNone(t *testing.T, window time.Duration) {
	t.Helper()
	select {
	case a := <-r.ch:
		t.Fatalf("不应扇出告警，实际收到 %+v", a)
	case <-time.After(window):
	}
}

// TestEmitDeliveryFailAlertOnFailedTarget 断言迁到 failed 时扇出 publish-fail 告警（FR-275）。
//
// 告警必须带 orderId / serverId / 失败原因，且类型是 publish-fail —— 否则运维无法定位
// 「哪张单、哪台服、为什么失败」。
func TestEmitDeliveryFailAlertOnFailedTarget(t *testing.T) {
	rec := newRecordingAlerter()
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

	got := rec.waitOne(t)
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
	// 恰好一条：异步扇出不得重复投递（重复会让运维收到两次同样的失败）。
	rec.assertNone(t, 100*time.Millisecond)
}

// TestEmitDeliveryFailAlertMessageTruncated 断言超长原因被截断到列宽内，且明细不丢。
//
// alert_event.message 是 size:512 not null：超列宽在 MySQL 严格模式下会让整条告警写入失败，
// 而 Dispatcher 只记 WARN——告警会在最需要它的那一次静默消失。
func TestEmitDeliveryFailAlertMessageTruncated(t *testing.T) {
	rec := newRecordingAlerter()
	s := &DeliveryOrchestrator{
		alerter: rec,
		now:     func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
	}
	rt := &orderRuntime{nsCode: "prod", order: &model.ChangeOrder{}}
	rt.order.ID = 7
	target := &model.ChangeTarget{ServerID: "lobby-1", Status: model.ChangeTargetStatusFailed}

	// 造一条远超 512 字符的原因（模拟多插件失败明细拼接）。
	long := strings.Repeat("插件失败：解析配置出错；", 80)
	s.emitDeliveryFailAlert(rt, target, map[string]any{
		"error":         long,
		"reload_report": `{"total":80,"failedCount":80}`,
	})

	got := rec.waitOne(t)
	if n := len([]rune(got.Message)); n > alertMessageMaxRunes {
		t.Fatalf("message 超列宽：%d > %d", n, alertMessageMaxRunes)
	}
	if !strings.Contains(got.Message, "明细见 detail") {
		t.Fatalf("截断后须指明明细去向，实际 message=%q", got.Message)
	}
	// 明细仍完整在 detail 里：截断只压摘要句，不压事实。
	if _, ok := got.Detail["reloadReport"]; !ok {
		t.Fatal("截断 message 不得丢掉 detail 里的逐插件明细")
	}
}

// TestEmitDeliveryFailAlertDoesNotBlockCaller 断言告警扇出不阻塞调用方（旁路不得拖住推进器主循环）。
//
// 用一条永不返回的通道模拟「webhook 卡住」，emitDeliveryFailAlert 必须立刻返回，
// 否则一轮 tick 里多个失败目标的超时会累加，把其余目标与全部活动单的推进一起拖住。
func TestEmitDeliveryFailAlertDoesNotBlockCaller(t *testing.T) {
	blocking := &blockingAlerter{release: make(chan struct{}), started: make(chan struct{})}
	defer close(blocking.release)
	s := &DeliveryOrchestrator{alerter: blocking, now: time.Now}
	rt := &orderRuntime{nsCode: "prod", order: &model.ChangeOrder{}}
	target := &model.ChangeTarget{ServerID: "lobby-1", Status: model.ChangeTargetStatusFailed}

	done := make(chan struct{})
	go func() {
		s.emitDeliveryFailAlert(rt, target, map[string]any{"error": "失败"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("告警扇出阻塞了调用方：主循环会被 webhook 超时拖住")
	}
	// 顺带确认投递动作真的被启动了（不是「因为根本没发所以没阻塞」的假通过）。
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("告警投递未被启动：调用方没阻塞只是因为它压根没发")
	}
}

// blockingAlerter 模拟「通道内部很慢」的告警器：DispatchAsync 本身按契约立即返回，
// 慢的是它派生的投递动作。
//
// 注意假件的语义必须与生产实现一致：生产 Dispatcher.DispatchAsync 是「起 goroutine 后立刻返回」，
// 假件若自己同步阻塞，测出来的就不是被测代码的问题，而是假件写错了。
type blockingAlerter struct {
	release chan struct{}
	started chan struct{}
}

// DispatchAsync 起 goroutine 后立即返回；投递动作会阻塞到 release 关闭。
func (b *blockingAlerter) DispatchAsync(_ context.Context, _ alert.Alert) {
	go func() {
		close(b.started)
		<-b.release
	}()
}

// TestEmitDeliveryFailAlertSkippedWhenNotWired 断言未装配告警器时静默跳过（旁路不得阻断交付）。
func TestEmitDeliveryFailAlertSkippedWhenNotWired(_ *testing.T) {
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

// TestFailRollbackWithResultPersistsReloadReport 回滚失败要落 reload_report 并告警。
//
// 回滚同样在改线上文件，失败就是交付失败；只回一句「回滚失败」等于把「哪个插件重载失败」的
// 根因丢掉，运维无从处置。这条锁定：明细必须落库、告警必须发出。
func TestFailRollbackWithResultPersistsReloadReport(t *testing.T) {
	rec := newRecordingAlerter()
	h := newOrchestratorHarness(t)
	h.orch.alerter = rec

	// 先把单走到 completed（回滚状态机要求单已生效完成，否则 applyRollbackTargetsInTx 会拒）。
	order := h.completedPushOnlyOrder(t)
	// casRollback 只接受从 running 迁移，故先走真实回滚入口把目标置为 running
	// （用生产同款 applyRollbackTargetsInTx，而不是测试里手改状态——手改会掩盖状态机约束）。
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "回滚验证"); err != nil {
		t.Fatalf("发起回滚失败: %v", err)
	}
	// 回滚目标先入 pending，推进一轮才下发改令并转 running（见 reconcileRollback 的分支）。
	h.tick()

	rt, err := h.orch.loadOrderRuntime(h.reload(order.ID))
	if err != nil {
		t.Fatalf("装载单运行时失败: %v", err)
	}
	target := targetByServer(t, h, order.ID, "t-1")
	if target.RollbackStatus != model.RollbackStatusRunning {
		t.Fatalf("夹具前置状态应为 running，实际 %s", target.RollbackStatus)
	}

	ok := h.orch.failRollbackWithResult(rt, &target,
		"回滚时 1/1 插件重载失败：top.wcpe.ReloadHook: 解析失败",
		deliveryCmdResult{Reload: &deliveryReloadReport{
			Total: 1, OKCount: 0, FailedCount: 1,
			Plugins: []deliveryReloadPlugin{{Listener: "top.wcpe.ReloadHook", Status: "failed", Reason: "解析失败"}},
		}})
	if !ok {
		t.Fatal("回滚失败迁移应命中 CAS")
	}

	got := targetByServer(t, h, order.ID, "t-1")
	if got.RollbackStatus != model.RollbackStatusFailed {
		t.Fatalf("回滚状态应为 failed，实际 %s", got.RollbackStatus)
	}
	if got.ReloadReport == "" {
		t.Fatal("回滚失败必须落 reload_report，否则运维看不到是哪个插件失败")
	}
	if !strings.Contains(got.ReloadReport, "top.wcpe.ReloadHook") {
		t.Fatalf("reload_report 须含失败插件标识，实际 %s", got.ReloadReport)
	}
	if got.RollbackError == "" {
		t.Fatal("回滚失败必须落 rollback_error")
	}
	// 告警必须发出（publish-fail 逐条可见）。
	alertGot := rec.waitOne(t)
	if alertGot.EventType() != alert.TypePublishFail {
		t.Fatalf("回滚失败也应扇出 publish-fail，实际 %q", alertGot.EventType())
	}
	if _, ok := alertGot.Detail["reloadReport"]; !ok {
		t.Fatal("回滚失败告警的 detail 须含逐插件明细")
	}
}
