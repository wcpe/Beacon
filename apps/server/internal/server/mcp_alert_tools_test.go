package server

import (
	"encoding/json"
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
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// alertMCPTestDB 打开内存 sqlite 并迁移告警相关表，供 MCP 告警工具的行为测试
// （不依赖 MySQL / DSN；与仓储层单测同手法，DSN 按测试名唯一以免共享内存库串扰）。
func alertMCPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Env{}, &model.EnvNamespace{}, &model.AlertEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移告警相关表失败: %v", err)
	}
	return db
}

// alertMCPFixture 是一个接好真实告警服务的 MCP 注册表：
// prod / dev 两个 namespace，env 只映射 prod —— 便于验证「观察范围外不可见、也不可批量改」。
type alertMCPFixture struct {
	db        *gorm.DB
	repo      *repository.AlertEventRepository
	audits    *repository.AuditLogRepository
	registry  *MCPToolRegistry
	principal auth.Principal
	prodCode  string
	devCode   string
	envID     uint
}

func newAlertMCPFixture(t *testing.T) *alertMCPFixture {
	t.Helper()
	db := alertMCPTestDB(t)

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

	repo := repository.NewAlertEventRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	svc := service.NewAlertEventService(db, repo, auditRepo)
	registry := &MCPToolRegistry{
		approvals: &service.ApprovalService{},
		alerts:    svc,
		reads: MCPReadServices{
			alertEvents: svc,
			scope:       service.NewObservationScopeResolver(repository.NewEnvRepository(db), repository.NewNamespaceRepository(db)),
		},
	}
	return &alertMCPFixture{
		db: db, repo: repo, audits: auditRepo, registry: registry,
		principal: auth.MCPPrincipal("alert-test", "告警工具测试", model.MCPClientProfileAutomation),
		prodCode:  prod.Code, devCode: dev.Code, envID: env.ID,
	}
}

// server 按 automation profile 构造一次 MCP server（写工具与只读工具同在该 profile 下注册）。
func (f *alertMCPFixture) server(t *testing.T) *mcp.Server {
	t.Helper()
	return f.registry.NewMCPServer(f.principal)
}

// seed 直插一条告警事件（绕开 service 默认值，便于构造指定状态的存量行）。
func (f *alertMCPFixture) seed(t *testing.T, ns, serverID, level, status string, at time.Time) model.AlertEvent {
	t.Helper()
	event := model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: level, Namespace: ns, ServerID: serverID,
		Message: serverID + " 健康状态异常", Detail: `{"to":"lost","address":"10.0.0.1:25565"}`,
		Status: status, CreatedAt: at, OccurrenceCount: 1,
	}
	if err := f.repo.Create(&event); err != nil {
		t.Fatalf("写入告警事件失败: %v", err)
	}
	return event
}

// get 回读一条告警当前落库状态。
func (f *alertMCPFixture) get(t *testing.T, id uint) model.AlertEvent {
	t.Helper()
	event, err := f.repo.Get(id)
	if err != nil {
		t.Fatalf("回读告警事件失败: %v", err)
	}
	return *event
}

