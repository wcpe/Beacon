package service

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestEmitDeliveryFailAlertCarriesRollbackReason 锁死护栏：回滚失败的告警必须带出失败原因。
//
// 回滚路径把原因落在 updates["rollback_error"] 而非 error。若告警靠按固定键名反查 updates，
// 回滚失败会退化成「交付失败（未提供原因）」——把唯一的根因线索丢掉，运维只能翻交付单。
// 因此 reason 由调用方显式传入，本用例守住这条约束。
func TestEmitDeliveryFailAlertCarriesRollbackReason(t *testing.T) {
	rec := newRecordingAlerter()
	s := &DeliveryOrchestrator{
		alerter: rec,
		now:     func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
	}
	rt := &orderRuntime{nsCode: "prod", order: &model.ChangeOrder{}}
	rt.order.ID = 99
	// 回滚失败时生效状态仍是 activated，回滚状态为 failed：两者都要呈现，避免被读成「已生效却报失败」。
	target := &model.ChangeTarget{
		ServerID:       "lobby-1",
		Status:         model.ChangeTargetStatusActivated,
		RollbackStatus: model.RollbackStatusFailed,
	}

	const reason = "回滚时 1/2 插件重载失败：com.example.A：解析失败"
	s.emitDeliveryFailAlert(rt, target, map[string]any{"rollback_error": reason}, reason)

	got := rec.waitOne(t)
	if !strings.Contains(got.Message, reason) {
		t.Fatalf("回滚失败告警必须带出原因，实际 message=%q", got.Message)
	}
	if got.Detail["error"] != reason {
		t.Fatalf("detail.error 必须带出原因，实际 %v", got.Detail["error"])
	}
	if got.Detail["rollbackStatus"] != model.RollbackStatusFailed {
		t.Fatalf("detail 必须标明这是回滚失败，实际 %v", got.Detail["rollbackStatus"])
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
		"reload_report": `{"total":2,"failedCount":1}`,
	}, "文件已落盘，仅配置变更通知失败：1/2 插件重载失败：A: 解析失败")

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
		"reload_report": `{"total":80,"failedCount":80}`,
	}, long)

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
		s.emitDeliveryFailAlert(rt, target, map[string]any{"reload_report": `{}`}, "失败")
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

	s.emitDeliveryFailAlert(rt, target, map[string]any{}, "失败") // 不应 panic
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
	// 光断言「发了告警」不够：若有人把 reason 传成空串，告警会退化成
	// 「交付失败（未提供原因）」，用例照样绿而根因丢失。必须锁住原因真的带出来了。
	if msg := alertGot.Message; !strings.Contains(msg, "插件重载失败") {
		t.Fatalf("回滚失败告警必须带出失败原因，实际 message=%q", msg)
	}
	if alertGot.Detail["error"] != got.RollbackError {
		t.Fatalf("告警 detail.error 必须等于落库的 rollback_error，实际 %v", alertGot.Detail["error"])
	}
	if alertGot.Detail["rollbackStatus"] != model.RollbackStatusFailed {
		t.Fatalf("告警 detail 必须标明回滚失败，实际 %v", alertGot.Detail["rollbackStatus"])
	}
}

// —— 回滚 done 分支的控制面兜底（M1）与 reload_report 收口（M2）——

// completeRollbacksWithResult 把某单全部目标的回滚命令置指定终态并附回执明细。
//
// 与 completeAllRollbacks 的差别只在「能带 result_detail」：M1 的兜底读的正是回执里的 reload 段。
func (h *orchestratorHarness) completeRollbacksWithResult(t *testing.T, orderID uint, status, result string) {
	t.Helper()
	for serverID := range h.rollbackTargetsByServer(orderID) {
		completeDeliveryCommand(t, h.env.db, orderID, serverID, model.CommandTypeDeliveryRollback, status, result)
	}
}

// reloadResultDetail 把逐插件重载明细编成命令回执原文（推进器经 parseDeliveryCmdResult 读它）。
func reloadResultDetail(t *testing.T, report *deliveryReloadReport) string {
	t.Helper()
	raw, err := json.Marshal(deliveryCmdResult{Reload: report})
	if err != nil {
		t.Fatalf("序列化回执明细失败: %v", err)
	}
	return string(raw)
}

