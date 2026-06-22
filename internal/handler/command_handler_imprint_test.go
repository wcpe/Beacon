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
	"github.com/wcpe/Beacon/internal/service"
)

// TestIngestImprintModeNoPanic FR-46 回归（review 🔴A）：拓印模式回传成功时 ReceiveIngest 返回 (nil,nil)，
// Ingest handler 必须判空、绝不读 nil.Created（否则 nil 解引用 panic → 500、整条拓印链路断裂）。
// 此前该 happy-path 仅由 //go:build integration 集成测试覆盖（普通 go test 跳过）、且 service 单测用
// `_, err :=` 丢首返回值恰好绕过，故跑测全绿却带崩溃。本用例把盲区下沉为普通 go test。
func TestIngestImprintModeNoPanic(t *testing.T) {
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
	cmdSvc := service.NewAgentCommandService(db, repository.NewAgentCommandRepository(db), fileSvc, auditRepo)
	h := NewCommandHandler(cmdSvc, nil) // Ingest 不用 insSvc，nil 足矣

	// 建一条 fetched 态拓印命令（mode=imprint + 目标 path）。
	cmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "lobby-1",
		Type: model.CommandTypeIngestPlugins, Status: model.CommandStatusFetched,
		Payload: `{"mode":"imprint","path":"Demo/config.yml"}`, Operator: "alice",
	}
	if e := db.Create(cmd).Error; e != nil {
		t.Fatalf("建命令失败: %v", e)
	}

	// agent 回传整棵树（含目标文件）。imprint 转存成功 → ReceiveIngest 返回 (nil,nil)。
	body, _ := json.Marshal(map[string]any{
		"commandId": cmd.ID,
		"files": []map[string]string{
			{"path": "Demo/config.yml", "content": "a: 1\n"},
			{"path": "Other/x.yml", "content": "b: 2\n"},
		},
	})
	r := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/files/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.Ingest(w, r) // 修复前：nil.Created → panic

	if w.Code != http.StatusOK {
		t.Fatalf("拓印回传应 200（handler 判空、不读 nil.Created），实际 %d，body=%s", w.Code, w.Body.String())
	}
	var got model.AgentCommand
	if e := db.First(&got, cmd.ID).Error; e != nil {
		t.Fatalf("查命令失败: %v", e)
	}
	if got.Status != model.CommandStatusReady {
		t.Fatalf("转存成功命令应转 ready，实际 %s", got.Status)
	}
}
