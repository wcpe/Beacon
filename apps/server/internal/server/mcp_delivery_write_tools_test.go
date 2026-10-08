package server

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// deliveryMCPWriteToolNames 是第二波的五个直执工具（4 段、动词结尾，与图纸 §3.1 逐字一致）。
var deliveryMCPWriteToolNames = []string{
	"beacon.delivery.order.create",
	"beacon.delivery.order.update",
	"beacon.delivery.order.diff-scan",
	"beacon.delivery.order.pause",
	"beacon.delivery.order.cancel",
}

// deliveryMCPApprovalToolNames 是既有六项「只建审批申请」的交付工具：
// FR-248 的拒绝文案收口对象（本波不改它们的语义，只换拒绝文案）。
var deliveryMCPApprovalToolNames = []string{
	"beacon.delivery.order.submit",
	"beacon.delivery.order.delete",
	"beacon.delivery.order.resume",
	"beacon.delivery.order.rollback",
	"beacon.delivery.batch.confirm",
	"beacon.delivery.rollback.finish",
}

// ── §3.1 目录与可见性 ──

// TestMCPDeliveryWriteToolsAreAutomationOnly 是 FR-246 / FR-247 的目录闸：
// 五个直执工具的等级（组单 low、止损 high）、AutomationOnly 与 OperationKind 留空必须与图纸逐项一致，
// 且 observer 一个都不可见（写工具对 observer 封闭）。
func TestMCPDeliveryWriteToolsAreAutomationOnly(t *testing.T) {
	wantRisk := map[string]string{
		"beacon.delivery.order.create":    MCPRiskLow,
		"beacon.delivery.order.update":    MCPRiskLow,
		"beacon.delivery.order.diff-scan": MCPRiskLow,
		"beacon.delivery.order.pause":     MCPRiskHigh,
		"beacon.delivery.order.cancel":    MCPRiskHigh,
	}
	observer := MCPToolNames(model.MCPClientProfileObserver)
	automation := MCPToolNames(model.MCPClientProfileAutomation)
	for _, name := range deliveryMCPWriteToolNames {
		if !containsMCPTool(automation, name) {
			t.Fatalf("automation 应可见交付直执工具 %s", name)
		}
		if containsMCPTool(observer, name) {
			t.Fatalf("observer 不得可见交付写工具 %s", name)
		}
		spec, ok := mcpToolSpecByName(name)
		if !ok {
			t.Fatalf("工具 %s 未登记入 mcpToolCatalog", name)
		}
		if spec.RiskLevel != wantRisk[name] {
			t.Fatalf("工具 %s 风险等级应为 %s，实际 %s", name, wantRisk[name], spec.RiskLevel)
		}
		if !spec.AutomationOnly {
			t.Fatalf("交付写工具 %s 必须带 AutomationOnly（仅 automation 可发现）", name)
		}
		if spec.OperationKind != "" {
			// 直执无审批票据：留空才不触发「catalog 等级 ≥ descriptor」约束、也不改 wantChecked 计数。
			t.Fatalf("工具 %s 的 OperationKind 应留空，实际 %q", name, spec.OperationKind)
		}
		if segments := strings.Split(name, "."); len(segments) != 4 {
			t.Fatalf("工具名 %s 应为 4 段，实际 %d 段", name, len(segments))
		}
	}
}

// TestMCPDeliveryWriteToolsRegisterOnRealPath 经真实注册路径枚举，确认五个工具确实进了 server
// （目录声明与真实注册两侧一致），且既有六项申请类工具仍在。
func TestMCPDeliveryWriteToolsRegisterOnRealPath(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	registered := listRegisteredTools(t, f.registry, f.principal)
	for _, name := range append(append([]string{}, deliveryMCPWriteToolNames...), deliveryMCPApprovalToolNames...) {
		if !containsMCPTool(registered, name) {
			t.Fatalf("工具 %s 未在真实注册路径出现", name)
		}
	}
}

// ── §3.5 错误理由映射 ──