// rollbackRunningFixture 把某单的 t-1 走到「回滚令已下发、rollback_status=running」（M1 用例公共前置）。
//
// 用**目标级**（子集）回滚而非整单回滚：子集只挑 t-1，故一个 bot 只产出一条告警，
// 「不得重复告警」的负向断言才有意义（整单回滚会让两台目标各发一条，第二条躺在 channel 里
// 既不会让 waitOne 失败，也会让 assertNone 误判成重复投递）。
func rollbackRunningFixture(t *testing.T, h *orchestratorHarness) *model.ChangeOrder {
	t.Helper()
	order := h.completedPushOnlyOrder(t)
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "回滚验证"); err != nil {
		t.Fatalf("发起回滚失败: %v", err)
	}
	h.tick() // pending→running（下发 delivery_rollback）
	got := targetByServer(t, h, order.ID, "t-1")
	if got.RollbackStatus != model.RollbackStatusRunning {
		t.Fatalf("夹具前置状态应为 running，实际 %s", got.RollbackStatus)
	}
	return h.reload(order.ID)
}

// TestRollbackDoneWithReloadFailureJudgedFailed 锁死 M1：回执 done 但明细含失败 → 判回滚失败并告警。
//
// 回滚腿此前从不解析 cmd.ResultDetail，只信 agent 的「整体 status=done」。一旦对端漂移
// （第三方 agent、或 agent 后续重构漏改某条早退路径）出现「整体 success + 明细 failed」，
// rollback_status 会被判 rolled_back —— 回滚假报成功，比生效假报成功更危险：
// 运维会以为线上已经退回去了。本用例是这条兜底的回归锁。
func TestRollbackDoneWithReloadFailureJudgedFailed(t *testing.T) {
	rec := newRecordingAlerter()
	h := newOrchestratorHarness(t)
	h.orch.alerter = rec
	order := rollbackRunningFixture(t, h)

	// 对端漂移形态：整体回执 done，明细里明确写着 1 个插件重载失败。
	result := reloadResultDetail(t, &deliveryReloadReport{
		Total: 2, OKCount: 1, FailedCount: 1,
		Plugins: []deliveryReloadPlugin{
			{Listener: "top.wcpe.ReloadHook", Status: "failed", Reason: "解析失败"},
			{Listener: "top.wcpe.Other", Status: deliveryReloadStatusOK},
		},
	})
	h.completeRollbacksWithResult(t, order.ID, model.CommandStatusDone, result)
	h.tick()

	got := targetByServer(t, h, order.ID, "t-1")
	if got.RollbackStatus != model.RollbackStatusFailed {
		t.Fatalf("回执 done 但明细含失败必须判回滚失败（不得假报成功），实际 %s", got.RollbackStatus)
	}
	if !strings.Contains(got.RollbackError, "重载明细含失败") {
		t.Fatalf("回滚失败原因须点明「明细含失败」，实际 %q", got.RollbackError)
	}
	if got.ReloadReport == "" {
		t.Fatal("回滚失败必须落 reload_report，否则运维看不到是哪个插件没重载回来")
	}

	alertGot := rec.waitOne(t)
	if alertGot.EventType() != alert.TypePublishFail {
		t.Fatalf("回滚失败应扇出 publish-fail，实际 %q", alertGot.EventType())
	}
	// 列表层可分辨（m2）：回滚失败时目标主状态仍是 activated，只写「交付失败：」会读成生效失败。
	if !strings.HasPrefix(alertGot.Message, "回滚失败：") {
		t.Fatalf("回滚失败的告警正文必须以「回滚失败：」开头，实际 %q", alertGot.Message)
	}
	if _, ok := alertGot.Detail["reloadReport"]; !ok {
		t.Fatal("回滚失败告警的 detail 须含逐插件明细")
	}
	// m1：告警读的必须是**收口后**的值，与库里同一口径。
	if alertGot.Detail["error"] != got.RollbackError {
		t.Fatalf("告警 detail.error 必须等于落库的 rollback_error，实际 %v", alertGot.Detail["error"])
	}
	// 不得重复告警：「调用点 + 漏斗」各发一次会让运维收到两条同样的失败。
	rec.assertNone(t, 100*time.Millisecond)
	// 单不得被自动收单：有 failed 目标要停在滚动回滚中待人工。
	if st := h.reload(order.ID).Status; st == model.ChangeOrderStatusRolledBack {
		t.Fatal("有回滚失败目标时整单不得自动收单为 rolled_back")
	}
}

