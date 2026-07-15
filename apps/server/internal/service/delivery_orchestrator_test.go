package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
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

// orchestratorHarness 打包推进器 + 数据面 + 可控时钟 + 内存指标，驱动同步 tick。
type orchestratorHarness struct {
	env   *deliveryTestEnv
	f     *deliveryFixture
	orch  *DeliveryOrchestrator
	blob  *DeliveryBlobService
	clock time.Time
}

// newOrchestratorHarness 装配推进器（复用交付测试库 + 夹具），注入可控时钟、观察窗提供方与配置灰度渲染器。
func newOrchestratorHarness(t *testing.T) *orchestratorHarness {
	t.Helper()
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	blobRepo := repository.NewDeliveryBlobRepository(env.db)
	repo := repository.NewChangeOrderRepository(env.db)
	cmdRepo := repository.NewAgentCommandRepository(env.db)
	auditRepo := repository.NewAuditLogRepository(env.db)
	blobSvc := NewDeliveryBlobService(env.db, blobRepo, repo, cmdRepo, &fakeBlobSettings{upload: 4, download: 64, capacity: 1 << 30})
	blobSvc.SetRoot(t.TempDir()) // 配置灰度渲染写真 blob 文件，隔离到临时根避免污染 CWD
	configSvc := NewConfigCenterService(env.db, repository.NewConfigFileRepository(env.db),
		repository.NewConfigLayerVersionRepository(env.db), auditRepo)
	blobSvc.SetConfigRenderer(configSvc, repository.NewConfigLayerVersionRepository(env.db), repository.NewConfigFileRepository(env.db))
	orch := NewDeliveryOrchestrator(env.db, repo, blobSvc, cmdRepo, auditRepo, env.health, metricwindow.New(0), nil)
	blobSvc.SetProgressWaker(orch)
	env.orders.SetObserveProvider(orch)
	h := &orchestratorHarness{env: env, f: f, orch: orch, blob: blobSvc, clock: time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)}
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

// completeAllActivates 把某单全部目标的生效命令置指定终态（模拟 agent 回执，如 restart 的「已开始关服」done）。
func (h *orchestratorHarness) completeAllActivates(t *testing.T, orderID uint, status string) {
	t.Helper()
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(orderID)
	for _, tg := range targets {
		completeDeliveryCommand(t, h.env.db, orderID, tg.ServerID, model.CommandTypeDeliveryActivate, status, "")
	}
}

// seedHeartbeat 向内存指标窗口注入一条目标 identity 的接收批（模拟心跳回归 / 残留，restart 生效判定用）。
// BucketStartMs 取 receivedAtMs 使各批唯一；ReceivedAtMs 即控制面接收时刻（UTC 毫秒），与 activating 起始锚点同口径比较。
func (h *orchestratorHarness) seedHeartbeat(serverID string, receivedAtMs int64) {
	h.orch.metrics.Upsert(metricwindow.Sample{
		NamespaceID: h.f.nsID, ServerID: serverID, Kind: model.ServerKindBackend,
		BucketStartMs: receivedAtMs, ReceivedAtMs: receivedAtMs,
	})
}