// TestMCPDeliveryRejectedReasonsCoverEveryDeliveryCode 断言映射表覆盖交付域全部错误码，
// 且表就是唯一文案真源（逐码经映射函数取回同一条短语）。
func TestMCPDeliveryRejectedReasonsCoverEveryDeliveryCode(t *testing.T) {
	// 期望表 = 图纸 §3.5 全部 16 行 + 交付域自身的 missing_reason（终止 / 整单回滚原因必填）。
	want := map[string]string{
		apperr.ErrApprovalReasonRequired.Code:       "必须填写原因（reason）",
		"illegal_state":                             "当前状态不允许该操作",
		apperr.ErrChangeNoItems.Code:                "变更单没有任何变更项，无法提交审批",
		apperr.ErrChangeNoTarget.Code:               "未解析出任何合格目标",
		apperr.ErrChangeNoRollbackTarget.Code:       "单内无曾推送的目标可回滚",
		apperr.ErrChangeSourceMissing.Code:          "未指定黄金模板源，无法扫描文件差异",
		apperr.ErrChangeSourceInvalid.Code:          "模板源必须已确认绑定且在线的 backend 子服",
		apperr.ErrChangeSourceSnapshotMissing.Code:  "模板源尚无文件资产快照，请先重扫",
		apperr.ErrChangeSelectorCrossNamespace.Code: "selector 引用了不属于本环境的实体",
		apperr.ErrChangeConfigVersionInvalid.Code:   "配置版本不存在或与作用域不匹配",
		apperr.ErrChangeBatchNotFound.Code:          "批次不存在",
		apperr.ErrChangeResumeModeRequired.Code:     "熔断/准备失败暂停必须指定 mode 与原因",
		apperr.ErrChangeOrderNotFound.Code:          "变更单不存在",
		apperr.ErrChangeApproverSeparation.Code:     "审批人不得是创建人",
		apperr.ErrChangeNotCreator.Code:             "仅创建人可撤回变更单",
		apperr.ErrForbidden.Code:                    "当前主体无权执行该操作",
		// 交付域自产（非 apperr 预定义）：与 approval_reason_required 同义，终止 / 整单回滚的原因必填。
		"missing_reason": "必须填写原因（reason）",
	}
	if len(mcpDeliveryRejectedReasons) != len(want) {
		t.Fatalf("映射表条目数 %d 与图纸期望 %d 不一致，实际键: %v",
			len(mcpDeliveryRejectedReasons), len(want), deliveryReasonTableKeys())
	}
	for code, phrase := range want {
		if got, ok := mcpDeliveryRejectedReasons[code]; !ok || got != phrase {
			t.Fatalf("错误码 %s 的短语应为 %q，实际 %q（存在=%v）", code, phrase, got, ok)
		}
		// illegal_state 的短语是稳定骨架，具体卡点（当前状态与动作）由领域错误细节拼上。
		if code == "illegal_state" {
			got := mcpDeliveryErrReason(&apperr.Error{Code: code, Message: "当前状态 rolling 不允许 编辑"})
			if got != phrase+"：当前状态 rolling 不允许 编辑" {
				t.Fatalf("illegal_state 应带当前状态与动作，实际 %q", got)
			}
			continue
		}
		// 其余码：映射函数必须回表中的短语本身，而不是领域错误自带措辞（稳定文案，不随内部措辞漂移）。
		got := mcpDeliveryErrReason(&apperr.Error{Code: code, Message: "领域内部措辞"})
		if got != phrase {
			t.Fatalf("错误码 %s 的映射结果应为 %q，实际 %q", code, phrase, got)
		}
	}
}

// deliveryReasonTableKeys 返回映射表的键（排序，便于失败信息稳定可读）。
func deliveryReasonTableKeys() []string {
	keys := make([]string, 0, len(mcpDeliveryRejectedReasons))
	for code := range mcpDeliveryRejectedReasons {
		keys = append(keys, code)
	}
	sort.Strings(keys)
	return keys
}

// TestMCPDeliveryErrReasonFallsBackWithoutEmptyText 断言「其他」行的两条兜底：
// 未列入表的领域错误沿用其脱敏中文说明；非领域错误回统一兜底文案——**绝不回空文案**。
func TestMCPDeliveryErrReasonFallsBackWithoutEmptyText(t *testing.T) {
	domain := &apperr.Error{Code: "invalid_param", Message: "namespaceId / title 必填"}
	if got := mcpDeliveryErrReason(domain); got != "namespaceId / title 必填" {
		t.Fatalf("未列入表的领域错误应沿用其说明，实际 %q", got)
	}
	empty := &apperr.Error{Code: "invalid_param"}
	if got := mcpDeliveryErrReason(empty); got == "" {
		t.Fatalf("领域错误无说明时不得回空文案")
	}
	if got := mcpDeliveryErrReason(errDeliveryWriteSentinel); got != mcpDeliveryRejectedFallbackReason {
		t.Fatalf("非领域错误应回统一兜底文案，实际 %q", got)
	}
	if got := mcpDeliveryErrReason(nil); got != "" {
		t.Fatalf("nil 错误应回空串，实际 %q", got)
	}
}

// errDeliveryWriteSentinel 模拟存储层等非领域错误（映射表之外的错误形态）。
var errDeliveryWriteSentinel = &deliveryWritePlainError{}

type deliveryWritePlainError struct{}

func (*deliveryWritePlainError) Error() string { return "sql: connection refused" }

// ── 夹具 ──

// deliveryWriteMCPFixture 接好交付组单 / 差异面 / 编排器三个真服务与审批域，
// 供第二波（FR-246/247/248/250）经真实 MCP 协议路径断言。
type deliveryWriteMCPFixture struct {
	db        *gorm.DB
	repo      *repository.ChangeOrderRepository
	audits    *repository.AuditLogRepository
	orders    *service.DeliveryOrderService
	registry  *MCPToolRegistry
	nsID      uint
	devNSID   uint
	envID     uint
	zoneID    uint
	principal auth.Principal
}