// TestRollbackDoneWithCleanReloadReportPersistsReport 回滚成功路径也要落 reload_report（有据可查）。
//
// 此前只有失败路径落库，「回滚到底把哪个版本的插件加载起来了」在成功后完全无痕迹；
// 与正推腿 reconcileActivatingByAck 成功落库的口径也不一致。
func TestRollbackDoneWithCleanReloadReportPersistsReport(t *testing.T) {
	rec := newRecordingAlerter()
	h := newOrchestratorHarness(t)
	h.orch.alerter = rec
	order := rollbackRunningFixture(t, h)

	result := reloadResultDetail(t, &deliveryReloadReport{
		Total: 2, OKCount: 2, FailedCount: 0,
		Plugins: []deliveryReloadPlugin{
			{Listener: "top.wcpe.A", Status: deliveryReloadStatusOK, AppliedMd5: strings.Repeat("a", 32)},
			{Listener: "top.wcpe.B", Status: deliveryReloadStatusOK},
		},
	})
	h.completeRollbacksWithResult(t, order.ID, model.CommandStatusDone, result)
	h.tick()

	got := targetByServer(t, h, order.ID, "t-1")
	if got.RollbackStatus != model.RollbackStatusRolledBack {
		t.Fatalf("明细正常应判 rolled_back，实际 %s（原因 %q）", got.RollbackStatus, got.RollbackError)
	}
	if !strings.Contains(got.ReloadReport, `"total":2`) {
		t.Fatalf("回滚成功路径必须落 reload_report，实际 %q", got.ReloadReport)
	}
	// 成功不得告警（告警只属于失败）。
	rec.assertNone(t, 100*time.Millisecond)
}

// TestRollbackDoneWithOversizedFailingReportStillFailed 超长报告既要被收口、也不得削弱一票否决。
//
// 报告大到必须截断，且失败插件落在条数上限之外：判定必须仍按**原始**报告命中失败
// （收口只影响落库 / 告警的展示内容），落库值同时要在限额内并保留关键事实。
func TestRollbackDoneWithOversizedFailingReportStillFailed(t *testing.T) {
	rec := newRecordingAlerter()
	h := newOrchestratorHarness(t)
	h.orch.alerter = rec
	order := rollbackRunningFixture(t, h)

	report := oversizedReloadReport(40, 25) // 唯一失败插件在下标 25，远超 10 条上限
	h.completeRollbacksWithResult(t, order.ID, model.CommandStatusDone, reloadResultDetail(t, report))
	h.tick()

	got := targetByServer(t, h, order.ID, "t-1")
	if got.RollbackStatus != model.RollbackStatusFailed {
		t.Fatalf("超长报告里的失败仍必须判回滚失败，实际 %s", got.RollbackStatus)
	}
	if n := len(got.ReloadReport); n > changeTargetReloadReportMaxBytes {
		t.Fatalf("reload_report 未被收口：%d 字节 > 上限 %d", n, changeTargetReloadReportMaxBytes)
	}
	stored := decodeTruncatedReport(t, got.ReloadReport)
	if !stored.Truncated || stored.Total != 40 || stored.FailedCount != 1 {
		t.Fatalf("截断形态必须保留总数 / 失败数并显式标注：%+v", stored)
	}
	rec.waitOne(t)
	rec.assertNone(t, 100*time.Millisecond)
}

