package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
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

// TestBrowseHandlerMissingNamespace 旧浏览入口在参数校验前即审批失败关闭。
func TestBrowseHandlerMissingNamespace(t *testing.T) {
	svc, _ := newBrowseTestSvc(t)
	h := NewBrowseHandler(svc, nil)
	r := httptest.NewRequest(http.MethodGet, "/admin/v1/instances/lobby-1/browse", nil)
	w := httptest.NewRecorder()
	h.Browse(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("旧浏览入口应 409，实际 %d", w.Code)
	}
}
