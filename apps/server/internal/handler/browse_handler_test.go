package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

type browseReportAuthenticatorStub struct {
	identity agentauth.Identity
	err      error
	calls    int
}

func (s *browseReportAuthenticatorStub) AuthenticateAgentReport(_, _, _, _ string) (agentauth.Identity, error) {
	s.calls++
	return s.identity, s.err
}

// newBrowseTestSvc 构造内存 sqlite 命令服务（含 fs-browse 浏览能力）。
func newBrowseTestSvc(t *testing.T) (*service.AgentCommandService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}, &model.FileObject{}, &model.FileRevision{}, &model.ZoneAssignment{}, &model.AuditLog{}, &model.ApprovalRequest{}, &model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"agent_command", "file_object", "file_revision", "zone_assignment", "audit_log", "approval_request", "sensitive_access_grant"} {
		_ = db.Exec("DELETE FROM " + tbl).Error
	}
	auditRepo := repository.NewAuditLogRepository(db)
	fileSvc := service.NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	svc := service.NewAgentCommandService(db, repository.NewAgentCommandRepository(db), fileSvc, auditRepo)
	return svc, db
}

// TestBrowseResultHandlerHappy agent 回传浏览结果（ok=true）→ 命令 done、转存结果、200。
func TestBrowseResultHandlerHappy(t *testing.T) {
	svc, db := newBrowseTestSvc(t)
	grants := service.NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	svc.SetSensitiveAccessGrants(grants)
	h := NewBrowseHandler(svc, nil) // BrowseResult 不用 instSvc
	h.SetReportAuthenticator(&browseReportAuthenticatorStub{identity: agentauth.Identity{Namespace: "prod", ServerID: "lobby-1", IdentityID: "identity"}})

	// 建一条 fetched 态 fs-browse 命令。
	cmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "lobby-1",
		Type: model.CommandTypeFsBrowse, Status: model.CommandStatusFetched,
		Payload: `{"op":"list","path":"AllinCore"}`, Operator: "alice",
	}
	if e := db.Create(cmd).Error; e != nil {
		t.Fatalf("建命令失败: %v", e)
	}
	if _, err := grants.CreatePending("apr_browse_handler", "human", "alice", authz.OperationAgentCommandFSBrowse, fmt.Sprintf("agent-command/%d", cmd.ID), "pending"); err != nil {
		t.Fatalf("创建待激活授权失败: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"namespace": "prod", "serverId": "lobby-1", "commandId": cmd.ID, "ok": true,
		"result": json.RawMessage(`{"path":"AllinCore","entries":[]}`),
	})
	r := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/files/browse-result", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.BrowseResult(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("回传应 200，实际 %d，body=%s", w.Code, w.Body.String())
	}
	var got model.AgentCommand
	_ = db.First(&got, cmd.ID).Error
	if got.Status != model.CommandStatusDone {
		t.Fatalf("命令应 done，实际 %s", got.Status)
	}
	if got.BrowseResult != `{"path":"AllinCore","entries":[]}` {
		t.Fatalf("应转存浏览结果，实际 %q", got.BrowseResult)
	}
}

// TestBrowseResultHandlerRejectsUnauthenticatedOrMismatchedIdentity 不信任请求体 namespace/server，只接受权威 v2 身份。
func TestBrowseResultHandlerRejectsUnauthenticatedOrMismatchedIdentity(t *testing.T) {
	svc, db := newBrowseTestSvc(t)
	grants := service.NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	svc.SetSensitiveAccessGrants(grants)
	h := NewBrowseHandler(svc, nil)
	cmd := &model.AgentCommand{NamespaceCode: "prod", ServerID: "lobby-1", Type: model.CommandTypeFsBrowse, Status: model.CommandStatusFetched, Operator: "alice"}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建浏览命令失败: %v", err)
	}
	if _, err := grants.CreatePending("apr_browse_identity", "human", "alice", authz.OperationAgentCommandFSBrowse, fmt.Sprintf("agent-command/%d", cmd.ID), "pending"); err != nil {
		t.Fatalf("创建待激活授权失败: %v", err)
	}
	authn := &browseReportAuthenticatorStub{identity: agentauth.Identity{Namespace: "prod", ServerID: "other", IdentityID: "identity"}}
	h.SetReportAuthenticator(authn)
	body, _ := json.Marshal(map[string]any{"namespace": "prod", "serverId": "lobby-1", "commandId": cmd.ID, "ok": true, "result": json.RawMessage(`{"content":"敏感正文"}`)})
	r := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/files/browse-result", bytes.NewReader(body))
	r.Header.Set("X-Beacon-Token", "token")
	r.Header.Set("X-Beacon-Identity", "identity")
	w := httptest.NewRecorder()
	h.BrowseResult(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("错权威身份回传应按不存在拒绝，实际 %d：%s", w.Code, w.Body.String())
	}
	if authn.calls != 1 {
		t.Fatalf("必须调用 v2 回传身份校验，实际 %d", authn.calls)
	}
	var stored model.AgentCommand
	if err := db.First(&stored, cmd.ID).Error; err != nil || stored.BrowseResult != "" || stored.Status != model.CommandStatusFetched {
		t.Fatalf("错身份不得写入敏感结果：%+v err=%v", stored, err)
	}
}

