package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
)

// fakeDeliverySettings 是审批职责分离开关的假实现（可随测试切换）。
type fakeDeliverySettings struct{ separation bool }

func (f *fakeDeliverySettings) GetBool(string) bool { return f.separation }

// deliveryTestEnv 打包交付域单测所需的库、双服务与内存真源。
type deliveryTestEnv struct {
	db       *gorm.DB
	orders   *DeliveryOrderService
	diff     *DeliveryDiffService
	preview  *AssetPreviewService
	health   *healthview.Store
	settings *fakeDeliverySettings
}

// newDeliveryTestEnv 打开独立命名内存 sqlite（单连接串行化）并迁移交付域涉及的全部表，装配双服务。
func newDeliveryTestEnv(t *testing.T) *deliveryTestEnv {
	t.Helper()
	name := "file:delivery_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(name), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Namespace{}, &model.BCCluster{}, &model.Region{}, &model.Zone{},
		&model.Server{}, &model.AgentIdentity{}, &model.ChangeOrder{}, &model.ChangeOrderItem{},
		&model.ChangeBatch{}, &model.ChangeTarget{}, &model.FileAsset{}, &model.FileAssetScan{},
		&model.AgentCommand{}, &model.Setting{}, &model.AuditLog{},
		&model.ConfigFile{}, &model.ConfigLayerVersion{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	repo := repository.NewChangeOrderRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	health := healthview.NewStore()
	settings := &fakeDeliverySettings{separation: true}
	preview := NewAssetPreviewService(db, repository.NewAgentCommandRepository(db),
		repository.NewFileAssetRepository(db), repository.NewSettingRepository(db),
		auditRepo, longpoll.NewHub(), nil, fakeAssetInstances{online: true})
	return &deliveryTestEnv{
		db: db, health: health, settings: settings, preview: preview,
		orders: NewDeliveryOrderService(db, repo, repository.NewConfigLayerVersionRepository(db),
			auditRepo, settings, health),
		diff: NewDeliveryDiffService(db, repo, repository.NewFileAssetRepository(db),
			auditRepo, preview, health),
	}
}

// deliveryFixture 是常用集群夹具：prod namespace + 一区两小区 + 模板源 / 两台目标 backend（均绑定 active 身份）。
type deliveryFixture struct {
	env      *deliveryTestEnv
	nsID     uint
	regionID uint
	zone1ID  uint
	zone2ID  uint
	srcRow   uint // 模板源 server 行 id（业务 serverId=src-1）
	t1Row    uint // 目标 t-1 行 id（zone1）
	t2Row    uint // 目标 t-2 行 id（zone2）
}

// seedDeliveryFixture 建集群夹具并把源与目标标为在线（健康视图）。
func seedDeliveryFixture(t *testing.T, env *deliveryTestEnv) *deliveryFixture {
	t.Helper()
	f := &deliveryFixture{env: env}
	f.nsID = seedDeliveryNamespace(t, env.db, "prod")
	cluster := model.BCCluster{NamespaceID: f.nsID, Name: "bc-1"}
	mustCreate(t, env.db, &cluster)
	region := model.Region{BCClusterID: cluster.ID, Name: "region-1"}
	mustCreate(t, env.db, &region)
	f.regionID = region.ID
	zone1 := model.Zone{RegionID: region.ID, Name: "zone-1"}
	zone2 := model.Zone{RegionID: region.ID, Name: "zone-2"}
	mustCreate(t, env.db, &zone1)
	mustCreate(t, env.db, &zone2)
	f.zone1ID, f.zone2ID = zone1.ID, zone2.ID
	f.srcRow = seedDeliveryServer(t, env.db, f.nsID, "src-1", model.ServerKindBackend, &zone1.ID, model.AgentIdentityStatusActive)
	f.t1Row = seedDeliveryServer(t, env.db, f.nsID, "t-1", model.ServerKindBackend, &zone1.ID, model.AgentIdentityStatusActive)
	f.t2Row = seedDeliveryServer(t, env.db, f.nsID, "t-2", model.ServerKindBackend, &zone2.ID, model.AgentIdentityStatusActive)
	markDeliveryOnline(env.health, f.nsID, "src-1", "t-1", "t-2")
	return f
}

// seedDeliveryNamespace 建（或复用同 code）namespace。
func seedDeliveryNamespace(t *testing.T, db *gorm.DB, code string) uint {
	t.Helper()
	var ns model.Namespace
	if err := db.Where("code = ?", code).First(&ns).Error; err != nil {
		ns = model.Namespace{Code: code, Name: code}
		mustCreate(t, db, &ns)
	}
	return ns.ID
}

// seedDeliveryServer 建 server 行；identityStatus 非空则同时建对应身份行。
func seedDeliveryServer(t *testing.T, db *gorm.DB, nsID uint, serverID, kind string, zoneID *uint, identityStatus string) uint {
	t.Helper()
	srv := model.Server{NamespaceID: nsID, ServerID: serverID, Kind: kind, ZoneID: zoneID}
	mustCreate(t, db, &srv)
	if identityStatus != "" {
		mustCreate(t, db, &model.AgentIdentity{
			IdentityID: "idn-" + serverID, NamespaceID: nsID, ServerID: serverID,
			Kind: kind, Status: identityStatus, StatusChangedAt: time.Now().UTC(),
		})
	}
	return srv.ID
}

// markDeliveryOnline 用健康视图内存真源把一组服标记为在线（healthy 无 lost）。
func markDeliveryOnline(store *healthview.Store, nsID uint, serverIDs ...string) {
	views := make([]healthview.View, 0, len(serverIDs))
	for _, id := range serverIDs {
		views = append(views, healthview.View{NamespaceID: nsID, ServerID: id, Kind: model.ServerKindBackend,
			Score: 90, Level: healthview.LevelHealthy})
	}
	store.ReplaceAll(views)
}

// mustCreate 落库并断言成功。
func mustCreate(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatalf("落库 %T 失败: %v", value, err)
	}
}

