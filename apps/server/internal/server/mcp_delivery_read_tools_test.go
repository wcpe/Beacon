package server

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// deliveryMCPReadToolNames 是 FR-245 的六个只读工具（4 段、动词结尾，与图纸 §3.1 逐字一致）。
var deliveryMCPReadToolNames = []string{
	"beacon.delivery.order.list",
	"beacon.delivery.order.get",
	"beacon.delivery.targets.list",
	"beacon.delivery.impact.get",
	"beacon.delivery.observe.get",
	"beacon.delivery.events.list",
}

// deliveryMCPTestDB 打开内存 sqlite 并迁移交付只读相关表
// （不依赖 MySQL / DSN；DSN 按测试名唯一以免共享内存库串扰）。
func deliveryMCPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Env{}, &model.EnvNamespace{},
		&model.ChangeOrder{}, &model.ChangeOrderItem{}, &model.ChangeBatch{}, &model.ChangeTarget{},
		&model.Server{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移交付相关表失败: %v", err)
	}
	return db
}

// deliveryMCPFixture 是接好真实交付读服务的 MCP 注册表：
// prod / dev 两个 namespace，env 只映射 prod —— 便于验证「范围外单被拒」。
type deliveryMCPFixture struct {
	db        *gorm.DB
	repo      *repository.ChangeOrderRepository
	registry  *MCPToolRegistry
	prodNSID  uint
	devNSID   uint
	envID     uint
	principal auth.Principal
}

func newDeliveryMCPFixture(t *testing.T) *deliveryMCPFixture {
	t.Helper()
	db := deliveryMCPTestDB(t)

	prod := &model.Namespace{Code: "prod", Name: "生产", Lifecycle: "active"}
	dev := &model.Namespace{Code: "dev", Name: "开发", Lifecycle: "active"}
	for _, ns := range []*model.Namespace{prod, dev} {
		if err := db.Create(ns).Error; err != nil {
			t.Fatalf("写入 namespace 失败: %v", err)
		}
	}
	env := &model.Env{Code: "prod-env", Name: "生产环境"}
	if err := db.Create(env).Error; err != nil {
		t.Fatalf("写入 env 失败: %v", err)
	}
	if err := db.Create(&model.EnvNamespace{EnvID: env.ID, NamespaceID: prod.ID}).Error; err != nil {
		t.Fatalf("写入 env→namespace 映射失败: %v", err)
	}

	repo := repository.NewChangeOrderRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	health := healthview.NewStore()
	orders := service.NewDeliveryOrderService(db, repo, repository.NewConfigLayerVersionRepository(db), auditRepo, nil, health)
	diff := service.NewDeliveryDiffService(db, repo, repository.NewFileAssetRepository(db), auditRepo, nil, health)
	registry := &MCPToolRegistry{
		approvals: &service.ApprovalService{},
		orders:    orders,
		reads: MCPReadServices{
			deliveryDiff: diff,
			scope:        service.NewObservationScopeResolver(repository.NewEnvRepository(db), repository.NewNamespaceRepository(db)),
		},
	}
	return &deliveryMCPFixture{
		db: db, repo: repo, registry: registry,
		prodNSID: prod.ID, devNSID: dev.ID, envID: env.ID,
		principal: auth.MCPPrincipal("delivery-test", "交付只读测试", model.MCPClientProfileAutomation),
	}
}

// server 按 automation profile 构造一次 MCP server（只读工具在该 profile 下同样注册）。
func (f *deliveryMCPFixture) server(t *testing.T) *mcp.Server {
	t.Helper()
	return f.registry.NewMCPServer(f.principal)
}

// seedOrder 直插一张变更单（绕开组单服务默认值与 selector 校验，便于构造指定状态存量行）。
func (f *deliveryMCPFixture) seedOrder(t *testing.T, nsID uint, title, status string) uint {
	t.Helper()
	order := &model.ChangeOrder{
		NamespaceID: nsID, Title: title, Status: status, BatchMode: "percent", BatchSizes: "[10,30,60]",
		ActivationMethod: "restart", ObserveWindowSec: 120, ActivateTimeoutSec: 300,
		PayloadState: "ready", CreatedBy: "ops", Selector: `{"all":true,"regions":[],"zones":[],"servers":[],"excludes":[]}`,
	}
	if err := f.repo.Create(order); err != nil {
		t.Fatalf("写入变更单失败: %v", err)
	}
	return order.ID
}

