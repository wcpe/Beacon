package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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
	orch.SetConfigRollbacker(configSvc, repository.NewConfigLayerVersionRepository(env.db))
	// 交付能力版本守卫（FR-264）：版本查身份表、最低版本热读交付测试设置。
	// **默认与生产一致：不校验**（真机默认为空串，避免上报串未核对时全量拒服）——
	// 其余交付用例的历史种子普遍不带 agent 版本，若此处默认开启会把它们全部误判为旧 agent。
	// 守卫专项用例须显式开启：`env.settings.Set(SettingDeliveryMinAgentVersion, deliveryTestMinAgentVersion)`
	// 并用 `seedAgentVersions` 给目标写版本。
	env.settings.Set(SettingDeliveryMinAgentVersion, deliveryDefaultMinAgentVersion)
	orch.SetCapabilityGuard(repository.NewAgentIdentityRepository(env.db), func() string {
		if env.settings.minAgentVersion != nil {
			return *env.settings.minAgentVersion
		}
		return deliveryDefaultMinAgentVersion
	})
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

// completeAllPushesWithResult 把某单全部目标的推送命令置成功并附指定回执摘要（计数用例自定 changedFileCount / backupPresent）。
func (h *orchestratorHarness) completeAllPushesWithResult(t *testing.T, orderID uint, result string) {
	t.Helper()
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(orderID)
	for _, tg := range targets {
		completeDeliveryCommand(t, h.env.db, orderID, tg.ServerID, model.CommandTypeDeliveryPush, model.CommandStatusDone, result)
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

// completeAllActivatesWithResult 把某单全部目标的生效命令置指定终态并附回执摘要（计数 / 备份字段用）。
func (h *orchestratorHarness) completeAllActivatesWithResult(t *testing.T, orderID uint, status, result string) {
	t.Helper()
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(orderID)
	for _, tg := range targets {
		completeDeliveryCommand(t, h.env.db, orderID, tg.ServerID, model.CommandTypeDeliveryActivate, status, result)
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

	if _, err := h.orch.applyStart(order.ID, "上线大厅", "ops", "10.0.0.1"); err != nil {
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
	if _, err := h.orch.applyConfirmBatch(order.ID, 1, "ops", "10.0.0.1"); err != nil {
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

	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认首批失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolling {
		t.Fatalf("确认首批后单仍 rolling: %s", got.Status)
	}

	driveBatchToAwaitConfirm()
	if _, err := h.orch.applyConfirmBatch(order.ID, 2, "ops", "ip"); err != nil {
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

	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyResume(order.ID, resumeModeRetryFailed, "已修复落盘", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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
	_, _ = h.orch.applyStart(order.ID, "", "ops", "ip")
	if _, err := h.orch.Cancel(order.ID, "  ", "ops", "ip"); err == nil {
		t.Fatal("空原因终止应被拒")
	}
}

// seedGrayConfigFile 建一个默认路径配置文件 + 指定作用域链上的一个定稿版本。
func seedGrayConfigFile(t *testing.T, db *gorm.DB, nsID uint, scopeKind string, scopeRefID uint, content string) (uint, uint) {
	t.Helper()
	return seedGrayConfigFileAtPath(t, db, nsID, "plugins/Gray/config.yml", scopeKind, scopeRefID, content)
}

// seedGrayConfigFileAtPath 建一个指定路径配置文件 + 指定作用域链上的一个定稿版本。
func seedGrayConfigFileAtPath(t *testing.T, db *gorm.DB, nsID uint, path, scopeKind string, scopeRefID uint, content string) (uint, uint) {
	t.Helper()
	file := model.ConfigFile{NamespaceID: nsID, Name: path, Format: "yaml"}
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
	if _, err := h.orch.applyStart(first, "", "ops", "ip"); err != nil {
		t.Fatalf("含配置项单应可启动: %v", err)
	}
	if h.reload(first).Status != model.ChangeOrderStatusRolling {
		t.Fatal("首单应进 rolling")
	}

	// 次单：灰度同一 (文件, zone1)、点名 t-2（与首单目标不相交排除目标冲突），应被配置作用域冲突拒绝。
	second := h.createApprovedConfigOrder(t, "配置灰度B", []string{"t-2"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	_, err := h.orch.applyStart(second, "", "ops", "ip")
	if err == nil {
		t.Fatal("配置作用域相交应拒绝启动")
	}
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != "config_scope_conflict" {
		t.Fatalf("应为 config_scope_conflict: %v", err)
	}
}

// TestOrchestratorFileManifestSourceKind 普通文件项在目标清单中显式标记 file_diff 来源。
func TestOrchestratorFileManifestSourceKind(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	target := agentauth.Identity{NamespaceID: h.f.nsID, Namespace: "prod", ServerID: "t-1", Kind: model.ServerKindBackend}
	manifest, err := h.blob.TargetManifest(target, order.ID)
	if err != nil {
		t.Fatalf("拉目标清单失败: %v", err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].SourceKind != "file_diff" {
		t.Fatalf("普通文件项 sourceKind 应为 file_diff: %+v", manifest.Files)
	}
	if len(manifest.Configs) != 0 {
		t.Fatalf("Configs 应恒为空，实际 %d", len(manifest.Configs))
	}
}

// TestOrchestratorConfigGrayRendersBlobAndManifest 配置灰度渲染 + blob 生成 + 清单归一（ADR-0071 决策1/2）：
// 启动即由控制面渲染配置明文写入就绪 blob；目标拉清单时配置项归一为文件项进 Files（sha 指向就绪 blob），Configs 空。
func TestOrchestratorConfigGrayRendersBlobAndManifest(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	order := h.createApprovedConfigOrder(t, "配置灰度落盘", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)

	if _, err := h.orch.applyStart(order, "", "ops", "ip"); err != nil {
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
		return
	}
	if cfg.Action != model.ChangeItemActionUpdate || cfg.SHA256 == "" || cfg.Size == 0 {
		t.Fatalf("配置文件项字段不符: %+v", cfg)
	}
	if cfg.SourceKind != "config_artifact" {
		t.Fatalf("配置冻结工件 sourceKind 应为 config_artifact: %+v", cfg)
	}
	if _, err := h.blob.Head(cfg.SHA256); err != nil {
		t.Fatalf("清单配置文件项的 blob 应已就绪: %v", err)
	}
}

// TestConfigArtifactOverridesSamePathFileDiff 同一路径同时出现文件差异与配置工件时，清单只保留配置工件。
func TestConfigArtifactOverridesSamePathFileDiff(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	order := h.createApprovedConfigOrder(t, "同路径配置优先", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	if _, err := h.orch.applyStart(order, "", "ops", "ip"); err != nil {
		t.Fatalf("启动配置单失败: %v", err)
	}
	path, action, sha, size := "plugins/Gray/config.yml", model.ChangeItemActionUpdate, strings.Repeat("a", 64), int64(1)
	mustCreate(t, h.env.db, &model.ChangeOrderItem{
		OrderID: order, Kind: model.ChangeItemKindFileDiff,
		Path: &path, Action: &action, SHA256: &sha, SizeBytes: &size,
	})

	target := agentauth.Identity{NamespaceID: h.f.nsID, Namespace: "prod", ServerID: "t-1", Kind: model.ServerKindBackend}
	manifest, err := h.blob.TargetManifest(target, order)
	if err != nil {
		t.Fatalf("拉目标清单失败: %v", err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != path || manifest.Files[0].SourceKind != DeliveryManifestSourceConfigArtifact {
		t.Fatalf("同路径冲突应由配置工件覆盖普通文件差异: %+v", manifest.Files)
	}
}

// TestPrepareConfigBlobsCombinesPinsPerFile 同一配置文件的多作用域变更必须合并全部 pin 后只冻结一次。
func TestPrepareConfigBlobsCombinesPinsPerFile(t *testing.T) {
	h := newOrchestratorHarness(t)
	file := model.ConfigFile{NamespaceID: h.f.nsID, Name: "plugins/Multi/config.yml", Format: "yaml"}
	mustCreate(t, h.env.db, &file)
	versions := []model.ConfigLayerVersion{
		{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeNamespace, ScopeRefID: h.f.nsID, VersionNo: 1, Content: "base: selected"},
		{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeNamespace, ScopeRefID: h.f.nsID, VersionNo: 2, Content: "base: drift"},
		{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeZone, ScopeRefID: h.f.zone1ID, VersionNo: 1, Content: "zone: selected"},
		{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeZone, ScopeRefID: h.f.zone1ID, VersionNo: 2, Content: "zone: drift"},
	}
	for i := range versions {
		mustCreate(t, h.env.db, &versions[i])
	}
	detail, err := h.env.orders.Create(h.f.nsID, ChangeOrderInput{
		Title: strPtr("同文件多作用域"), Selector: &ChangeSelector{Servers: []string{"t-1"}},
	}, "ops-chen", "10.0.0.1")
	if err != nil {
		t.Fatalf("建配置单失败: %v", err)
	}
	changes := []ChangeConfigInput{
		{ConfigScopeKind: model.ConfigScopeNamespace, ConfigScopeID: h.f.nsID, ConfigToVersionID: versions[0].ID},
		{ConfigScopeKind: model.ConfigScopeZone, ConfigScopeID: h.f.zone1ID, ConfigToVersionID: versions[2].ID},
	}
	if _, err := h.env.orders.Update(detail.ID, ChangeOrderInput{ConfigChanges: &changes}, "ops-chen", ""); err != nil {
		t.Fatalf("挂配置版本失败: %v", err)
	}
	setOrderStatus(t, h.env.db, detail.ID, model.ChangeOrderStatusApproved)
	if _, err := h.orch.applyStart(detail.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动配置单失败: %v", err)
	}

	target := agentauth.Identity{NamespaceID: h.f.nsID, Namespace: "prod", ServerID: "t-1", Kind: model.ServerKindBackend}
	manifest, err := h.blob.TargetManifest(target, detail.ID)
	if err != nil {
		t.Fatalf("拉目标清单失败: %v", err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].SourceKind != DeliveryManifestSourceConfigArtifact {
		t.Fatalf("同文件多作用域应只冻结一个配置工件: %+v", manifest.Files)
	}
	reader, _, release, err := h.blob.Open(manifest.Files[0].SHA256)
	if err != nil {
		t.Fatalf("打开配置工件失败: %v", err)
	}
	content, readErr := io.ReadAll(reader)
	_ = reader.Close()
	release()
	if readErr != nil {
		t.Fatalf("读取配置工件失败: %v", readErr)
	}
	plain := string(content)
	if !strings.Contains(plain, "base: selected") || !strings.Contains(plain, "zone: selected") || strings.Contains(plain, "drift") {
		t.Fatalf("配置工件应同时固定两个选中版本且不受 head 漂移影响: %s", plain)
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

// TestPrepareConfigBlobsReplacesStaleArtifacts 重跑准备时应原子替换本单旧工件，清除已移除路径与目标授权。
func TestPrepareConfigBlobsReplacesStaleArtifacts(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	order := h.createApprovedConfigOrder(t, "替换旧工件", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	mustCreate(t, h.env.db, &model.DeliveryConfigArtifact{
		OrderID: order, ServerID: "t-2", Path: "plugins/Stale/config.yml", SHA256: strings.Repeat("f", 64), SizeBytes: 1,
	})

	if err := h.blob.PrepareConfigBlobs(order, []string{"t-1"}); err != nil {
		t.Fatalf("准备配置 blob 失败: %v", err)
	}
	var arts []model.DeliveryConfigArtifact
	if err := h.env.db.Where("order_id = ?", order).Find(&arts).Error; err != nil {
		t.Fatalf("查询配置工件失败: %v", err)
	}
	if len(arts) != 1 || arts[0].ServerID != "t-1" || arts[0].Path != "plugins/Gray/config.yml" {
		t.Fatalf("重跑准备后应只保留当前目标与路径的工件: %+v", arts)
	}
}

// TestOrchestratorConfigBlobDownloadAuthorized 配置灰度冻结工件下载授权端到端（ADR-0071，修复 push 阶段 config blob 恒 403）：
// 含配置项单启动即落工件 → 目标可授权拉取渲染 config blob（含 HEAD），非目标 403，单转终态后不再授权。
func TestOrchestratorConfigBlobDownloadAuthorized(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	order := h.createApprovedConfigOrder(t, "配置灰度授权", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	if _, err := h.orch.applyStart(order, "", "ops", "ip"); err != nil {
		t.Fatalf("配置灰度单启动失败: %v", err)
	}
	// 从目标清单取渲染 config 文件项的 sha（即 agent push 阶段要下载的 blob）。
	target := agentauth.Identity{NamespaceID: h.f.nsID, Namespace: "prod", ServerID: "t-1", Kind: model.ServerKindBackend}
	manifest, err := h.blob.TargetManifest(target, order)
	if err != nil {
		t.Fatalf("拉目标清单失败: %v", err)
	}
	var sha string
	for i := range manifest.Files {
		if manifest.Files[i].Path == "plugins/Gray/config.yml" {
			sha = manifest.Files[i].SHA256
		}
	}
	if sha == "" {
		t.Fatalf("清单应含渲染 config 文件项: %+v", manifest.Files)
	}
	// 目标可授权下载（修复前此处恒 blob_forbidden）。
	if err := h.blob.AuthorizeBlobDownload(target, sha); err != nil {
		t.Fatalf("目标应可授权下载 config blob: %v", err)
	}
	// HEAD 经下载侧委派同样放行。
	if err := h.blob.AuthorizeBlobHead(target, sha); err != nil {
		t.Fatalf("目标应可授权 HEAD config blob: %v", err)
	}
	// 非目标 t-2 不授权。
	nonTarget := agentauth.Identity{NamespaceID: h.f.nsID, Namespace: "prod", ServerID: "t-2", Kind: model.ServerKindBackend}
	if err := h.blob.AuthorizeBlobDownload(nonTarget, sha); !errors.Is(err, apperr.ErrDeliveryBlobForbidden) {
		t.Fatalf("非目标应 blob_forbidden，实际 %v", err)
	}
	// 单转终态（completed 非活动）后不再授权（保留期由清理护栏另管）。
	setOrderStatus(t, h.env.db, order, model.ChangeOrderStatusCompleted)
	if err := h.blob.AuthorizeBlobDownload(target, sha); !errors.Is(err, apperr.ErrDeliveryBlobForbidden) {
		t.Fatalf("单转终态后应 blob_forbidden，实际 %v", err)
	}
}

// TestOrchestratorConfigManifestMissingArtifact 含配置项但工件未准备时清单明确报错（ADR-0071，不静默漏发配置）。
// 直接固化目标（使 isOrderTarget 走快照命中）但不跑 PrepareConfigBlobs，模拟 payload 准备缺口。
func TestOrchestratorConfigManifestMissingArtifact(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	order := h.createApprovedConfigOrder(t, "工件未准备", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	mustCreate(t, h.env.db, &model.ChangeTarget{
		OrderID: order, BatchID: 1, ServerID: "t-1", Status: model.ChangeTargetStatusPending,
	})

	target := agentauth.Identity{NamespaceID: h.f.nsID, Namespace: "prod", ServerID: "t-1", Kind: model.ServerKindBackend}
	if _, err := h.blob.TargetManifest(target, order); !errors.Is(err, apperr.ErrDeliveryConfigArtifactMissing) {
		t.Fatalf("工件未准备应返回 config_artifact_missing，实际 %v", err)
	}
}

// TestOrchestratorConfigManifestPartialArtifactMissing 多配置单仅缺部分工件时也必须 fail-closed，不能下发残缺清单。
func TestOrchestratorConfigManifestPartialArtifactMissing(t *testing.T) {
	h := newOrchestratorHarness(t)
	_, zoneVersion := seedGrayConfigFileAtPath(t, h.env.db, h.f.nsID, "plugins/Gray/config.yml", model.ConfigScopeZone, h.f.zone1ID, "zone: true")
	_, namespaceVersion := seedGrayConfigFileAtPath(t, h.env.db, h.f.nsID, "plugins/Other/config.yml", model.ConfigScopeNamespace, h.f.nsID, "namespace: true")
	detail, err := h.env.orders.Create(h.f.nsID, ChangeOrderInput{
		Title: strPtr("部分工件缺失"), Selector: &ChangeSelector{Servers: []string{"t-1"}},
	}, "ops-chen", "10.0.0.1")
	if err != nil {
		t.Fatalf("建配置单失败: %v", err)
	}
	changes := []ChangeConfigInput{
		{ConfigScopeKind: model.ConfigScopeZone, ConfigScopeID: h.f.zone1ID, ConfigToVersionID: zoneVersion},
		{ConfigScopeKind: model.ConfigScopeNamespace, ConfigScopeID: h.f.nsID, ConfigToVersionID: namespaceVersion},
	}
	if _, err := h.env.orders.Update(detail.ID, ChangeOrderInput{ConfigChanges: &changes}, "ops-chen", ""); err != nil {
		t.Fatalf("挂配置版本失败: %v", err)
	}
	setOrderStatus(t, h.env.db, detail.ID, model.ChangeOrderStatusApproved)
	if _, err := h.orch.applyStart(detail.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动配置单失败: %v", err)
	}
	if err := h.env.db.Where("order_id = ? AND server_id = ? AND path = ?", detail.ID, "t-1", "plugins/Other/config.yml").
		Delete(&model.DeliveryConfigArtifact{}).Error; err != nil {
		t.Fatalf("删除一条工件失败: %v", err)
	}

	target := agentauth.Identity{NamespaceID: h.f.nsID, Namespace: "prod", ServerID: "t-1", Kind: model.ServerKindBackend}
	if _, err := h.blob.TargetManifest(target, detail.ID); !errors.Is(err, apperr.ErrDeliveryConfigArtifactMissing) {
		t.Fatalf("部分工件缺失应返回 config_artifact_missing，实际 %v", err)
	}
}

// TestOrchestratorConfigSwitchOnLastBatch 末批切版记账 + 审计（ADR-0071 决策4）：含配置项单末批确认即 completed，
// 记一条配置切版审计，且「已交付版本」经 completed 单历史反查得到本单 to_version（下一单 from 锚点来源）。
func TestOrchestratorConfigSwitchOnLastBatch(t *testing.T) {
	h := newOrchestratorHarness(t)
	fileID, versionID := seedGrayConfigFile(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID, "a: 1")
	order := h.createApprovedConfigOrder(t, "配置灰度切版", []string{"t-1"}, model.ConfigScopeZone, h.f.zone1ID, versionID)
	// push_only + 短观察窗便于驱动到末批完成（切版记账与生效方式无关）。
	h.env.db.Model(&model.ChangeOrder{}).Where("id = ?", order).Updates(map[string]any{
		"activation_method": model.ActivationMethodPushOnly, "observe_window_sec": 5, "batch_sizes": encodeBatchSizes([]int{100}),
	})

	if _, err := h.orch.applyStart(order, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order)
	h.tick()
	h.advance(6 * time.Second)
	h.tick()
	if _, err := h.orch.applyConfirmBatch(order, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认末批失败: %v", err)
	}
	if h.reload(order).Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("确认末批后单应 completed: %s", h.reload(order).Status)
	}
	if got := countAudit(t, h.env.db, model.ActionDeliveryOrderConfigSwitch); got != 1 {
		t.Fatalf("应记 1 条配置切版审计，实际 %d", got)
	}
	// 末批 completed 后「已交付版本」= 本单 to_version（下一单 from 锚点来源，ADR-0071 决策3）。
	delivered, err := repository.NewChangeOrderRepository(h.env.db).FindLatestDeliveredToVersionID(fileID, model.ConfigScopeZone, h.f.zone1ID)
	if err != nil || delivered == nil || *delivered != versionID {
		t.Fatalf("末批完成后应查到已交付版本 = %d，实际 %v（err=%v）", versionID, delivered, err)
	}
}

// TestOrchestratorStartConflict 目标集与其他活动单相交时拒绝启动（ADR-0071 §4.1）。
func TestOrchestratorStartConflict(t *testing.T) {
	h := newOrchestratorHarness(t)
	first := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(first.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("首单启动失败: %v", err)
	}
	// 第二单目标集（默认夹具同为 t-1/t-2）与首单相交 → 冲突。
	second := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	_, err := h.orch.applyStart(second.ID, "", "ops", "ip")
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

	if _, err := h.orch.applyStart(detail.ID, "", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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

// prepareHotReloadActivatingOrder 建立已进入 activating 的 hot_reload 单。
func prepareHotReloadActivatingOrder(t *testing.T) (*orchestratorHarness, *model.ChangeOrder) {
	t.Helper()
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodHotReload, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick()
	if got := h.targetStatuses(order.ID)[model.ChangeTargetStatusActivating]; got != 2 {
		t.Fatalf("hot_reload 推送后应有 2 个 activating 目标，实际 %d", got)
	}
	return h, order
}

// ageActivateCommands 把本单生效命令创建时间移到给定时刻，供超时测试使用。
func ageActivateCommands(t *testing.T, h *orchestratorHarness, orderID uint, createdAt time.Time) {
	t.Helper()
	var commands []model.AgentCommand
	if err := h.env.db.Where("type = ?", model.CommandTypeDeliveryActivate).Find(&commands).Error; err != nil {
		t.Fatalf("查询生效命令失败: %v", err)
	}
	for i := range commands {
		var payload deliveryCommandPayload
		if json.Unmarshal([]byte(commands[i].Payload), &payload) == nil && payload.OrderID == orderID {
			if err := h.env.db.Model(&model.AgentCommand{}).Where("id = ?", commands[i].ID).Update("created_at", createdAt).Error; err != nil {
				t.Fatalf("调整生效命令时间失败: %v", err)
			}
		}
	}
}

// TestOrchestratorHotReloadAckStateMachine 锁定 hot_reload 控制面回执推进：成功、失败、过期与超时。
func TestOrchestratorHotReloadAckStateMachine(t *testing.T) {
	cases := []struct {
		name, commandStatus, targetStatus string
		timeout                           bool
	}{
		{name: "成功回执", commandStatus: model.CommandStatusDone, targetStatus: model.ChangeTargetStatusActivated},
		{name: "失败回执", commandStatus: model.CommandStatusFailed, targetStatus: model.ChangeTargetStatusFailed},
		{name: "命令过期", commandStatus: model.CommandStatusExpired, targetStatus: model.ChangeTargetStatusFailed},
		{name: "等待超时", commandStatus: model.CommandStatusPending, targetStatus: model.ChangeTargetStatusFailed, timeout: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, order := prepareHotReloadActivatingOrder(t)
			if tc.timeout {
				ageActivateCommands(t, h, order.ID, h.clock.Add(-61*time.Second))
			} else {
				h.completeAllActivates(t, order.ID, tc.commandStatus)
			}
			h.tick()
			if got := h.targetStatuses(order.ID)[tc.targetStatus]; got != 2 {
				t.Fatalf("目标状态 %s 应有 2 个，实际 %d", tc.targetStatus, got)
			}
			if tc.timeout {
				var expired int64
				h.env.db.Model(&model.AgentCommand{}).Where("type = ? AND status = ?", model.CommandTypeDeliveryActivate, model.CommandStatusExpired).Count(&expired)
				if expired != 2 {
					t.Fatalf("超时后生效命令应有 2 个 expired，实际 %d", expired)
				}
			}
		})
	}
}

// TestOrchestratorActivateFailureCarriesCounts 锁定 FR-266「已落盘仅通知失败」的部分成功口径：
// agent 生效回执 failed 时携带的变更计数与备份标记必须落到目标行、并经目标视图透出——否则失败目标与
// 「什么都没做」同形，运维与机器主体都无从判断该回滚还是重推。
func TestOrchestratorActivateFailureCarriesCounts(t *testing.T) {
	h, order := prepareHotReloadActivatingOrder(t)
	// 生效回执 failed：文件已落盘、仅通知失败（部分成功）——带 2 个变更文件与备份存在。
	h.completeAllActivatesWithResult(t, order.ID, model.CommandStatusFailed,
		`{"changedFileCount":2,"skippedFileCount":0,"backupPresent":true,"error":"文件已落盘（本单变更已生效于磁盘），仅配置变更通知失败"}`)
	h.tick()

	targets, err := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	if err != nil {
		t.Fatalf("查目标失败: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("应有 2 个目标，实际 %d", len(targets))
	}
	for i := range targets {
		if targets[i].Status != model.ChangeTargetStatusFailed {
			t.Fatalf("目标应 failed，实际 %s", targets[i].Status)
		}
		if targets[i].ChangedFileCount != 2 {
			t.Fatalf("失败目标应保留回执变更计数 2，实际 %d", targets[i].ChangedFileCount)
		}
		if !targets[i].BackupPresent {
			t.Fatalf("失败目标应保留备份标记（可否回滚的依据）")
		}
		if targets[i].Error == "" {
			t.Fatalf("失败目标应留可读原因")
		}
	}

	// 视图透出：目标视图（GET .../targets 的数据源）逐字段带出计数与备份标记。
	views := changeTargetViews(targets, map[uint]int{}, nil)
	if len(views) != 2 {
		t.Fatalf("视图应有 2 行，实际 %d", len(views))
	}
	for i := range views {
		if views[i].ChangedFileCount != 2 || !views[i].BackupPresent {
			t.Fatalf("视图应透出计数与备份标记，实际 count=%d backup=%v",
				views[i].ChangedFileCount, views[i].BackupPresent)
		}
		if views[i].Error == nil || *views[i].Error == "" {
			t.Fatalf("视图应透出失败原因")
		}
	}
}

// TestOrchestratorActivateFailureZeroCountsKeepsPushedFacts 锁定计数「只增不减」：
// restart 关服失败一类 0 计数回执不得把推送阶段已落定的 changed_file_count / backup_present 清零。
func TestOrchestratorActivateFailureZeroCountsKeepsPushedFacts(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID) // 推送回执：changedFileCount=1、backupPresent=true
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivating] != 2 {
		t.Fatalf("restart 推送落定后应 activating: %v", c)
	}

	// 关服原语失败：agent 回执只带原因、无计数。
	h.completeAllActivatesWithResult(t, order.ID, model.CommandStatusFailed, `{"error":"优雅关服失败：调度器不可用"}`)
	h.tick()

	targets, err := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	if err != nil {
		t.Fatalf("查目标失败: %v", err)
	}
	for i := range targets {
		if targets[i].Status != model.ChangeTargetStatusFailed {
			t.Fatalf("目标应 failed，实际 %s", targets[i].Status)
		}
		if targets[i].ChangedFileCount != 1 {
			t.Fatalf("0 计数回执不得清零推送阶段计数，实际 %d", targets[i].ChangedFileCount)
		}
		if !targets[i].BackupPresent {
			t.Fatalf("0 计数回执不得清零推送阶段备份标记")
		}
	}
}

// TestOrchestratorActivateFailureSmallerCountKeepsPushedFacts 锁定「只增不减」对**更小正数**同样成立：
// 推送阶段落定 3 项（1 个普通文件 + 2 个配置工件），生效阶段按工件数取下界 2 → 目标行必须保持 3，
// 否则「改了 3 个」会被下界回执改写成「改了 2 个」（FR-266 评审 P1）。
func TestOrchestratorActivateFailureSmallerCountKeepsPushedFacts(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodHotReload, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushesWithResult(t, order.ID, `{"changedFileCount":3,"skippedFileCount":0,"backupPresent":true}`)
	h.tick()
	if c := h.targetStatuses(order.ID); c[model.ChangeTargetStatusActivating] != 2 {
		t.Fatalf("hot_reload 推送落定后应 activating: %v", c)
	}

	// 生效失败回执按配置工件数取下界（2 < 3）：不得把已落定的 3 覆写成 2。
	h.completeAllActivatesWithResult(t, order.ID, model.CommandStatusFailed,
		`{"changedFileCount":2,"skippedFileCount":0,"backupPresent":true,"error":"文件已落盘，仅配置变更通知失败"}`)
	h.tick()

	targets, err := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	if err != nil {
		t.Fatalf("查目标失败: %v", err)
	}
	for i := range targets {
		if targets[i].Status != model.ChangeTargetStatusFailed {
			t.Fatalf("目标应 failed，实际 %s", targets[i].Status)
		}
		if targets[i].ChangedFileCount != 3 {
			t.Fatalf("更小的正数回执不得覆写已落定计数（期望 3，实际 %d）", targets[i].ChangedFileCount)
		}
		if !targets[i].BackupPresent {
			t.Fatalf("备份标记应保持 true")
		}
	}
}

// TestOrchestratorPushingFailureMarksPushed 锁定「推送中途失败但盘上已改」的回滚资格（FR-266 终审 P2）：
// 变更计数 > 0 或备份已在 → 补 pushed_at（回滚候选 / 预检都以它为准），目标可被整单回滚覆盖；
// 下载 / 备份阶段失败（计数 0 且无备份）不补，保持「没动盘就不入回滚集」。
func TestOrchestratorPushingFailureMarksPushed(t *testing.T) {
	cases := []struct {
		name         string
		result       string
		wantPushedAt bool
		wantChanged  int
		wantBackup   bool
	}{
		{
			name:         "覆盖中途失败且备份在盘",
			result:       `{"changedFileCount":1,"backupPresent":true,"error":"覆盖中途失败（已变更 1 项，备份已在盘）"}`,
			wantPushedAt: true, wantChanged: 1, wantBackup: true,
		},
		{
			name:         "覆盖 0 项但备份已在",
			result:       `{"changedFileCount":0,"backupPresent":true,"error":"覆盖中途失败（已变更 0 项，备份已在盘）"}`,
			wantPushedAt: true, wantChanged: 0, wantBackup: true,
		},
		{
			name:         "下载阶段失败未动盘",
			result:       `{"changedFileCount":0,"backupPresent":false,"error":"流式下载 / 校验失败"}`,
			wantPushedAt: false, wantChanged: 0, wantBackup: false,
		},
		{
			name:         "备份阶段失败未动盘",
			result:       `{"changedFileCount":0,"backupPresent":false,"error":"备份失败，未改动原文件"}`,
			wantPushedAt: false, wantChanged: 0, wantBackup: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOrchestratorHarness(t)
			order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0)
			if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
				t.Fatalf("启动失败: %v", err)
			}
			h.tick()
			// 推送回执 failed（携部分成功事实）。
			targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
			for _, tg := range targets {
				completeDeliveryCommand(t, h.env.db, order.ID, tg.ServerID, model.CommandTypeDeliveryPush, model.CommandStatusFailed, tc.result)
			}
			h.tick()

			rows, err := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
			if err != nil {
				t.Fatalf("查目标失败: %v", err)
			}
			for i := range rows {
				if rows[i].Status != model.ChangeTargetStatusFailed {
					t.Fatalf("目标应 failed，实际 %s", rows[i].Status)
				}
				if (rows[i].PushedAt != nil) != tc.wantPushedAt {
					t.Fatalf("pushed_at 命中期望 %v，实际 %v", tc.wantPushedAt, rows[i].PushedAt)
				}
				if rows[i].ChangedFileCount != tc.wantChanged {
					t.Fatalf("计数期望 %d，实际 %d", tc.wantChanged, rows[i].ChangedFileCount)
				}
				if rows[i].BackupPresent != tc.wantBackup {
					t.Fatalf("备份标记期望 %v，实际 %v", tc.wantBackup, rows[i].BackupPresent)
				}
			}
			// 回滚候选口径：仅「曾覆盖磁盘」的目标计入整单回滚目标集。
			n, err := repository.NewChangeOrderRepository(h.env.db).CountTargetsToRollback(order.ID)
			if err != nil {
				t.Fatalf("统计回滚候选失败: %v", err)
			}
			want := int64(0)
			if tc.wantPushedAt {
				want = int64(len(rows))
			}
			if n != want {
				t.Fatalf("回滚候选数期望 %d，实际 %d", want, n)
			}
		})
	}
}

// —— restart 生效判定 = 心跳回归观测（M4，spec §4.6.1 / ADR-0070）——

// TestOrchestratorRestartHeartbeatReturnActivates restart 生效脊柱：
// 推送落定转 activating（不直接 activated）→ agent 回执「已开始关服」仍不判 activated →
// 心跳回归（起始后新指标批）才判 activated → observing → 确认 → completed。
func TestOrchestratorRestartHeartbeatReturnActivates(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0)
	if _, err := h.orch.applyStart(order.ID, "重启大厅", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
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

// —— 整单回滚（FR-167，spec §4.7.2）——

// completedPushOnlyOrder 走完整 push_only 脊柱到 completed（目标 activated + pushed_at + backup_present）。
func (h *orchestratorHarness) completedPushOnlyOrder(t *testing.T) *model.ChangeOrder {
	t.Helper()
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick() // pushed→activated（push_only），批 observing
	h.advance(6 * time.Second)
	h.tick() // observing→awaiting_confirm
	if _, err := h.orch.applyConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	return h.reload(order.ID)
}

// completedRestartOrder 走完整 restart 脊柱到 completed（心跳回归 activated）。
func (h *orchestratorHarness) completedRestartOrder(t *testing.T) *model.ChangeOrder {
	t.Helper()
	order := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodRestart, 0)
	if _, err := h.orch.applyStart(order.ID, "", "ops", "ip"); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	h.tick()
	h.completeAllPushes(t, order.ID)
	h.tick() // → activating
	h.advance(3 * time.Second)
	h.seedHeartbeat("t-1", h.clock.UnixMilli())
	h.seedHeartbeat("t-2", h.clock.UnixMilli())
	h.tick() // 心跳回归 → activated，批 observing
	h.advance(6 * time.Second)
	h.tick() // observing→awaiting_confirm
	if _, err := h.orch.applyConfirmBatch(order.ID, 1, "ops", "ip"); err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	return h.reload(order.ID)
}

// rollbackTargetsByServer 取某单逐台目标行并按键 serverId 建索引（回滚断言用）。
func (h *orchestratorHarness) rollbackTargetsByServer(orderID uint) map[string]model.ChangeTarget {
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(orderID)
	byServer := make(map[string]model.ChangeTarget, len(targets))
	for _, tg := range targets {
		byServer[tg.ServerID] = tg
	}
	return byServer
}

// completeAllRollbacks 把某单全部目标的回滚命令置指定终态（模拟 agent 还原备份回执）。
func (h *orchestratorHarness) completeAllRollbacks(t *testing.T, orderID uint, status string) {
	t.Helper()
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(orderID)
	for _, tg := range targets {
		completeDeliveryCommand(t, h.env.db, orderID, tg.ServerID, model.CommandTypeDeliveryRollback, status, "")
	}
}

// TestOrchestratorRollbackPushOnlyHappyPath push_only 整单回滚：completed→rolling_back→下发回滚→还原回执→rolled_back。
func TestOrchestratorRollbackPushOnlyHappyPath(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	if order.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("前置应 completed: %s", order.Status)
	}
	if _, err := h.orch.applyRollback(order.ID, "回退变更", "ops", "ip"); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRollingBack {
		t.Fatalf("应 rolling_back: %s", got.Status)
	}
	h.tick() // 下发 delivery_rollback，rollback_status pending→running
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick() // 还原回执 done → rolled_back → 单自动 rolled_back
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("push_only 回滚应 rolled_back: %s", got.Status)
	}
	if countAudit(t, h.env.db, model.ActionDeliveryOrderRollback) != 1 {
		t.Fatal("应记 1 条回滚审计")
	}
}

// TestOrchestratorRollbackRestartHeartbeatReturn restart 回滚：还原后 agent 关服，心跳回归才判 rolled_back。
func TestOrchestratorRollbackRestartHeartbeatReturn(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedRestartOrder(t)
	if _, err := h.orch.applyRollback(order.ID, "回退", "ops", "ip"); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	h.tick() // 下发回滚
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick() // 还原 done → 重置回滚重启锚点（首次），仍 running 等心跳
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRollingBack {
		t.Fatalf("重置锚点后应仍 rolling_back（等心跳）: %s", got.Status)
	}
	// 心跳回归 → rolled_back。
	h.advance(3 * time.Second)
	h.seedHeartbeat("t-1", h.clock.UnixMilli())
	h.seedHeartbeat("t-2", h.clock.UnixMilli())
	h.tick()
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("restart 回滚心跳回归应 rolled_back: %s", got.Status)
	}
}

// TestOrchestratorRollbackRejects 回滚入参 / 状态校验：原因必填、非法态拒绝。
func TestOrchestratorRollbackRejects(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	if _, err := h.orch.applyRollback(order.ID, "  ", "ops", "ip"); err == nil {
		t.Fatal("空原因应拒绝")
	}
	draft := h.createApprovedFileOrder(t, []int{100}, model.ActivationMethodPushOnly, 0)
	if _, err := h.orch.applyRollback(draft.ID, "回退", "ops", "ip"); err == nil {
		t.Fatal("approved（未曾推送）单回滚应拒绝非法态")
	}
}

// TestOrchestratorRollbackBackupMissingFails 备份缺失目标预检直接 failed，其余正常回滚；有 failed 停待人工 FinishRollback。
func TestOrchestratorRollbackBackupMissingFails(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	// 人为让 t-1 备份缺失（保留策略清理场景）。
	if err := h.env.db.Model(&model.ChangeTarget{}).
		Where("order_id = ? AND server_id = ?", order.ID, "t-1").
		Update("backup_present", false).Error; err != nil {
		t.Fatalf("置备份缺失失败: %v", err)
	}
	if _, err := h.orch.applyRollback(order.ID, "回退", "ops", "ip"); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	h.tick()
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick()
	targets, _ := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	byID := map[string]model.ChangeTarget{}
	for _, tg := range targets {
		byID[tg.ServerID] = tg
	}
	if byID["t-1"].RollbackStatus != model.RollbackStatusFailed {
		t.Fatalf("备份缺失目标应 failed: %s", byID["t-1"].RollbackStatus)
	}
	if byID["t-2"].RollbackStatus != model.RollbackStatusRolledBack {
		t.Fatalf("有备份目标应 rolled_back: %s", byID["t-2"].RollbackStatus)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRollingBack {
		t.Fatalf("有 failed 应停 rolling_back 待人工: %s", got.Status)
	}
	// 人工结束回滚。
	if _, err := h.orch.applyFinishRollback(order.ID, "ops", "ip"); err != nil {
		t.Fatalf("结束回滚失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("结束回滚后应 rolled_back: %s", got.Status)
	}
}

// TestOrchestratorRollbackRetryOnlyResetsFailedTargets 回滚重试（FR-262r，spec §4.7.2「失败目标可重试」）：
// 重试只把失败目标重置 pending 重推，已成功目标与首次回滚时刻都不动，且每次动作各记一条审计。
func TestOrchestratorRollbackRetryOnlyResetsFailedTargets(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	// 人为让 t-1 备份缺失（保留策略清理场景）→ 首次回滚它直接 failed。
	if err := h.env.db.Model(&model.ChangeTarget{}).
		Where("order_id = ? AND server_id = ?", order.ID, "t-1").
		Update("backup_present", false).Error; err != nil {
		t.Fatalf("置备份缺失失败: %v", err)
	}
	first, err := h.orch.applyRollback(order.ID, "回退变更", "ops", "ip")
	if err != nil {
		t.Fatalf("首次回滚失败: %v", err)
	}
	if first.RollbackAt == nil {
		t.Fatal("首次回滚应记 rollback_at")
	}
	firstAt := *first.RollbackAt
	h.tick()
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick()
	byServer := h.rollbackTargetsByServer(order.ID)
	if byServer["t-1"].RollbackStatus != model.RollbackStatusFailed {
		t.Fatalf("备份缺失目标应 failed: %s", byServer["t-1"].RollbackStatus)
	}
	if byServer["t-2"].RollbackStatus != model.RollbackStatusRolledBack {
		t.Fatalf("有备份目标应 rolled_back: %s", byServer["t-2"].RollbackStatus)
	}

	retried, err := h.orch.applyRollback(order.ID, "补做备份后重试", "ops2", "ip")
	if err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if retried.Status != model.ChangeOrderStatusRollingBack {
		t.Fatalf("重试后应仍 rolling_back: %s", retried.Status)
	}
	if retried.RollbackAt == nil || !retried.RollbackAt.Equal(firstAt) {
		t.Fatal("重试不得改写首次回滚时刻（rollback_at）")
	}
	afterRetry := h.rollbackTargetsByServer(order.ID)
	if afterRetry["t-1"].RollbackStatus != model.RollbackStatusPending {
		t.Fatalf("重试应把失败目标重置 pending: %s", afterRetry["t-1"].RollbackStatus)
	}
	if afterRetry["t-1"].RollbackError != "" {
		t.Fatalf("重试应清空失败原因: %q", afterRetry["t-1"].RollbackError)
	}
	if afterRetry["t-2"].RollbackStatus != model.RollbackStatusRolledBack {
		t.Fatalf("重试不得重置已成功目标: %s", afterRetry["t-2"].RollbackStatus)
	}

	// 重推：失败目标重新下发回滚命令并走到终态，单自动收口。
	h.tick()
	if got := h.rollbackTargetsByServer(order.ID)["t-1"].RollbackStatus; got != model.RollbackStatusRunning {
		t.Fatalf("重试后应重新下发回滚命令（running）: %s", got)
	}
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick()
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("重试补齐后应自动 rolled_back: %s", got.Status)
	}
	if n := countAudit(t, h.env.db, model.ActionDeliveryOrderRollback); n != 2 {
		t.Fatalf("首次回滚 + 重试应各记一条审计（共 2），实际 %d", n)
	}
}

// TestOrchestratorRollbackInitializesNullStatusCoveredTargets 推进器穷尽分类（FR-262r）：
// 曾覆盖磁盘但 rollback_status 为空的目击行不得被静默丢弃——就地补回滚初态并纳入本次推进。
func TestOrchestratorRollbackInitializesNullStatusCoveredTargets(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	if _, err := h.orch.applyRollback(order.ID, "回退变更", "ops", "ip"); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	// 模拟历史数据 / 人工改库：t-1 曾被覆盖却丢了回滚初态（rollback_status 置空）。
	if err := h.env.db.Model(&model.ChangeTarget{}).
		Where("order_id = ? AND server_id = ?", order.ID, "t-1").
		Update("rollback_status", nil).Error; err != nil {
		t.Fatalf("清空回滚状态失败: %v", err)
	}
	h.tick()
	if got := h.rollbackTargetsByServer(order.ID)["t-1"].RollbackStatus; got != model.RollbackStatusRunning {
		t.Fatalf("被覆盖但无回滚态的目标应被补初态并下发命令（running）: %s", got)
	}
	var cmdCount int64
	if err := h.env.db.Model(&model.AgentCommand{}).
		Where("server_id = ? AND type = ?", "t-1", model.CommandTypeDeliveryRollback).
		Count(&cmdCount).Error; err != nil {
		t.Fatalf("统计回滚命令失败: %v", err)
	}
	if cmdCount != 1 {
		t.Fatalf("补初态目标应恰好下发 1 条回滚命令，实际 %d", cmdCount)
	}
}

// TestOrchestratorRollbackFinishesWhenNothingToRollBack 推进器显式归类非回滚目标（FR-262r）：
// 单内无任何回滚目标（全部目标从未覆盖磁盘）时自动收口，不再永久停留 rolling_back。
func TestOrchestratorRollbackFinishesWhenNothingToRollBack(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	if _, err := h.orch.applyRollback(order.ID, "回退变更", "ops", "ip"); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	// 模拟全部目标都未进入回滚态且未覆盖磁盘：单仍被置为 rolling_back。
	if err := h.env.db.Model(&model.ChangeTarget{}).Where("order_id = ?", order.ID).
		Updates(map[string]any{"rollback_status": nil, "pushed_at": nil}).Error; err != nil {
		t.Fatalf("清空目标回滚态失败: %v", err)
	}
	h.tick()
	got := h.reload(order.ID)
	if got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("无回滚目标应自动收口为 rolled_back，实际 %s", got.Status)
	}
	if got.FinishedAt == nil {
		t.Fatal("自动收口应记 finished_at")
	}
}

// —— FR-270 目标级（子集）回滚 / FR-271 交付版本与回滚记录 ——

// countConfigVersions 统计某配置文件的版本行数（判「配置版本是否被回退」用）。
func countConfigVersions(t *testing.T, db *gorm.DB, fileID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.ConfigLayerVersion{}).Where("config_file_id = ?", fileID).Count(&n).Error; err != nil {
		t.Fatalf("统计配置版本失败: %v", err)
	}
	return n
}

// attachConfigItem 给已有单挂一条 config_change 项（回滚锚点 = from 版本），用于验证回滚有无触及配置版本。
func attachConfigItem(t *testing.T, db *gorm.DB, orderID, fileID, fromVersionID, scopeID uint, scopeKind string) {
	t.Helper()
	item := model.ChangeOrderItem{
		OrderID: orderID, Kind: model.ChangeItemKindConfigChange,
		ConfigScopeKind: &scopeKind, ConfigScopeID: &scopeID, ConfigFromVersionID: &fromVersionID,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("挂配置变更项失败: %v", err)
	}
	_ = fileID
}

// runRollbackTargets 以目标级回滚语义在事务内执行（生产路径由审批 worker 调用同一函数）。
func (h *orchestratorHarness) runRollbackTargets(t *testing.T, orderID uint, serverIDs []string, reason string) error {
	t.Helper()
	order, err := h.env.orders.requireOrder(orderID)
	if err != nil {
		t.Fatalf("读取变更单失败: %v", err)
	}
	return h.env.db.Transaction(func(tx *gorm.DB) error {
		return h.orch.applyRollbackTargetsInTx(tx, order, serverIDs, reason, "ops", "ip")
	})
}

// rollbackRecords 读某单的回滚动作记录（倒序）。
func (h *orchestratorHarness) rollbackRecords(t *testing.T, orderID uint) []model.ChangeRollbackRecord {
	t.Helper()
	records, err := repository.NewChangeOrderRepository(h.env.db).ListRollbackRecords(orderID)
	if err != nil {
		t.Fatalf("读回滚动作记录失败: %v", err)
	}
	return records
}

// seedConfigFileWithHead 建配置文件并从 from 版本再推一版（head ≠ from），使「回退到 from」必然生成新版本行——
// 这样「版本行数是否增加」才能判别配置回退**是否真的发生**（链上只有一版时回退会撞幂等而看不出差别）。
func seedConfigFileWithHead(t *testing.T, db *gorm.DB, nsID uint, scopeKind string, scopeRefID uint) (uint, uint) {
	t.Helper()
	fileID, fromVersionID := seedGrayConfigFile(t, db, nsID, scopeKind, scopeRefID, "a: 1")
	// 回退原语按 content_hash 判幂等，故两版必须带**不同**哈希，否则会撞 ErrConfigNoChange 而不生成新版本。
	if err := db.Model(&model.ConfigLayerVersion{}).Where("id = ?", fromVersionID).
		Update("content_hash", "hash-from").Error; err != nil {
		t.Fatalf("置 from 版本哈希失败: %v", err)
	}
	head := model.ConfigLayerVersion{
		ConfigFileID: fileID, ScopeLevel: scopeKind, ScopeRefID: scopeRefID,
		VersionNo: 2, Content: "a: 2", ContentHash: "hash-head",
	}
	mustCreate(t, db, &head)
	return fileID, fromVersionID
}

// TestTargetRollbackOnlySelectedFilesAndKeepsOrderStatus 目标级子集回滚（FR-270）：
// 只回滚选中目标的文件；未选中目标一个字节不动；配置版本不回退；单主状态保持 completed。
func TestTargetRollbackOnlySelectedFilesAndKeepsOrderStatus(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	configFileID, fromVersion := seedConfigFileWithHead(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID)
	attachConfigItem(t, h.env.db, order.ID, configFileID, fromVersion, h.f.zone1ID, model.ConfigScopeZone)
	versionsBefore := countConfigVersions(t, h.env.db, configFileID)

	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "只回滚出问题的一台"); err != nil {
		t.Fatalf("目标级回滚失败: %v", err)
	}
	// 单主状态不变：子集回滚不是整单动作。
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("子集回滚后单主状态应保持 completed: %s", got.Status)
	}
	if got := h.reload(order.ID).RollbackAt; got != nil {
		t.Fatal("子集回滚不得写整单 rollback_at（那不是整单动作）")
	}
	byServer := h.rollbackTargetsByServer(order.ID)
	if byServer["t-1"].RollbackStatus != model.RollbackStatusPending {
		t.Fatalf("选中目标应进入回滚初态 pending: %s", byServer["t-1"].RollbackStatus)
	}
	if byServer["t-2"].RollbackStatus != "" {
		t.Fatalf("未选中目标不得进入回滚态: %s", byServer["t-2"].RollbackStatus)
	}
	// 配置版本不回退：既有回退原语会生成新版本行，未新增即证明本次没走配置回退。
	if after := countConfigVersions(t, h.env.db, configFileID); after != versionsBefore {
		t.Fatalf("子集回滚不得回退配置版本（版本行 %d → %d）", versionsBefore, after)
	}

	// 推进：只有选中目标收到回滚命令。
	h.tick()
	var cmds []model.AgentCommand
	if err := h.env.db.Where("type = ?", model.CommandTypeDeliveryRollback).Find(&cmds).Error; err != nil {
		t.Fatalf("读回滚命令失败: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("应只对选中目标下发 1 条回滚命令，实际 %d", len(cmds))
	}
	if got := h.rollbackTargetsByServer(order.ID)["t-1"].RollbackStatus; got != model.RollbackStatusRunning {
		t.Fatalf("选中目标回滚命令应下发（running）: %s", got)
	}
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick()
	byServer = h.rollbackTargetsByServer(order.ID)
	if byServer["t-1"].RollbackStatus != model.RollbackStatusRolledBack {
		t.Fatalf("选中目标应回滚成功: %s", byServer["t-1"].RollbackStatus)
	}
	if byServer["t-2"].RollbackStatus != "" {
		t.Fatalf("未选中目标仍不应有回滚态: %s", byServer["t-2"].RollbackStatus)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("回滚完成也不得改变单主状态: %s", got.Status)
	}
}

// TestTargetRollbackRecordsActionAndConfigFlag 回滚动作记录（FR-270 / FR-271）：
// 每次动作一行，含操作人 / 原因 / 台数 / 是否回退配置 / 逐台结果，且与审计同事务互指。
func TestTargetRollbackRecordsActionAndConfigFlag(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "单台回滚"); err != nil {
		t.Fatalf("目标级回滚失败: %v", err)
	}
	records := h.rollbackRecords(t, order.ID)
	if len(records) != 1 {
		t.Fatalf("应落 1 条回滚动作记录，实际 %d", len(records))
	}
	if records[0].Kind != model.RollbackKindTargets || records[0].ConfigRolledBack ||
		records[0].TargetCount != 1 || records[0].Operator != "ops" || records[0].Reason != "单台回滚" {
		t.Fatalf("子集动作记录字段不符: %+v", records[0])
	}
	rows, err := repository.NewChangeOrderRepository(h.env.db).ListRollbackRecordTargets([]uint{records[0].ID})
	if err != nil || len(rows) != 1 || rows[0].ServerID != "t-1" {
		t.Fatalf("动作应只含选中目标一行: %v / %+v", err, rows)
	}
	// 逐台结果随终态更新：推进到 rolled_back 后记录里该台也应是 rolled_back。
	h.tick()
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick()
	rows, _ = repository.NewChangeOrderRepository(h.env.db).ListRollbackRecordTargets([]uint{records[0].ID})
	if len(rows) != 1 || rows[0].Result != model.RollbackStatusRolledBack {
		t.Fatalf("动作记录的逐台结果应随终态更新: %+v", rows)
	}
	// 审计一路可追溯：同一次动作写一条落 kind / serverIds / recordId 的审计。
	var audit model.AuditLog
	if err := h.env.db.Where("action = ?", model.ActionDeliveryOrderRollback).
		Order("id DESC").First(&audit).Error; err != nil {
		t.Fatalf("读回滚审计失败: %v", err)
	}
	for _, want := range []string{`"kind":"targets"`, `"serverIds":["t-1"]`,
		fmt.Sprintf(`"recordId":%d`, records[0].ID)} {
		if !strings.Contains(audit.Detail, want) {
			t.Fatalf("审计 detail 应含 %s，实际 %s", want, audit.Detail)
		}
	}
}

// TestTargetRollbackFullSelectionFallsBackToOrderRollback 全选等价整单回滚（FR-270）：
// 选中集合覆盖全部可回滚目标时回落整单路径——配置版本回退、单状态迁移到 rolling_back。
func TestTargetRollbackFullSelectionFallsBackToOrderRollback(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	configFileID, fromVersion := seedConfigFileWithHead(t, h.env.db, h.f.nsID, model.ConfigScopeZone, h.f.zone1ID)
	attachConfigItem(t, h.env.db, order.ID, configFileID, fromVersion, h.f.zone1ID, model.ConfigScopeZone)
	versionsBefore := countConfigVersions(t, h.env.db, configFileID)

	if err := h.runRollbackTargets(t, order.ID, []string{"t-1", "t-2"}, "全选回滚"); err != nil {
		t.Fatalf("全选回滚失败: %v", err)
	}
	got := h.reload(order.ID)
	if got.Status != model.ChangeOrderStatusRollingBack {
		t.Fatalf("全选应回落整单回滚（rolling_back）: %s", got.Status)
	}
	if got.RollbackAt == nil {
		t.Fatal("整单回滚应记 rollback_at")
	}
	if after := countConfigVersions(t, h.env.db, configFileID); after <= versionsBefore {
		t.Fatalf("全选等价整单回滚应回退配置版本（版本行 %d → %d）", versionsBefore, after)
	}
	records := h.rollbackRecords(t, order.ID)
	if len(records) != 1 || records[0].Kind != model.RollbackKindOrder || !records[0].ConfigRolledBack ||
		records[0].TargetCount != 2 {
		t.Fatalf("全选应落一条整单动作记录且标记已回退配置: %+v", records)
	}
}

// TestTargetRollbackRejectsInvalidTargetScope 选中集合必须完整落在本单可回滚目标内，否则整单拒绝（不部分执行）。
func TestTargetRollbackRejectsInvalidTargetScope(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	for _, serverIDs := range [][]string{{}, {"  "}, {"t-9"}, {"t-1", "t-9"}} {
		if err := h.runRollbackTargets(t, order.ID, serverIDs, "越界回滚"); err == nil {
			t.Fatalf("越界目标集应被拒绝: %+v", serverIDs)
		}
	}
	byServer := h.rollbackTargetsByServer(order.ID)
	if byServer["t-1"].RollbackStatus != "" || byServer["t-2"].RollbackStatus != "" {
		t.Fatal("拒绝的请求不得部分落库")
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("拒绝的请求不得改变单状态: %s", got.Status)
	}
}

// TestDeliveredVersionFallsBackAfterRollback 当前交付版本读模型（FR-271）：
// 交付后指向该单；某台被回滚后该台回退显示（无更早交付即无记录），未回滚的台不受影响。
func TestDeliveredVersionFallsBackAfterRollback(t *testing.T) {
	h := newOrchestratorHarness(t)
	repo := repository.NewChangeOrderRepository(h.env.db)
	order := h.completedPushOnlyOrder(t)

	versions, err := repo.FindCurrentDeliveredVersions([]string{"t-1", "t-2"})
	if err != nil || len(versions) != 2 {
		t.Fatalf("交付后两台都应查到当前交付版本: %v / %+v", err, versions)
	}
	for _, version := range versions {
		if version.OrderID != order.ID || version.OrderTitle != order.Title || version.ActivatedAt.IsZero() {
			t.Fatalf("当前交付版本应指向本单: %+v", version)
		}
	}

	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "单台回滚"); err != nil {
		t.Fatalf("目标级回滚失败: %v", err)
	}
	h.tick()
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick()

	versions, err = repo.FindCurrentDeliveredVersions([]string{"t-1", "t-2"})
	if err != nil || len(versions) != 1 || versions[0].ServerID != "t-2" {
		t.Fatalf("被回滚的台应回退显示（无更早交付即无记录），未回滚台不受影响: %v / %+v", err, versions)
	}
}

// TestTargetRollbackRetryAfterFailure 子集回滚失败后可再发起一次子集回滚（FR-270 与 §4.7.2 重试口径一致）：
// 第二次动作是独立的一条记录，逐台结果各自留痕。
func TestTargetRollbackRetryAfterFailure(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	if err := h.env.db.Model(&model.ChangeTarget{}).
		Where("order_id = ? AND server_id = ?", order.ID, "t-1").
		Update("backup_present", false).Error; err != nil {
		t.Fatalf("置备份缺失失败: %v", err)
	}
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "首轮"); err != nil {
		t.Fatalf("首轮子集回滚失败: %v", err)
	}
	byServer := h.rollbackTargetsByServer(order.ID)
	if byServer["t-1"].RollbackStatus != model.RollbackStatusFailed {
		t.Fatalf("备份缺失目标预检应直接 failed: %s", byServer["t-1"].RollbackStatus)
	}
	// 补做备份后再重试（备份仍缺失时重推只会再失败一次，那是正确行为而非缺陷）。
	if err := h.env.db.Model(&model.ChangeTarget{}).
		Where("order_id = ? AND server_id = ?", order.ID, "t-1").
		Update("backup_present", true).Error; err != nil {
		t.Fatalf("恢复备份标记失败: %v", err)
	}
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "重试"); err != nil {
		t.Fatalf("重试子集回滚失败: %v", err)
	}
	if got := h.rollbackTargetsByServer(order.ID)["t-1"].RollbackStatus; got != model.RollbackStatusPending {
		t.Fatalf("重试应把该台重置 pending: %s", got)
	}
	records := h.rollbackRecords(t, order.ID)
	if len(records) != 2 {
		t.Fatalf("两次动作应各留一条记录，实际 %d", len(records))
	}
	if records[0].Reason != "重试" || records[1].Reason != "首轮" {
		t.Fatalf("记录应倒序且各自留原因: %+v", records)
	}
	rows, _ := repository.NewChangeOrderRepository(h.env.db).ListRollbackRecordTargets([]uint{records[1].ID})
	if len(rows) != 1 || rows[0].Result != model.RollbackStatusFailed {
		t.Fatalf("首轮记录应保留该台当时的失败结果: %+v", rows)
	}
}

// —— 返工补强：逐台结果归属、在途拒绝、空重试拒绝 ——

// TestTargetRollbackRecordsKeepPerActionOwnership 逐台结果必须写回**它所属那次动作**的记录（FR-271）。
// 用「本单最新一条动作」定位会在交错动作下错配：后发起的动作（它不含先前动作仍在途的那台）会抢走归属，
// 使先前动作记录里的该台永远停在 pending —— 结果凭空丢失。
func TestTargetRollbackRecordsKeepPerActionOwnership(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	// 动作 A：只回滚 t-2（单内 2 台里的 1 台 → 子集路径，单主状态不变）
	if err := h.runRollbackTargets(t, order.ID, []string{"t-2"}, "动作A"); err != nil {
		t.Fatalf("动作A失败: %v", err)
	}
	h.tick() // 动作 A 的 t-2 下发 → running（仍在途）
	// 动作 B：只回滚 t-1。它在记录表里是最新一条，且不含 t-2。
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "动作B"); err != nil {
		t.Fatalf("动作B失败: %v", err)
	}
	h.tick()
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick()

	records := h.rollbackRecords(t, order.ID)
	if len(records) != 2 {
		t.Fatalf("应落两条动作记录，实际 %d", len(records))
	}
	byReason := map[string]model.ChangeRollbackRecord{}
	for _, record := range records {
		byReason[record.Reason] = record
	}
	repo := repository.NewChangeOrderRepository(h.env.db)
	rowsA, _ := repo.ListRollbackRecordTargets([]uint{byReason["动作A"].ID})
	if len(rowsA) != 1 || rowsA[0].ServerID != "t-2" || rowsA[0].Result != model.RollbackStatusRolledBack {
		t.Fatalf("动作A 的 t-2 结果应回写到自己那条记录，实际 %+v", rowsA)
	}
	rowsB, _ := repo.ListRollbackRecordTargets([]uint{byReason["动作B"].ID})
	if len(rowsB) != 1 || rowsB[0].ServerID != "t-1" || rowsB[0].Result != model.RollbackStatusRolledBack {
		t.Fatalf("动作B 的 t-1 结果应回写到自己那条记录，实际 %+v", rowsB)
	}
}

// TestTargetRollbackRejectsTargetsStillInFlight 在途目标不得被再次置初态（防同一台被两次动作叠加下发）。
func TestTargetRollbackRejectsTargetsStillInFlight(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "首轮"); err != nil {
		t.Fatalf("首轮失败: %v", err)
	}
	if got := h.rollbackTargetsByServer(order.ID)["t-1"].RollbackStatus; got != model.RollbackStatusPending {
		t.Fatalf("前置应 pending: %s", got)
	}
	// 在途（pending）时再次发起：必须拒绝，且不落第二条动作记录
	err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "在途叠加")
	if err == nil {
		t.Fatal("在途目标再次发起应被拒绝")
	}
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != "rollback_in_progress" {
		t.Fatalf("应为 rollback_in_progress: %v", err)
	}
	if records := h.rollbackRecords(t, order.ID); len(records) != 1 {
		t.Fatalf("被拒的动作不得落记录，实际 %d 条", len(records))
	}
	// 推进到 running 后同样拒绝
	h.tick()
	if got := h.rollbackTargetsByServer(order.ID)["t-1"].RollbackStatus; got != model.RollbackStatusRunning {
		t.Fatalf("前置应 running: %s", got)
	}
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "运行中叠加"); err == nil {
		t.Fatal("回滚中的目标再次发起应被拒绝")
	}
}

// TestRollbackRetryRejectsWhenNoFailedTarget 无失败目标时「回滚重试」明确拒绝，不落 targetCount=0 的空动作记录。
func TestRollbackRetryRejectsWhenNoFailedTarget(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	if _, err := h.orch.applyRollback(order.ID, "整单回退", "ops", "ip"); err != nil {
		t.Fatalf("首次回滚失败: %v", err)
	}
	h.tick() // 两台都下发 → running（在途、尚无失败）
	for serverID, target := range h.rollbackTargetsByServer(order.ID) {
		if target.RollbackStatus != model.RollbackStatusRunning {
			t.Fatalf("前置 %s 应 running: %s", serverID, target.RollbackStatus)
		}
	}
	before := len(h.rollbackRecords(t, order.ID))
	err := func() error {
		_, e := h.orch.applyRollback(order.ID, "无失败目标的重试", "ops", "ip")
		return e
	}()
	if err == nil {
		t.Fatal("无失败目标的重试应被拒绝")
	}
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != "no_failed_rollback_target" {
		t.Fatalf("应为 no_failed_rollback_target: %v", err)
	}
	if after := len(h.rollbackRecords(t, order.ID)); after != before {
		t.Fatalf("被拒的重试不得落动作记录：前 %d 条、后 %d 条", before, after)
	}
	if got := countAudit(t, h.env.db, model.ActionDeliveryOrderRollback); got != 1 {
		t.Fatalf("被拒的重试不得写审计（应仍为首次那 1 条），实际 %d", got)
	}
}

// TestTargetsViewReportsRollbackEligibleCount 目标分页必须给出**可回滚目标数**（曾推送）这一独立口径（FR-270）：
// 前端「全选等价整单回滚」的判定基数不能用 total（含从未推送的台），否则界面明示会与后端语义相反。
func TestTargetsViewReportsRollbackEligibleCount(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	// t-2 模拟「从未推送」：仍是本单目标（total 计入）但不可回滚（eligible 不计入）
	if err := h.env.db.Model(&model.ChangeTarget{}).
		Where("order_id = ? AND server_id = ?", order.ID, "t-2").
		Update("pushed_at", nil).Error; err != nil {
		t.Fatalf("置未推送失败: %v", err)
	}
	view, err := h.env.orders.Targets(order.ID, repository.ChangeTargetQuery{})
	if err != nil {
		t.Fatalf("读目标分页失败: %v", err)
	}
	if view.Total != 2 {
		t.Fatalf("total 应含未推送目标（2），实际 %d", view.Total)
	}
	if view.RollbackEligibleCount != 1 {
		t.Fatalf("rollbackEligibleCount 应只计曾推送目标（1），实际 %d", view.RollbackEligibleCount)
	}
	// 口径与服务端执行期判定同源：全覆盖 = 选中 1 台即等价整单
	picked := []string{"t-1"}
	if int64(len(picked)) != view.RollbackEligibleCount {
		t.Fatalf("选中数 %d 应等于可回滚数 %d", len(picked), view.RollbackEligibleCount)
	}
}

// TestTargetRollbackConcurrentIntersectingSubsets 并发批准相交目标集不得让同一台留下两条未终态记录（FR-270）。
// 申请期守卫是 check-then-act，两次并发批准会在检查与置态之间互相穿透；拦截必须落在 UPDATE 的 WHERE 上（CAS）。
// 这里直接驱动 CAS 层：A 已把 t-1 置为在途后，B 对同一台的置态必须**不命中且不改行**，B 的整体请求被拒且零留痕。
func TestTargetRollbackConcurrentIntersectingSubsets(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	repo := repository.NewChangeOrderRepository(h.env.db)

	// 动作 A：回滚 {t-1} → t-1 进入在途（pending）
	if err := h.runRollbackTargets(t, order.ID, []string{"t-1"}, "动作A"); err != nil {
		t.Fatalf("动作A失败: %v", err)
	}
	targets := h.rollbackTargetsByServer(order.ID)
	if targets["t-1"].RollbackStatus != model.RollbackStatusPending {
		t.Fatalf("A 置态后 t-1 应 pending: %s", targets["t-1"].RollbackStatus)
	}
	recordIDBefore := targets["t-1"].ID

	// CAS 层直测（模拟 B 已越过申请期守卫、正要置态的那一刻）：在途目标不得被重新置态
	ok, err := repo.InitTargetRollbackCAS(recordIDBefore, true, rollbackBackupMissingReason)
	if err != nil {
		t.Fatalf("CAS 调用失败: %v", err)
	}
	if ok {
		t.Fatal("在途目标的 CAS 置态不得命中")
	}
	afterCAS := h.rollbackTargetsByServer(order.ID)["t-1"]
	if afterCAS.RollbackStatus != model.RollbackStatusPending || afterCAS.RollbackError != "" {
		t.Fatalf("被拒的 CAS 不得改写目标行: %+v", afterCAS)
	}

	// 动作 B：与 A 相交的 {t-1, t-2} → 前置守卫命中，整单拒绝
	err = h.runRollbackTargets(t, order.ID, []string{"t-1", "t-2"}, "动作B")
	if err == nil {
		t.Fatal("相交子集的并发动作应被拒绝")
	}
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != "rollback_in_progress" {
		t.Fatalf("应为 rollback_in_progress: %v", err)
	}
	// 零留痕：被拒事务整体回滚，A 的记录与 t-1 的在途态都不受影响，t-2 也不得被置态
	if records := h.rollbackRecords(t, order.ID); len(records) != 1 || records[0].Reason != "动作A" {
		t.Fatalf("被拒动作不得留痕，实际 %+v", records)
	}
	afterB := h.rollbackTargetsByServer(order.ID)
	if afterB["t-1"].RollbackStatus != model.RollbackStatusPending {
		t.Fatalf("t-1 应仍是 A 的在途态: %s", afterB["t-1"].RollbackStatus)
	}
	if afterB["t-2"].RollbackStatus != "" {
		t.Fatalf("被拒动作不得置态未涉及的台: %s", afterB["t-2"].RollbackStatus)
	}
	// 反证：同一时刻「不在途」的 t-2（从未进入回滚）CAS 必须命中——否则上面那次 false 可能只是恒假。
	// 放在末尾，避免它自身置态干扰前面的「零留痕」断言。
	if ok, err := repo.InitTargetRollbackCAS(afterB["t-2"].ID, true, rollbackBackupMissingReason); err != nil || !ok {
		t.Fatalf("未进入回滚的目标 CAS 应命中: %v / %v", ok, err)
	}
	if got := h.rollbackTargetsByServer(order.ID)["t-2"].RollbackStatus; got != model.RollbackStatusPending {
		t.Fatalf("命中后 t-2 应 pending: %s", got)
	}
}

// TestInitRollbackTargetsCASBlockedRejectsWholeAction 置初态 CAS 的整单拒绝路径（FR-270）：
// 同一事务内「一台空态 + 一台在途」时，空态台先被置态、在途台未命中即返回 blocked，
// 调用方据此整单拒绝 → 事务回滚 → **先被置态的台必须原样复原**。
// 否则会出现「被拒的动作留下半截置态」：目标被改成 pending 却没进任何动作记录，成为孤儿在途。
func TestInitRollbackTargetsCASBlockedRejectsWholeAction(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	targets, err := repository.NewChangeOrderRepository(h.env.db).ListTargetsByOrder(order.ID)
	if err != nil {
		t.Fatalf("读目标失败: %v", err)
	}
	byServer := map[string]*model.ChangeTarget{}
	for i := range targets {
		byServer[targets[i].ServerID] = &targets[i]
	}

	// 模拟「整单拒绝」：执行器在本事务内逐台置态，一旦返回 blocked 即返回错误让事务回滚。
	blockedSeen := ""
	err = h.env.db.Transaction(func(tx *gorm.DB) error {
		repoTx := repository.NewChangeOrderRepository(tx)
		// 事务内置 t-1 为在途（模拟并发批准的抢占）
		if e := tx.Model(&model.ChangeTarget{}).Where("id = ?", byServer["t-1"].ID).
			Update("rollback_status", model.RollbackStatusPending).Error; e != nil {
			return e
		}
		// 顺序刻意让 t-2（空态）先命中、t-1（在途）后未命中
		blocked, e := initRollbackTargetsCAS(repoTx,
			[]*model.ChangeTarget{byServer["t-2"], byServer["t-1"]}, rollbackBackupMissingReason)
		if e != nil {
			return e
		}
		blockedSeen = blocked
		return errors.New("模拟整单拒绝")
	})
	if err == nil || err.Error() != "模拟整单拒绝" {
		t.Fatalf("事务应因整单拒绝而回滚: %v", err)
	}
	if blockedSeen != "t-1" {
		t.Fatalf("应指出未命中的是在途的 t-1，实际 %q", blockedSeen)
	}
	// 关键断言：先命中的 t-2 随事务回滚复原为「未进入回滚」，不得留半截置态
	after := h.rollbackTargetsByServer(order.ID)
	if after["t-2"].RollbackStatus != "" || after["t-2"].RollbackError != "" {
		t.Fatalf("被拒动作已命中的台必须随事务回滚复原: %+v", after["t-2"])
	}
	if after["t-1"].RollbackStatus != "" {
		t.Fatalf("在途台也应随事务回滚复原: %+v", after["t-1"])
	}
	// 零留痕：没有动作记录，目标也没有回滚态
	if records := h.rollbackRecords(t, order.ID); len(records) != 0 {
		t.Fatalf("被拒动作不得落动作记录，实际 %+v", records)
	}
}

// TestFinishRollbackWritesNoActionRecord 人工「结束回滚」是收单动作而非回滚动作（FR-271，spec §3.6）：
// 它不改变任何目标的回滚结果，故不落动作记录（只写审计）；否则「回滚过几次」的读数会被收单动作污染。
func TestFinishRollbackWritesNoActionRecord(t *testing.T) {
	h := newOrchestratorHarness(t)
	order := h.completedPushOnlyOrder(t)
	// 让 t-1 备份缺失：回滚后停在 rolling_back 等人工收单
	if err := h.env.db.Model(&model.ChangeTarget{}).
		Where("order_id = ? AND server_id = ?", order.ID, "t-1").
		Update("backup_present", false).Error; err != nil {
		t.Fatalf("置备份缺失失败: %v", err)
	}
	if _, err := h.orch.applyRollback(order.ID, "回退变更", "ops", "ip"); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	h.tick()
	h.completeAllRollbacks(t, order.ID, model.CommandStatusDone)
	h.tick()
	before := len(h.rollbackRecords(t, order.ID))
	if before != 1 {
		t.Fatalf("前置应有 1 条回滚动作记录，实际 %d", before)
	}
	finishBefore := countAudit(t, h.env.db, model.ActionDeliveryOrderRollbackFinish)
	if _, err := h.orch.applyFinishRollback(order.ID, "ops", "ip"); err != nil {
		t.Fatalf("结束回滚失败: %v", err)
	}
	if got := h.reload(order.ID); got.Status != model.ChangeOrderStatusRolledBack {
		t.Fatalf("结束回滚后应收口为 rolled_back: %s", got.Status)
	}
	if after := len(h.rollbackRecords(t, order.ID)); after != before {
		t.Fatalf("结束回滚不得新增动作记录：前 %d 条、后 %d 条", before, after)
	}
	if got := countAudit(t, h.env.db, model.ActionDeliveryOrderRollbackFinish); got != finishBefore+1 {
		t.Fatalf("结束回滚应写审计（收单动作的唯一留痕），前 %d 条、后 %d 条", finishBefore, got)
	}
}

// TestConfigRollbackIdempotent 配置回退幂等判定（ADR-0071 决策6）：ErrConfigNoChange 与撤销层「无可撤销」INVALID_PARAM 当成功吞。
func TestConfigRollbackIdempotent(t *testing.T) {
	if !isConfigRollbackIdempotent(apperr.ErrConfigNoChange) {
		t.Fatal("ErrConfigNoChange 应判幂等")
	}
	if !isConfigRollbackIdempotent(apperr.New(400, "INVALID_PARAM", "该层无可撤销的贡献")) {
		t.Fatal("撤销层无可撤销 INVALID_PARAM 应判幂等")
	}
	if isConfigRollbackIdempotent(apperr.ErrChangeConfigVersionInvalid) {
		t.Fatal("其他错误不应判幂等")
	}
}