// strPtr / intPtr 简化入参指针构造。
func strPtr(s string) *string { return &s }
func intPtr(v int) *int       { return &v }

// createDraftOrder 经服务创建最小 draft 单（模板源 src-1 + scanDir plugins/ + 点名目标 t-1/t-2）。
func createDraftOrder(t *testing.T, f *deliveryFixture) *ChangeOrderDetailView {
	t.Helper()
	detail, err := f.env.orders.Create(f.nsID, ChangeOrderInput{
		Title:          strPtr("发布大厅插件"),
		SourceServerID: strPtr("src-1"),
		ScanDir:        strPtr("plugins/"),
		Selector:       &ChangeSelector{Servers: []string{"t-1", "t-2"}},
	}, "ops-chen", "10.0.0.1")
	if err != nil {
		t.Fatalf("创建 draft 单失败: %v", err)
	}
	return detail
}

// seedConfigItem 直接插入一条配置变更项（绕过校验，供状态机 / 提交前置测试铺数据）。
func seedConfigItem(t *testing.T, db *gorm.DB, orderID uint) {
	t.Helper()
	kind := model.ConfigScopeZone
	scopeID := uint(1)
	toID := uint(1)
	mustCreate(t, db, &model.ChangeOrderItem{
		OrderID: orderID, Kind: model.ChangeItemKindConfigChange,
		ConfigScopeKind: &kind, ConfigScopeID: &scopeID, ConfigToVersionID: &toID,
	})
}

// setOrderStatus 直接改单状态（铺各状态起点）。
func setOrderStatus(t *testing.T, db *gorm.DB, orderID uint, status string) {
	t.Helper()
	if err := db.Model(&model.ChangeOrder{}).Where("id = ?", orderID).
		Update("status", status).Error; err != nil {
		t.Fatalf("置单状态失败: %v", err)
	}
}

