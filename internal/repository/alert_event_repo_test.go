package repository

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
)

// newAlertEventTestDB 打开私有内存 sqlite 并迁移 alert_event，供过滤/分页单测（不依赖 MySQL/DSN）。
// 用私有 dsn（非 shared cache）隔离，避免与其它仓库单测共享内存库串扰。
func newAlertEventTestDB(t *testing.T) *AlertEventRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:alertevent_"+t.Name()+"?mode=memory"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AlertEvent{}); err != nil {
		t.Fatalf("迁移 alert_event 失败: %v", err)
	}
	return NewAlertEventRepository(db)
}

func seedAlertEvent(t *testing.T, r *AlertEventRepository, typ, level, ns, serverID string, at time.Time) {
	t.Helper()
	if err := r.Create(&model.AlertEvent{
		Type: typ, Level: level, Namespace: ns, ServerID: serverID,
		Message: serverID + " online → " + level, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写告警事件失败: %v", err)
	}
}

// TestAlertEventListTimeDesc 无过滤按时间倒序返回全部，total 正确。
func TestAlertEventListTimeDesc(t *testing.T) {
	r := newAlertEventTestDB(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "a", base)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelCritical, "prod", "b", base.Add(time.Minute))

	items, total, err := r.List(AlertEventFilter{Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("应 2 条，实际 total=%d len=%d", total, len(items))
	}
	if items[0].ServerID != "b" {
		t.Fatalf("时间倒序最新应为 b，实际 %s", items[0].ServerID)
	}
}

// TestAlertEventListFilters 类型/级别/环境/时间过滤各自正确。
func TestAlertEventListFilters(t *testing.T) {
	r := newAlertEventTestDB(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "a", base)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelCritical, "prod", "b", base.Add(time.Minute))
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelCritical, "dev", "c", base.Add(2*time.Minute))

	// 级别过滤
	_, total, _ := r.List(AlertEventFilter{Level: model.AlertLevelCritical, Page: 1, Size: 20})
	if total != 2 {
		t.Fatalf("critical 应 2 条，实际 %d", total)
	}
	// 环境过滤
	_, total, _ = r.List(AlertEventFilter{Namespace: "dev", Page: 1, Size: 20})
	if total != 1 {
		t.Fatalf("dev 应 1 条，实际 %d", total)
	}
	// 类型过滤（不存在的类型）
	_, total, _ = r.List(AlertEventFilter{Type: model.AlertEventTypePublishFail, Page: 1, Size: 20})
	if total != 0 {
		t.Fatalf("publish-fail 应 0 条，实际 %d", total)
	}
	// 时间过滤：from 取第二条起
	from := base.Add(30 * time.Second)
	_, total, _ = r.List(AlertEventFilter{From: from, Page: 1, Size: 20})
	if total != 2 {
		t.Fatalf("from 过滤应 2 条，实际 %d", total)
	}
}

// TestAlertEventListPagination 分页：每页 1 条、共 3 条 → 第 2 页取中间一条。
func TestAlertEventListPagination(t *testing.T) {
	r := newAlertEventTestDB(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "a", base)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "b", base.Add(time.Minute))
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "c", base.Add(2*time.Minute))

	items, total, err := r.List(AlertEventFilter{Page: 2, Size: 1})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 3 || len(items) != 1 {
		t.Fatalf("应 total=3 当页 1 条，实际 total=%d len=%d", total, len(items))
	}
	// 时间倒序 c,b,a → 第 2 页（size=1）为 b
	if items[0].ServerID != "b" {
		t.Fatalf("第 2 页应为 b，实际 %s", items[0].ServerID)
	}
}