// seedHealthUnhealthy 向内存健康视图注入指定目标为 unhealthy（模拟 restart 后冷启动的健康态）。
func (h *orchestratorHarness) seedHealthUnhealthy(serverIDs ...string) {
	views := make([]healthview.View, 0, len(serverIDs))
	for _, sid := range serverIDs {
		views = append(views, healthview.View{
			NamespaceID: h.f.nsID, ServerID: sid, Kind: model.ServerKindBackend,
			Score: 0, Level: healthview.LevelUnhealthy,
		})
	}
	h.env.health.ReplaceAll(views)
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

// seedGrayConfigFile 建一个配置文件 + 指定作用域链上的一个定稿版本，返回 (fileID, versionID)（配置灰度测试用）。
func seedGrayConfigFile(t *testing.T, db *gorm.DB, nsID uint, scopeKind string, scopeRefID uint, content string) (uint, uint) {
	t.Helper()
	file := model.ConfigFile{NamespaceID: nsID, Name: "plugins/Gray/config.yml", Format: "yaml"}
	mustCreate(t, db, &file)
	v := model.ConfigLayerVersion{
		ConfigFileID: file.ID, ScopeLevel: scopeKind, ScopeRefID: scopeRefID,
		VersionNo: 1, Content: content,
	}
	mustCreate(t, db, &v)
	return file.ID, v.ID
}

// createApprovedConfigOrder 建一张纯配置灰度单（点名 servers + 指定作用域挂 toVersion），经服务校验后置 approved，返回单 id。
func (h *orchestratorHarness) createApprovedConfigOrder(t *testing.T, title string, servers []string, scopeKind string, scopeID, toVersionID uint) uint {
	t.Helper()
	detail, err := h.env.orders.Create(h.f.nsID, ChangeOrderInput{
		Title: strPtr(title), Selector: &ChangeSelector{Servers: servers},
	}, "ops-chen", "10.0.0.1")
	if err != nil {
		t.Fatalf("建配置单失败: %v", err)
	}
	if _, err := h.env.orders.Update(detail.ID, ChangeOrderInput{ConfigChanges: &[]ChangeConfigInput{
		{ConfigScopeKind: scopeKind, ConfigScopeID: scopeID, ConfigToVersionID: toVersionID},
	}}, "ops-chen", ""); err != nil {
		t.Fatalf("挂配置版本失败: %v", err)
	}
	setOrderStatus(t, h.env.db, detail.ID, model.ChangeOrderStatusApproved)
	return detail.ID
}

// TestOrchestratorConfigScopeConflict 配置作用域冲突守卫（ADR-0071 决策5）：两单灰度同一 (文件, 作用域)
// 且目标不相交时，后启单被 config_scope_conflict 拒绝；同时证明含配置项单不再被前置拒（首单正常进 rolling）。
func TestOrchestratorConfigScopeConflict(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")

	// 首单：灰度 zone1 配置、点名 t-1，正常进 rolling（证明含配置项单不再被拒）。
	first := h.createApprovedConfigOrder(t, "配置灰度A", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	if _, err := h.orch.Start(first, "", "ops", "ip"); err != nil {
		t.Fatalf("含配置项单应可启动: %v", err)
	}
	if h.reload(first).Status != model.ChangeOrderStatusRolling {
		t.Fatal("首单应进 rolling")
	}

	// 次单：灰度同一 (文件, zone1)、点名 t-2（与首单目标不相交排除目标冲突），应被配置作用域冲突拒绝。
	second := h.createApprovedConfigOrder(t, "配置灰度B", []string{"t-2"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	_, err := h.orch.Start(second, "", "ops", "ip")
	if err == nil {
		t.Fatal("配置作用域相交应拒绝启动")
	}
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != "config_scope_conflict" {
		t.Fatalf("应为 config_scope_conflict: %v", err)
	}
}

// TestOrchestratorConfigGrayRendersBlobAndManifest 配置灰度渲染 + blob 生成 + 清单归一（ADR-0071 决策1/2）：
// 启动即由控制面渲染配置明文写入就绪 blob；目标拉清单时配置项归一为文件项进 Files（sha 指向就绪 blob），Configs 空。
func TestOrchestratorConfigGrayRendersBlobAndManifest(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	order := h.createApprovedConfigOrder(t, "配置灰度落盘", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)

	if _, err := h.orch.Start(order, "", "ops", "ip"); err != nil {
		t.Fatalf("配置灰度单启动失败: %v", err)
	}
	var readyCount int64
	h.env.db.Model(&model.DeliveryBlob{}).Where("state = ?", model.DeliveryBlobStateReady).Count(&readyCount)
	if readyCount == 0 {
		t.Fatal("配置灰度启动后应已写入至少一个就绪 blob")
	}

	id := agentauth.Identity{NamespaceID: h.f.nsID, Namespace: "prod", ServerID: "t-1", Kind: model.ServerKindBackend}
	manifest, err := h.blob.TargetManifest(id, order)
	if err != nil {
		t.Fatalf("拉目标清单失败: %v", err)
	}
	if len(manifest.Configs) != 0 {
		t.Fatalf("配置项应归一进 Files、Configs 为空，实际 %d", len(manifest.Configs))
	}
	var cfg *DeliveryManifestFileView
	for i := range manifest.Files {
		if manifest.Files[i].Path == "plugins/Gray/config.yml" {
			cfg = &manifest.Files[i]
		}
	}
	if cfg == nil {
		t.Fatalf("清单 Files 应含渲染后的配置文件: %+v", manifest.Files)
	}
	if cfg.Action != model.ChangeItemActionUpdate || cfg.SHA256 == "" || cfg.Size == 0 {
		t.Fatalf("配置文件项字段不符: %+v", cfg)
	}
	if _, err := h.blob.Head(cfg.SHA256); err != nil {
		t.Fatalf("清单配置文件项的 blob 应已就绪: %v", err)
	}
}

// TestPrepareConfigBlobsDedupsPerTarget per-target 渲染同作用域层去重（content-addressed）：
// namespace 层灰度时 t-1 / t-2 链上只有 namespace 层有贡献 → 渲染相同明文 → 同 sha 只落一个 blob。
func TestPrepareConfigBlobsDedupsPerTarget(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeNamespace, h.f.nsID, "shared: true")
	order := h.createApprovedConfigOrder(t, "命名空间层灰度", []string{"t-1", "t-2"}, model.ConfigScopeNamespace, h.f.nsID, versionID)

	if err := h.blob.PrepareConfigBlobs(order, []string{"t-1", "t-2"}); err != nil {
		t.Fatalf("准备配置 blob 失败: %v", err)
	}
	var count int64
	h.env.db.Model(&model.DeliveryBlob{}).Where("state = ?", model.DeliveryBlobStateReady).Count(&count)
	if count != 1 {
		t.Fatalf("同作用域两目标渲染相同明文应去重为 1 个 blob，实际 %d", count)
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

// —— restart 生效判定 = 心跳回归观测（M4，spec §4.6.1 / ADR-0070）——

// TestOrchestratorRestartHeartbeatReturnActivates restart 生效脊柱：
// 推送落定转 activating（不直接 activated）→ agent 回执「已开始关服」仍不判 activated →
// 心跳回归（起始后新指标批）才判 activated → observing → 确认 → completed。
func TestOrchestratorRestartHeartbeatReturnActivates(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0)
	if _, err := h.orch.Start(order.ID, "重启大厅", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick() // 下发 2 目标 pushing
	h.completeAllPushes(t, order.ID)
	h.tick() // pushing→pushed→activating（restart 下发 delivery_activate，不直接 activated）
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivating] != 2 {
		t.Fatalf("restart 推送落定后应 activating（等心跳回归）: %v", c)
	}

	// agent 回执「已开始关服」(done) 不代表 activated——进程已关，需真心跳回归才算数。
	h.completeAllActivates(t, order.ID, model.CommandStatusDone)
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivating] != 2 {
		t.Fatalf("回执「已开始关服」不应直接判 activated: %v", c)
	}

	// 心跳回归：起始时刻之后接收的新指标批 → activated。
	h.advance(3 * time.Second)
	h.seedHeartbeat("t-1", h.clock.UnixMilli())
	h.seedHeartbeat("t-2", h.clock.UnixMilli())
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivated] != 2 {
		t.Fatalf("心跳回归后应 2 目标 activated: %v", c)
	}
	batches, _ := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if batches[0].Status != model.ChangeBatchStatusObserving {
		t.Fatalf("全 activated 后批应 observing: %s", batches[0].Status)
	}

	// 观察窗到点 → awaiting_confirm → 确认 → completed。
	h.advance(6 * time.Second)
	h.tick()
	if _, err := h.orch.ConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("确认末批后单应 completed: %s", got.Status)
	}
}

// TestOrchestratorRestartOnlyPostStartHeartbeat 只认起始时刻之后的心跳批：
// 起始之前的残留心跳（虽新鲜）不得误判回归；起始之后的新批才判 activated。
func TestOrchestratorRestartOnlyPostStartHeartbeat(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0)
	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick() // → activating，activating_started_at = 当前 clock（记为 T0）
	startMs := h.clock.UnixMilli()

	// 关服前残留心跳：起始之前接收的批（新鲜但在锚点之前）——不得误判回归。
	h.seedHeartbeat("t-1", startMs-3000)
	h.seedHeartbeat("t-2", startMs-3000)
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivating] != 2 {
		t.Fatalf("起始前的残留心跳不得误判回归，应仍 activating: %v", c)
	}

	// 起始之后的新心跳批 → 判回归 activated。
	h.advance(2 * time.Second)
	h.seedHeartbeat("t-1", h.clock.UnixMilli())
	h.seedHeartbeat("t-2", h.clock.UnixMilli())
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivated] != 2 {
		t.Fatalf("起始后的新心跳应判 activated: %v", c)
	}
}

