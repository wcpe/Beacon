package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
)

// —— 批次切分穷举单测（spec §4.4.1；纯函数，稳定可复现）——

// TestPlanBatchCounts 穷举 percent / count 批次切分：逐批取整 / 剩余进末批 / 补位末批 / 边界。
func TestPlanBatchCounts(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		sizes []int
		total int
		want  []int
	}{
		{"百分比逐批向上取整+末批兜底", model.BatchModePercent, []int{5, 20, 75}, 10, []int{1, 2, 7}},
		{"百分比之和不足100自动补末批", model.BatchModePercent, []int{10, 20}, 10, []int{1, 2, 7}},
		{"百分比小目标每批至少1", model.BatchModePercent, []int{10, 30, 60}, 2, []int{1, 1}},
		{"百分比单批100全量", model.BatchModePercent, []int{100}, 5, []int{5}},
		{"数量逐批固定台数+剩余进末批", model.BatchModeCount, []int{1, 10, 50}, 100, []int{1, 10, 50, 39}},
		{"数量首批超总数即封顶", model.BatchModeCount, []int{10}, 3, []int{3}},
		{"数量精确用尽无补位", model.BatchModeCount, []int{2, 3}, 5, []int{2, 3}},
		{"零目标返回空", model.BatchModePercent, []int{5, 20, 75}, 0, []int{}},
		{"单目标单批", model.BatchModeCount, []int{5}, 1, []int{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planBatchCounts(c.mode, c.sizes, c.total)
			if len(got) != len(c.want) {
				t.Fatalf("批数不符: 期望 %v 实际 %v", c.want, got)
			}
			sum := 0
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("第 %d 批不符: 期望 %v 实际 %v", i+1, c.want, got)
				}
				sum += got[i]
			}
			if c.total > 0 && sum != c.total {
				t.Fatalf("批次总数 %d 应等于目标总数 %d", sum, c.total)
			}
		})
	}
}

// TestPlanBatchMembersStable 批成员按字典序稳定切分（同输入必同输出，可复现）。
func TestPlanBatchMembersStable(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e"}
	members := planBatchMembers(model.BatchModeCount, []int{2, 2}, ids)
	if len(members) != 3 || members[0][0] != "a" || members[1][0] != "c" || members[2][0] != "e" {
		t.Fatalf("批成员切分不符: %+v", members)
	}
}

// —— 推进器端到端 push_only 脊柱 ——

// orchestratorHarness 打包推进器 + 可控时钟 + 内存指标，驱动同步 tick。
type orchestratorHarness struct {
	env   *deliveryTestEnv
	f     *deliveryFixture
	orch  *DeliveryOrchestrator
	clock time.Time
}

// newOrchestratorHarness 装配推进器（复用交付测试库 + 夹具），注入可控时钟与观察窗提供方。
func newOrchestratorHarness(t *testing.T) *orchestratorHarness {
	t.Helper()
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	blobRepo := repository.NewDeliveryBlobRepository(env.db)
	repo := repository.NewChangeOrderRepository(env.db)
	cmdRepo := repository.NewAgentCommandRepository(env.db)
	auditRepo := repository.NewAuditLogRepository(env.db)
	blobSvc := NewDeliveryBlobService(env.db, blobRepo, repo, cmdRepo, &fakeBlobSettings{upload: 4, download: 64, capacity: 1 << 30})
	orch := NewDeliveryOrchestrator(env.db, repo, blobSvc, cmdRepo, auditRepo, env.health, metricwindow.New(0), nil)
	blobSvc.SetProgressWaker(orch)
	env.orders.SetObserveProvider(orch)
	h := &orchestratorHarness{env: env, f: f, orch: orch, clock: time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)}
	orch.now = func() time.Time { return h.clock }
	return h
}

// tick 同步推进一轮。
func (h *orchestratorHarness) tick() { h.orch.advanceActiveOrders(context.Background()) }

