package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// newSettingsHandler 用内存 sqlite 装配设置处理器（迁移 setting + audit_log，不依赖 MySQL）。
// 用 t.Name() 作每测试独立内存库，避免接入全局 file::memory: 共享缓存而跨测试死锁。
func newSettingsHandler(t *testing.T) (*SettingsHandler, *gorm.DB) {
	t.Helper()
	dsn := "file:settingsh_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	// 单连接：sqlite shared-cache 下避免并发写 "table is locked"。
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.Setting{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"setting", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	svc, err := service.NewSettingsService(db, repository.NewSettingRepository(db), repository.NewAuditLogRepository(db))
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	return NewSettingsHandler(svc), db
}

// reqWithKeyParam 构造带 chi 路径参数 {key} 与 JSON body 的 PUT 请求。
func reqWithKeyParam(key, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/admin/v1/settings/"+key, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("key", key)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// TestSettingsListReturnsHotKeys GET /settings 返回全部热改项（含类型 / 默认 / 说明）。
func TestSettingsListReturnsHotKeys(t *testing.T) {
	h, _ := newSettingsHandler(t)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List 应 200，实际 %d", rec.Code)
	}
	var resp struct {
		Items []service.SettingView `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Items) != 13 {
		t.Fatalf("应列出 13 个热改项，实际 %d", len(resp.Items))
	}
}

// TestSettingsListRedactsProxyCredentials GET /settings 对含凭据的 update.proxy-url 回显脱敏，不泄露明文口令（FR-98）。
func TestSettingsListRedactsProxyCredentials(t *testing.T) {
	h, _ := newSettingsHandler(t)
	// 先经 PUT 落库一个含凭据代理。
	put := httptest.NewRecorder()
	h.Update(put, reqWithKeyParam(service.SettingUpdateProxyURL, `{"value":"http://user:hunter2@proxy:8080"}`))
	if put.Code != http.StatusOK {
		t.Fatalf("更新 proxy-url 应 200，实际 %d（body=%s）", put.Code, put.Body.String())
	}
	// GET 回显必须脱敏。
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/settings", nil))
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("GET /settings 响应不应含明文口令，实际 body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "http://***:***@proxy:8080") {
		t.Fatalf("GET /settings 应回显脱敏代理值，实际 body=%s", rec.Body.String())
	}
}

// TestSettingsUpdateOK PUT /settings/{key} 合法值 200 并落库。
func TestSettingsUpdateOK(t *testing.T) {
	h, db := newSettingsHandler(t)
	rec := httptest.NewRecorder()
	h.Update(rec, reqWithKeyParam(service.SettingHealthTTLSec, `{"value":"45"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("合法更新应 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var n int64
	db.Model(&model.Setting{}).Where("setting_key = ? AND value = ?", service.SettingHealthTTLSec, "45").Count(&n)
	if n != 1 {
		t.Fatalf("更新值应落库，实际命中 %d", n)
	}
}

// TestSettingsUpdateRejectsBadKeyOrValue 白名单外 key 与非法值均 400。
func TestSettingsUpdateRejectsBadKeyOrValue(t *testing.T) {
	h, _ := newSettingsHandler(t)

	// 白名单外 key → 400
	rec := httptest.NewRecorder()
	h.Update(rec, reqWithKeyParam("auth.password", `{"value":"x"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("白名单外 key 应 400，实际 %d", rec.Code)
	}

	// 非法值（越界）→ 400
	rec = httptest.NewRecorder()
	h.Update(rec, reqWithKeyParam(service.SettingHealthTTLSec, `{"value":"0"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法值应 400，实际 %d", rec.Code)
	}

	// 枚举外 log.level → 400
	rec = httptest.NewRecorder()
	h.Update(rec, reqWithKeyParam(service.SettingLogLevel, `{"value":"TRACE"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("枚举外 log.level 应 400，实际 %d", rec.Code)
	}
}
