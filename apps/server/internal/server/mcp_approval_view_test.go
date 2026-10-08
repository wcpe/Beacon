package server

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// ── §3.6 A：票据投影（纯函数层） ──

// TestMCPDeliveryTicketViewAddsOrderIDAndImpactSummary 守住票据投影的两键与键名现状：
// MCP 侧既有键仍是 operation（HTTP 面为 operationKey），本项只加 orderId 与 impactSummary；
// impactSummary 为对象且计数为 0 时保留 0（不省略），便于 AI 稳定解析。
func TestMCPDeliveryTicketViewAddsOrderIDAndImpactSummary(t *testing.T) {
	ticket := service.DeliveryApprovalTicketView{
		ApprovalRequestID: "apr_ticket_1", Status: model.ApprovalStatusPending,
		OperationKey: authz.OperationDeliveryApprove, OrderID: 87,
		ImpactSummary: service.DeliveryImpactSummaryView{TargetCount: 20, BatchCount: 3, PayloadFiles: 3},
	}
	view := mcpDeliveryTicketView(ticket)
	for key, want := range map[string]any{
		"approvalRequestId": "apr_ticket_1", "status": model.ApprovalStatusPending,
		"operation": authz.OperationDeliveryApprove, "orderId": uint(87),
	} {
		if view[key] != want {
			t.Fatalf("票据键 %s 应为 %v，实际 %v", key, want, view[key])
		}
	}
	if _, ok := view["operationKey"]; ok {
		t.Fatalf("MCP 侧键名应保持 operation，不得改成 HTTP 面的 operationKey: %v", view)
	}
	summary, ok := view["impactSummary"].(service.DeliveryImpactSummaryView)
	if !ok {
		t.Fatalf("impactSummary 应为影响摘要对象，实际 %T", view["impactSummary"])
	}
	// payloadConfigs 未设置 → 0 保留而不省略。
	if summary != ticket.ImpactSummary || summary.PayloadConfigs != 0 {
		t.Fatalf("影响摘要应原样带出且零值不省略，实际 %+v", summary)
	}
}

// ── §3.6 A：真服务 + 真 MCP 协议路径 ──

// mcpTicketSettings 是交付域所需的最小设置源（审批人分离与本用例无关）。
type mcpTicketSettings struct{}

func (mcpTicketSettings) GetBool(string) bool { return false }

// mcpDeliveryApprovalFixture 装好交付域与审批域的真服务，供经 MCP 协议路径的票据与视图断言复用。
type mcpDeliveryApprovalFixture struct {
	db       *gorm.DB
	orders   *service.DeliveryOrderService
	approval *service.ApprovalService
	registry *MCPToolRegistry
	nsID     uint
}