// advance 推进可控时钟。
func (h *orchestratorHarness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

// createApprovedFileOrder 建一张含单文件项、blob 就绪、已 approved 的单（可指定批次 / 生效 / 熔断阈值）。
func (h *orchestratorHarness) createApprovedFileOrder(t *testing.T, batchSizes []int, method string, failureRate int) *model.ChangeOrder {
	t.Helper()
	detail := createDraftOrder(t, h.f)
	seedFileItemWithBlob(t, h.env.db, detail.ID, "plugins/demo.jar", fmt.Sprintf("%064d", detail.ID), 128)
	updates := map[string]any{
		"status": model.ChangeOrderStatusApproved, "batch_sizes": encodeBatchSizes(batchSizes),
		"activation_method": method, "failure_rate_threshold_percent": failureRate,
		"observe_window_sec": 5, "activate_timeout_sec": 60,
	}
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", detail.ID).Updates(updates).Error; err != nil {
		t.Fatalf("置单为 approved 失败: %v", err)
	}
	order, _ := repository.NewChangeOrderRepository(h.env.db).FindByID(detail.ID)
	return order
}

// seedFileItemWithBlob 插一条 file_diff 项并预置对应 ready blob（使 payload 直接就绪）。
func seedFileItemWithBlob(t *testing.T, db *gorm.DB, orderID uint, path, sha string, size int64) {
	t.Helper()
	action := model.ChangeItemActionAdd
	p, s := path, sha
	mustCreate(t, db, &model.ChangeOrderItem{
		OrderID: orderID, Kind: model.ChangeItemKindFileDiff, Path: &p, Action: &action, SHA256: &s, SizeBytes: &size,
	})
	mustCreate(t, db, &model.DeliveryBlob{SHA256: sha, SizeBytes: size, State: model.DeliveryBlobStateReady, CreatedAt: time.Now().UTC()})
}

// repeatHex 生成够长的伪 hex（凑满 64 位 sha256 形状，测试用）。
func repeatHex(seed string, n int) string {
	out := ""
	for len(out) < n {
		out += seed
	}
	return out[:n]
}

// completeDeliveryCommand 把某单某类型某目标的在途命令直接置终态（模拟 agent 回执落定，绕过 HTTP 层）。
func completeDeliveryCommand(t *testing.T, db *gorm.DB, orderID uint, serverID, cmdType, status string, result string) {
	t.Helper()
	var cmds []model.AgentCommand
	if err := db.Where("server_id = ? AND type = ?", serverID, cmdType).Find(&cmds).Error; err != nil {
		t.Fatalf("查命令失败: %v", err)
	}
	for i := range cmds {
		var p struct {
			OrderID uint `json:"orderId"`
		}
		if json.Unmarshal([]byte(cmds[i].Payload), &p) == nil && p.OrderID == orderID {
			if err := db.Model(&model.AgentCommand{}).Where("id = ?", cmds[i].ID).
				Updates(map[string]any{"status": status, "result_detail": result}).Error; err != nil {
				t.Fatalf("置命令终态失败: %v", err)
			}
		}
	}
}

// pushResult 是一条推送成功回执摘要。
const pushResult = `{"changedFileCount":1,"skippedFileCount":0,"backupPresent":true}`

// completeAllPushes 把某单全部目标的推送命令置成功（推动 pushing→pushed→activated）。
func (h *orchestratorHarness) completeAllPushes(t *testing.T, orderID uint) {
	t.Helper()
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(orderID)
	for _, tg := range targets {
		completeDeliveryCommand(t, h.env.db, orderID, tg.ServerID, model.CommandTypeDeliveryPush, model.CommandStatusDone, pushResult)
	}
}

// targetStatuses 取某单目标状态计数。
func (h *orchestratorHarness) targetStatuses(orderID uint) map[string]int64 {
	counts, _ := repository.NewChangeOrderRepository(h.env.db).CountTargetsByStatus(orderID)
	return counts
}

// reload 取单最新态。
func (h *orchestratorHarness) reload(orderID uint) *model.ChangeOrder {
	o, _ := repository.NewChangeOrderRepository(h.env.db).FindByID(orderID)
	return o
}