// seedBatch 直插一个批次（目标视图的 batchNo 换算依据）。
func (f *deliveryMCPFixture) seedBatch(t *testing.T, orderID uint, batchNo int) uint {
	t.Helper()
	batch := &model.ChangeBatch{OrderID: orderID, BatchNo: batchNo, Status: "running", PlannedCount: 1}
	if err := f.db.Create(batch).Error; err != nil {
		t.Fatalf("写入批次失败: %v", err)
	}
	return batch.ID
}

// seedServer 直插一行 server 事实（影响预览的逐目标行按页内 server 行组装）。
func (f *deliveryMCPFixture) seedServer(t *testing.T, nsID uint, serverID string) {
	t.Helper()
	srv := &model.Server{NamespaceID: nsID, ServerID: serverID, Kind: model.ServerKindBackend, Lifecycle: model.ServerLifecycleActive}
	if err := f.db.Create(srv).Error; err != nil {
		t.Fatalf("写入 server 失败: %v", err)
	}
}

// seedTarget 直插一个目标行。
func (f *deliveryMCPFixture) seedTarget(t *testing.T, orderID, batchID uint, serverID string) {
	t.Helper()
	target := &model.ChangeTarget{
		OrderID: orderID, BatchID: batchID, ServerID: serverID, Status: "failed",
		ChangedFileCount: 2, BackupPresent: true, Error: "推送超时",
	}
	if err := f.db.Create(target).Error; err != nil {
		t.Fatalf("写入目标失败: %v", err)
	}
}

// sortedMapKeys 返回 map 的键（排序，便于失败信息稳定可读）。
func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// assertExactKeys 断言 map 的键集合与期望**完全一致**（多键 / 少键都算失败）：
// 有界投影的守护——多出的大字段（如 items / batches / detail）会在此暴露。
func assertExactKeys(t *testing.T, label string, got map[string]any, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s 键数 %d 与期望 %d 不一致，实际键: %v", label, len(got), len(want), sortedMapKeys(got))
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("%s 缺字段 %s，实际键: %v", label, key, sortedMapKeys(got))
		}
	}
}

// TestMCPDeliveryReadToolsVisibleForBothProfiles 是 FR-245 的可见性闸：
// 六个只读工具在 observer 与 automation **两个 profile 都可见**（清单与真实注册两侧），
// 且目录定级为 low、不带 AutomationOnly、OperationKind 留空，命名保持 4 段动词结尾。
func TestMCPDeliveryReadToolsVisibleForBothProfiles(t *testing.T) {
	f := newDeliveryMCPFixture(t)
	for _, profile := range []string{model.MCPClientProfileObserver, model.MCPClientProfileAutomation} {
		principal := auth.MCPPrincipal("delivery-visible", "交付可见性测试", profile)
		registered := listRegisteredTools(t, f.registry, principal)
		declared := MCPToolNames(profile)
		for _, name := range deliveryMCPReadToolNames {
			if !containsMCPTool(registered, name) {
				t.Fatalf("profile=%s：工具 %s 未在真实注册路径出现", profile, name)
			}
			if !containsMCPTool(declared, name) {
				t.Fatalf("profile=%s：工具 %s 未在目录清单出现", profile, name)
			}
		}
	}
	for _, name := range deliveryMCPReadToolNames {
		spec, ok := mcpToolSpecByName(name)
		if !ok {
			t.Fatalf("工具 %s 未登记入 mcpToolCatalog", name)
		}
		if spec.RiskLevel != MCPRiskLow {
			t.Fatalf("只读工具 %s 应为 low，实际 %s", name, spec.RiskLevel)
		}
		if spec.AutomationOnly {
			t.Fatalf("只读工具 %s 不得带 AutomationOnly（否则 observer 不可见）", name)
		}
		if spec.OperationKind != "" {
			t.Fatalf("只读工具 %s 的 OperationKind 应留空，实际 %q", name, spec.OperationKind)
		}
		if segments := strings.Split(name, "."); len(segments) != 4 {
			t.Fatalf("工具名 %s 应为 4 段，实际 %d 段", name, len(segments))
		}
	}
}

