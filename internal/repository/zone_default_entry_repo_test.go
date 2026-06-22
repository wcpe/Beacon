package repository

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
)

// newDefaultEntryTestDB 打开内存 sqlite 并迁移 zone_default_entry（不依赖 MySQL/DSN）。
func newDefaultEntryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ZoneDefaultEntry{}); err != nil {
		t.Fatalf("迁移 zone_default_entry 失败: %v", err)
	}
	if err := db.Exec("DELETE FROM zone_default_entry").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return db
}

// TestDefaultEntryUpsertUnique 验证：同 (ns, group, zone) 仅一条，重复 Upsert 覆盖而非新增。
func TestDefaultEntryUpsertUnique(t *testing.T) {
	r := NewZoneDefaultEntryRepository(newDefaultEntryTestDB(t))

	if _, err := r.Upsert("prod", "area1", "zoneA", "lobby-1"); err != nil {
		t.Fatalf("首次 Upsert 失败: %v", err)
	}
	// 覆盖为另一个 serverId
	if _, err := r.Upsert("prod", "area1", "zoneA", "lobby-2"); err != nil {
		t.Fatalf("覆盖 Upsert 失败: %v", err)
	}

	got, err := r.FindByZone("prod", "area1", "zoneA")
	if err != nil || got == nil {
		t.Fatalf("FindByZone 失败: err=%v got=%v", err, got)
	}
	if got.DefaultServerID != "lobby-2" {
		t.Fatalf("覆盖后默认入口应为 lobby-2，实际 %s", got.DefaultServerID)
	}

	list, err := r.List("prod", "")
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("同 zone 重复 Upsert 应仅 1 条，实际 %d", len(list))
	}
}

// TestDefaultEntryFindAndDelete 验证 FindByZone 未命中返回 nil、Delete 命中与未命中。
func TestDefaultEntryFindAndDelete(t *testing.T) {
	r := NewZoneDefaultEntryRepository(newDefaultEntryTestDB(t))

	if got, err := r.FindByZone("prod", "area1", "zoneA"); err != nil || got != nil {
		t.Fatalf("未设默认入口应返回 (nil, nil)，实际 got=%v err=%v", got, err)
	}

	if _, err := r.Upsert("prod", "area1", "zoneA", "lobby-1"); err != nil {
		t.Fatalf("Upsert 失败: %v", err)
	}
	deleted, err := r.Delete("prod", "area1", "zoneA")
	if err != nil || !deleted {
		t.Fatalf("Delete 应命中，实际 deleted=%v err=%v", deleted, err)
	}
	// 再删不命中
	deleted, err = r.Delete("prod", "area1", "zoneA")
	if err != nil || deleted {
		t.Fatalf("重复 Delete 应不命中，实际 deleted=%v err=%v", deleted, err)
	}
}

// TestDefaultEntryListFilter 验证 List 按 ns / group 过滤与稳定排序。
func TestDefaultEntryListFilter(t *testing.T) {
	r := NewZoneDefaultEntryRepository(newDefaultEntryTestDB(t))
	mustUpsert := func(ns, g, z, s string) {
		if _, err := r.Upsert(ns, g, z, s); err != nil {
			t.Fatalf("Upsert %s/%s/%s 失败: %v", ns, g, z, err)
		}
	}
	mustUpsert("prod", "area2", "zoneB", "b-1")
	mustUpsert("prod", "area1", "zoneA", "a-1")
	mustUpsert("test", "area1", "zoneA", "t-1")

	prod, err := r.List("prod", "")
	if err != nil {
		t.Fatalf("List prod 失败: %v", err)
	}
	if len(prod) != 2 {
		t.Fatalf("prod 应 2 条，实际 %d", len(prod))
	}
	// 排序：area1 在 area2 前
	if prod[0].GroupCode != "area1" || prod[1].GroupCode != "area2" {
		t.Fatalf("List 应按 group 升序，实际 %s,%s", prod[0].GroupCode, prod[1].GroupCode)
	}

	area1, err := r.List("prod", "area1")
	if err != nil {
		t.Fatalf("List prod/area1 失败: %v", err)
	}
	if len(area1) != 1 || area1[0].DefaultServerID != "a-1" {
		t.Fatalf("prod/area1 应 1 条 a-1，实际 %+v", area1)
	}
}