// TestChangeOrderCreateDefaults 创建默认值对齐 devmock：percent/[10,30,60]/restart/120/300/20/30 + draft + 审计。
func TestChangeOrderCreateDefaults(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	detail := createDraftOrder(t, f)

	if detail.Status != model.ChangeOrderStatusDraft || detail.BatchMode != model.BatchModePercent {
		t.Fatalf("默认状态 / 批模式不符: %+v", detail.ChangeOrderSummaryView)
	}
	if len(detail.BatchSizes) != 3 || detail.BatchSizes[0] != 10 || detail.ObserveWindowSec != 120 ||
		detail.ActivateTimeoutSec != 300 || detail.FailureRateThresholdPercent != 20 ||
		detail.UnhealthyRateThresholdPercent != 30 {
		t.Fatalf("默认策略不符: %+v", detail.ChangeOrderSummaryView)
	}
	if detail.PayloadState != model.PayloadStatePending || detail.CreatedBy != "ops-chen" {
		t.Fatalf("payloadState / createdBy 不符: %+v", detail.ChangeOrderSummaryView)
	}
	if got := countAudit(t, env.db, model.ActionDeliveryOrderCreate); got != 1 {
		t.Fatalf("应记 1 条 delivery.order.create 审计，实际 %d", got)
	}

	// 必填校验与 namespace 存在性。
	if _, err := env.orders.Create(f.nsID, ChangeOrderInput{}, "ops", ""); err == nil {
		t.Fatal("缺 title 应拒绝")
	}
	if _, err := env.orders.Create(0, ChangeOrderInput{Title: strPtr("x")}, "ops", ""); err == nil {
		t.Fatal("缺 namespaceId 应拒绝")
	}
	if _, err := env.orders.Create(9999, ChangeOrderInput{Title: strPtr("x")}, "ops", ""); err == nil {
		t.Fatal("namespace 不存在应拒绝")
	}
}

