package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// agentSettingsApprovalHandlers 是带审批核心的处理器夹具。
//
// 一并暴露 ApprovalService：本文件要用它把申请推进到非 pending 状态，验证 202 里的 status
// 是从申请实体读出的真实值而不是硬编码。
type agentSettingsApprovalHandlers struct {
	handler  *V2AgentSettingsHandler
	db       *gorm.DB
	nsID     uint
	approval *service.ApprovalService
}

// newAgentSettingsApprovalHandlers 装配带审批核心的 v2 agent 配置处理器。
//
// 与 newAgentSettingsHandlers 分开：那条夹具刻意不注入审批核心，用来锁「普通键直写」的
// 老行为；本夹具专门覆盖高危键的 202 出口，两者混用会让「有没有装审批」变得含糊。
func newAgentSettingsApprovalHandlers(t *testing.T) agentSettingsApprovalHandlers {
	t.Helper()
	dsn := "file:agentsettingsha_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.AgentSetting{}, &model.AuditLog{},
		&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	ns := &model.Namespace{Code: "prod", Name: "生产", Lifecycle: model.NamespaceLifecycleActive}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("建环境失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	approval := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db),
		repository.NewAuditLogRepository(db), registry)
	svc := service.NewAgentSettingsService(db, repository.NewAgentSettingRepository(db),
		repository.NewAuditLogRepository(db), repository.NewNamespaceRepository(db))
	svc.SetApprovalService(approval)
	service.RegisterAgentSettingsApprovalAdapter(registry, svc)
	return agentSettingsApprovalHandlers{handler: NewV2AgentSettingsHandler(svc), db: db, nsID: ns.ID, approval: approval}
}

// putAgentSettings 用管理员主体提交一次批量写入，返回响应记录器。
func putAgentSettings(t *testing.T, h *V2AgentSettingsHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/admin/v2/agent-settings", strings.NewReader(body))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("alice")))
	rec := httptest.NewRecorder()
	h.Put(rec, req)
	return rec
}

// ticketField 从 202 响应体里取单个顶层字段。
func ticketField(t *testing.T, rec *httptest.ResponseRecorder, field string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析票据失败: %v：%s", err, rec.Body.String())
	}
	raw, hit := payload[field]
	if !hit {
		t.Fatalf("票据缺字段 %q：%s", field, rec.Body.String())
	}
	text, ok := raw.(string)
	if !ok {
		t.Fatalf("票据字段 %q 应为字符串，实际 %T", field, raw)
	}
	return text
}

// TestAgentSettingsPutDangerousKeyReturns202 锁住前端已实现的死分支终于接上：
// 高危键 PUT 返回 **202 + 票据**，且响应体顶层就带 approvalRequestId
// （前端按 'approvalRequestId' in result 判形状，嵌在别的字段里会被当成「已下发」）。
func TestAgentSettingsPutDangerousKeyReturns202(t *testing.T) {
	f := newAgentSettingsApprovalHandlers(t)

	body := `{"namespaceId":` + itoa(f.nsID) + `,"serverId":"lobby-1","confirmationScope":"prod/lobby-1",` +
		`"values":{"override.command-whitelist":"reload, taboolib"},"reason":"开启三方插件命令派发"}`
	rec := putAgentSettings(t, f.handler, body)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("高危键应返回 202，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var ticket map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
		t.Fatalf("解析票据失败: %v", err)
	}
	if raw, hit := ticket["approvalRequestId"]; !hit || raw == "" {
		t.Fatalf("票据顶层必须带 approvalRequestId（前端按此判定 202），实际 %s", rec.Body.String())
	}
	if ticket["status"] != model.ApprovalStatusPending {
		t.Fatalf("票据状态应为 pending，实际 %v", ticket["status"])
	}
	// 202 只表示受理：库内必须没有任何配置行。
	var rows int64
	if err := f.db.Model(&model.AgentSetting{}).Count(&rows).Error; err != nil {
		t.Fatalf("统计配置行失败: %v", err)
	}
	if rows != 0 {
		t.Fatalf("202 时绝不能已落库，实际 %d 行", rows)
	}
}