// newMCPDeliveryApprovalFixture 起独立内存 sqlite（夹具集群：模板源 src-1 + 目标 t-1 / t-2）。
func newMCPDeliveryApprovalFixture(t *testing.T) *mcpDeliveryApprovalFixture {
	t.Helper()
	dsn := "file:mcp_ticket_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
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
	if err := db.AutoMigrate(&model.Namespace{}, &model.BCCluster{}, &model.Region{}, &model.Zone{},
		&model.Server{}, &model.ServerTag{}, &model.AgentIdentity{}, &model.ChangeOrder{},
		&model.ChangeOrderItem{}, &model.ChangeBatch{}, &model.ChangeTarget{}, &model.DeliveryBlob{},
		&model.DeliveryConfigArtifact{}, &model.FileAsset{}, &model.FileAssetScan{}, &model.AgentCommand{},
		&model.Setting{}, &model.AuditLog{}, &model.ConfigFile{}, &model.ConfigLayerVersion{},
		&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移交付与审批表失败: %v", err)
	}
	ns := &model.Namespace{Code: "prod", Name: "生产", Lifecycle: "active"}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("写入 namespace 失败: %v", err)
	}
	cluster := &model.BCCluster{NamespaceID: ns.ID, Name: "bc-1"}
	if err := db.Create(cluster).Error; err != nil {
		t.Fatalf("写入 BC 集群失败: %v", err)
	}
	region := &model.Region{BCClusterID: cluster.ID, Name: "region-1"}
	if err := db.Create(region).Error; err != nil {
		t.Fatalf("写入大区失败: %v", err)
	}
	zone1, zone2 := &model.Zone{RegionID: region.ID, Name: "zone-1"}, &model.Zone{RegionID: region.ID, Name: "zone-2"}
	for _, zone := range []*model.Zone{zone1, zone2} {
		if err := db.Create(zone).Error; err != nil {
			t.Fatalf("写入小区失败: %v", err)
		}
	}
	health := healthview.NewStore()
	views := make([]healthview.View, 0, 3)
	for _, seed := range []struct {
		serverID string
		zoneID   uint
	}{{"src-1", zone1.ID}, {"t-1", zone1.ID}, {"t-2", zone2.ID}} {
		if err := db.Create(&model.Server{NamespaceID: ns.ID, ServerID: seed.serverID,
			Kind: model.ServerKindBackend, ZoneID: &seed.zoneID}).Error; err != nil {
			t.Fatalf("写入 server %s 失败: %v", seed.serverID, err)
		}
		if err := db.Create(&model.AgentIdentity{IdentityID: "idn-" + seed.serverID, NamespaceID: ns.ID,
			ServerID: model.NullableServerID(seed.serverID), Kind: model.ServerKindBackend,
			Status: model.AgentIdentityStatusActive, StatusChangedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatalf("写入身份 %s 失败: %v", seed.serverID, err)
		}
		views = append(views, healthview.View{NamespaceID: ns.ID, ServerID: seed.serverID,
			Kind: model.ServerKindBackend, Score: 90, Level: healthview.LevelHealthy})
	}
	health.ReplaceAll(views)

	orders := service.NewDeliveryOrderService(db, repository.NewChangeOrderRepository(db),
		repository.NewConfigLayerVersionRepository(db), repository.NewAuditLogRepository(db), mcpTicketSettings{}, health)
	// 审批适配器只需登记交付审批操作的描述即可（本用例从不执行领域动作）。
	approvalRegistry := authz.NewApprovalRegistry()
	approvalRegistry.Register(authz.OperationDeliveryApprove,
		authz.TransactionalAdapterFunc(func(_ *gorm.DB, _ authz.ApprovalRequest, _ authz.Permit) (func(), error) { return nil, nil }))
	approval := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db),
		repository.NewAuditLogRepository(db), approvalRegistry)
	orders.SetApprovalService(approval)
	registry := &MCPToolRegistry{approvals: approval}
	registry.SetDeliveryOrderService(orders)
	return &mcpDeliveryApprovalFixture{db: db, orders: orders, approval: approval, registry: registry, nsID: ns.ID}
}

// createDraft 建一张含一条文件项与一条配置项的最小 draft 单。
func (f *mcpDeliveryApprovalFixture) createDraft(t *testing.T) uint {
	t.Helper()
	title, source, scanDir := "发布大厅插件", "src-1", "plugins/"
	detail, err := f.orders.Create(f.nsID, service.ChangeOrderInput{
		Title: &title, SourceServerID: &source, ScanDir: &scanDir,
		Selector: &service.ChangeSelector{Servers: []string{"t-1", "t-2"}},
	}, "ops-chen", "192.0.2.10")
	if err != nil {
		t.Fatalf("创建 draft 单失败: %v", err)
	}
	path, action, sha, size := "plugins/demo.jar", model.ChangeItemActionAdd, strings.Repeat("ab", 32), int64(64)
	if err := f.db.Create(&model.ChangeOrderItem{OrderID: detail.ID, Kind: model.ChangeItemKindFileDiff,
		Path: &path, Action: &action, SHA256: &sha, SizeBytes: &size}).Error; err != nil {
		t.Fatalf("写入文件项失败: %v", err)
	}
	scopeKind, scopeID, toVersion := model.ConfigScopeZone, uint(1), uint(1)
	if err := f.db.Create(&model.ChangeOrderItem{OrderID: detail.ID, Kind: model.ChangeItemKindConfigChange,
		ConfigScopeKind: &scopeKind, ConfigScopeID: &scopeID, ConfigToVersionID: &toVersion}).Error; err != nil {
		t.Fatalf("写入配置项失败: %v", err)
	}
	return detail.ID
}