// TestChangeOrderDetailContractShape 守护 P3 教训：响应必须 camelCase、可空字段显式 null、数组恒非 null。
func TestChangeOrderDetailContractShape(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	detail, err := env.orders.Create(f.nsID, ChangeOrderInput{Title: strPtr("契约形状")}, "ops", "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	body := string(raw)
	for _, key := range []string{`"namespaceId"`, `"sourceServerId":null`, `"scanDir":""`, `"batchSizes":[10,30,60]`,
		`"activationMethod"`, `"observeWindowSec"`, `"payloadState"`, `"diffSnapshotAt":null`, `"createdBy"`,
		`"selector":{"all":false,"regions":[],"zones":[],"servers":[],"excludes":[]}`,
		`"items":[]`, `"batches":[]`, `"targetCounts":{}`, `"rollbackCounts":{}`} {
		if !strings.Contains(body, key) {
			t.Fatalf("响应缺契约片段 %s，实际 %s", key, body)
		}
	}
	if strings.Contains(body, "ID\"") || strings.Contains(body, `"Title"`) {
		t.Fatalf("响应泄漏 PascalCase 字段: %s", body)
	}
}

// TestChangeOrderStateMachineLegalFlow 合法链：draft →(submit) pending →(approve) approved →(withdraw) draft
// →(submit) pending →(reject) draft；各步字段与审计随迁。
func TestChangeOrderStateMachineLegalFlow(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedConfigItem(t, env.db, order.ID)

	submitted, err := env.orders.Submit(order.ID, "ops-chen", "")
	if err != nil || submitted.Status != model.ChangeOrderStatusPendingApproval || submitted.SubmittedAt == nil {
		t.Fatalf("提交失败: %v / %+v", err, submitted)
	}
	approved, err := env.orders.Approve(order.ID, "看过影响面", "admin", "")
	if err != nil || approved.Status != model.ChangeOrderStatusApproved {
		t.Fatalf("审批失败: %v", err)
	}
	if approved.ApprovedBy == nil || *approved.ApprovedBy != "admin" || approved.ApprovedAt == nil {
		t.Fatalf("审批人 / 时间未落: %+v", approved)
	}
	withdrawn, err := env.orders.Withdraw(order.ID, "ops-chen", "")
	if err != nil || withdrawn.Status != model.ChangeOrderStatusDraft {
		t.Fatalf("撤回失败: %v", err)
	}
	if withdrawn.ApprovedBy != nil || withdrawn.ApprovedAt != nil {
		t.Fatalf("撤回应作废审批记录: %+v", withdrawn)
	}
	if _, err := env.orders.Submit(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("再次提交失败: %v", err)
	}
	rejected, err := env.orders.Reject(order.ID, "批次太大", "admin", "")
	if err != nil || rejected.Status != model.ChangeOrderStatusDraft {
		t.Fatalf("驳回失败: %v", err)
	}
	if rejected.RejectReason == nil || *rejected.RejectReason != "批次太大" {
		t.Fatalf("驳回原因未落: %+v", rejected)
	}
	for action, want := range map[string]int64{
		model.ActionDeliveryOrderSubmit: 2, model.ActionDeliveryOrderApprove: 1,
		model.ActionDeliveryOrderWithdraw: 1, model.ActionDeliveryOrderReject: 1,
	} {
		if got := countAudit(t, env.db, action); got != want {
			t.Fatalf("审计 %s 应 %d 条，实际 %d", action, want, got)
		}
	}
}

// TestChangeOrderStateMachineIllegalTransitions 穷举 9 状态 × 7 动作的非法迁移一律 409 illegal_state。
func TestChangeOrderStateMachineIllegalTransitions(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)

	allStatuses := []string{
		model.ChangeOrderStatusDraft, model.ChangeOrderStatusPendingApproval, model.ChangeOrderStatusApproved,
		model.ChangeOrderStatusRolling, model.ChangeOrderStatusPaused, model.ChangeOrderStatusCompleted,
		model.ChangeOrderStatusCancelled, model.ChangeOrderStatusRollingBack, model.ChangeOrderStatusRolledBack,
	}
	legal := map[string]map[string]bool{
		"submit":    {model.ChangeOrderStatusDraft: true},
		"withdraw":  {model.ChangeOrderStatusPendingApproval: true, model.ChangeOrderStatusApproved: true},
		"approve":   {model.ChangeOrderStatusPendingApproval: true},
		"reject":    {model.ChangeOrderStatusPendingApproval: true},
		"patch":     {model.ChangeOrderStatusDraft: true, model.ChangeOrderStatusApproved: true},
		"delete":    {model.ChangeOrderStatusDraft: true},
		"diff-scan": {model.ChangeOrderStatusDraft: true},
	}
	run := func(action string, id uint) error {
		switch action {
		case "submit":
			_, err := env.orders.Submit(id, "ops-chen", "")
			return err
		case "withdraw":
			_, err := env.orders.Withdraw(id, "ops-chen", "")
			return err
		case "approve":
			_, err := env.orders.Approve(id, "", "admin", "")
			return err
		case "reject":
			_, err := env.orders.Reject(id, "原因", "admin", "")
			return err
		case "patch":
			_, err := env.orders.Update(id, ChangeOrderInput{Title: strPtr("改标题")}, "ops-chen", "")
			return err
		case "delete":
			return env.orders.Delete(id, "ops-chen", "")
		default: // diff-scan
			_, err := env.diff.DiffScan(id, "ops-chen", "")
			return err
		}
	}
	for _, status := range allStatuses {
		for action, allowedFrom := range legal {
			if allowedFrom[status] {
				continue // 合法迁移由 TestChangeOrderStateMachineLegalFlow 等用例覆盖
			}
			order := createDraftOrder(t, f)
			seedConfigItem(t, env.db, order.ID)
			setOrderStatus(t, env.db, order.ID, status)
			err := run(action, order.ID)
			ae := mustAppErr(t, err, "illegal_state", http.StatusConflict)
			if !strings.Contains(ae.Message, status) {
				t.Fatalf("[%s@%s] 错误信息应含当前状态，实际 %q", action, status, ae.Message)
			}
		}
	}
}