// TestAgentSettingsPutSafeKeyReturns200WithoutTicket 是 HTTP 层的回归锁：
// 不含高危键的 PUT 仍 200 直写，且响应体不带 approvalTicket 字段（老客户端逐字节不变）。
func TestAgentSettingsPutSafeKeyReturns200WithoutTicket(t *testing.T) {
	f := newAgentSettingsApprovalHandlers(t)

	body := `{"namespaceId":` + itoa(f.nsID) + `,"confirmationScope":"prod",` +
		`"values":{"timing.poll-timeout-ms":"20000"},"reason":"调整长轮询挂起上限"}`
	rec := putAgentSettings(t, f.handler, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("普通键应 200 直写，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if _, hit := payload["approvalTicket"]; hit {
		t.Fatalf("普通键响应不应出现 approvalTicket 字段，实际 %s", rec.Body.String())
	}
	if _, hit := payload["approvalRequestId"]; hit {
		t.Fatalf("普通键响应不应出现 approvalRequestId，实际 %s", rec.Body.String())
	}
	var rows []model.AgentSetting
	if err := f.db.Where("namespace_id = ? AND setting_key = ?", f.nsID, "timing.poll-timeout-ms").
		Find(&rows).Error; err != nil {
		t.Fatalf("查配置行失败: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != "20000" {
		t.Fatalf("普通键应立即落库，实际 %+v", rows)
	}
}

// TestAgentSettingsPutDangerousKeyRequiresReason 审批申请必须带原因：
// 批准人看到的唯一上下文就是这句话，空着等于让他盲签。
func TestAgentSettingsPutDangerousKeyRequiresReason(t *testing.T) {
	f := newAgentSettingsApprovalHandlers(t)

	body := `{"namespaceId":` + itoa(f.nsID) + `,"confirmationScope":"prod",` +
		`"values":{"override.command-whitelist":"reload"}}`
	rec := putAgentSettings(t, f.handler, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺原因应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "approval_reason_required") {
		t.Fatalf("应带 approval_reason_required，实际 %s", rec.Body.String())
	}
}

// TestAgentSettingsPutTicketStatusIsReadFromEntity 锁住 202 里的 status 是**从申请实体读出的真实值**。
//
// 前端要靠这个字段分支（pending = 等审批 / executing = 已批准正在下发 / 其它 = 已终结），
// 一旦 handler 把它硬编码成 pending，前端就会把「已批准正在执行」也显示成「等待批准」。
// 用 executing 做探针最能暴露硬编码：它绝不等于 pending，也不可能被碰巧算出来。
func TestAgentSettingsPutTicketStatusIsReadFromEntity(t *testing.T) {
	f := newAgentSettingsApprovalHandlers(t)

	body := `{"namespaceId":` + itoa(f.nsID) + `,"serverId":"lobby-1","confirmationScope":"prod/lobby-1",` +
		`"values":{"override.command-whitelist":"reload"},"reason":"开启命令派发"}`
	first := putAgentSettings(t, f.handler, body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("首次提交应 202，实际 %d：%s", first.Code, first.Body.String())
	}
	requestID := ticketField(t, first, "approvalRequestId")

	// 批准（执行器未启动，故停在 executing）：这不是 pending，硬编码的 status 会在这里露馅。
	if _, err := f.approval.Approve(requestID, auth.HumanPrincipal("bob"), "10.0.0.10"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	var stored model.ApprovalRequest
	if err := f.db.Where("request_id = ?", requestID).First(&stored).Error; err != nil {
		t.Fatalf("重读审批申请失败: %v", err)
	}
	if stored.Status != model.ApprovalStatusExecuting {
		t.Fatalf("夹具预期申请停在 executing，实际 %s", stored.Status)
	}

	// 同内容重提：旧票未终结，按幂等复用它——但状态必须如实回 executing。
	second := putAgentSettings(t, f.handler, body)
	if second.Code != http.StatusAccepted {
		t.Fatalf("重提应 202，实际 %d：%s", second.Code, second.Body.String())
	}
	if got := ticketField(t, second, "approvalRequestId"); got != requestID {
		t.Fatalf("未终结的旧票应被复用：%s vs %s", got, requestID)
	}
	if got := ticketField(t, second, "status"); got != stored.Status {
		t.Fatalf("202 的 status 必须取自申请实体（%s），实际 %s", stored.Status, got)
	}
}