// TestMCPDeliverySubmitTicketCarriesOrderIDAndImpactSummary 经真实 MCP 协议路径调
// beacon.delivery.order.submit：机器主体在结构化输出里必须看得到 orderId 与 impactSummary，
// 否则 AI 提交后无从判断"申请的是哪张单、影响面多大"。
func TestMCPDeliverySubmitTicketCarriesOrderIDAndImpactSummary(t *testing.T) {
	f := newMCPDeliveryApprovalFixture(t)
	orderID := f.createDraft(t)
	principal := auth.MCPPrincipal("client-fr249-a", "交付客户端", model.MCPClientProfileAutomation)
	server := f.registry.NewMCPServer(principal)

	out := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.delivery.order.submit", map[string]any{
		"orderId": orderID, "reason": "提交审批", "idempotencyKey": "mcp-ticket-submit",
	}))
	if out["approvalRequestId"] == nil || out["approvalRequestId"] == "" {
		t.Fatalf("票据应带 approvalRequestId: %v", out)
	}
	if out["orderId"] != float64(orderID) {
		t.Fatalf("MCP 票据 orderId 应为 %d，实际 %v", orderID, out["orderId"])
	}
	if out["operation"] != authz.OperationDeliveryApprove {
		t.Fatalf("MCP 票据既有键 operation 应保持 delivery.approve，实际 %v", out["operation"])
	}
	summary, ok := out["impactSummary"].(map[string]any)
	if !ok {
		t.Fatalf("impactSummary 应为对象: %v", out["impactSummary"])
	}
	// 提交时刻目标与批次尚未固化 → 两项计数为 0（保留 0、不省略）；载荷按变更项种类分计。
	want := map[string]any{"targetCount": float64(0), "batchCount": float64(0), "payloadFiles": float64(1), "payloadConfigs": float64(1)}
	for key, value := range want {
		if summary[key] != value {
			t.Fatalf("impactSummary.%s 应为 %v，实际 %v（%v）", key, value, summary[key], summary)
		}
	}
}

// ── §3.6 B：审批视图两档与机器主体隔离 ──

// seedRequest 直插一条审批行（绕开服务默认值，便于构造带失败原因与决定时间线的存量行）。
// 幂等键由 requestID 派生：同一主体可播种多条而互不撞 (主体, operation, 幂等键) 唯一约束。
// 冻结载荷哈希按「payload 原文的 sha256」填真值——撤回 / 拒绝的终态回调会校验它，缺了会被拒。
func (f *mcpDeliveryApprovalFixture) seedRequest(t *testing.T, requestID, requesterID string) {
	t.Helper()
	payload := `{"orderId":87}`
	sum := sha256.Sum256([]byte(payload))
	if err := f.db.Create(&model.ApprovalRequest{
		RequestID: requestID, OperationKey: authz.OperationDeliveryApprove, OperationKind: authz.OperationDeliveryApprove,
		SchemaVersion: 1, RequiredCapability: auth.CapabilityApprovalRequest, RiskLevel: "high",
		ResourceType: model.TargetTypeChangeOrder, ResourceID: "87", Payload: payload,
		FrozenPayloadSHA256: hex.EncodeToString(sum[:]), IdempotencyKey: "seed-" + requestID,
		Status: model.ApprovalStatusPending, RequesterType: auth.PrincipalKindMCP, RequesterID: requesterID,
		RequestedBy: "mcp:" + requesterID, Version: 1,
	}).Error; err != nil {
		t.Fatalf("写入审批行失败: %v", err)
	}
}