func newDeliveryWriteMCPFixture(t *testing.T) *deliveryWriteMCPFixture {
	t.Helper()
	dsn := "file:mcp_delivery_write_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Namespace{}, &model.Env{}, &model.EnvNamespace{}, &model.BCCluster{},
		&model.Region{}, &model.Zone{}, &model.Server{}, &model.AgentIdentity{}, &model.ChangeOrder{},
		&model.ChangeOrderItem{}, &model.ChangeBatch{}, &model.ChangeTarget{}, &model.FileAsset{},
		&model.FileAssetScan{}, &model.ConfigFile{}, &model.ConfigLayerVersion{}, &model.AuditLog{},
		&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移交付相关表失败: %v", err)
	}

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
	// env 只映射 prod：以 envId 收窄范围即可验证「跨 namespace 的组单与止损被拒」。
	if err := db.Create(&model.EnvNamespace{EnvID: env.ID, NamespaceID: prod.ID}).Error; err != nil {
		t.Fatalf("写入 env→namespace 映射失败: %v", err)
	}
	cluster := &model.BCCluster{NamespaceID: prod.ID, Name: "bc-1"}
	if err := db.Create(cluster).Error; err != nil {
		t.Fatalf("写入 BC 集群失败: %v", err)
	}
	region := &model.Region{BCClusterID: cluster.ID, Name: "region-1"}
	if err := db.Create(region).Error; err != nil {
		t.Fatalf("写入大区失败: %v", err)
	}
	zone := &model.Zone{RegionID: region.ID, Name: "zone-1"}
	if err := db.Create(zone).Error; err != nil {
		t.Fatalf("写入小区失败: %v", err)
	}

	repo := repository.NewChangeOrderRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	health := healthview.NewStore()
	orders := service.NewDeliveryOrderService(db, repo, repository.NewConfigLayerVersionRepository(db),
		auditRepo, deliveryWriteSettings{}, health)
	diff := service.NewDeliveryDiffService(db, repo, repository.NewFileAssetRepository(db), auditRepo, nil, health)
	orch := service.NewDeliveryOrchestrator(db, repo, nil, nil, auditRepo, nil, nil, nil)

	// 审批域：本用例只走「建申请」路径，适配器体不执行领域动作。
	approvalRegistry := authz.NewApprovalRegistry()
	for _, kind := range []string{
		authz.OperationDeliveryApprove, authz.OperationDeliveryDraftDelete, authz.OperationDeliveryRollbackFinish,
	} {
		approvalRegistry.Register(kind,
			authz.TransactionalAdapterFunc(func(_ *gorm.DB, _ authz.ApprovalRequest, _ authz.Permit) (func(), error) { return nil, nil }))
	}
	approval := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db), auditRepo, approvalRegistry)
	orders.SetApprovalService(approval)
	orch.SetApprovalService(approval)

	registry := &MCPToolRegistry{approvals: approval, orders: orders, delivery: orch}
	registry.SetReadServices(MCPReadServices{
		deliveryDiff: diff,
		scope:        service.NewObservationScopeResolver(repository.NewEnvRepository(db), repository.NewNamespaceRepository(db)),
	})
	return &deliveryWriteMCPFixture{
		db: db, repo: repo, audits: auditRepo, orders: orders, registry: registry,
		nsID: prod.ID, devNSID: dev.ID, envID: env.ID, zoneID: zone.ID,
		principal: auth.MCPPrincipal("delivery-write", "交付写工具测试", model.MCPClientProfileAutomation),
	}
}

// deliveryWriteSettings 是交付域所需的最小设置源（审批人分离与本用例无关）。
type deliveryWriteSettings struct{}

func (deliveryWriteSettings) GetBool(string) bool { return false }

func (f *deliveryWriteMCPFixture) server(t *testing.T) *mcp.Server {
	t.Helper()
	return f.registry.NewMCPServer(f.principal)
}

// seedOrder 直插一张变更单（绕开组单服务默认值与 selector 校验，便于构造指定状态的存量行）。
func (f *deliveryWriteMCPFixture) seedOrder(t *testing.T, nsID uint, title, status string, sourceServerID string) uint {
	t.Helper()
	order := &model.ChangeOrder{
		NamespaceID: nsID, Title: title, Status: status, SourceServerID: sourceServerID,
		BatchMode: "percent", BatchSizes: "[10,30,60]", ActivationMethod: "restart",
		ObserveWindowSec: 120, ActivateTimeoutSec: 300, PayloadState: "pending", CreatedBy: "ops",
		Selector: `{"all":false,"regions":[],"zones":[],"servers":["t-1"],"excludes":[]}`,
	}
	if err := f.repo.Create(order); err != nil {
		t.Fatalf("写入变更单失败: %v", err)
	}
	return order.ID
}