// TestBrowseResultHandlerRejectsAuthenticationFailure 认证失败时不得处理回传。
func TestBrowseResultHandlerRejectsAuthenticationFailure(t *testing.T) {
	svc, _ := newBrowseTestSvc(t)
	h := NewBrowseHandler(svc, nil)
	h.SetReportAuthenticator(&browseReportAuthenticatorStub{err: apperr.ErrUnauthorized})
	r := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/files/browse-result", bytes.NewBufferString(`{"commandId":1,"ok":false}`))
	w := httptest.NewRecorder()
	h.BrowseResult(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("认证失败应返回 401，实际 %d", w.Code)
	}
}

// TestBrowseResultHandlerInvalidBody 非法 JSON → 400。
func TestBrowseResultHandlerInvalidBody(t *testing.T) {
	svc, _ := newBrowseTestSvc(t)
	h := NewBrowseHandler(svc, nil)
	r := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/files/browse-result", bytes.NewReader([]byte("not-json")))
	w := httptest.NewRecorder()
	h.BrowseResult(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实际 %d", w.Code)
	}
}

// TestBrowseHandlerMissingNamespace 直接浏览缺少 namespace 时应按参数错误拒绝。
func TestBrowseHandlerMissingNamespace(t *testing.T) {
	svc, _ := newBrowseTestSvc(t)
	h := NewBrowseHandler(svc, nil)
	r := httptest.NewRequest(http.MethodGet, "/admin/v1/instances/lobby-1/browse", nil)
	w := httptest.NewRecorder()
	h.Browse(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺少 namespace 应返回 400，实际 %d", w.Code)
	}
}