// TestMCPApprovalViewSplitsLightweightAndFullProjection 断言 own.list 与 own.get 的字段档位差异：
// 列表只带截断后的失败摘要与结束时间；详情另给审批理由、审批主体与执行时间线（有值即返）；
// 两侧都不投影审批申请的 text 列 impactSummary / safeSummary（避免与票据对象键同名两型）。
func TestMCPApprovalViewSplitsLightweightAndFullProjection(t *testing.T) {
	f := newMCPDeliveryApprovalFixture(t)
	f.seedRequest(t, "apr_fr249_view", "client-fr249-view")
	longSummary := strings.Repeat("失败摘要", 80) // 240 字符，超过列表档上限
	approvedBy := "human:admin"
	decidedAt, approvedAt := time.Date(2026, 7, 16, 8, 1, 0, 0, time.UTC), time.Date(2026, 7, 16, 8, 2, 0, 0, time.UTC)
	executedAt, finishedAt := time.Date(2026, 7, 16, 8, 3, 0, 0, time.UTC), time.Date(2026, 7, 16, 8, 4, 0, 0, time.UTC)
	if err := f.db.Model(&model.ApprovalRequest{}).Where("request_id = ?", "apr_fr249_view").Updates(map[string]any{
		"failure_summary": longSummary, "reject_reason": "风险过高", "decision_reason": "影响面过大",
		"approved_by": approvedBy, "decided_at": decidedAt, "approved_at": approvedAt,
		"executed_at": executedAt, "finished_at": finishedAt,
		// text 列：两档都不得投影（同名两型）。
		"impact_summary": "审批快照影响摘要文本", "safe_summary": "安全摘要文本",
	}).Error; err != nil {
		t.Fatalf("补齐审批时间线失败: %v", err)
	}
	server := f.registry.NewMCPServer(auth.MCPPrincipal("client-fr249-view", "视图客户端", model.MCPClientProfileAutomation))

	list := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.approvals.own.list", map[string]any{}))
	rows := mcpItems(t, list)
	if len(rows) != 1 {
		t.Fatalf("列表应回 1 条自己的申请，实际 %d 条: %v", len(rows), rows)
	}
	row := rows[0]
	if got := row["failureSummary"].(string); len([]rune(got)) != mcpApprovalFailureSummaryMaxRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("列表失败摘要应截断到 %d 字符并加省略号，实际 %d 字符", mcpApprovalFailureSummaryMaxRunes, len([]rune(got)))
	}
	if row["finishedAt"] != finishedAt.Format(time.RFC3339) {
		t.Fatalf("列表应带 finishedAt，实际 %v", row["finishedAt"])
	}
	for _, key := range []string{"rejectReason", "decisionReason", "approvedBy", "decidedAt", "approvedAt", "executedAt"} {
		if _, ok := row[key]; ok {
			t.Fatalf("列表档不得带详情字段 %s: %v", key, row)
		}
	}
	for _, forbidden := range []string{"impactSummary", "safeSummary"} {
		if _, ok := row[forbidden]; ok {
			t.Fatalf("审批投影不得透出 text 列 %s（与票据对象键同名两型）: %v", forbidden, row)
		}
	}

	detail := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.approvals.own.get", map[string]any{"requestId": "apr_fr249_view"}))
	for key, want := range map[string]any{
		"rejectReason": "风险过高", "decisionReason": "影响面过大", "approvedBy": approvedBy,
		"decidedAt": decidedAt.Format(time.RFC3339), "approvedAt": approvedAt.Format(time.RFC3339),
		"executedAt": executedAt.Format(time.RFC3339), "finishedAt": finishedAt.Format(time.RFC3339),
	} {
		if detail[key] != want {
			t.Fatalf("详情字段 %s 应为 %v，实际 %v", key, want, detail[key])
		}
	}
	if detail["failureSummary"] != longSummary {
		t.Fatalf("详情失败摘要应为完整文本，实际 %v 字符", len([]rune(detail["failureSummary"].(string))))
	}
	for _, forbidden := range []string{"impactSummary", "safeSummary"} {
		if _, ok := detail[forbidden]; ok {
			t.Fatalf("详情投影不得透出 text 列 %s: %v", forbidden, detail)
		}
	}
}