// seedServer 直插一行 server 事实（差异扫描的目标解析按它组装）。
func (f *deliveryWriteMCPFixture) seedServer(t *testing.T, nsID uint, serverID string) uint {
	t.Helper()
	zoneID := f.zoneID
	srv := &model.Server{NamespaceID: nsID, ServerID: serverID, Kind: model.ServerKindBackend,
		Lifecycle: model.ServerLifecycleActive, ZoneID: &zoneID}
	if err := f.db.Create(srv).Error; err != nil {
		t.Fatalf("写入 server 失败: %v", err)
	}
	// 合格目标要求身份已确认绑定（activeServerIDs）。
	if err := f.db.Create(&model.AgentIdentity{IdentityID: "idn-" + serverID, NamespaceID: nsID,
		ServerID: model.NullableServerID(serverID), Kind: model.ServerKindBackend,
		Status: model.AgentIdentityStatusActive, StatusChangedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("写入身份失败: %v", err)
	}
	return srv.ID
}

// seedAsset 直插一行文件资产（差异计算的源 / 目标侧输入）。
func (f *deliveryWriteMCPFixture) seedAsset(t *testing.T, nsID, serverRowID uint, path, sha string) {
	t.Helper()
	asset := &model.FileAsset{NamespaceID: nsID, ServerID: serverRowID, Path: path, Ext: ".jar",
		SHA256: sha, Size: 10, ScannedAt: time.Now().UTC()}
	if err := f.db.Create(asset).Error; err != nil {
		t.Fatalf("写入文件资产失败: %v", err)
	}
}

// seedAssetScan 直插模板源的扫描概要行（缺它即「源尚无快照」，差异扫描必拒）。
func (f *deliveryWriteMCPFixture) seedAssetScan(t *testing.T, nsID, serverRowID uint) {
	t.Helper()
	scan := &model.FileAssetScan{NamespaceID: nsID, ServerID: serverRowID,
		ManifestDigest: strings.Repeat("ab", 32), FileCount: 2, TotalSize: 20, ScannedAt: time.Now().UTC()}
	if err := f.db.Create(scan).Error; err != nil {
		t.Fatalf("写入扫描概要失败: %v", err)
	}
}

// seedConfigVersion 直插一个小区层的配置版本，返回版本 id（组单一次成型用例的配置变更项入参）。
func (f *deliveryWriteMCPFixture) seedConfigVersion(t *testing.T) uint {
	t.Helper()
	file := &model.ConfigFile{NamespaceID: f.nsID, Name: "plugins/Demo/config.yml", Format: "yaml"}
	if err := f.db.Create(file).Error; err != nil {
		t.Fatalf("写入配置文件失败: %v", err)
	}
	version := &model.ConfigLayerVersion{ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeZone,
		ScopeRefID: f.zoneID, VersionNo: 1, Content: "a: 1\n", ContentHash: strings.Repeat("cd", 32), CreatedBy: "ops"}
	if err := f.db.Create(version).Error; err != nil {
		t.Fatalf("写入配置版本失败: %v", err)
	}
	return version.ID
}

// orderRow 回读变更单落库状态。
func (f *deliveryWriteMCPFixture) orderRow(t *testing.T, id uint) *model.ChangeOrder {
	t.Helper()
	order, err := f.repo.FindByID(id)
	if err != nil {
		t.Fatalf("回读变更单失败: %v", err)
	}
	if order == nil {
		t.Fatalf("变更单 %d 应存在", id)
	}
	return order
}

// auditClientIPs 取某动作的全部审计来源地址（验证 MCP 面的 clientIP 口径统一为 mcp）。
func (f *deliveryWriteMCPFixture) auditClientIPs(t *testing.T, action string) []string {
	t.Helper()
	var rows []model.AuditLog
	if err := f.db.Where("action = ?", action).Find(&rows).Error; err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	ips := make([]string, 0, len(rows))
	for _, row := range rows {
		ips = append(ips, row.ClientIP)
	}
	return ips
}

// ── FR-246 组单：一次成型与审计口径 ──

// TestMCPDeliveryOrderCreateBindsConfigChangesInOneShot 是 FR-246 的修复点闸：
// 经 MCP 一次调用建单即可携带 configChanges（不再「先建 draft 再 PATCH」两段式）；
// 未提供即纯文件单；两条路径的审计来源地址统一为 mcp。
func TestMCPDeliveryOrderCreateBindsConfigChangesInOneShot(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	server := f.server(t)
	versionID := f.seedConfigVersion(t)

	out := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.create", map[string]any{
		"namespaceId": strconv.FormatUint(uint64(f.nsID), 10),
		"title":       "大厅插件发布",
		"batchMode":   model.BatchModePercent,
		"batchSizes":  []int{10, 30, 60},
		"selector":    map[string]any{"all": true},
		"configChanges": []map[string]any{{
			"configScopeKind": model.ConfigScopeZone, "configScopeId": f.zoneID, "configToVersionId": versionID,
		}},
	}))
	assertExactKeys(t, "order.create", out, "orderId", "status")
	orderID := uint(out["orderId"].(float64))
	if out["status"] != model.ChangeOrderStatusDraft {
		t.Fatalf("新建单应为 draft，实际 %v", out["status"])
	}
	items, err := f.repo.ListItems(orderID)
	if err != nil {
		t.Fatalf("回读变更项失败: %v", err)
	}
	if len(items) != 1 || items[0].Kind != model.ChangeItemKindConfigChange {
		t.Fatalf("创建应一次成型写入配置变更项，实际 %+v", items)
	}
	if items[0].ConfigToVersionID == nil || *items[0].ConfigToVersionID != versionID {
		t.Fatalf("配置变更项的目标版本应为 %d，实际 %+v", versionID, items[0].ConfigToVersionID)
	}

	// 无 configChanges：纯文件单（不产生任何空壳配置项）。
	fileOnly := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.create", map[string]any{
		"namespaceId": strconv.FormatUint(uint64(f.nsID), 10),
		"title":       "纯文件单",
		"selector":    map[string]any{"all": true},
	}))
	fileOnlyID := uint(fileOnly["orderId"].(float64))
	fileItems, err := f.repo.ListItems(fileOnlyID)
	if err != nil {
		t.Fatalf("回读变更项失败: %v", err)
	}
	if len(fileItems) != 0 {
		t.Fatalf("纯文件单不应有变更项，实际 %+v", fileItems)
	}

	// clientIP 口径统一：MCP 面写审计记 "mcp"（不是真实来源地址，也不是空串）。
	if ips := f.auditClientIPs(t, model.ActionDeliveryOrderCreate); len(ips) != 2 {
		t.Fatalf("应有 2 条创建审计，实际 %v", ips)
	} else {
		for _, ip := range ips {
			if ip != "mcp" {
				t.Fatalf("MCP 面审计来源地址应为 mcp，实际 %q", ip)
			}
		}
	}

	// 配置项非法：整事务回滚——既不落空单，拒绝文案也来自映射表（config_version_invalid）。
	var beforeCount int64
	if err := f.db.Model(&model.ChangeOrder{}).Count(&beforeCount).Error; err != nil {
		t.Fatalf("统计变更单失败: %v", err)
	}
	bad := mustCallMCPTool(t, server, "beacon.delivery.order.create", map[string]any{
		"namespaceId": strconv.FormatUint(uint64(f.nsID), 10), "title": "坏配置单",
		"selector": map[string]any{"all": true},
		"configChanges": []map[string]any{{
			"configScopeKind": model.ConfigScopeZone, "configScopeId": f.zoneID, "configToVersionId": 999999,
		}},
	})
	if !bad.IsError {
		t.Fatalf("配置版本不存在应被拒: %+v", bad)
	}
	if want := mcpDeliveryRejectedReasons[apperr.ErrChangeConfigVersionInvalid.Code]; !strings.Contains(mcpResultText(bad), want) {
		t.Fatalf("拒绝文案应含 %q，实际 %q", want, mcpResultText(bad))
	}
	var afterCount int64
	if err := f.db.Model(&model.ChangeOrder{}).Count(&afterCount).Error; err != nil {
		t.Fatalf("统计变更单失败: %v", err)
	}
	if beforeCount != afterCount {
		t.Fatalf("配置项非法时不应落单：前 %d 张、后 %d 张", beforeCount, afterCount)
	}
}