// TestMCPDeliveryReadProjectionsAreBounded 断言六个投影函数的字段边界：
// 列表只回摘要 7 键、详情不回 items / batches（大字段）、逐台行与事件键集合固定。
func TestMCPDeliveryReadProjectionsAreBounded(t *testing.T) {
	at := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	pauseKind := "manual"
	pauseReason := "人工暂停"
	targetErr := "推送超时"
	batchNo := 1

	list := mcpDeliveryOrderListView(&service.ChangeOrderListView{
		Items: []service.ChangeOrderSummaryView{{
			ID: 7, NamespaceID: 3, Title: "单", Description: "说明", ScanDir: "plugins/",
			Status: "rolling", PauseKind: &pauseKind, CreatedBy: "ops", CreatedAt: at, PayloadState: "ready",
		}},
		Total: 1,
	})
	if list["total"] != int64(1) {
		t.Fatalf("列表 total 应为 1，实际 %v", list["total"])
	}
	rows, ok := list["items"].([]map[string]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("列表 items 形状异常: %+v", list["items"])
	}
	assertExactKeys(t, "order.list 列表项", rows[0],
		"id", "title", "status", "pauseKind", "createdBy", "createdAt", "payloadState")

	detail := mcpDeliveryOrderDetailView(&service.ChangeOrderDetailView{
		ChangeOrderSummaryView: service.ChangeOrderSummaryView{
			ID: 7, NamespaceID: 3, Title: "单", Description: "说明", ScanDir: "plugins/",
			Status: "rolling", PauseKind: &pauseKind, PauseReason: &pauseReason, CreatedBy: "ops",
			CreatedAt: at, UpdatedAt: at, PayloadState: "ready",
		},
		Selector:       service.ChangeSelector{All: true},
		Items:          []service.ChangeOrderItemView{{ID: 1, Kind: "file_diff"}},
		Batches:        []service.ChangeBatchView{{BatchNo: 1}},
		TargetCounts:   map[string]int64{"pending": 2},
		RollbackCounts: map[string]int64{"none": 2},
	})
	assertExactKeys(t, "order.get 详情", detail,
		"id", "title", "description", "namespaceId", "status", "pauseKind", "pauseReason", "selector",
		"batchMode", "batchSizes", "activationMethod", "observeWindowSec", "activateTimeoutSec",
		"failureRateThresholdPercent", "unhealthyRateThresholdPercent", "payloadState", "createdBy",
		"submittedAt", "approvedAt", "startedAt", "finishedAt", "cancelReason",
		"rollbackBy", "rollbackReason", "rollbackAt", "targetCounts", "rollbackCounts")
	for _, forbidden := range []string{"items", "batches", "scanDir", "sourceServerId", "diffSnapshotAt"} {
		if _, ok := detail[forbidden]; ok {
			t.Fatalf("order.get 详情不得透传大字段 / 非契约字段: %s", forbidden)
		}
	}
	selector, ok := detail["selector"].(map[string]any)
	if !ok {
		t.Fatalf("selector 应为对象: %+v", detail["selector"])
	}
	assertExactKeys(t, "order.get selector", selector, "all", "regions", "zones", "servers", "excludes")

	targets := mcpDeliveryTargetsView(&service.ChangeTargetPageView{
		Items: []service.ChangeTargetView{{
			ServerID: "t-1", BatchNo: 1, Status: "failed", ChangedFileCount: 2,
			SkippedFileCount: 1, BackupPresent: true, Error: &targetErr,
		}},
		Total: 1,
	})
	targetRows, ok := targets["items"].([]map[string]any)
	if !ok || len(targetRows) != 1 {
		t.Fatalf("targets.list items 形状异常: %+v", targets["items"])
	}
	assertExactKeys(t, "targets.list 行", targetRows[0],
		"serverId", "batchNo", "status", "rollbackStatus", "error", "rollbackError",
		"backupPresent", "changedFileCount", "skippedFileCount", "pushedAt", "activatedAt")

	impact := mcpDeliveryImpactView(&service.ChangeImpactView{
		Summary: service.ChangeImpactSummaryView{
			TargetTotal: 1, Batches: []service.ChangeImpactBatchView{{BatchNo: 1, Count: 1}},
			FileTotal: 2, TotalBytes: 30, TransferBytes: 20, ConfigScopeCount: 1, SnapshotAt: &at,
		},
		Targets: service.ChangeImpactTargetsPageView{
			Items: []service.ChangeImpactTargetView{{
				ServerID: "t-1", Online: true, Level: "healthy", AddCount: 1, UpdateCount: 1,
				ConfigScopes: []service.ChangeImpactConfigScopeView{{ScopeKind: "zone", ScopeID: 9}},
			}},
			Total: 1,
		},
	})
	summary, ok := impact["summary"].(map[string]any)
	if !ok {
		t.Fatalf("impact.get summary 形状异常: %+v", impact["summary"])
	}
	assertExactKeys(t, "impact.get summary", summary,
		"targetTotal", "batches", "fileTotal", "totalBytes", "transferBytes", "configScopeCount", "snapshotAt")
	impactTargets, ok := impact["targets"].(map[string]any)
	if !ok {
		t.Fatalf("impact.get targets 形状异常: %+v", impact["targets"])
	}
	assertExactKeys(t, "impact.get targets", impactTargets, "items", "total")
	impactRows, ok := impactTargets["items"].([]map[string]any)
	if !ok || len(impactRows) != 1 {
		t.Fatalf("impact.get targets.items 形状异常: %+v", impactTargets["items"])
	}
	assertExactKeys(t, "impact.get 逐目标行", impactRows[0],
		"serverId", "online", "level", "addCount", "updateCount", "deleteCount", "skipCount", "configScopes")

	observe := mcpDeliveryObserveView(&service.ChangeObserveView{
		BatchNo:          &batchNo,
		ObserveStartedAt: &at,
		Targets: []service.ChangeObserveTargetSeries{{
			ServerID: "t-1",
			Series:   []service.ChangeObserveSeriesPoint{{TsMs: 1, Score: 90, Level: "healthy", TPS: 19.5, Alerts: 0}},
		}},
	})
	assertExactKeys(t, "observe.get", observe, "batchNo", "observeStartedAt", "targets")
	observeTargets, ok := observe["targets"].([]map[string]any)
	if !ok || len(observeTargets) != 1 {
		t.Fatalf("observe.get targets 形状异常: %+v", observe["targets"])
	}
	assertExactKeys(t, "observe.get 逐目标序列", observeTargets[0], "serverId", "series")
	points, ok := observeTargets[0]["series"].([]map[string]any)
	if !ok || len(points) != 1 {
		t.Fatalf("observe.get series 形状异常: %+v", observeTargets[0]["series"])
	}
	assertExactKeys(t, "observe.get 序列点", points[0], "tsMs", "score", "level", "tps", "alerts")

	events := mcpDeliveryEventsView(&service.ChangeEventsView{
		Events: []service.ChangeOrderEventView{{Seq: 1, At: at, Type: "order_status", OrderID: 7, Status: "rolling"}},
	})
	assertExactKeys(t, "events.list", events, "events")
	eventRows, ok := events["events"].([]map[string]any)
	if !ok || len(eventRows) != 1 {
		t.Fatalf("events.list events 形状异常: %+v", events["events"])
	}
	assertExactKeys(t, "events.list 事件", eventRows[0], "seq", "at", "type", "orderId", "batchNo", "serverId", "status")
}

