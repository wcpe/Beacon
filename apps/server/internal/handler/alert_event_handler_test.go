package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// newAlertEventHandler 用私有内存 sqlite 装配告警事件处理器（迁移 alert_event + audit_log，不依赖 MySQL）。
func newAlertEventHandler(t *testing.T) (*AlertEventHandler, *service.AlertEventService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:alerthdl_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.AlertEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	svc := service.NewAlertEventService(db, repository.NewAlertEventRepository(db), repository.NewAuditLogRepository(db))
	return NewAlertEventHandler(svc), svc, db
}

// handleReq 构造带 {id} 路径参数 + 登录身份 + JSON body 的处理请求。
func handleReq(id uint, operator, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/admin/v1/alert-events/"+strconv.FormatUint(uint64(id), 10)+"/handle", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatUint(uint64(id), 10))
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithOperator(ctx, operator)
	return r.WithContext(ctx)
}

// TestAlertEventListViewHasStatusFields List 响应含 status 族字段，未处理时 handledBy/handledAt/handleNote 为 null（对齐契约 string|null）。
func TestAlertEventListViewHasStatusFields(t *testing.T) {
	h, svc, _ := newAlertEventHandler(t)
	e := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, Namespace: "prod", ServerID: "s1", Message: "s1 lost"}
	if err := svc.Record(e); err != nil {
		t.Fatalf("落库失败: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/alert-events", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List 应 200，实际 %d", rec.Code)
	}
	// 逐字对照 packages/contracts AlertEventItem：解析为 map 校验字段存在与 null 语义。
	var resp struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(resp.Items))
	}
	item := resp.Items[0]
	for _, key := range []string{"id", "type", "level", "serverId", "namespace", "message", "detail", "createdAt", "status", "handledBy", "handledAt", "handleNote"} {
		if _, ok := item[key]; !ok {
			t.Fatalf("响应缺契约字段 %q，实际字段集 %v", key, item)
		}
	}
	if string(item["status"]) != `"open"` {
		t.Fatalf("新告警 status 应为 open，实际 %s", item["status"])
	}
	for _, key := range []string{"handledBy", "handledAt", "handleNote"} {
		if string(item[key]) != "null" {
			t.Fatalf("未处理时 %q 应为 null，实际 %s", key, item[key])
		}
	}
}

// TestHandleEndpointFrontendWording /handle 接受前端契约措辞 {status, note}，返回更新后的行（status/handledBy/handleNote 就位）。
func TestHandleEndpointFrontendWording(t *testing.T) {
	h, svc, _ := newAlertEventHandler(t)
	e := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: "prod", ServerID: "s1", Message: "s1 degraded"}
	_ = svc.Record(e)

	rec := httptest.NewRecorder()
	h.Handle(rec, handleReq(e.ID, "alice", `{"status":"resolved","note":"已重启"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("handle 应 200，实际 %d 体 %s", rec.Code, rec.Body.String())
	}
	var view struct {
		Status     string  `json:"status"`
		HandledBy  *string `json:"handledBy"`
		HandleNote *string `json:"handleNote"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if view.Status != model.AlertEventStatusResolved {
		t.Fatalf("应返回 resolved，实际 %q", view.Status)
	}
	if view.HandledBy == nil || *view.HandledBy != "alice" {
		t.Fatalf("handledBy 应取登录身份 alice，实际 %v", view.HandledBy)
	}
	if view.HandleNote == nil || *view.HandleNote != "已重启" {
		t.Fatalf("handleNote 应回显，实际 %v", view.HandleNote)
	}
}

// TestHandleEndpointAdrWording /handle 兼容 ADR-0064 措辞 {action, handleNote}。
func TestHandleEndpointAdrWording(t *testing.T) {
	h, svc, _ := newAlertEventHandler(t)
	e := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: "prod", ServerID: "s1", Message: "x"}
	_ = svc.Record(e)

	rec := httptest.NewRecorder()
	h.Handle(rec, handleReq(e.ID, "bob", `{"action":"acknowledge","handleNote":"排查中"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("handle 应 200，实际 %d", rec.Code)
	}
	var view struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if view.Status != model.AlertEventStatusAcknowledged {
		t.Fatalf("acknowledge 应转 acknowledged，实际 %q", view.Status)
	}
}

// TestHandleEndpointInvalidAction 非法动作返回 400。
func TestHandleEndpointInvalidAction(t *testing.T) {
	h, svc, _ := newAlertEventHandler(t)
	e := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: "prod", ServerID: "s1", Message: "x"}
	_ = svc.Record(e)

	rec := httptest.NewRecorder()
	h.Handle(rec, handleReq(e.ID, "alice", `{"status":"open"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法动作应 400，实际 %d", rec.Code)
	}
}

// TestHandleEndpointNotFound 处理不存在的事件返回 404。
func TestHandleEndpointNotFound(t *testing.T) {
	h, _, _ := newAlertEventHandler(t)
	rec := httptest.NewRecorder()
	h.Handle(rec, handleReq(9999, "alice", `{"status":"resolved"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404，实际 %d", rec.Code)
	}
}