// TestBrowseHandlerGetWithoutCommandIDDoesNotCreate GET 只能读取指定命令结果，不能创建浏览命令。
func TestBrowseHandlerGetWithoutCommandIDDoesNotCreate(t *testing.T) {
	svc, db := newBrowseTestSvc(t)
	registry := runtime.NewRegistry()
	if _, err := registry.Register(&runtime.Instance{Namespace: "prod", ServerID: "lobby-1", Address: "127.0.0.1:25565"}, time.Minute, time.Now().UTC()); err != nil {
		t.Fatalf("注册在线实例失败: %v", err)
	}
	instSvc := service.NewInstanceService(nil, registry, nil, nil, nil, time.Second, time.Minute)
	h := NewBrowseHandler(svc, instSvc)
	r := httptest.NewRequest(http.MethodGet, "/admin/v1/instances/lobby-1/browse?namespace=prod&op=list", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("serverId", "lobby-1")
	r = r.WithContext(context.WithValue(auth.WithOperator(r.Context(), "alice"), chi.RouteCtxKey, routeContext))
	w := httptest.NewRecorder()
	h.Browse(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 commandId 的 GET 应拒绝而非创建命令，实际 %d：%s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&model.AgentCommand{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("GET 不得创建浏览命令，数量=%d err=%v", count, err)
	}
}

// prepareBrowseReadScope 迁移并清理读路径反查所需的 namespace / server 表（与其它用例共享同一内存库）。
func prepareBrowseReadScope(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}); err != nil {
		t.Fatalf("迁移 namespace / server 表失败: %v", err)
	}
	for _, tbl := range []string{"server", "namespace"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
}

// seedBrowseScopeServer 写入一条 (namespace, serverId) 归属，供读路径按 serverId 反查命名空间。
func seedBrowseScopeServer(t *testing.T, db *gorm.DB, namespaceCode, serverID string) {
	t.Helper()
	var namespace model.Namespace
	if err := db.Where("code = ?", namespaceCode).First(&namespace).Error; err != nil {
		namespace = model.Namespace{Code: namespaceCode, Name: namespaceCode}
		if err := db.Create(&namespace).Error; err != nil {
			t.Fatalf("创建 namespace %s 失败: %v", namespaceCode, err)
		}
	}
	if err := db.Create(&model.Server{NamespaceID: namespace.ID, ServerID: serverID, Kind: "backend"}).Error; err != nil {
		t.Fatalf("创建 server %s/%s 失败: %v", namespaceCode, serverID, err)
	}
}

// doBrowseGet 以指定 serverId 路由参数与操作人上下文调用 GET 读正文端点。
func doBrowseGet(t *testing.T, h *BrowseHandler, target, serverID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("serverId", serverID)
	r = r.WithContext(context.WithValue(auth.WithOperator(r.Context(), "alice"), chi.RouteCtxKey, routeContext))
	w := httptest.NewRecorder()
	h.Browse(w, r)
	return w
}

// TestBrowseHandlerRejectsCrossNamespaceRead GET 读正文必须限定在请求的 namespace；
// 同名 serverId 跨环境共存时缺省 namespace 无法消歧，一律按不存在拒绝。
func TestBrowseHandlerRejectsCrossNamespaceRead(t *testing.T) {
	svc, db := newBrowseTestSvc(t)
	prepareBrowseReadScope(t, db)
	seedBrowseScopeServer(t, db, "prod", "lobby-1")
	seedBrowseScopeServer(t, db, "test", "lobby-1")
	h := NewBrowseHandler(svc, nil)
	const result = `{"path":"Demo/config.yml","content":"enabled: true\n"}`
	cmd := &model.AgentCommand{
		NamespaceCode: "test", ServerID: "lobby-1", Type: model.CommandTypeFsBrowse,
		Status: model.CommandStatusDone, Payload: `{"op":"file","path":"Demo/config.yml"}`,
		BrowseResult: result, Operator: "alice",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建浏览命令失败: %v", err)
	}
	path := "/admin/v1/instances/lobby-1/browse?commandId=" + strconv.FormatUint(uint64(cmd.ID), 10)

	if w := doBrowseGet(t, h, path+"&namespace=prod", "lobby-1"); w.Code != http.StatusNotFound {
		t.Fatalf("跨 namespace 读取应按不存在拒绝，实际 %d：%s", w.Code, w.Body.String())
	}
	if w := doBrowseGet(t, h, path, "lobby-1"); w.Code != http.StatusNotFound {
		t.Fatalf("同名 serverId 跨环境歧义应拒绝，实际 %d：%s", w.Code, w.Body.String())
	}
	w := doBrowseGet(t, h, path+"&namespace=test", "lobby-1")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != result {
		t.Fatalf("正确 namespace 应返回正文，实际 %d：%s", w.Code, w.Body.String())
	}
}

// TestBrowseHandlerReadWithoutNamespaceUsesUniqueScope 未带 namespace 的旧调用方在归属唯一时仍可读，保持向后兼容。
func TestBrowseHandlerReadWithoutNamespaceUsesUniqueScope(t *testing.T) {
	svc, db := newBrowseTestSvc(t)
	prepareBrowseReadScope(t, db)
	seedBrowseScopeServer(t, db, "prod", "lobby-9")
	h := NewBrowseHandler(svc, nil)
	const result = `{"path":"Demo/config.yml","content":"enabled: true\n"}`
	cmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "lobby-9", Type: model.CommandTypeFsBrowse,
		Status: model.CommandStatusDone, Payload: `{"op":"file","path":"Demo/config.yml"}`,
		BrowseResult: result, Operator: "alice",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建浏览命令失败: %v", err)
	}
	path := "/admin/v1/instances/lobby-9/browse?commandId=" + strconv.FormatUint(uint64(cmd.ID), 10)
	w := doBrowseGet(t, h, path, "lobby-9")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != result {
		t.Fatalf("归属唯一时应按 serverId 反查成功，实际 %d：%s", w.Code, w.Body.String())
	}
}

// TestBrowseHandlerRejectsExpiredResult 过期正文经 handler 读取时不得返回正文。
func TestBrowseHandlerRejectsExpiredResult(t *testing.T) {
	svc, db := newBrowseTestSvc(t)
	prepareBrowseReadScope(t, db)
	seedBrowseScopeServer(t, db, "prod", "lobby-8")
	if !db.Migrator().HasColumn("agent_command", "browse_result_expires_at") {
		if err := db.Exec("ALTER TABLE agent_command ADD COLUMN browse_result_expires_at datetime").Error; err != nil {
			t.Fatalf("添加浏览结果过期测试列失败: %v", err)
		}
	}
	h := NewBrowseHandler(svc, nil)
	cmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "lobby-8", Type: model.CommandTypeFsBrowse,
		Status: model.CommandStatusDone, Payload: `{"op":"file","path":"Demo/config.yml"}`,
		BrowseResult: `{"content":"敏感正文"}`, Operator: "alice",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建浏览命令失败: %v", err)
	}
	if err := db.Model(&model.AgentCommand{}).Where("id = ?", cmd.ID).
		Update("browse_result_expires_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatalf("设置浏览结果过期时间失败: %v", err)
	}
	path := "/admin/v1/instances/lobby-8/browse?commandId=" + strconv.FormatUint(uint64(cmd.ID), 10)
	w := doBrowseGet(t, h, path+"&namespace=prod", "lobby-8")
	if w.Code != http.StatusGone {
		t.Fatalf("过期正文应按已消费拒绝，实际 %d：%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "敏感正文") {
		t.Fatalf("过期正文绝不能出现在响应里，实际 %s", w.Body.String())
	}
}
