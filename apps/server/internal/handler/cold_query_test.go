package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// newTestSettings 构造挂测试库的运维设置服务（冷查询 handler 读 archive.cold-query-max-days）。
// 空库时 GetInt 回退白名单默认（cold-query-max-days=31），足以驱动 handler 冷查询范围校验。
func newTestSettings(t *testing.T, db *gorm.DB) *service.SettingsService {
	t.Helper()
	s, err := service.NewSettingsService(db, repository.NewSettingRepository(db), repository.NewAuditLogRepository(db))
	if err != nil {
		t.Fatalf("构造测试设置服务失败: %v", err)
	}
	return s
}

// openHandlerSQLite 打开一个独立命名的内存 sqlite 库（冷查询 handler 测试用）。
func openHandlerSQLite(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })
	return db
}

// newConnColdRouter 构造挂热 + 冷双 sqlite 库的连接管理面路由，返回热 / 冷两侧仓库供造数。
func newConnColdRouter(t *testing.T, name string) (chi.Router, *repository.ConnDetailRepository, *repository.ConnDetailRepository) {
	t.Helper()
	hotDB := openHandlerSQLite(t, name+"_hot")
	arcDB := openHandlerSQLite(t, name+"_arc")
	hotRepo := repository.NewConnDetailRepository(hotDB)
	hotRepo.SetArchiveDB(arcDB)
	h := NewV2ConnectionAdminHandler(service.NewConnQueryService(hotRepo), newTestSettings(t, hotDB))
	r := chi.NewRouter()
	r.Get("/admin/v2/connections", h.List)
	return r, hotRepo, repository.NewConnDetailRepository(arcDB)
}

// TestConnColdMissingRange400 冷查询缺时间范围 → 400。
func TestConnColdMissingRange400(t *testing.T) {
	r, _, _ := newConnColdRouter(t, "conn_cold_norange")
	code, _ := getJSON(t, r, "/admin/v2/connections?includeArchived=true&serverId=proxy-1")
	if code != http.StatusBadRequest {
		t.Fatalf("缺时间范围应 400，实际 %d", code)
	}
}

// TestConnColdRangeTooWide400 冷查询跨度超 cold-query-max-days（默认 31 天）→ 400。
func TestConnColdRangeTooWide400(t *testing.T) {
	r, _, _ := newConnColdRouter(t, "conn_cold_wide")
	to := time.Now().UTC()
	from := to.Add(-40 * 24 * time.Hour) // 40 天 > 31
	url := fmt.Sprintf("/admin/v2/connections?includeArchived=true&serverId=proxy-1&from=%s&to=%s",
		from.Format(time.RFC3339), to.Format(time.RFC3339))
	code, _ := getJSON(t, r, url)
	if code != http.StatusBadRequest {
		t.Fatalf("超 31 天应 400，实际 %d", code)
	}
}

// TestConnColdUnavailable503 归档不可达（未注入 archive）时冷查询 → 503，不静默只返回热库。
func TestConnColdUnavailable503(t *testing.T) {
	r, _ := newConnAdminRouter(t, "conn_cold_503") // 该 router 的仓库未注入 archive
	base := time.Now().UTC().Add(-5 * time.Minute).UnixMilli()
	from, to := isoOf(base-time.Hour.Milliseconds()), isoOf(base+time.Hour.Milliseconds())
	url := fmt.Sprintf("/admin/v2/connections?includeArchived=true&serverId=proxy-1&from=%s&to=%s", from, to)
	code, _ := getJSON(t, r, url)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("归档不可达应 503，实际 %d", code)
	}
}

// TestConnColdMetaAndMerge 冷查询正常路径：返回 200 + includeArchived 元信息 + 跨热 / 冷归并结果。
func TestConnColdMetaAndMerge(t *testing.T) {
	r, hotRepo, arcRepo := newConnColdRouter(t, "conn_cold_ok")
	base := time.Now().UTC().Add(-10 * time.Minute).UnixMilli()
	seedConn(t, hotRepo, uuidV7AtHandler(base+100, "h1"), "proxy-1", "p-1", base+100, false, "")
	seedConn(t, arcRepo, uuidV7AtHandler(base+200, "a1"), "proxy-1", "p-2", base+200, false, "")

	from, to := isoOf(base-time.Hour.Milliseconds()), isoOf(base+time.Hour.Milliseconds())
	url := fmt.Sprintf("/admin/v2/connections?includeArchived=true&serverId=proxy-1&from=%s&to=%s", from, to)
	code, body := getJSON(t, r, url)
	if code != http.StatusOK {
		t.Fatalf("冷查询应 200，实际 %d：%v", code, body)
	}
	if body["includeArchived"] != true {
		t.Fatalf("响应应带 includeArchived=true 元信息，实际 %v", body["includeArchived"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("应跨热 / 冷归并 2 条，实际 %d", len(items))
	}
	// 降序：归档的 a1(base+200) 在前、热的 h1(base+100) 在后。
	first, _ := items[0].(map[string]any)
	if first["connId"] != uuidV7AtHandler(base+200, "a1") {
		t.Fatalf("归并降序首条应为归档 a1，实际 %v", first["connId"])
	}
}