// seedChainScenario 布置全链路场景：一条 prod 单（1 批 3 目标 + 3 台 server 事实）
// 与一条 dev 单，返回两单 id。
func (f *deliveryMCPFixture) seedChainScenario(t *testing.T) (uint, uint) {
	t.Helper()
	prodOrder := f.seedOrder(t, f.prodNSID, "prod 单", model.ChangeOrderStatusRolling)
	batchID := f.seedBatch(t, prodOrder, 1)
	for _, serverID := range []string{"t-1", "t-2", "t-3"} {
		f.seedServer(t, f.prodNSID, serverID)
		f.seedTarget(t, prodOrder, batchID, serverID)
	}
	devOrder := f.seedOrder(t, f.devNSID, "dev 单", model.ChangeOrderStatusDraft)
	return prodOrder, devOrder
}

// TestMCPDeliveryReadChainIsBounded 是 FR-245 的链路闸（真实服务 + 内存库）：
// 列表 → 详情 → 目标分页依次可读且返回有界；列表缺 namespaceId 即拒。
func TestMCPDeliveryReadChainIsBounded(t *testing.T) {
	f := newDeliveryMCPFixture(t)
	prodOrder, _ := f.seedChainScenario(t)
	server := f.server(t)
	prodScope := strconv.FormatUint(uint64(f.prodNSID), 10)

	// ① 列表：namespaceId 必填（缺省即拒），指定 prod 只回 prod 单
	if res := mustCallMCPTool(t, server, "beacon.delivery.order.list", map[string]any{}); !res.IsError {
		t.Fatalf("order.list 缺 namespaceId 应被拒: %+v", res)
	}
	listOut := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.list", map[string]any{"namespaceId": prodScope}))
	listItems := mcpItems(t, listOut)
	if len(listItems) != 1 || listOut["total"] != float64(1) {
		t.Fatalf("prod 应只回 1 单，实际 total=%v items=%v", listOut["total"], listItems)
	}
	if listItems[0]["id"] != float64(prodOrder) {
		t.Fatalf("列表应只含 prod 单 %d，实际 %v", prodOrder, listItems[0]["id"])
	}
	assertExactKeys(t, "order.list 列表项", listItems[0],
		"id", "title", "status", "pauseKind", "createdBy", "createdAt", "payloadState")

	// ② 详情：摘要 + 计数，绝无 items / batches
	detailOut := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.get", map[string]any{"orderId": prodOrder}))
	if detailOut["status"] != model.ChangeOrderStatusRolling || detailOut["namespaceId"] != float64(f.prodNSID) {
		t.Fatalf("详情摘要字段不符: %v", detailOut)
	}
	if counts, ok := detailOut["targetCounts"].(map[string]any); !ok || counts["failed"] != float64(3) {
		t.Fatalf("详情应回目标计数（failed=3），实际 %v", detailOut["targetCounts"])
	}
	for _, forbidden := range []string{"items", "batches"} {
		if _, ok := detailOut[forbidden]; ok {
			t.Fatalf("详情不得透传大字段 %s", forbidden)
		}
	}

	// ③ 目标分页：total 恒为全量、items 受 pageSize 约束
	targetsOut := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.targets.list", map[string]any{"orderId": prodOrder, "page": 1, "pageSize": 2}))
	targetRows := mcpItems(t, targetsOut)
	if len(targetRows) != 2 || targetsOut["total"] != float64(3) {
		t.Fatalf("目标分页应有 2 行 total=3，实际 total=%v rows=%v", targetsOut["total"], targetRows)
	}
	if targetRows[0]["batchNo"] != float64(1) || targetRows[0]["error"] != "推送超时" || targetRows[0]["backupPresent"] != true {
		t.Fatalf("目标行投影字段不符: %v", targetRows[0])
	}
}

