package handler

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
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