// TestOrchestratorRestartTimeoutFailsAndBreaks 「关了没起来」安全阀：
// activate_timeout_sec 内心跳未回归 → 全 failed 并计入失败率熔断。
func TestOrchestratorRestartTimeoutFailsAndBreaks(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 50) // 失败率阈值 50%
	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick() // → activating（activate_timeout_sec=60）
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivating] != 2 {
		t.Fatalf("应 activating: %v", c)
	}

	// 关了没起来：超 activate_timeout_sec 仍无心跳回归 → 全 failed 并触发失败率熔断。
	h.advance(61 * time.Second)
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusFailed] != 2 {
		t.Fatalf("超时未回归应 2 目标 failed: %v", c)
	}
	got := h.reload(order.ID)
	if got.Status != model.ChangeOrderStatusPaused || got.PauseKind != model.PauseKindCircuitBreak {
		t.Fatalf("超时 failed 应计入熔断（circuit_break 暂停）: %+v", got)
	}
	if countAudit(t, h.env.db, model.ActionDeliveryOrderCircuitBreak) != 1 {
		t.Fatal("应记 1 条系统熔断审计")
	}
}

// TestOrchestratorRestartAckFailedFailsImmediately 关服指令回执 failed → 直接 failed（不等心跳回归 / 超时）。
func TestOrchestratorRestartAckFailedFailsImmediately(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0)
	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick() // → activating

	// t-1 关服指令回执 failed → 直接 failed；t-2 未回执且无心跳 → 仍 activating（等回归 / 超时）。
	completeDeliveryCommand(t, h.env.db, order.ID, "t-1", model.CommandTypeDeliveryActivate,
		model.CommandStatusFailed, `{"error":"关服广播失败"}`)
	h.tick()
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	byID := map[string]model.ChangeTarget{}
	for _, tg := range targets {
		byID[tg.ServerID] = tg
	}
	if byID["t-1"].Status != model.ChangeTargetStatusFailed {
		t.Fatalf("t-1 关服回执 failed 应直接 failed: %s", byID["t-1"].Status)
	}
	if byID["t-1"].Error == "" {
		t.Fatal("t-1 failed 应带脱敏原因")
	}
	if byID["t-2"].Status != model.ChangeTargetStatusActivating {
		t.Fatalf("t-2 未回执应仍 activating（等心跳回归 / 超时）: %s", byID["t-2"].Status)
	}
}