// TestCasRollbackMissDoesNotAlert 锁死 `to==failed && ok` 里的 ok：CAS 未命中不得告警。
//
// ok 是唯一的防误报开关——前态不符说明状态压根没变，此时发告警就是凭空制造一起失败。
// 用例同时给出正向对照（命中后必须发），确保「没发」不是因为告警通道根本不工作。
func TestCasRollbackMissDoesNotAlert(t *testing.T) {
	rec := newRecordingAlerter()
	h := newOrchestratorHarness(t)
	h.orch.alerter = rec
	order := h.completedPushOnlyOrder(t)
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "回滚验证"); err != nil {
		t.Fatalf("发起回滚失败: %v", err)
	}
	// 尚未下发回滚令：目标仍 pending，而 casRollback 只认 running → 必不命中。
	rt, err := h.orch.loadOrderRuntime(h.reload(order.ID))
	if err != nil {
		t.Fatalf("装载单运行时失败: %v", err)
	}
	pending := targetByServer(t, h, order.ID, "t-1")
	if pending.RollbackStatus != model.RollbackStatusPending {
		t.Fatalf("夹具前置状态应为 pending，实际 %s", pending.RollbackStatus)
	}
	if h.orch.casRollback(rt, &pending, model.RollbackStatusFailed, map[string]any{"rollback_error": "能力不足"}) {
		t.Fatal("前态不符时 CAS 不应命中")
	}
	rec.assertNone(t, 150*time.Millisecond)

	// 正向对照：把目标真正推到 running 后再迁 failed，必须命中并发出一条告警。
	h.tick()
	rt, err = h.orch.loadOrderRuntime(h.reload(order.ID))
	if err != nil {
		t.Fatalf("装载单运行时失败: %v", err)
	}
	running := targetByServer(t, h, order.ID, "t-1")
	if running.RollbackStatus != model.RollbackStatusRunning {
		t.Fatalf("下发后应 running，实际 %s", running.RollbackStatus)
	}
	if !h.orch.casRollback(rt, &running, model.RollbackStatusFailed, map[string]any{"rollback_error": "能力不足"}) {
		t.Fatal("前态相符时 CAS 应命中")
	}
	if got := rec.waitOne(t); !strings.HasPrefix(got.Message, "回滚失败：") {
		t.Fatalf("命中后必须发回滚失败告警，实际 %q", got.Message)
	}
}

// TestCasTargetCapsErrorMessage 锁死「capMapError 真的被调用」。
//
// sqlite 不校验 VARCHAR 长度，删掉 casTarget / casRollbackFrom 里的收口调用全套测试照样绿——
// 但生产 MySQL 严格模式下超宽会让整条 CAS UPDATE 报 1406、目标每 tick 用同一个超宽值重试。
// 故必须读回库内值断言「已经被截断」，而不是只看 CAS 是否成功。
func TestCasTargetCapsErrorMessage(t *testing.T) {
	h := newOrchestratorHarness(t)
	rt, target := pushingTargetFixture(t, h)

	// 4800 字节的失败原因，远超 change_target.error 的 1024 列宽。
	longReason := strings.Repeat("覆盖失败：目标磁盘只读；", 300)
	if !h.orch.failTarget(rt, &target, model.ChangeTargetStatusPushing, longReason) {
		t.Fatal("失败迁移应命中 CAS")
	}

	got := targetByServer(t, h, rt.order.ID, "t-1")
	if got.Error == "" {
		t.Fatal("失败原因必须落库")
	}
	if n := len([]rune(got.Error)); n > changeTargetErrorMaxRunes {
		t.Fatalf("error 未被收口：%d 字符 > 上限 %d（生产上会直接 1406）", n, changeTargetErrorMaxRunes)
	}
	if !strings.HasSuffix(got.Error, "…（已截断）") {
		t.Fatalf("超宽原因必须显式标注截断，实际结尾 %q", got.Error[max(0, len(got.Error)-24):])
	}
}

