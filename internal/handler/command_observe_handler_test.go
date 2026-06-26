package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// newCommandObserveHandler 背靠内存 sqlite 构造命令观测处理器 + 底层 DB（供播种）。
func newCommandObserveHandler(t *testing.T) (*CommandObserveHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}); err != nil {
		t.Fatalf("迁移 agent_command 失败: %v", err)
	}
	if err := db.Exec("DELETE FROM agent_command").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	svc := service.NewCommandObserveService(repository.NewAgentCommandRepository(db))
	return NewCommandObserveHandler(svc), db
}

func seedHandlerCmd(t *testing.T, db *gorm.DB, serverID, cmdType, status string, at time.Time) {
	t.Helper()
	if err := db.Create(&model.AgentCommand{
		NamespaceCode: "prod", ServerID: serverID, Type: cmdType, Status: status,
		Payload: `{"scope":"group"}`, ResultDetail: "summary",
		ImprintContent: "SECRET-IMPRINT", LogContent: "SECRET-LOG",
		Operator: "admin", CreatedAt: at, UpdatedAt: at,
	}).Error; err != nil {
		t.Fatalf("写命令失败: %v", err)
	}
}

// TestCommandListHandlerNoSensitive 列表 200，含 ageSeconds，且响应体绝不含敏感瞬态字段内容。
func TestCommandListHandlerNoSensitive(t *testing.T) {
	h, db := newCommandObserveHandler(t)
	seedHandlerCmd(t, db, "lobby-1", model.CommandTypeIngestPlugins, model.CommandStatusPending, time.Now().UTC().Add(-30*time.Second))

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/commands?namespace=prod", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应 200，实际 %d", rec.Code)
	}
	// 原始响应体不得出现敏感瞬态内容。
	raw := rec.Body.String()
	if strings.Contains(raw, "SECRET-IMPRINT") || strings.Contains(raw, "SECRET-LOG") {
		t.Fatalf("响应体绝不应含敏感瞬态字段内容：%s", raw)
	}
	if strings.Contains(raw, "imprintContent") || strings.Contains(raw, "logContent") || strings.Contains(raw, "payload") {
		t.Fatalf("响应体绝不应含敏感字段名：%s", raw)
	}

	var body struct {
		Total int `json:"total"`
		Items []struct {
			CommandID    uint   `json:"commandId"`
			ServerID     string `json:"serverId"`
			Type         string `json:"type"`
			Status       string `json:"status"`
			ResultDetail string `json:"resultDetail"`
			AgeSeconds   int64  `json:"ageSeconds"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if body.Total != 1 || len(body.Items) != 1 {
		t.Fatalf("应 1 条，实际 total=%d len=%d", body.Total, len(body.Items))
	}
	it := body.Items[0]
	if it.ServerID != "lobby-1" || it.Type != model.CommandTypeIngestPlugins || it.ResultDetail != "summary" {
		t.Fatalf("元数据不一致: %+v", it)
	}
	// 已等时长应 >= 约 30 秒。
	if it.AgeSeconds < 25 {
		t.Fatalf("ageSeconds 应约 30 秒，实际 %d", it.AgeSeconds)
	}
}

// TestCommandListHandlerInvalidStatus 非法 status 返 400。
func TestCommandListHandlerInvalidStatus(t *testing.T) {
	h, _ := newCommandObserveHandler(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/commands?status=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 status 应 400，实际 %d", rec.Code)
	}
}

// TestCommandAnalyticsHandler 聚合 200，字段就位。
func TestCommandAnalyticsHandler(t *testing.T) {
	h, db := newCommandObserveHandler(t)
	at := time.Now().UTC().Add(-2 * time.Hour)
	seedHandlerCmd(t, db, "lobby-1", model.CommandTypeIngestPlugins, model.CommandStatusDone, at)
	seedHandlerCmd(t, db, "lobby-2", model.CommandTypeTailLogs, model.CommandStatusFailed, at)

	rec := httptest.NewRecorder()
	h.Analytics(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/commands/analytics?namespace=prod", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应 200，实际 %d", rec.Code)
	}
	var body commandAnalyticsView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if body.Total != 2 {
		t.Fatalf("total 应 2，实际 %d", body.Total)
	}
	if len(body.ByType) != 2 || len(body.ByStatus) != 2 || len(body.ByServer) != 2 {
		t.Fatalf("聚合分组数不一致: %+v", body)
	}
}

// TestCommandAnalyticsHandlerWindowCap 窗口 >92 天返 400。
func TestCommandAnalyticsHandlerWindowCap(t *testing.T) {
	h, _ := newCommandObserveHandler(t)
	rec := httptest.NewRecorder()
	url := "/admin/v1/commands/analytics?from=2026-01-01T00:00:00Z&to=2026-06-01T00:00:00Z"
	h.Analytics(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("窗口 >92 天应 400，实际 %d", rec.Code)
	}
}
