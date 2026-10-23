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

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// newAgentSettingsHandlers 用内存 sqlite 装配两个 agent 配置处理器（迁移 namespace / server /
// agent_setting / audit_log，不依赖 MySQL）。每测试独立内存库，避免跨测试共享缓存死锁。
func newAgentSettingsHandlers(t *testing.T) (*V2AgentSettingsHandler, *AgentSettingsHandler, *gorm.DB, uint) {
	t.Helper()
	dsn := "file:agentsettingsh_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.AgentSetting{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	ns := &model.Namespace{Code: "prod", Name: "生产", Lifecycle: model.NamespaceLifecycleActive}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("建环境失败: %v", err)
	}
	svc := service.NewAgentSettingsService(db, repository.NewAgentSettingRepository(db),
		repository.NewAuditLogRepository(db), repository.NewNamespaceRepository(db))
	return NewV2AgentSettingsHandler(svc), NewAgentSettingsHandler(svc), db, ns.ID
}

// TestAgentSettingsListRequiresNamespaceId GET 缺 namespaceId 必须 400：
// 缺参静默按 0 处理会读出一个空视图，让调用方误以为「该环境没有任何下发」。
func TestAgentSettingsListRequiresNamespaceId(t *testing.T) {
	admin, _, _, _ := newAgentSettingsHandlers(t)

	rec := httptest.NewRecorder()
	admin.List(rec, httptest.NewRequest(http.MethodGet, "/admin/v2/agent-settings", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 namespaceId 应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestAgentSettingsListReturnsScopeAndSource GET 必须给出作用域标识与逐键来源，
// 否则前端无法回答「这个值是哪来的」，也无法回填写端点的二次确认串。
func TestAgentSettingsListReturnsScopeAndSource(t *testing.T) {
	admin, _, _, nsID := newAgentSettingsHandlers(t)

	body := `{"namespaceId":` + itoa(nsID) + `,"serverId":"lobby-1","confirmationScope":"prod/lobby-1",` +
		`"values":{"delivery.config-reload-timeout-ms":"30000"},"reason":"初始下发"}`
	rec := httptest.NewRecorder()
	admin.Put(rec, httptest.NewRequest(http.MethodPut, "/admin/v2/agent-settings", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("写入应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	admin.List(rec, httptest.NewRequest(http.MethodGet, "/admin/v2/agent-settings?namespaceId="+itoa(nsID)+"&serverId=lobby-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("读取应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var view service.AgentSettingScopeView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if view.ScopeLabel != "prod/lobby-1" || view.NamespaceCode != "prod" || view.ServerID != "lobby-1" {
		t.Fatalf("作用域标识不符：%+v", view)
	}
	found := false
	for _, item := range view.Items {
		if item.Key != "delivery.config-reload-timeout-ms" {
			continue
		}
		found = true
		if item.Value != "30000" || item.Source != service.AgentSettingSourceServer || !item.Known {
			t.Fatalf("该项应为 server 来源且已知：%+v", item)
		}
	}
	if !found {
		t.Fatal("响应缺少已写入的键")
	}
}

// TestAgentSettingsPutRejectsForbiddenAndUnknownKeys PUT 的键级拦截在 handler 层同样生效
// （400 + 专用码），确保即使绕过前端直接调接口也无法写入劫持键。
func TestAgentSettingsPutRejectsForbiddenAndUnknownKeys(t *testing.T) {
	admin, _, db, nsID := newAgentSettingsHandlers(t)

	cases := []struct{ body, wantCode string }{
		{`{"namespaceId":` + itoa(nsID) + `,"confirmationScope":"prod","values":{"beacon.endpoints":"http://evil:8848"}}`,
			"AGENT_SETTING_KEY_FORBIDDEN"},
		{`{"namespaceId":` + itoa(nsID) + `,"confirmationScope":"prod","values":{"beacon.bootstrap-token":"x"}}`,
			"AGENT_SETTING_KEY_FORBIDDEN"},
		{`{"namespaceId":` + itoa(nsID) + `,"confirmationScope":"prod","values":{"identity.identity-id":"x"}}`,
			"AGENT_SETTING_KEY_FORBIDDEN"},
		{`{"namespaceId":` + itoa(nsID) + `,"confirmationScope":"prod","values":{"no.such.key":"1"}}`,
			"AGENT_SETTING_KEY_NOT_ALLOWED"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		admin.Put(rec, httptest.NewRequest(http.MethodPut, "/admin/v2/agent-settings", strings.NewReader(c.body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("应 400，实际 %d：%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), c.wantCode) {
			t.Fatalf("响应应带 %s，实际 %s", c.wantCode, rec.Body.String())
		}
	}
	var count int64
	if err := db.Model(&model.AgentSetting{}).Count(&count).Error; err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("被拒写入不得落库，实际 %d 行", count)
	}
}

// TestAgentSettingsPutRejectsConfirmMismatch 二次确认不符必须 400 且带期望的作用域标识。
func TestAgentSettingsPutRejectsConfirmMismatch(t *testing.T) {
	admin, _, _, nsID := newAgentSettingsHandlers(t)

	body := `{"namespaceId":` + itoa(nsID) + `,"serverId":"lobby-1","confirmationScope":"prod",` +
		`"values":{"timing.poll-timeout-ms":"20000"}}`
	rec := httptest.NewRecorder()
	admin.Put(rec, httptest.NewRequest(http.MethodPut, "/admin/v2/agent-settings", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AGENT_SETTING_CONFIRM_MISMATCH") ||
		!strings.Contains(rec.Body.String(), "prod/lobby-1") {
		t.Fatalf("响应应带专用码与期望作用域，实际 %s", rec.Body.String())
	}
}

// TestAgentSettingsPullRequiresIdentity 数据面端点缺权威身份必须 401（fail-closed）：
// 该端点的 namespace / serverId 只能来自鉴权中间件注入的身份，绝不信请求自报值。
func TestAgentSettingsPullRequiresIdentity(t *testing.T) {
	_, agentHandler, _, _ := newAgentSettingsHandlers(t)

	rec := httptest.NewRecorder()
	// 故意带上自报的 query，模拟「试图用参数冒充别台服」。
	agentHandler.Settings(rec, httptest.NewRequest(http.MethodGet,
		"/beacon/v2/agent/settings?serverId=lobby-9&namespace=prod", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无身份应 401，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestAgentSettingsPullIgnoresSpoofedScope 即便请求里自报别的 serverId，响应也只回身份作用域的值。
func TestAgentSettingsPullIgnoresSpoofedScope(t *testing.T) {
	_, agentHandler, db, nsID := newAgentSettingsHandlers(t)

	// 给 lobby-9 下发一个值（身份是 lobby-1 的 agent 不该看到它）。
	rows := []model.AgentSetting{
		{NamespaceID: nsID, ServerID: "lobby-9", Key: "messaging.rpc-timeout-ms", Value: "9000"},
		{NamespaceID: nsID, ServerID: "lobby-1", Key: "timing.request-timeout-ms", Value: "6000"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("预置行失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/beacon/v2/agent/settings?serverId=lobby-9", nil)
	req = req.WithContext(agentauth.WithIdentity(req.Context(), agentauth.Identity{
		NamespaceID: nsID, Namespace: "prod", ServerID: "lobby-1", IdentityID: "identity-1",
	}))
	rec := httptest.NewRecorder()
	agentHandler.Settings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var view service.AgentSettingsPullView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if view.ServerID != "lobby-1" {
		t.Fatalf("响应 serverId 应取自身份，实际 %q", view.ServerID)
	}
	if _, leaked := view.Values["messaging.rpc-timeout-ms"]; leaked {
		t.Fatalf("不得返回他服配置，实际 %v", view.Values)
	}
	if view.Values["timing.request-timeout-ms"] != "6000" {
		t.Fatalf("应返回自身作用域的值，实际 %v", view.Values)
	}
}