// auditTotal 统计某动作的审计行数（验证「同事务写审计」）。
func (f *alertMCPFixture) auditTotal(t *testing.T, action string) int64 {
	t.Helper()
	_, total, err := f.audits.List(repository.AuditFilter{Action: action, Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	return total
}

// mcpStructuredMap 取工具结果的结构化输出并归一为 map（经 JSON 往返，兼容 SDK 的返回形态）。
func mcpStructuredMap(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res == nil || res.StructuredContent == nil {
		t.Fatalf("工具结果缺少结构化输出: %+v", res)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("序列化结构化输出失败: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析结构化输出失败: %v", err)
	}
	return out
}

// mcpItems 取结构化输出的 items 数组（元素已归一为 map）。
func mcpItems(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, ok := out["items"].([]any)
	if !ok {
		t.Fatalf("结构化输出缺少 items 数组: %v", out)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("items 元素不是对象: %v", item)
		}
		items = append(items, row)
	}
	return items
}

// TestMCPAlertEventListProjectsSummaryWithoutDetail 验证只读列表的投影与过滤：
// 字段齐备、空值落 null、**detail 绝不出现**，且 status / level / namespace 过滤各自生效。
func TestMCPAlertEventListProjectsSummaryWithoutDetail(t *testing.T) {
	f := newAlertMCPFixture(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	f.seed(t, f.prodCode, "lobby-1", model.AlertLevelWarning, model.AlertEventStatusOpen, base)
	f.seed(t, f.prodCode, "lobby-2", model.AlertLevelCritical, model.AlertEventStatusResolved, base.Add(time.Minute))
	f.seed(t, f.devCode, "arena-1", model.AlertLevelCritical, model.AlertEventStatusOpen, base.Add(2*time.Minute))
	server := f.server(t)

	// ① 无过滤：3 条，时间倒序（最新 arena-1 在前）
	out := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.alerts.events.list", map[string]any{}))
	items := mcpItems(t, out)
	if len(items) != 3 || out["total"] != float64(3) {
		t.Fatalf("应有 3 条（total=3），实际 %v %v", out["total"], items)
	}
	if items[0]["serverId"] != "arena-1" {
		t.Fatalf("时间倒序最新应为 arena-1，实际 %v", items[0]["serverId"])
	}
	// ② 投影字段齐备且不透传 detail（含地址等上下文，是既有只读工具的硬约束）
	for _, item := range items {
		for _, key := range []string{"id", "type", "level", "status", "serverId", "namespace", "message", "createdAt", "lastAt", "occurrenceCount", "handledBy", "handledAt", "handleNote", "severityOverride"} {
			if _, ok := item[key]; !ok {
				t.Fatalf("投影缺字段 %s: %v", key, item)
			}
		}
		for _, forbidden := range []string{"detail", "overriddenBy", "overriddenAt", "toStatus"} {
			if _, ok := item[forbidden]; ok {
				t.Fatalf("告警 MCP 响应泄露受限字段 %s: %v", forbidden, item)
			}
		}
		if _, ok := item["createdAt"].(string); !ok {
			t.Fatalf("createdAt 应为 RFC3339 字符串: %v", item["createdAt"])
		}
		if item["handledBy"] != nil || item["handledAt"] != nil || item["handleNote"] != nil || item["severityOverride"] != nil {
			t.Fatalf("未处理告警的处理留痕与改级标记应为 null: %v", item)
		}
	}

	// ③ 三档过滤各自生效
	cases := []struct {
		name   string
		args   map[string]any
		expect int
	}{
		{"level=critical", map[string]any{"level": model.AlertLevelCritical}, 2},
		{"status=open", map[string]any{"status": model.AlertEventStatusOpen}, 2},
		{"namespace=prod", map[string]any{"namespace": f.prodCode}, 2},
		{"namespace+status", map[string]any{"namespace": f.prodCode, "status": model.AlertEventStatusOpen}, 1},
	}
	for _, tc := range cases {
		got := mcpItems(t, mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.alerts.events.list", tc.args)))
		if len(got) != tc.expect {
			t.Fatalf("%s 应 %d 条，实际 %d %v", tc.name, tc.expect, len(got), got)
		}
	}
}

// TestMCPAlertEventListRespectsObservationScope 验证只读列表不越界：
// env 只映射 prod 时，dev 的告警不出现在结果里；失效范围参数则直接拒绝。
func TestMCPAlertEventListRespectsObservationScope(t *testing.T) {
	f := newAlertMCPFixture(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	f.seed(t, f.prodCode, "lobby-1", model.AlertLevelWarning, model.AlertEventStatusOpen, base)
	f.seed(t, f.devCode, "arena-1", model.AlertLevelCritical, model.AlertEventStatusOpen, base.Add(time.Minute))
	server := f.server(t)

	out := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.alerts.events.list", map[string]any{"envId": strconv.FormatUint(uint64(f.envID), 10)}))
	items := mcpItems(t, out)
	if len(items) != 1 || items[0]["namespace"] != f.prodCode {
		t.Fatalf("env 范围应只回 prod 的 1 条，实际 %v", items)
	}
	// 越界叠加：env 命中的范围（prod）与范围外 namespace code（dev）AND 之后为空集——不泄露、也不报错
	outOfScope := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.alerts.events.list", map[string]any{"envId": strconv.FormatUint(uint64(f.envID), 10), "namespace": f.devCode}))
	if items := mcpItems(t, outOfScope); len(items) != 0 {
		t.Fatalf("范围外 namespace 与 env 组合应为空集，实际 %v", items)
	}
	// 失效范围（不存在的 namespaceId）→ 拒绝
	if stale := mustCallMCPTool(t, server, "beacon.alerts.events.list", map[string]any{"namespaceId": "99999"}); !stale.IsError {
		t.Fatalf("失效观测范围应被拒绝: %+v", stale)
	}
}

// TestMCPAlertEventHandleValidatesStatusAndNote 验证单条处理的两条前置校验
// （状态白名单、说明必填）都在 MCP 层拒绝、不落库，且成功路径的处置人与状态正确。
func TestMCPAlertEventHandleValidatesStatusAndNote(t *testing.T) {
	f := newAlertMCPFixture(t)
	seeded := f.seed(t, f.prodCode, "lobby-1", model.AlertLevelCritical, model.AlertEventStatusOpen, time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC))
	server := f.server(t)

	id := seeded.ID
	call := func(args map[string]any) *mcp.CallToolResult {
		return mustCallMCPTool(t, server, "beacon.alerts.events.handle", args)
	}

	// ① 合法 null 值状态（status 缺省 = 空串）拒绝
	if res := call(map[string]any{"id": id, "status": "", "note": "运维已接手"}); !res.IsError {
		t.Fatalf("空 status 应被拒绝: %+v", res)
	}
	// ② 非法状态拒绝（不依赖 service 层报错）
	if res := call(map[string]any{"id": id, "status": "dismiss", "note": "运维已接手"}); !res.IsError {
		t.Fatalf("非法 status 应被拒绝: %+v", res)
	}
	// ③ 说明空白拒绝
	if res := call(map[string]any{"id": id, "status": model.AlertEventStatusAcknowledged, "note": "   "}); !res.IsError {
		t.Fatalf("空白 note 应被拒绝: %+v", res)
	}
	// 以上三次拒绝都不得改动落库状态与审计
	if got := f.get(t, id); got.Status != model.AlertEventStatusOpen || got.HandledBy != "" {
		t.Fatalf("被拒绝的调用不得改动告警: %+v", got)
	}
	if n := f.auditTotal(t, model.ActionAlertEventAcknowledge); n != 0 {
		t.Fatalf("被拒绝的调用不得写审计，实际 %d 行", n)
	}

	// ④ 成功路径：状态 / 处置人 / 处置时刻 / 说明落库，并与审计同事务
	out := mcpStructuredMap(t, call(map[string]any{"id": id, "status": model.AlertEventStatusAcknowledged, "note": "已确认，正在排查"}))
	if out["id"] != float64(id) || out["status"] != model.AlertEventStatusAcknowledged {
		t.Fatalf("处理结果投影不符: %v", out)
	}
	if out["handledBy"] != f.principal.AuditRef() {
		t.Fatalf("handledBy 应为调用主体审计引用 %q，实际 %v", f.principal.AuditRef(), out["handledBy"])
	}
	if _, ok := out["handledAt"].(string); !ok {
		t.Fatalf("handledAt 应为 RFC3339 字符串: %v", out["handledAt"])
	}
	got := f.get(t, id)
	if got.Status != model.AlertEventStatusAcknowledged || got.HandledBy != f.principal.AuditRef() || got.HandleNote != "已确认，正在排查" || got.HandledAt == nil {
		t.Fatalf("处理结果未落库: %+v", got)
	}
	if n := f.auditTotal(t, model.ActionAlertEventAcknowledge); n != 1 {
		t.Fatalf("处理应恰好写 1 行专项审计，实际 %d", n)
	}

	// ⑤ 目标不存在 → 拒绝且给出可读原因
	if res := call(map[string]any{"id": id + 1000, "status": model.AlertEventStatusResolved, "note": "关闭"}); !res.IsError {
		t.Fatalf("不存在的告警应被拒绝: %+v", res)
	}
}

// TestMCPAlertEventBatchHandleOnlyTouchesOpenRowsAndIsIdempotent 是批量处理的核心断言：
// 只影响观察范围内 status='open' 的行、已处理行与范围外行原样保留、重复调用幂等（affected=0）。
func TestMCPAlertEventBatchHandleOnlyTouchesOpenRowsAndIsIdempotent(t *testing.T) {
	f := newAlertMCPFixture(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	openA := f.seed(t, f.prodCode, "lobby-1", model.AlertLevelWarning, model.AlertEventStatusOpen, base)
	openB := f.seed(t, f.prodCode, "lobby-2", model.AlertLevelCritical, model.AlertEventStatusOpen, base.Add(time.Minute))
	outside := f.seed(t, f.devCode, "arena-1", model.AlertLevelCritical, model.AlertEventStatusOpen, base.Add(2*time.Minute))
	// 已处理行：手工处理痕迹必须在批量后被完整保留
	done := f.seed(t, f.prodCode, "lobby-3", model.AlertLevelWarning, model.AlertEventStatusResolved, base.Add(3*time.Minute))
	handledAt := base.Add(4 * time.Minute)
	if err := f.db.Model(&model.AlertEvent{}).Where("id = ?", done.ID).
		Updates(map[string]any{"handled_by": "admin", "handled_at": handledAt, "handle_note": "手工关闭"}).Error; err != nil {
		t.Fatalf("预置已处理行失败: %v", err)
	}
	server := f.server(t)
	envArg := strconv.FormatUint(uint64(f.envID), 10)
	batchArgs := map[string]any{
		"envId": envArg, "status": model.AlertEventStatusResolved,
		"note": "实例已下线，批量关闭",
	}

	// ① 说明为空白 → 拒绝且零改动
	if res := mustCallMCPTool(t, server, "beacon.alerts.events.batch-handle", map[string]any{"envId": envArg, "status": model.AlertEventStatusResolved, "note": "  "}); !res.IsError {
		t.Fatalf("空白 note 的批量应被拒绝: %+v", res)
	}
	// ② 非法状态 → 拒绝且零改动
	if res := mustCallMCPTool(t, server, "beacon.alerts.events.batch-handle", map[string]any{"envId": envArg, "status": "close", "note": "批量关闭"}); !res.IsError {
		t.Fatalf("非法 status 的批量应被拒绝: %+v", res)
	}
	if got := f.get(t, openA.ID); got.Status != model.AlertEventStatusOpen || got.HandledBy != "" {
		t.Fatalf("被拒绝的批量不得改动告警: %+v", got)
	}
	if n := f.auditTotal(t, model.ActionAlertEventBatchHandled); n != 0 {
		t.Fatalf("被拒绝的批量不得写审计，实际 %d 行", n)
	}

	// ③ 正式批量：受影响数 = 范围内 2 条 open
	out := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.alerts.events.batch-handle", batchArgs))
	if out["affected"] != float64(2) {
		t.Fatalf("应影响 2 行（范围内 open），实际 %v", out["affected"])
	}
	for _, id := range []uint{openA.ID, openB.ID} {
		got := f.get(t, id)
		if got.Status != model.AlertEventStatusResolved || got.HandledBy != f.principal.AuditRef() ||
			got.HandleNote != "实例已下线，批量关闭" || got.HandledAt == nil {
			t.Fatalf("范围内 open 行未被正确批量处理: %+v", got)
		}
	}
	// ④ 已处理行原样保留（一条 UPDATE 只打 status='open' 的行）
	if got := f.get(t, done.ID); got.HandledBy != "admin" || got.HandleNote != "手工关闭" || got.HandledAt == nil || !got.HandledAt.Equal(handledAt) {
		t.Fatalf("已处理行不得被批量改写: %+v", got)
	}
	// ⑤ 观察范围外的 open 行不受影响
	if got := f.get(t, outside.ID); got.Status != model.AlertEventStatusOpen || got.HandledBy != "" {
		t.Fatalf("范围外告警不得被批量处理: %+v", got)
	}
	// ⑥ 幂等：重复调用 affected=0，且不产生额外改动
	again := mcpStructuredMap(t, mustCallMCPTool(t, server, "beacon.alerts.events.batch-handle", batchArgs))
	if again["affected"] != float64(0) {
		t.Fatalf("重复批量应 affected=0（幂等），实际 %v", again["affected"])
	}
	if n := f.auditTotal(t, model.ActionAlertEventBatchHandled); n != 2 {
		t.Fatalf("两次批量调用应各写 1 行审计，实际 %d", n)
	}
}

// TestMCPAlertEventToolsAreAutomationOnly 验证三个新工具的分级与 profile 覆盖：
// 列表对 observer / automation 都可见；两个处置工具仅 automation 可见，且登记风险等级为 low / high。
func TestMCPAlertEventToolsAreAutomationOnly(t *testing.T) {
	const listTool, handleTool, batchTool = "beacon.alerts.events.list", "beacon.alerts.events.handle", "beacon.alerts.events.batch-handle"
	wantRisk := map[string]string{listTool: MCPRiskLow, handleTool: MCPRiskHigh, batchTool: MCPRiskHigh}
	for name, level := range wantRisk {
		spec, ok := mcpToolSpecByName(name)
		if !ok {
			t.Fatalf("工具 %s 未登记入 mcpToolCatalog", name)
		}
		if spec.RiskLevel != level {
			t.Fatalf("工具 %s 风险等级应为 %s，实际 %s", name, level, spec.RiskLevel)
		}
		if spec.OperationKind != "" {
			t.Fatalf("工具 %s 无审批语义，OperationKind 应为空，实际 %q", name, spec.OperationKind)
		}
	}
	observer := MCPToolNames(model.MCPClientProfileObserver)
	automation := MCPToolNames(model.MCPClientProfileAutomation)
	if !containsMCPTool(observer, listTool) || !containsMCPTool(automation, listTool) {
		t.Fatalf("告警只读列表应对两个 profile 可见: observer=%v automation=%v", containsMCPTool(observer, listTool), containsMCPTool(automation, listTool))
	}
	for _, name := range []string{handleTool, batchTool} {
		if containsMCPTool(observer, name) {
			t.Fatalf("告警处置工具 %s 不得对 observer 可见", name)
		}
		if !containsMCPTool(automation, name) {
			t.Fatalf("告警处置工具 %s 应对 automation 可见", name)
		}
	}
}