// —— restart 重启预热宽限：冷启动 unhealthy 不误熔断（真机逮，push_only 不重启故测不出）——

// restartWarmupObserveOrder 建一张 restart 单并放长观察窗（> 预热宽限），走到全 activated + observing。
// 返回 order；调用方随后 seedHealthUnhealthy 注入冷启动健康态再断言熔断行为。
func (h *orchestratorHarness) restartWarmupObserveOrder(t *testing.T) *model.ChangeOrder {
	t.Helper()
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0) // 失败率阈值 0（关闭），只验健康恶化
	// 观察窗放长到 200s（> 预热宽限 90s），使「预热期内不熔断」与「预热后才熔断」两阶段都落在观察窗内可观测。
	if err := h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order.ID).Update("observe_window_sec", 200).Error; err != nil {
		t.Fatalf("放长观察窗失败: %v", err)
	}
	if _, err := h.orch.Start(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick() // → activating
	h.advance(3 * time.Second)
	h.seedHeartbeat("t-1", h.clock.UnixMilli())
	h.seedHeartbeat("t-2", h.clock.UnixMilli())
	h.tick() // 心跳回归 → activated，批 observing
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivated] != 2 {
		t.Fatalf("心跳回归后应 2 目标 activated: %v", c)
	}
	return order
}

// TestOrchestratorRestartWarmupSkipsHealthBreak restart 目标 activated 后冷启动 unhealthy：
// 预热宽限期内（< 90s）健康恶化不得熔断——重启固有的短暂不健康不是「生效导致的健康恶化」。
func TestOrchestratorRestartWarmupSkipsHealthBreak(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.restartWarmupObserveOrder(t)

	// 冷启动健康：两目标 unhealthy（模拟 restart 重启后健康评分未预热）。
	h.seedHealthUnhealthy("t-1", "t-2")

	// 预热宽限期内（30s < 90s）推进：不得熔断。
	h.advance(30 * time.Second)
	h.tick()
	batches, _ := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if batches[0].Status != model.ChangeBatchStatusObserving {
		t.Fatalf("重启预热期内 unhealthy 不应熔断，批应仍 observing: %s", batches[0].Status)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolling {
		t.Fatalf("重启预热期内不应熔断暂停，单应仍 rolling: %s", got.Status)
	}
}

// TestOrchestratorRestartHealthBreaksAfterWarmup restart 目标预热宽限期后仍 unhealthy：
// 视为真实健康恶化 → 熔断（预热保护只挡冷启动瞬态，不放过真不健康）。
func TestOrchestratorRestartHealthBreaksAfterWarmup(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.restartWarmupObserveOrder(t)
	h.seedHealthUnhealthy("t-1", "t-2")

	// 预热宽限期后（95s > 90s）仍 unhealthy，观察窗（200s）未到：真实健康恶化 → 熔断。
	h.advance(95 * time.Second)
	h.tick()
	got := h.reload(order.ID)
	if got.Status != model.ChangeOrderStatusPaused || got.PauseKind != model.PauseKindCircuitBreak {
		t.Fatalf("预热后仍 unhealthy 应熔断暂停（circuit_break）: %+v", got)
	}
	batches, _ := repository.NewChangeOrderRepository(h.env.db).ListBatches(order.ID)
	if batches[0].Status != model.ChangeBatchStatusFailed {
		t.Fatalf("预热后健康恶化批应 failed: %s", batches[0].Status)
	}
	if countAudit(t, h.env.db, model.ActionDeliveryOrderCircuitBreak) != 1 {
		t.Fatal("健康恶化熔断应记 1 条系统熔断审计")
	}
}