// TestMCPDeliveryReadImpactObserveEventsAreBounded 覆盖余下三工具（影响 / 观察窗 / 事件）：
// 影响逐目标受分页约束且汇总为全量目标数；观察窗与事件沿用 HTTP 端点的有界条数。
func TestMCPDeliveryReadImpactObserveEventsAreBounded(t *testing.T) {
	f := newDeliveryMCPFixture(t)
	prodOrder, _ := f.seedChainScenario(t)
	server := f.server(t)

	// ④ 影响预览：汇总 + 逐目标分页（冻结目标快照为准）
	impactOut := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.impact.get", map[string]any{"orderId": prodOrder, "pageSize": 2}))
	impactSummary, ok := impactOut["summary"].(map[string]any)
	if !ok || impactSummary["targetTotal"] != float64(3) {
		t.Fatalf("影响汇总的目标数应为 3，实际 %v", impactOut["summary"])
	}
	impactTargets, ok := impactOut["targets"].(map[string]any)
	if !ok || impactTargets["total"] != float64(3) {
		t.Fatalf("影响逐目标 total 应为 3，实际 %v", impactOut["targets"])
	}
	if rows, ok := impactTargets["items"].([]any); !ok || len(rows) != 2 {
		t.Fatalf("影响逐目标应受 pageSize 约束为 2 行，实际 %v", impactTargets["items"])
	}

	// ⑤ 观察窗：未装配观察窗提供方 → 空形态（数组非 null），非拒绝
	observeOut := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.observe.get", map[string]any{"orderId": prodOrder}))
	if targets, ok := observeOut["targets"].([]any); !ok || len(targets) != 0 {
		t.Fatalf("观察窗应为空数组而非 null: %+v", observeOut["targets"])
	}
	if observeOut["batchNo"] != nil {
		t.Fatalf("无活动批时 batchNo 应为 null: %v", observeOut["batchNo"])
	}

	// ⑥ 事件：派生快照按生命周期时间戳产出（至少含 draft 起点）
	eventsOut := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.events.list", map[string]any{"orderId": prodOrder}))
	eventRows, ok := eventsOut["events"].([]any)
	if !ok || len(eventRows) == 0 {
		t.Fatalf("事件列表应非空: %v", eventsOut["events"])
	}
}