// TestMCPDeliveryOrderUpdateEditsDraftInPlace 覆盖编辑工具：draft 可改标题并落审计（同样记 mcp）。
func TestMCPDeliveryOrderUpdateEditsDraftInPlace(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	orderID := f.seedOrder(t, f.nsID, "旧标题", model.ChangeOrderStatusDraft, "")
	server := f.server(t)

	out := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.update", map[string]any{
		"orderId": orderID, "title": "新标题",
	}))
	assertExactKeys(t, "order.update", out, "orderId", "status")
	if f.orderRow(t, orderID).Title != "新标题" {
		t.Fatalf("编辑未落库，当前标题 %q", f.orderRow(t, orderID).Title)
	}
	ips := f.auditClientIPs(t, model.ActionDeliveryOrderUpdate)
	if len(ips) != 1 || ips[0] != "mcp" {
		t.Fatalf("编辑审计来源地址应为 mcp，实际 %v", ips)
	}
}

// TestMCPDeliveryOrderDiffScanReturnsAggregate 覆盖差异扫描：返回**计数聚合**而非逐文件明细，
// 且聚合口径与差异算法的三种动作逐一对应（add / update / delete / total / snapshotAt）。
func TestMCPDeliveryOrderDiffScanReturnsAggregate(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	srcID := f.seedServer(t, f.nsID, "src-1")
	targetID := f.seedServer(t, f.nsID, "t-1")
	f.seedAsset(t, f.nsID, srcID, "plugins/a.jar", strings.Repeat("aa", 32))
	f.seedAsset(t, f.nsID, srcID, "plugins/new.jar", strings.Repeat("cc", 32))
	f.seedAsset(t, f.nsID, targetID, "plugins/a.jar", strings.Repeat("bb", 32))
	f.seedAsset(t, f.nsID, targetID, "plugins/gone.jar", strings.Repeat("dd", 32))
	f.seedAssetScan(t, f.nsID, srcID)
	orderID := f.seedOrder(t, f.nsID, "待扫差异", model.ChangeOrderStatusDraft, "src-1")
	// scanDir 决定参与比对的前缀：默认空串会连同未写的其余路径一起比对，这里显式收窄到 plugins。
	if err := f.db.Model(&model.ChangeOrder{}).Where("id = ?", orderID).
		Update("scan_dir", "plugins").Error; err != nil {
		t.Fatalf("设置扫描目录失败: %v", err)
	}
	server := f.server(t)

	out := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.diff-scan", map[string]any{"orderId": orderID}))
	assertExactKeys(t, "order.diff-scan", out, "add", "update", "delete", "total", "snapshotAt")
	want := map[string]float64{"add": 1, "update": 1, "delete": 1, "total": 3}
	for key, value := range want {
		if out[key] != value {
			t.Fatalf("差异扫描 %s 应为 %v，实际 %v（%v）", key, value, out[key], out)
		}
	}
	if out["snapshotAt"] == nil || out["snapshotAt"] == "" {
		t.Fatalf("差异扫描应回快照时间: %v", out)
	}
}