// TestCasTargetCapsReloadReport 锁死 M2：超长 reload_report 落库前被收口，且保留关键事实。
//
// reload_report 与 error 在**同一条** CAS UPDATE 里，却一直没被收口：约 5 个插件同时自报满长
// detail 就越过 TEXT 的 64KB，后果正是收口想消灭的那个形态——严格模式 1406 → CAS 返回 err →
// 目标卡在原状态、每 tick 用同一个超宽值重试。
func TestCasTargetCapsReloadReport(t *testing.T) {
	rec := newRecordingAlerter()
	h := newOrchestratorHarness(t)
	h.orch.alerter = rec
	rt, target := pushingTargetFixture(t, h)

	report := oversizedReloadReport(40, 3, 20, 37)
	if !h.orch.failTargetWithResult(rt, &target, model.ChangeTargetStatusPushing, "推送失败",
		deliveryCmdResult{Reload: report}) {
		t.Fatal("失败迁移应命中 CAS")
	}

	got := targetByServer(t, h, rt.order.ID, "t-1")
	if n := len(got.ReloadReport); n > changeTargetReloadReportMaxBytes {
		t.Fatalf("reload_report 未被收口：%d 字节 > 上限 %d", n, changeTargetReloadReportMaxBytes)
	}
	stored := decodeTruncatedReport(t, got.ReloadReport)
	if !stored.Truncated {
		t.Fatal("截断形态必须显式标注 truncated，否则残缺会被当成全量")
	}
	if stored.Total != 40 || stored.FailedCount != 3 {
		t.Fatalf("截断不得丢掉总数 / 失败数：%+v", stored)
	}
	if len(stored.Plugins) == 0 || len(stored.Plugins) > reloadReportMaxPlugins {
		t.Fatalf("保留条数应在 1..%d 之间，实际 %d", reloadReportMaxPlugins, len(stored.Plugins))
	}
	if stored.Plugins[0].Listener != "top.wcpe.Plugin03" {
		t.Fatalf("截断必须优先保留失败明细，实际首条 %q", stored.Plugins[0].Listener)
	}

	// 告警侧复用同一份收口结果：否则整份报告塞进 alert_event.detail（同为 TEXT）会写入失败，
	// 而 Dispatcher 只记 WARN——事件页那条告警会静默消失。
	alertGot := rec.waitOne(t)
	raw, okc := alertGot.Detail["reloadReport"].(json.RawMessage)
	if !okc {
		t.Fatalf("告警 detail.reloadReport 类型不符：%T", alertGot.Detail["reloadReport"])
	}
	if n := len(raw); n > changeTargetReloadReportMaxBytes {
		t.Fatalf("告警 detail.reloadReport 未被收口：%d 字节", n)
	}
}

// TestCasRollbackCapsErrorMessage 回滚漏斗同样必须收口 rollback_error，且告警读收口后的值。
//
// 与 TestCasTargetCapsErrorMessage 成对：两个漏斗（casTarget / casRollbackFrom）各有独立的一行收口，
// 只测一个会让另一个被删掉而不自知。同时锁 m1——告警不得读到未收口原文，否则同一条原因
// 在 DB 与告警里两个口径（这里断言两者逐字节相等）。
func TestCasRollbackCapsErrorMessage(t *testing.T) {
	rec := newRecordingAlerter()
	h := newOrchestratorHarness(t)
	h.orch.alerter = rec
	order := rollbackRunningFixture(t, h)
	rt, err := h.orch.loadOrderRuntime(h.reload(order.ID))
	if err != nil {
		t.Fatalf("装载单运行时失败: %v", err)
	}
	running := targetByServer(t, h, order.ID, "t-1")

	longReason := strings.Repeat("能力守卫拒绝：agent 版本过旧；", 300)
	if !h.orch.failRollback(rt, &running, longReason) {
		t.Fatal("回滚失败迁移应命中 CAS")
	}

	got := targetByServer(t, h, order.ID, "t-1")
	if n := len([]rune(got.RollbackError)); n > changeTargetErrorMaxRunes {
		t.Fatalf("rollback_error 未被收口：%d 字符 > 上限 %d", n, changeTargetErrorMaxRunes)
	}
	if !strings.HasSuffix(got.RollbackError, "…（已截断）") {
		t.Fatalf("超宽原因必须显式标注截断，实际结尾 %q", got.RollbackError[max(0, len(got.RollbackError)-24):])
	}
	alertGot := rec.waitOne(t)
	if alertGot.Detail["error"] != got.RollbackError {
		t.Fatalf("告警与库内必须同口径：detail=%v 库内=%q", alertGot.Detail["error"], got.RollbackError)
	}
	if n := len([]rune(alertGot.Message)); n > alertMessageMaxRunes {
		t.Fatalf("告警正文超列宽：%d > %d", n, alertMessageMaxRunes)
	}
}