// TestMCPOwnApprovalViewsStayIsolatedPerMachinePrincipal 是负向用例：
// 投影扩字段不得放宽主体隔离——机器主体只能看到自己创建的申请，
// 他人的申请在 own.list 不可见、own.get 一律拒绝（与"不存在"同形，不泄露目标是否存在）。
func TestMCPOwnApprovalViewsStayIsolatedPerMachinePrincipal(t *testing.T) {
	f := newMCPDeliveryApprovalFixture(t)
	f.seedRequest(t, "apr_fr249_own", "client-fr249-owner")
	f.seedRequest(t, "apr_fr249_other", "client-fr249-other")
	server := f.registry.NewMCPServer(auth.MCPPrincipal("client-fr249-owner", "本人客户端", model.MCPClientProfileAutomation))

	list := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.approvals.own.list", map[string]any{}))
	rows := mcpItems(t, list)
	if len(rows) != 1 || rows[0]["approvalRequestId"] != "apr_fr249_own" {
		t.Fatalf("机器主体只应看到自己的申请，实际 %v", rows)
	}
	if _, err := callMCPTool(t, server, "beacon.approvals.own.get", map[string]any{"requestId": "apr_fr249_own"}); err != nil {
		t.Fatalf("读取自己的申请不应失败: %v", err)
	}
	res, err := callMCPTool(t, server, "beacon.approvals.own.get", map[string]any{"requestId": "apr_fr249_other"})
	if err == nil && !res.IsError {
		t.Fatalf("读取他人申请应被拒绝，实际返回: %+v", res)
	}
}

// ── §3.6 B：写工具沿用轻量档 ──

// assertLightweightApprovalResponse 断言某工具的响应用的是轻量档：
// 字段集与列表档逐键一致，且不含详情档独有键——审批理由、审批主体与执行时间线只由 own.get 给出。
func assertLightweightApprovalResponse(t *testing.T, tool string, out map[string]any) {
	t.Helper()
	want := []string{"approvalRequestId", "status", "operation", "resultRef", "createdAt", "expiresAt", "failureSummary", "finishedAt"}
	if len(out) != len(want) {
		t.Fatalf("%s 响应用轻量档，字段数应为 %d，实际 %d: %v", tool, len(want), len(out), out)
	}
	for _, key := range want {
		if _, ok := out[key]; !ok {
			t.Fatalf("%s 响应缺轻量档字段 %s: %v", tool, key, out)
		}
	}
	for _, key := range []string{"rejectReason", "decisionReason", "approvedBy", "decidedAt", "approvedAt", "executedAt", "impactSummary", "safeSummary"} {
		if _, ok := out[key]; ok {
			t.Fatalf("%s 响应混入详情档字段 %s: %v", tool, key, out)
		}
	}
}

