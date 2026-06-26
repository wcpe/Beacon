package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
	"github.com/wcpe/Beacon/internal/service"
)

// newBrowseTestSvc 构造装配了 browseHub 的命令服务 + 内存 sqlite（含 fs-browse 浏览能力）。
func newBrowseTestSvc(t *testing.T) (*service.AgentCommandService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}, &model.FileObject{}, &model.FileRevision{}, &model.ZoneAssignment{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"agent_command", "file_object", "file_revision", "zone_assignment", "audit_log"} {
		_ = db.Exec("DELETE FROM " + tbl).Error
	}
	auditRepo := repository.NewAuditLogRepository(db)
	fileSvc := service.NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	svc := service.NewAgentCommandService(db, repository.NewAgentCommandRepository(db), fileSvc, auditRepo)
	svc.SetBrowseResultHub(longpoll.NewHub())
	return svc, db
}

// TestBrowseResultHandlerHappy agent 回传浏览结果（ok=true）→ 命令 done、转存结果、200。
func TestBrowseResultHandlerHappy(t *testing.T) {
	svc, db := newBrowseTestSvc(t)
	h := NewBrowseHandler(svc, nil) // BrowseResult 不用 instSvc

	// 建一条 fetched 态 fs-browse 命令。
	cmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "lobby-1",
		Type: model.CommandTypeFsBrowse, Status: model.CommandStatusFetched,
		Payload: `{"op":"list","path":"AllinCore"}`, Operator: "alice",
	}
	if e := db.Create(cmd).Error; e != nil {
		t.Fatalf("建命令失败: %v", e)
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

// TestBrowseHandlerMissingNamespace admin 浏览缺 namespace → 400（不进在线校验 / 不建命令）。
func TestBrowseHandlerMissingNamespace(t *testing.T) {
	svc, _ := newBrowseTestSvc(t)
	h := NewBrowseHandler(svc, nil)
	r := httptest.NewRequest(http.MethodGet, "/admin/v1/instances/lobby-1/browse", nil)
	w := httptest.NewRecorder()
	h.Browse(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 namespace 应 400，实际 %d", w.Code)
	}
}