// ── FR-247 止损 ──

// TestMCPDeliveryOrderPauseAndCancelTakeEffect 覆盖止损两项的真效果与审计口径：
// pause 把 rolling 打成 paused(manual)；cancel 需原因、把 paused 打成 cancelled 并落原因；
// 两者审计来源地址均为 mcp。
func TestMCPDeliveryOrderPauseAndCancelTakeEffect(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	orderID := f.seedOrder(t, f.nsID, "进行中的灰度", model.ChangeOrderStatusRolling, "")
	server := f.server(t)

	paused := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.pause", map[string]any{"orderId": orderID}))
	assertExactKeys(t, "order.pause", paused, "orderId", "status")
	if paused["status"] != model.ChangeOrderStatusPaused {
		t.Fatalf("暂停后状态应为 paused，实际 %v", paused["status"])
	}
	order := f.orderRow(t, orderID)
	if order.PauseKind != model.PauseKindManual {
		t.Fatalf("人工暂停应记 pauseKind=manual，实际 %q", order.PauseKind)
	}

	cancelled := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.cancel", map[string]any{
		"orderId": orderID, "reason": "大厅崩溃率飙升",
	}))
	assertExactKeys(t, "order.cancel", cancelled, "orderId", "status")
	if cancelled["status"] != model.ChangeOrderStatusCancelled {
		t.Fatalf("终止后状态应为 cancelled，实际 %v", cancelled["status"])
	}
	order = f.orderRow(t, orderID)
	if order.CancelReason != "大厅崩溃率飙升" {
		t.Fatalf("终止原因应落库，实际 %q", order.CancelReason)
	}
	for _, action := range []string{model.ActionDeliveryOrderPause, model.ActionDeliveryOrderCancel} {
		ips := f.auditClientIPs(t, action)
		if len(ips) != 1 || ips[0] != "mcp" {
			t.Fatalf("止损审计 %s 来源地址应为 mcp，实际 %v", action, ips)
		}
	}
}

// ── FR-248 拒绝文案（真实服务 + 真实拒绝路径） ──

// TestMCPDeliveryWriteToolsRejectWithStableReasons 逐工具走真实拒绝路径，断言返回的是
// §3.5 表中的稳定短语（而不是既有的硬编码文案）。
func TestMCPDeliveryWriteToolsRejectWithStableReasons(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	server := f.server(t)
	// draft 单（无模板源）：差异扫描应报「未指定黄金模板源」。
	draftNoSource := f.seedOrder(t, f.nsID, "无源草稿", model.ChangeOrderStatusDraft, "")
	rolling := f.seedOrder(t, f.nsID, "进行中", model.ChangeOrderStatusRolling, "")

	cases := []struct {
		name   string
		tool   string
		args   map[string]any
		reason string
	}{
		{
			name: "建单缺落地环境", tool: "beacon.delivery.order.create",
			args:   map[string]any{"title": "缺环境", "selector": map[string]any{"all": true}},
			reason: mcpDeliveryNamespaceRejectedReason,
		},
		{
			name: "建单缺标题", tool: "beacon.delivery.order.create",
			args: map[string]any{
				"namespaceId": strconv.FormatUint(uint64(f.nsID), 10), "title": "",
				"selector": map[string]any{"all": true},
			},
			// 未列入映射表的领域错误沿用其自带中文说明（「其他」行：不得回空文案）。
			reason: "namespaceId / title 必填",
		},
		{
			name: "编辑进行中的单", tool: "beacon.delivery.order.update",
			args:   map[string]any{"orderId": rolling, "title": "改标题"},
			reason: "当前状态不允许该操作：当前状态 rolling 不允许 编辑",
		},
		{
			name: "无源草稿扫差异", tool: "beacon.delivery.order.diff-scan",
			args:   map[string]any{"orderId": draftNoSource},
			reason: apperr.ErrChangeSourceMissing.Message,
		},
		{
			name: "暂停未启动的单", tool: "beacon.delivery.order.pause",
			args:   map[string]any{"orderId": draftNoSource},
			reason: "当前状态不允许该操作：当前状态 draft 不允许 暂停",
		},
		{
			name: "终止未启动的单", tool: "beacon.delivery.order.cancel",
			args:   map[string]any{"orderId": draftNoSource, "reason": "停"},
			reason: "当前状态不允许该操作：当前状态 draft 不允许 紧急终止",
		},
		{
			name: "提交缺原因", tool: "beacon.delivery.order.submit",
			args:   map[string]any{"orderId": draftNoSource, "reason": "   ", "idempotencyKey": "mcp-write-reason"},
			reason: mcpDeliveryRejectedReasons[apperr.ErrApprovalReasonRequired.Code],
		},
		{
			name: "删除缺原因", tool: "beacon.delivery.order.delete",
			args:   map[string]any{"orderId": draftNoSource, "reason": "  ", "idempotencyKey": "mcp-write-del-reason"},
			reason: mcpDeliveryRejectedReasons[apperr.ErrApprovalReasonRequired.Code],
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := mustCallMCPTool(t, server, tc.tool, tc.args)
			if !res.IsError {
				t.Fatalf("%s 应被拒: %+v", tc.tool, res)
			}
			if !strings.Contains(mcpResultText(res), tc.reason) {
				t.Fatalf("%s 的拒绝文案应含 %q，实际 %q", tc.tool, tc.reason, mcpResultText(res))
			}
		})
	}
}