// TestChangeOrderSubmitPreconditions 提交前置：无项 / 无目标 / 文件项缺源 / 源不合格逐一拦截。
func TestChangeOrderSubmitPreconditions(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)

	// 无变更项 → no_items。
	empty := createDraftOrder(t, f)
	_ = mustAppErr(t, mustErr(env.orders.Submit(empty.ID, "ops-chen", "")), "no_items", http.StatusBadRequest)

	// 有项但 selector 解析出 0 目标 → no_target。
	noTarget, err := env.orders.Create(f.nsID, ChangeOrderInput{Title: strPtr("无目标")}, "ops-chen", "")
	if err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	seedConfigItem(t, env.db, noTarget.ID)
	_ = mustAppErr(t, mustErr(env.orders.Submit(noTarget.ID, "ops-chen", "")), "no_target", http.StatusBadRequest)

	// 含文件项但无模板源 → missing_source。
	fileNoSource, err := env.orders.Create(f.nsID, ChangeOrderInput{
		Title: strPtr("缺源文件单"), Selector: &ChangeSelector{Servers: []string{"t-1"}},
	}, "ops-chen", "")
	if err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	path, action, sha := "plugins/a.yml", model.ChangeItemActionAdd, "aa"
	size := int64(10)
	mustCreate(t, env.db, &model.ChangeOrderItem{OrderID: fileNoSource.ID, Kind: model.ChangeItemKindFileDiff,
		Path: &path, Action: &action, SHA256: &sha, SizeBytes: &size})
	_ = mustAppErr(t, mustErr(env.orders.Submit(fileNoSource.ID, "ops-chen", "")), "missing_source", http.StatusBadRequest)

	// 源离线（健康视图仅目标在线）→ source_invalid。
	offlineSrc := createDraftOrder(t, f)
	seedConfigItem(t, env.db, offlineSrc.ID)
	markDeliveryOnline(env.health, f.nsID, "t-1", "t-2")
	_ = mustAppErr(t, mustErr(env.orders.Submit(offlineSrc.ID, "ops-chen", "")), "source_invalid", http.StatusBadRequest)
	markDeliveryOnline(env.health, f.nsID, "src-1", "t-1", "t-2")

	// 源身份未确认绑定 → source_invalid。
	unbound, err := env.orders.Create(f.nsID, ChangeOrderInput{
		Title: strPtr("源未绑定"), SourceServerID: strPtr("pending-src"),
		Selector: &ChangeSelector{Servers: []string{"t-1"}},
	}, "ops-chen", "")
	if err == nil {
		seedConfigItem(t, env.db, unbound.ID)
	}
	// pending-src 尚不存在 → 组单期结构校验直接拒绝。
	_ = mustAppErr(t, err, "source_invalid", http.StatusBadRequest)
	seedDeliveryServer(t, env.db, f.nsID, "pending-src", model.ServerKindBackend, &f.zone1ID, model.AgentIdentityStatusPending)
	markDeliveryOnline(env.health, f.nsID, "src-1", "t-1", "t-2", "pending-src")
	unbound2, err := env.orders.Create(f.nsID, ChangeOrderInput{
		Title: strPtr("源未绑定2"), SourceServerID: strPtr("pending-src"),
		Selector: &ChangeSelector{Servers: []string{"t-1"}},
	}, "ops-chen", "")
	if err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	seedConfigItem(t, env.db, unbound2.ID)
	_ = mustAppErr(t, mustErr(env.orders.Submit(unbound2.ID, "ops-chen", "")), "source_invalid", http.StatusBadRequest)
}