// TestOrchestratorPushOnlyHappyPath push_only 端到端：start→dispatch→pushed→activated→observing→awaiting_confirm→confirm→completed。
func TestOrchestratorPushOnlyHappyPath(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)

	if _, err := h.orch.Start(order.ID, "上线大厅", "ops", "10.0.0.1"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	got := h.reload(order.ID)
	if got.Status != model.ChangeOrderStatusRolling || got.PayloadState != model.PayloadStateReady {
		t.Fatalf("启动后应 rolling+ready: %+v", got)
	}

	// tick 1：首批 running 下发两目标 pending→pushing。
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusPushing] != 2 {
		t.Fatalf("首批应下发 2 目标 pushing: %v", c)
	}

	// 回执成功 → tick：pushing→pushed→activated（push_only）→ 全终态 observing。
	h.completeAllPushes(t, order.ID)
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivated] != 2 {
		t.Fatalf("回执成功后应 2 目标 activated: %v", c)
	}
	batches, _ := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if batches[0].Status != model.ChangeBatchStatusObserving {
		t.Fatalf("全终态后批应 observing: %s", batches[0].Status)
	}

	// 观察窗到点 → awaiting_confirm。
	h.advance(6 * time.Second)
	h.tick()
	batches, _ = repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if batches[0].Status != model.ChangeBatchStatusAwaitingConfirm {
		t.Fatalf("观察窗到点后批应 awaiting_confirm: %s", batches[0].Status)
	}

	// 末批确认 → 单 completed。
	if _, err := h.orch.ConfirmBatch(order.ID, 1, "ops", "10.0.0.1"); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("末批确认后单应 completed: %s", got.Status)
	}
	if countAudit(t, h.env.db, model.ActionDeliveryOrderStart) != 1 ||
		countAudit(t, h.env.db, model.ActionDeliveryOrderBatchConfirm) != 1 {
		t.Fatal("应各记 1 条 start / batch_confirm 审计")
	}
}

// TestOrchestratorMultiBatchGate 多批推进门：确认首批才启动次批，末批确认才完成整单。
func TestOrchestratorMultiBatchGate(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{50, 50}, model.ActivationMethodPushOnly, 0) // 2 目标切 2 批各 1

	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	batches, _ := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if len(batches) != 2 {
		t.Fatalf("应切 2 批: %d", len(batches))
	}

	driveBatchToAwaitConfirm := func() {
		h.tick()
		h.completeAllPushes(t, order.ID)
		h.tick()
		h.advance(6 * time.Second)
		h.tick()
	}

	driveBatchToAwaitConfirm()
	batches, _ = repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if batches[0].Status != model.ChangeBatchStatusAwaitingConfirm || batches[1].Status != model.ChangeBatchStatusPending {
		t.Fatalf("首批 awaiting、次批 pending: %+v", batches)
	}
	if _, err := h.orch.ConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认首批失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolling {
		t.Fatalf("确认首批后单仍 rolling: %s", got.Status)
	}

	driveBatchToAwaitConfirm()
	if _, err := h.orch.ConfirmBatch(order.ID, 2, "ops", "ip"); err != nil {
		t.Fatalf("确认末批失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("确认末批后单应 completed: %s", got.Status)
	}
}