// TestMCPDeliveryReadRejectsCrossNamespaceOrder 是 FR-245 的负向闸：
// 以 prod 范围（namespaceId 或 envId）读 dev 单，五个按 orderId 定位的工具一律拒绝；
// 范围外与不存在**共用同一条**拒绝文案（不泄露范围外单是否存在）。
func TestMCPDeliveryReadRejectsCrossNamespaceOrder(t *testing.T) {
	f := newDeliveryMCPFixture(t)
	prodOrder, devOrder := f.seedChainScenario(t)
	server := f.server(t)
	prodScope := strconv.FormatUint(uint64(f.prodNSID), 10)

	// ⑦ 跨 namespace：以 prod 范围读 dev 单，五个按 orderId 定位的工具一律拒绝
	for _, tool := range []string{
		"beacon.delivery.order.get", "beacon.delivery.targets.list",
		"beacon.delivery.impact.get", "beacon.delivery.observe.get", "beacon.delivery.events.list",
	} {
		res := mustCallMCPTool(t, server, tool, map[string]any{"orderId": devOrder, "namespaceId": prodScope})
		if !res.IsError {
			t.Fatalf("%s 以 prod 范围读 dev 单应被拒: %+v", tool, res)
		}
	}
	// env 同样收窄：env 只映射 prod，故读 dev 单同样被拒
	if res := mustCallMCPTool(t, server, "beacon.delivery.order.get", map[string]any{
		"orderId": devOrder, "envId": strconv.FormatUint(uint64(f.envID), 10),
	}); !res.IsError {
		t.Fatalf("以 prod-env 范围读 dev 单应被拒: %+v", res)
	}
	// 范围外与不存在**同一条**拒绝文案（不泄露范围外单是否存在）
	outside := mustCallMCPTool(t, server, "beacon.delivery.order.get", map[string]any{"orderId": devOrder, "namespaceId": prodScope})
	missing := mustCallMCPTool(t, server, "beacon.delivery.order.get", map[string]any{"orderId": devOrder + 1000, "namespaceId": prodScope})
	if mcpResultText(outside) != mcpResultText(missing) {
		t.Fatalf("范围外与不存在应共用同一条拒绝文案: %q vs %q", mcpResultText(outside), mcpResultText(missing))
	}
	// 反向：prod 范围内读 prod 单照常可读（拒绝不是「一律拒绝」）
	if res := mustCallMCPTool(t, server, "beacon.delivery.order.get", map[string]any{"orderId": prodOrder, "namespaceId": prodScope}); res.IsError {
		t.Fatalf("prod 范围内读 prod 单不应被拒: %+v", res)
	}
}