// TestMCPApprovalWriteToolsReuseLightweightView 锁定 own.withdraw / approve / reject 三个写工具
// 沿用轻量档：响应字段集与 own.list 行一致、不含详情档独有键。
//
// 三者都回"刚操作完的那条申请"，若其中任一偷偷换成全量档，AI 会在一次写调用里拿到
// 本该按需拉取的审批理由与时间线——档位划分就在写路径上破了口。
func TestMCPApprovalWriteToolsReuseLightweightView(t *testing.T) {
	// 审批决定工具仅当显式开启 mcp.allow-approval-decide 时才注册（与内网闭环部署一致），用后复位。
	t.Cleanup(func() { auth.SetMCPApprovalDecide(false) })
	auth.SetMCPApprovalDecide(true)

	f := newMCPDeliveryApprovalFixture(t)
	// 主体须在开关开启后构造，能力集合才含 approval.decide。
	principal := auth.MCPPrincipal("client-fr249-write", "写工具客户端", model.MCPClientProfileAutomation)
	server := f.registry.NewMCPServer(principal)
	f.seedRequest(t, "apr_fr249_withdraw", "client-fr249-write")
	f.seedRequest(t, "apr_fr249_approve", "client-fr249-write")
	f.seedRequest(t, "apr_fr249_reject", "client-fr249-write")

	cases := []struct {
		tool   string
		args   map[string]any
		status string
	}{
		{"beacon.approvals.own.withdraw", map[string]any{"requestId": "apr_fr249_withdraw"}, model.ApprovalStatusWithdrawn},
		{"beacon.approvals.approve", map[string]any{"requestId": "apr_fr249_approve"}, model.ApprovalStatusExecuting},
		{"beacon.approvals.reject", map[string]any{"requestId": "apr_fr249_reject", "reason": "风险过高"}, model.ApprovalStatusRejected},
	}
	for _, tc := range cases {
		out := mcpStructuredMap(t, mustCallMCPTool(t, server, tc.tool, tc.args))
		// 先确认工具真的做了事（状态已迁移），再判档位——否则"空响应"会被误判成轻量档。
		if out["status"] != tc.status {
			t.Fatalf("%s 后状态应为 %s，实际 %v（%v）", tc.tool, tc.status, out["status"], out)
		}
		assertLightweightApprovalResponse(t, tc.tool, out)
	}

	// 与 own.list 行逐键比对：写工具响应就是列表档的那一份投影。
	rows := mcpItems(t, mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.approvals.own.list", map[string]any{})))
	if len(rows) != len(cases) {
		t.Fatalf("列表应回 %d 条自己的申请，实际 %d 条: %v", len(cases), len(rows), rows)
	}
	for _, row := range rows {
		assertLightweightApprovalResponse(t, "beacon.approvals.own.list 行", row)
	}
}

// ── §3.6 B：档位划分（纯函数层，逐键锁定） ──

// TestMCPApprovalViewTiersKeepFieldSets 逐键锁定轻量档与全量档的字段集合，
// 防止后续"顺手"把详情字段塞进列表（列表一次回多行，字段越多越容易撑大响应体）。
func TestMCPApprovalViewTiersKeepFieldSets(t *testing.T) {
	approvedBy := "human:admin"
	req := &model.ApprovalRequest{
		RequestID: "apr_tiers", Status: model.ApprovalStatusFailed, OperationKey: authz.OperationDeliveryApprove,
		ResultRef: "change-order-87", FailureSummary: "执行失败：目标不在线", RejectReason: "风险过高",
		DecisionReason: "影响面过大", ApprovedBy: &approvedBy,
	}
	light := mcpApprovalViewLightweight(req)
	wantLight := []string{"approvalRequestId", "status", "operation", "resultRef", "createdAt", "expiresAt", "failureSummary", "finishedAt"}
	if len(light) != len(wantLight) {
		t.Fatalf("轻量档字段数应为 %d，实际 %d: %v", len(wantLight), len(light), light)
	}
	for _, key := range wantLight {
		if _, ok := light[key]; !ok {
			t.Fatalf("轻量档缺字段 %s: %v", key, light)
		}
	}
	full := mcpApprovalViewFull(req)
	wantFull := append(append([]string{}, wantLight...),
		"rejectReason", "decisionReason", "approvedBy", "decidedAt", "approvedAt", "executedAt")
	if len(full) != len(wantFull) {
		t.Fatalf("全量档字段数应为 %d，实际 %d: %v", len(wantFull), len(full), full)
	}
	for _, key := range wantFull {
		if _, ok := full[key]; !ok {
			t.Fatalf("全量档缺字段 %s: %v", key, full)
		}
	}
	// 空时间与空主体回空串（有值即返、不组装数组，也不落 null 让 AI 二次判空）。
	for _, key := range []string{"decidedAt", "approvedAt", "executedAt", "finishedAt"} {
		if full[key] != "" {
			t.Fatalf("未落库的时间字段 %s 应回空串，实际 %v", key, full[key])
		}
	}
	if full["approvedBy"] != approvedBy || full["failureSummary"] != req.FailureSummary {
		t.Fatalf("全量档应原样带出审批主体与完整失败摘要: %v", full)
	}
	if summary, ok := light["failureSummary"].(string); !ok || summary != req.FailureSummary {
		t.Fatalf("轻量档也应带失败摘要（未超上限即原样）: %v", light)
	}
}