// TestOrchestratorCircuitBreakFailureRate 失败率熔断 + retry_failed 恢复。
func TestOrchestratorCircuitBreakFailureRate(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 50) // 阈值 50%

	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick() // 下发 2 目标 pushing
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	// 一成功一失败：failed/planned = 1/2 = 50% ≥ 阈值 → 熔断。
	completeDeliveryCommand(t, h.env.db, order.ID, targets[0].ServerID, model.CommandTypeDeliveryPush, model.CommandStatusDone, pushResult)
	completeDeliveryCommand(t, h.env.db, order.ID, targets[1].ServerID, model.CommandTypeDeliveryPush, model.CommandStatusFailed, `{"error":"落盘失败"}`)
	h.tick()

	got := h.reload(order.ID)
	if got.Status != model.ChangeOrderStatusPaused || got.PauseKind != model.PauseKindCircuitBreak {
		t.Fatalf("应熔断暂停(circuit_break): %+v", got)
	}
	batches, _ := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if batches[0].Status != model.ChangeBatchStatusFailed || batches[0].BreakReason == "" {
		t.Fatalf("批应 failed 且有 break_reason: %+v", batches[0])
	}
	if countAudit(t, h.env.db, model.ActionDeliveryOrderCircuitBreak) != 1 {
		t.Fatal("应记 1 条系统熔断审计")
	}

	// retry_failed：重置失败目标重推。
	if _, err := h.orch.Resume(order.ID, resumeModeRetryFailed, "已修复落盘", "ops", "ip"); err != nil {
		t.Fatalf("继续失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolling {
		t.Fatalf("retry_failed 后应 rolling: %s", got.Status)
	}
	h.tick() // 重推被重置的目标
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusPushing] != 1 {
		t.Fatalf("应重推 1 个失败目标: %v", c)
	}
	h.completeAllPushes(t, order.ID)
	h.tick()
	h.advance(6 * time.Second)
	h.tick()
	if _, err := h.orch.ConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("恢复后应可完成: %s", got.Status)
	}
}

// TestOrchestratorPauseKeepsInFlight 人工暂停不下发新目标，但在途目标继续走到终态。
func TestOrchestratorPauseKeepsInFlight(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick() // 下发 2 目标 pushing
	if _, err := h.orch.Pause(order.ID, "ops", "ip"); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusPaused || got.PauseKind != model.PauseKindManual {
		t.Fatalf("应人工暂停: %+v", got)
	}
	// 暂停期间在途目标回执 → 继续收口到 activated（不制造半截覆盖）。
	h.completeAllPushes(t, order.ID)
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivated] != 2 {
		t.Fatalf("暂停期在途目标应收口 activated: %v", c)
	}
}

// TestOrchestratorCancelSkipsPending 紧急终止把未开始目标置 skipped、单 cancelled。
func TestOrchestratorCancelSkipsPending(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 未 tick（未下发），目标皆 pending → 终止应全部 skipped。
	if _, err := h.orch.Cancel(order.ID, "误发布", "ops", "ip"); err != nil {
		t.Fatalf("终止失败: %v", err)
	}
	got := h.reload(order.ID)
	if got.Status != model.ChangeOrderStatusCancelled || got.CancelReason != "误发布" {
		t.Fatalf("应 cancelled 且记原因: %+v", got)
	}
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusSkipped] != 2 {
		t.Fatalf("未开始目标应 skipped: %v", c)
	}
}

// TestOrchestratorCancelRequiresReason 紧急终止原因必填。
func TestOrchestratorCancelRequiresReason(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	_, _ = h.orch.Start(order.ID, "", "ops", "ip")
	if _, err := h.orch.Cancel(order.ID, "  ", "ops", "ip"); err == nil {
		t.Fatal("空原因终止应被拒")
	}
}

// TestOrchestratorRejectsConfigOrder M3 含配置项单启动被拒（配置灰度切版为 M4 接缝）。
func TestOrchestratorRejectsConfigOrder(t *testing.T) {
	h := newOrchestratorHarness(t)
	detail := createDraftOrder(t, h.f)
	seedConfigItem(t, h.env.db, detail.ID)
	setOrderStatus(t, h.env.db, detail.ID, model.ChangeOrderStatusApproved)
	if _, err := h.orch.Start(detail.ID, "", "ops", "ip"); err != apperr.ErrChangeConfigGrayUnsupported {
		t.Fatalf("含配置项单应拒: %v", err)
	}
}