// TestCapReasonRunesKeepsTruncationMarker 锁死截断标记。
//
// 只断言 rune 数会让「裸切 r[:limit]」的实现照样绿——那样的实现会把原因截成半句而不作任何
// 说明，运维读到的是「解析失败：字段」这种被硬切掉尾巴的假象。
func TestCapReasonRunesKeepsTruncationMarker(t *testing.T) {
	long := strings.Repeat("插件重载失败；", 300)
	got := capReasonRunes(long, 200)
	if n := len([]rune(got)); n > 200 {
		t.Fatalf("返回值 rune 数超限：%d", n)
	}
	if !strings.HasSuffix(got, "…（已截断）") {
		t.Fatalf("截断必须显式标注，实际结尾 %q", got[max(0, len(got)-24):])
	}
	if !strings.HasPrefix(got, "插件重载失败；") {
		t.Fatalf("截断只该去尾、不该破坏开头事实，实际开头 %q", got)
	}
	// 未超限必须原样返回（不得无端加标记）。
	if short := capReasonRunes("略", 200); short != "略" {
		t.Fatalf("未超限不应改写，实际 %q", short)
	}
}

// oversizedReloadReport 造一份远超 TEXT 上限的逐插件重载明细（模拟多插件同时自报满长 detailJson）。
//
// 单插件 detailJson 4000 字符 × 3 字节（utf8mb4 中文）≈ 12KB，40 个插件 ≈ 480KB，必超 64KB。
// failedAt 指定哪些下标为失败插件：用于验证截断后仍优先保留失败明细。
func oversizedReloadReport(total int, failedAt ...int) *deliveryReloadReport {
	failed := make(map[int]bool, len(failedAt))
	for _, i := range failedAt {
		failed[i] = true
	}
	report := &deliveryReloadReport{Total: total, TimeoutMs: 30000}
	report.Plugins = make([]deliveryReloadPlugin, 0, total)
	for i := 0; i < total; i++ {
		p := deliveryReloadPlugin{
			Listener: fmt.Sprintf("top.wcpe.Plugin%02d", i), Status: deliveryReloadStatusOK,
			Reason: "重载成功", DetailJSON: strings.Repeat("明细", 2000),
		}
		if failed[i] {
			p.Status = "failed"
			p.Reason = "解析配置失败：" + strings.Repeat("字段缺失；", 60)
			report.FailedCount++
		} else {
			report.OKCount++
		}
		report.Plugins = append(report.Plugins, p)
	}
	return report
}

// decodeTruncatedReport 解出收口后的退化形态（解不开即失败：落库值必须是合法 JSON）。
func decodeTruncatedReport(t *testing.T, raw string) reloadReportTruncated {
	t.Helper()
	var got reloadReportTruncated
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("收口后的 reload_report 不是合法 JSON: %v，原文 %q", err, raw)
	}
	return got
}

// pushingTargetFixture 建一张已启动、首批目标处于 pushing 的单（漏斗级用例直接构造失败迁移用）。
func pushingTargetFixture(t *testing.T, h *orchestratorHarness) (*orderRuntime, model.ChangeTarget) {
	t.Helper()
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick() // 首批下发 → pushing
	rt, err := h.orch.loadOrderRuntime(h.reload(order.ID))
	if err != nil {
		t.Fatalf("装载单运行时失败: %v", err)
	}
	target := targetByServer(t, h, order.ID, "t-1")
	if target.Status != model.ChangeTargetStatusPushing {
		t.Fatalf("夹具前置状态应为 pushing，实际 %s", target.Status)
	}
	return rt, target
}