// TestChangeOrderApproverSeparation 审批分离：开启时创建人自批 403；关闭后放行（spec §4.7 / §8#6）。
func TestChangeOrderApproverSeparation(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedConfigItem(t, env.db, order.ID)
	if _, err := env.orders.Submit(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	// 默认开启：创建人自批被拒。
	_ = mustAppErr(t, mustErr(env.orders.Approve(order.ID, "", "ops-chen", "")), "approver_separation", http.StatusForbidden)
	// 关闭开关：创建人自批放行。
	env.settings.separation = false
	if _, err := env.orders.Approve(order.ID, "", "ops-chen", ""); err != nil {
		t.Fatalf("关闭分离后创建人自批应放行: %v", err)
	}
}

// TestChangeOrderWithdrawOnlyCreator 撤回仅创建人可执行（spec §4.1「创建人撤回」）。
func TestChangeOrderWithdrawOnlyCreator(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedConfigItem(t, env.db, order.ID)
	if _, err := env.orders.Submit(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	_ = mustAppErr(t, mustErr(env.orders.Withdraw(order.ID, "admin", "")), "not_creator", http.StatusForbidden)
	if _, err := env.orders.Withdraw(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("创建人撤回应成功: %v", err)
	}
}

// TestChangeOrderRejectRequiresReason 驳回必填原因（spec §4.8.1 高风险操作）。
func TestChangeOrderRejectRequiresReason(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	_ = mustAppErr(t, mustErr(env.orders.Reject(order.ID, "  ", "admin", "")), "missing_reason", http.StatusBadRequest)
}

// TestChangeOrderPatchApprovedRevokesApproval approved 后任何编辑自动作废审批回 draft 并入审计（spec §4.1）。
func TestChangeOrderPatchApprovedRevokesApproval(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedConfigItem(t, env.db, order.ID)
	if _, err := env.orders.Submit(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if _, err := env.orders.Approve(order.ID, "", "admin", ""); err != nil {
		t.Fatalf("审批失败: %v", err)
	}

	patched, err := env.orders.Update(order.ID, ChangeOrderInput{Title: strPtr("改后标题")}, "ops-chen", "")
	if err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	if patched.Status != model.ChangeOrderStatusDraft || patched.ApprovedBy != nil || patched.ApprovedAt != nil {
		t.Fatalf("approved 编辑应回 draft 且作废审批: %+v", patched.ChangeOrderSummaryView)
	}
	if !auditDetailContains(t, env.db, model.ActionDeliveryOrderUpdate, `"revokedApproval":true`) {
		t.Fatal("审计 detail 应记录审批作废")
	}
}

// TestChangeOrderPatchValidation 编辑字段校验：非法 batchMode / batchSizes / 阈值 / scanDir 拒绝。
func TestChangeOrderPatchValidation(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)

	cases := []ChangeOrderInput{
		{BatchMode: strPtr("blue_green")},
		{BatchSizes: &[]int{}},
		{BatchSizes: &[]int{0}},
		{BatchSizes: &[]int{120}}, // percent 模式 >100
		{ActivationMethod: strPtr("reboot")},
		{ObserveWindowSec: intPtr(0)},
		{FailureRateThresholdPercent: intPtr(101)},
		{ScanDir: strPtr("../escape")},
		{Title: strPtr("  ")},
	}
	for i, input := range cases {
		if _, err := env.orders.Update(order.ID, input, "ops-chen", ""); err == nil {
			t.Fatalf("用例 %d 应校验失败: %+v", i, input)
		}
	}
	// 合法编辑：count 模式允许 >100 的台数。
	if _, err := env.orders.Update(order.ID, ChangeOrderInput{
		BatchMode: strPtr(model.BatchModeCount), BatchSizes: &[]int{1, 200},
	}, "ops-chen", ""); err != nil {
		t.Fatalf("count 模式台数 >100 应放行: %v", err)
	}
}

// TestChangeOrderDeleteDraftCascades 删除 draft 单：物理删单 + 级联删 items + 审计。
func TestChangeOrderDeleteDraftCascades(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedConfigItem(t, env.db, order.ID)

	if err := env.orders.Delete(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := env.orders.Get(order.ID); err == nil {
		t.Fatal("删除后详情应 404")
	}
	var itemCount int64
	if err := env.db.Model(&model.ChangeOrderItem{}).Where("order_id = ?", order.ID).Count(&itemCount).Error; err != nil || itemCount != 0 {
		t.Fatalf("items 应级联删除，剩 %d err=%v", itemCount, err)
	}
	if got := countAudit(t, env.db, model.ActionDeliveryOrderDelete); got != 1 {
		t.Fatalf("应记 1 条 delete 审计，实际 %d", got)
	}
}

// TestChangeOrderConfigChangesFromAnchor from 锚点三分支（ADR-0071）：
// ① 最近 completed 单交付的 to 版本；② 无交付取 based_on；③ 链首版为 null。并校验非法输入拒绝。
func TestChangeOrderConfigChangesFromAnchor(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)

	// 配置文件 + zone1 链三个版本：v1(基线, basedOn nil) → v2(basedOn v1) → v3(basedOn v2)。
	file := model.ConfigFile{NamespaceID: f.nsID, Name: "plugins/Foo/config.yml", Format: "yaml"}
	mustCreate(t, env.db, &file)
	v1 := model.ConfigLayerVersion{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeZone, ScopeRefID: f.zone1ID, VersionNo: 1, Content: "a: 1"}
	mustCreate(t, env.db, &v1)
	v2 := model.ConfigLayerVersion{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeZone, ScopeRefID: f.zone1ID, VersionNo: 2, Content: "a: 2", BasedOnVersionID: &v1.ID}
	mustCreate(t, env.db, &v2)
	v3 := model.ConfigLayerVersion{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeZone, ScopeRefID: f.zone1ID, VersionNo: 3, Content: "a: 3", BasedOnVersionID: &v2.ID}
	mustCreate(t, env.db, &v3)

	patchTo := func(order *ChangeOrderDetailView, versionID uint) *ChangeOrderDetailView {
		t.Helper()
		detail, err := env.orders.Update(order.ID, ChangeOrderInput{ConfigChanges: &[]ChangeConfigInput{
			{ConfigScopeKind: model.ConfigScopeZone, ConfigScopeID: f.zone1ID, ConfigToVersionID: versionID},
		}}, "ops-chen", "")
		if err != nil {
			t.Fatalf("挂配置版本失败: %v", err)
		}
		return detail
	}
	configItem := func(detail *ChangeOrderDetailView) ChangeOrderItemView {
		t.Helper()
		for _, item := range detail.Items {
			if item.Kind == model.ChangeItemKindConfigChange {
				return item
			}
		}
		t.Fatal("未找到配置变更项")
		return ChangeOrderItemView{}
	}

	order := createDraftOrder(t, f)
	// 分支③：链首版 v1（无交付、无 based_on）→ from = null。
	if item := configItem(patchTo(order, v1.ID)); item.ConfigFromVersionID != nil {
		t.Fatalf("链首版 from 应为 null，实际 %v", *item.ConfigFromVersionID)
	}
	// 分支②：无已交付 → from = based_on（v2 → v1）。
	if item := configItem(patchTo(order, v2.ID)); item.ConfigFromVersionID == nil || *item.ConfigFromVersionID != v1.ID {
		t.Fatalf("无交付时 from 应取 based_on=v1，实际 %v", item.ConfigFromVersionID)
	}
	// 分支①：造一条已 completed 单交付 (file, zone1)→v1，再挂 v3 → from 应取已交付 v1（而非 based_on 的 v2）。
	delivered := createDraftOrder(t, f)
	kind, scopeID := model.ConfigScopeZone, f.zone1ID
	mustCreate(t, env.db, &model.ChangeOrderItem{OrderID: delivered.ID, Kind: model.ChangeItemKindConfigChange,
		ConfigScopeKind: &kind, ConfigScopeID: &scopeID, ConfigToVersionID: &v1.ID})
	now := time.Now().UTC()
	if err := env.db.Model(&model.ChangeOrder{}).Where("id = ?", delivered.ID).
		Updates(map[string]any{"status": model.ChangeOrderStatusCompleted, "finished_at": now}).Error; err != nil {
		t.Fatalf("置 completed 失败: %v", err)
	}
	if item := configItem(patchTo(order, v3.ID)); item.ConfigFromVersionID == nil || *item.ConfigFromVersionID != v1.ID {
		t.Fatalf("有交付时 from 应取最近交付 v1，实际 %v", item.ConfigFromVersionID)
	}

	// 非法输入：版本与作用域不匹配 / 版本不存在 / 重复作用域。
	badScope := ChangeConfigInput{ConfigScopeKind: model.ConfigScopeZone, ConfigScopeID: f.zone2ID, ConfigToVersionID: v1.ID}
	if _, err := env.orders.Update(order.ID, ChangeOrderInput{ConfigChanges: &[]ChangeConfigInput{badScope}}, "ops", ""); err == nil {
		t.Fatal("版本与作用域不匹配应拒绝")
	}
	missing := ChangeConfigInput{ConfigScopeKind: model.ConfigScopeZone, ConfigScopeID: f.zone1ID, ConfigToVersionID: 9999}
	if _, err := env.orders.Update(order.ID, ChangeOrderInput{ConfigChanges: &[]ChangeConfigInput{missing}}, "ops", ""); err == nil {
		t.Fatal("版本不存在应拒绝")
	}
	dup := ChangeConfigInput{ConfigScopeKind: model.ConfigScopeZone, ConfigScopeID: f.zone1ID, ConfigToVersionID: v1.ID}
	if _, err := env.orders.Update(order.ID, ChangeOrderInput{ConfigChanges: &[]ChangeConfigInput{dup, dup}}, "ops", ""); err == nil {
		t.Fatal("重复作用域应拒绝")
	}
}

// TestChangeOrderEventsDerivation 事件确定性派生：末事件与当前状态对齐、seq 递增、只反映真实时间戳。
func TestChangeOrderEventsDerivation(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)
	seedConfigItem(t, env.db, order.ID)

	// 新建 draft：仅一条 draft 事件。
	events, err := env.orders.Events(order.ID)
	if err != nil || len(events.Events) != 1 || events.Events[0].Status != model.ChangeOrderStatusDraft {
		t.Fatalf("draft 单应只有 1 条 draft 事件: %v / %+v", err, events)
	}

	// 提交 + 驳回：draft → pending_approval → 末条补 draft（与当前状态对齐）。
	if _, err := env.orders.Submit(order.ID, "ops-chen", ""); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if _, err := env.orders.Reject(order.ID, "重排批次", "admin", ""); err != nil {
		t.Fatalf("驳回失败: %v", err)
	}
	events, err = env.orders.Events(order.ID)
	if err != nil {
		t.Fatalf("取事件失败: %v", err)
	}
	statuses := make([]string, 0, len(events.Events))
	for i, evt := range events.Events {
		if evt.Seq != i+1 || evt.Type != "order_status" || evt.OrderID != order.ID {
			t.Fatalf("事件形状不符: %+v", evt)
		}
		statuses = append(statuses, evt.Status)
	}
	want := []string{model.ChangeOrderStatusDraft, model.ChangeOrderStatusPendingApproval, model.ChangeOrderStatusDraft}
	if strings.Join(statuses, ",") != strings.Join(want, ",") {
		t.Fatalf("事件序列应 %v，实际 %v", want, statuses)
	}
}

// TestChangeOrderTargetsAndObserveEmptyShape M1 读端点形态：targets 空页、observe 空形态（数组非 null）。
func TestChangeOrderTargetsAndObserveEmptyShape(t *testing.T) {
	env := newDeliveryTestEnv(t)
	f := seedDeliveryFixture(t, env)
	order := createDraftOrder(t, f)

	targets, err := env.orders.Targets(order.ID, repository.ChangeTargetQuery{})
	if err != nil || targets.Total != 0 || len(targets.Items) != 0 {
		t.Fatalf("未启动单 targets 应空页: %v / %+v", err, targets)
	}
	observe, err := env.orders.Observe(order.ID)
	if err != nil || observe.BatchNo != nil || observe.ObserveStartedAt != nil || observe.Targets == nil || len(observe.Targets) != 0 {
		t.Fatalf("observe 应恒空形态: %v / %+v", err, observe)
	}
	raw, _ := json.Marshal(observe)
	if string(raw) != `{"batchNo":null,"observeStartedAt":null,"targets":[]}` {
		t.Fatalf("observe 契约形状不符: %s", raw)
	}
}

// mustErr 提取 (view, err) 形式返回值的错误（简化断言）。
func mustErr[T any](_ T, err error) error { return err }