// TestOrchestratorStartConflict 目标集与其他活动单相交时拒绝启动（ADR-0071 §4.1）。
func TestOrchestratorStartConflict(t *testing.T) {
	h := newOrchestratorHarness(t)
	first := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.Start(first.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("首单启动失败: %v", err)
	}
	// 第二单目标集（默认夹具同为 t-1/t-2）与首单相交 → 冲突。
	second := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	_, err := h.orch.Start(second.ID, "", "ops", "ip")
	if err == nil {
		t.Fatal("目标相交应拒绝启动")
	}
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != "start_conflict" {
		t.Fatalf("应为 start_conflict: %v", err)
	}
}

// TestOrchestratorPayloadPrepUploadToReady payload 缺失 blob→uploading+下发上传命令；上传落定→ready→首批 running。
func TestOrchestratorPayloadPrepUploadToReady(t *testing.T) {
	h := newOrchestratorHarness(t)
	detail := createDraftOrder(t, h.f)
	// 只建 file 项、不预置 blob → 启动即缺失。
	sha := repeatHex("bc", 64)
	action := model.ChangeItemActionAdd
	p, s, size := "plugins/x.jar", sha, int64(64)
	mustCreate(t, h.env.db, &model.ChangeOrderItem{
		OrderID: detail.ID, Kind: model.ChangeItemKindFileDiff, Path: &p, Action: &action, SHA256: &s, SizeBytes: &size,
	})
	h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", detail.ID).
		Updates(map[string]any{"status": model.ChangeOrderStatusApproved, "batch_sizes": encodeBatchSizes([]int{100}),
			"activation_method": model.ActivationMethodPushOnly})

	if _, err := h.orch.Start(detail.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	got := h.reload(detail.ID)
	if got.PayloadState != model.PayloadStateUploading {
		t.Fatalf("缺失 blob 应 uploading: %s", got.PayloadState)
	}
	var uploadCmds int64
	h.env.db.Model(&model.AgentCommand{}).Where("type = ?", model.CommandTypeDeliveryUpload).Count(&uploadCmds)
	if uploadCmds != 1 {
		t.Fatalf("应下发 1 条上传命令: %d", uploadCmds)
	}
	batches, _ := repository.NewChangeOrderRepository(h.env.db).ListBatches(detail.ID)
	if batches[0].Status != model.ChangeBatchStatusPending {
		t.Fatalf("payload 未就绪时首批应 pending: %s", batches[0].Status)
	}

	// 模拟模板源上传完成：blob 就绪 + 上传命令 done → tick → payload ready + 首批 running。
	mustCreate(t, h.env.db, &model.DeliveryBlob{SHA256: sha, SizeBytes: size, State: model.DeliveryBlobStateReady, CreatedAt: time.Now().UTC()})
	completeDeliveryCommand(t, h.env.db, detail.ID, "src-1", model.CommandTypeDeliveryUpload, model.CommandStatusDone, "")
	h.tick()
	if got := h.reload(detail.ID); got.PayloadState != model.PayloadStateReady {
		t.Fatalf("上传落定后应 ready: %s", got.PayloadState)
	}
	h.tick() // markPayloadReady 后 wake 再跑一轮下发
	if c := h.targetStatuses(detail.ID); c[model.ChangeTargetStatusPushing] != 2 {
		t.Fatalf("payload 就绪后应下发首批: %v", c)
	}
}

// TestOrchestratorRecoveryAfterRestart 控制面重启（新推进器实例）按库内状态恢复推进（spec §4.1 恢复语义）。
func TestOrchestratorRecoveryAfterRestart(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick() // 下发 pushing
	h.completeAllPushes(t, order.ID)

	// 模拟重启：新建推进器实例（共用同库），drainActive 应续推 pushing→activated。
	repo := repository.NewChangeOrderRepository(h.env.db)
	fresh := NewDeliveryOrchestrator(h.env.db, repo, h.orch.blobs, h.orch.cmdRepo, h.orch.audit, h.env.health, metricwindow.New(0), nil)
	fresh.now = func() time.Time { return h.clock }
	fresh.advanceActiveOrders(context.Background())
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivated] != 2 {
		t.Fatalf("重启后应续推至 activated: %v", c)
	}
}