// TestMCPDeliveryOrderRejectsInvalidResumeModeAndBlankReason 覆盖两个 MCP 面前置校验：
// resume 的 mode 必须在枚举内；cancel 的 reason 必填（与 HTTP 面一致）。
func TestMCPDeliveryOrderRejectsInvalidResumeModeAndBlankReason(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	paused := f.seedOrder(t, f.nsID, "已暂停", model.ChangeOrderStatusPaused, "")
	server := f.server(t)

	for _, mode := range []string{"", "retry-all"} {
		res := mustCallMCPTool(t, server, "beacon.delivery.order.resume", map[string]any{
			"orderId": paused, "mode": mode, "reason": "继续", "idempotencyKey": "mcp-resume-mode",
		})
		if !res.IsError || !strings.Contains(mcpResultText(res), mcpDeliveryResumeModeRejectedReason) {
			t.Fatalf("mode=%q 应被枚举校验拒绝，实际 %q", mode, mcpResultText(res))
		}
	}
	res := mustCallMCPTool(t, server, "beacon.delivery.order.cancel", map[string]any{"orderId": paused, "reason": "   "})
	if !res.IsError || !strings.Contains(mcpResultText(res), mcpDeliveryReasonRequiredRejectedReason) {
		t.Fatalf("终止缺原因应被拒，实际 %q", mcpResultText(res))
	}
}

// TestMCPDeliveryResumeModeIsDescribedInSchema 断言 resume 工具的输入 schema 里
// mode 字段带描述且描述列出两个合法取值（AI 侧可见的枚举说明）。
func TestMCPDeliveryResumeModeIsDescribedInSchema(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	schema := mcpToolInputSchemaJSON(t, f.registry, f.principal, "beacon.delivery.order.resume")
	for _, mode := range []string{mcpDeliveryResumeModeRetryFailed, mcpDeliveryResumeModeSkipFailed} {
		if !strings.Contains(schema, mode) {
			t.Fatalf("resume 的输入 schema 应列出 mode 取值 %s，实际 %s", mode, schema)
		}
	}
}

// TestMCPDeliveryApprovalToolsLeaveBusinessTablesUntouched 是「只建申请」的零副作用闸与幂等闸：
// 交付申请类工具只创建审批申请——业务表（单 / 目标 / 批次 / 执行回执）逐表不变，
// 同 idempotencyKey 重放返回同一申请且只落一行。
func TestMCPDeliveryApprovalToolsLeaveBusinessTablesUntouched(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	orderID := f.seedOrder(t, f.nsID, "待删除草稿", model.ChangeOrderStatusDraft, "")
	rollbackOrder := f.seedOrder(t, f.nsID, "回滚中", model.ChangeOrderStatusRollingBack, "")
	server := f.server(t)

	before := deliveryBusinessRowCounts(t, f.db)
	args := map[string]any{"orderId": orderID, "reason": "清理草稿", "idempotencyKey": "mcp-write-idem"}
	first := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.delete", args))
	second := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.delete", args))
	if first["approvalRequestId"] != second["approvalRequestId"] {
		t.Fatalf("同幂等键重放应返回同一申请：%v vs %v", first["approvalRequestId"], second["approvalRequestId"])
	}
	// 结束回滚同样是「只建申请」：回滚中的单在申请阶段不得被改成终态。
	finishOut := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.rollback.finish", map[string]any{
		"orderId": rollbackOrder, "idempotencyKey": "mcp-write-finish",
	}))
	if finishOut["orderId"] != float64(rollbackOrder) {
		t.Fatalf("结束回滚票据应带归属单号，实际 %v", finishOut)
	}
	if got := f.orderRow(t, rollbackOrder).Status; got != model.ChangeOrderStatusRollingBack {
		t.Fatalf("结束回滚申请阶段状态应保持 rolling_back，实际 %s", got)
	}

	// 业务表逐表零副作用（审批表与审计表不在其中：建申请本就该落它们）。
	after := deliveryBusinessRowCounts(t, f.db)
	for _, table := range []string{"change_order", "change_order_item", "change_batch", "change_target"} {
		if before[table] != after[table] {
			t.Fatalf("只建申请的工具不应改动业务表 %s：前 %d 行、后 %d 行", table, before[table], after[table])
		}
	}
	if got := f.orderRow(t, orderID).Status; got != model.ChangeOrderStatusDraft {
		t.Fatalf("草稿删除申请阶段单应仍为 draft，实际 %s", got)
	}
	var receipts int64
	if err := f.db.Model(&model.ApprovalExecutionReceipt{}).Count(&receipts).Error; err != nil {
		t.Fatalf("统计执行回执失败: %v", err)
	}
	if receipts != 0 {
		t.Fatalf("申请类工具不得产生执行回执，实际 %d 行", receipts)
	}
	// 同幂等键只落一行申请。
	var approvals int64
	if err := f.db.Model(&model.ApprovalRequest{}).Count(&approvals).Error; err != nil {
		t.Fatalf("统计审批申请失败: %v", err)
	}
	if approvals != 2 {
		t.Fatalf("两次调用（删除 + 结束回滚）应各落一行申请，实际 %d 行", approvals)
	}
}

