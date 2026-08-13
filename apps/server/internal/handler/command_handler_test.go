package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

type commandReportAuthenticatorStub struct {
	identity          agentauth.Identity
	authErr           error
	receiveErr        error
	authenticateCalls int
	receiveCalls      int
}

func (s *commandReportAuthenticatorStub) AuthenticateAgentReport(_, _, _, _ string) (agentauth.Identity, error) {
	s.authenticateCalls++
	return s.identity, s.authErr
}

func (s *commandReportAuthenticatorStub) ReceiveDirectoryResyncResult(_ agentauth.Identity, _ uint, _ bool, _ string) error {
	s.receiveCalls++
	return s.receiveErr
}

func newCommandResultHandler(t *testing.T) (*CommandHandler, *gorm.DB) {
	t.Helper()
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}); err != nil {
		t.Fatalf("迁移 agent_command 失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	repo := repository.NewAgentCommandRepository(db)
	svc := service.NewAgentCommandService(db, repo, nil, nil)
	return NewCommandHandler(svc, nil), db
}

func seedFetchedCommand(t *testing.T, db *gorm.DB, commandType string) *model.AgentCommand {
	t.Helper()
	cmd := &model.AgentCommand{
		NamespaceCode: "prod",
		ServerID:      "server-1",
		Type:          commandType,
		Payload:       "{}",
		Status:        model.CommandStatusFetched,
		Operator:      "agent",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建 fetched 命令失败: %v", err)
	}
	return cmd
}

func reportCommandResult(h *CommandHandler, commandID uint) *httptest.ResponseRecorder {
	body := bytes.NewBufferString(`{"commandId":` + fmt.Sprint(commandID) + `,"ok":true,"reason":""}`)
	req := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/commands/result", body)
	req.Header.Set("X-Beacon-Token", "token")
	req.Header.Set("X-Beacon-Identity", "identity")
	req.Header.Set("X-Beacon-Boot", "boot")
	req.RemoteAddr = "203.0.113.10:25565"
	rec := httptest.NewRecorder()
	h.ReportResult(rec, req)
	return rec
}

func TestCommandHandlerReportDirectoryResyncReceiveFailureReturnsError(t *testing.T) {
	h, db := newCommandResultHandler(t)
	cmd := seedFetchedCommand(t, db, model.CommandTypeBCDirectoryResync)
	authn := &commandReportAuthenticatorStub{
		identity:   agentauth.Identity{Namespace: "prod", ServerID: "server-1", IdentityID: "identity"},
		receiveErr: apperr.ErrCommandNotFound,
	}
	h.SetReportAuthenticator(authn)

	rec := reportCommandResult(h, cmd.ID)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("目录重同步接收失败应返回 404 而非成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if authn.authenticateCalls != 1 || authn.receiveCalls != 1 {
		t.Fatalf("目录重同步应各调用一次认证与接收，实际 authenticate=%d receive=%d", authn.authenticateCalls, authn.receiveCalls)
	}
}

func TestCommandHandlerReportDirectoryResyncAuthenticationFailureShortCircuits(t *testing.T) {
	h, db := newCommandResultHandler(t)
	cmd := seedFetchedCommand(t, db, model.CommandTypeBCDirectoryResync)
	authn := &commandReportAuthenticatorStub{authErr: apperr.ErrCommandNotFound}
	h.SetReportAuthenticator(authn)

	rec := reportCommandResult(h, cmd.ID)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("目录重同步认证失败应返回 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if authn.authenticateCalls != 1 || authn.receiveCalls != 0 {
		t.Fatalf("认证失败必须短路接收，实际 authenticate=%d receive=%d", authn.authenticateCalls, authn.receiveCalls)
	}
}

func TestCommandHandlerReportResyncConfigPathUnchanged(t *testing.T) {
	h, db := newCommandResultHandler(t)
	cmd := seedFetchedCommand(t, db, model.CommandTypeResyncConfig)
	authn := &commandReportAuthenticatorStub{authErr: errors.New("普通路径不应认证")}
	h.SetReportAuthenticator(authn)

	rec := reportCommandResult(h, cmd.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("普通重同步回传应保持 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if authn.authenticateCalls != 0 || authn.receiveCalls != 0 {
		t.Fatalf("普通重同步不得调用目录重同步认证器，实际 authenticate=%d receive=%d", authn.authenticateCalls, authn.receiveCalls)
	}
	var got model.AgentCommand
	if err := db.First(&got, cmd.ID).Error; err != nil {
		t.Fatalf("读取普通重同步命令失败: %v", err)
	}
	if got.Status != model.CommandStatusDone {
		t.Fatalf("普通重同步成功回传应推进 done，实际 %s", got.Status)
	}
}

// TestCommandHandlerResyncCreatesApprovalTicket 验证 FR-209 仅受理审批申请，不在 HTTP 请求内下发命令。
func TestCommandHandlerResyncCreatesApprovalTicket(t *testing.T) {
	h, db := newResyncApprovalHandler(t)
	rec := requestResync(h, `{"reason":"需要重拉权威配置"}`, "resync-handler-1")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("重同步申请应返回 202，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var response service.ApprovalTicketView
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.ApprovalRequestID == "" || response.Status != model.ApprovalStatusPending {
		t.Fatalf("应返回 pending 审批票据，response=%+v err=%v", response, err)
	}
	var commands int64
	if err := db.Model(&model.AgentCommand{}).Count(&commands).Error; err != nil || commands != 0 {
		t.Fatalf("申请阶段不得下发命令，count=%d err=%v", commands, err)
	}
}

// TestCommandHandlerResyncRequiresReasonAndIdempotencyKey 验证审批入口不接受无理由或无幂等键的危险操作。
func TestCommandHandlerResyncRequiresReasonAndIdempotencyKey(t *testing.T) {
	h, _ := newResyncApprovalHandler(t)
	if rec := requestResync(h, `{}`, "resync-handler-missing-reason"); rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少 reason 应返回 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if rec := requestResync(h, `{"reason":"需要重拉权威配置"}`, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少 Idempotency-Key 应返回 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func newResyncApprovalHandler(t *testing.T) (*CommandHandler, *gorm.DB) {
	t.Helper()
	_, db := newCommandResultHandler(t)
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移重同步审批测试表失败: %v", err)
	}
	commandService := service.NewAgentCommandService(db, repository.NewAgentCommandRepository(db), nil, repository.NewAuditLogRepository(db))
	approvalRegistry := authz.NewApprovalRegistry()
	approvalService := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), approvalRegistry)
	commandService.SetApprovalService(approvalService)
	service.RegisterAgentCommandApprovalAdapters(approvalRegistry, commandService,
		service.NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))
	registry := runtime.NewRegistry()
	if _, err := registry.Register(&runtime.Instance{Namespace: "prod", ServerID: "server-1", Address: "127.0.0.1:25565"}, time.Minute, time.Now().UTC()); err != nil {
		t.Fatalf("注册在线实例失败: %v", err)
	}
	instanceService := service.NewInstanceService(nil, registry, nil, nil, nil, time.Second, time.Minute)
	return NewCommandHandler(commandService, instanceService), db
}

func requestResync(h *CommandHandler, body, idempotencyKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/instances/server-1/resync?namespace=prod", bytes.NewBufferString(body))
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	ctx := auth.WithPrincipal(req.Context(), auth.HumanPrincipal("alice"))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("serverId", "server-1")
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeContext))
	rec := httptest.NewRecorder()
	h.Resync(rec, req)
	return rec
}