// deliveryBusinessRowCounts 统计业务表行数（零副作用断言的基线）。
func deliveryBusinessRowCounts(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, 4)
	for _, model0 := range []any{&model.ChangeOrder{}, &model.ChangeOrderItem{}, &model.ChangeBatch{}, &model.ChangeTarget{}} {
		var count int64
		if err := db.Model(model0).Count(&count).Error; err != nil {
			t.Fatalf("统计行数失败: %v", err)
		}
		var table string
		switch model0.(type) {
		case *model.ChangeOrder:
			table = "change_order"
		case *model.ChangeOrderItem:
			table = "change_order_item"
		case *model.ChangeBatch:
			table = "change_batch"
		default:
			table = "change_target"
		}
		counts[table] = count
	}
	return counts
}

// TestMCPDeliveryWritesRejectCrossNamespaceAndMissingOrder 是观测范围闸：
// 以 prod env 收窄范围操作系统中的 dev 单，四个按 orderId 定位的工具一律拒绝；
// 且范围外与不存在**共用同一条**文案（不泄露范围外单是否存在）。
func TestMCPDeliveryWritesRejectCrossNamespaceAndMissingOrder(t *testing.T) {
	f := newDeliveryWriteMCPFixture(t)
	devOrder := f.seedOrder(t, f.devNSID, "dev 单", model.ChangeOrderStatusRolling, "")
	server := f.server(t)
	envScope := strconv.FormatUint(uint64(f.envID), 10)
	// 跨 namespace 建单同样被拒（收窄范围不含 dev）。
	createRes := mustCallMCPTool(t, server, "beacon.delivery.order.create", map[string]any{
		"namespaceId": strconv.FormatUint(uint64(f.devNSID), 10), "envId": envScope,
		"title": "跨环境建单", "selector": map[string]any{"all": true},
	})
	if !createRes.IsError {
		t.Fatalf("跨 namespace 建单应被拒: %+v", createRes)
	}

	for _, tool := range []string{
		"beacon.delivery.order.update", "beacon.delivery.order.diff-scan",
		"beacon.delivery.order.pause", "beacon.delivery.order.cancel",
	} {
		// 每个工具的入参各自成契约：只有 cancel 收 reason（多传即被 schema 拒）。
		args := map[string]any{"orderId": devOrder, "envId": envScope}
		if tool == "beacon.delivery.order.cancel" {
			args["reason"] = "止损"
		}
		outside := mustCallMCPTool(t, server, tool, args)
		if !outside.IsError {
			t.Fatalf("%s 以 prod 范围操作 dev 单应被拒: %+v", tool, outside)
		}
		args["orderId"] = devOrder + 1000
		missing := mustCallMCPTool(t, server, tool, args)
		if !missing.IsError {
			t.Fatalf("%s 操作不存在的单应被拒: %+v", tool, missing)
		}
		if mcpResultText(outside) != mcpResultText(missing) {
			t.Fatalf("%s 范围外与不存在应共用同一条拒绝文案: %q vs %q",
				tool, mcpResultText(outside), mcpResultText(missing))
		}
		if !strings.Contains(mcpResultText(outside), mcpDeliveryOutOfScopeRejectedReason) {
			t.Fatalf("%s 的拒绝文案应为 %q，实际 %q", tool, mcpDeliveryOutOfScopeRejectedReason, mcpResultText(outside))
		}
		// 反向：范围外拒绝不是「一律拒绝」——同工具在范围内可正常执行。
		if tool != "beacon.delivery.order.update" {
			continue
		}
		inScope := mustCallMCPTool(t, server, tool, map[string]any{
			"orderId": f.seedOrder(t, f.nsID, "prod 单", model.ChangeOrderStatusDraft, ""),
			"envId":   envScope, "title": "范围内改名",
		})
		if inScope.IsError {
			t.Fatalf("%s 在范围内应可执行: %+v", tool, inScope)
		}
	}
}

// mcpToolInputSchemaJSON 取某工具的输入 schema JSON 文本（断言字段描述用）。
func mcpToolInputSchemaJSON(t *testing.T, registry *MCPToolRegistry, principal auth.Principal, name string) string {
	t.Helper()
	ctx := context.Background()
	server := registry.NewMCPServer(principal)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("服务端连接失败: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "schema-test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("客户端连接失败: %v", err)
	}
	defer func() { _ = session.Close() }()

	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("枚举工具失败: %v", err)
		}
		if tool.Name != name {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("序列化输入 schema 失败: %v", err)
		}
		return string(raw)
	}
	t.Fatalf("未在注册集合中找到工具 %s", name)
	return ""
}
