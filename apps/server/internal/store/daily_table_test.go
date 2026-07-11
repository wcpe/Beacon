package store

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// openMemSQLite 打开一个独立命名的内存 sqlite 库（供日表基建单测隔离）。
func openMemSQLite(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })
	return db
}

// TestDailyTableName 校验日表名按 UTC 日期后缀拼接（纯函数）。
func TestDailyTableName(t *testing.T) {
	// 用带非零偏移时区的时刻，验证按 UTC 归日（+08:00 的 07-12 00:30 其 UTC 仍是 07-11 16:30）。
	loc := time.FixedZone("UTC+8", 8*3600)
	day := time.Date(2026, 7, 12, 0, 30, 0, 0, loc)
	if got := DailyTableName("metric_sample", day); got != "metric_sample_20260711" {
		t.Fatalf("日表名应按 UTC 归日为 metric_sample_20260711，实际 %s", got)
	}
}

// TestEnsureDailyTableCreatesAndCaches 校验按需建表 + 进程缓存短路（缓存后不再探测 / 建表）。
func TestEnsureDailyTableCreatesAndCaches(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db := openMemSQLite(t, "daily_cache_test")
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

	name, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day)
	if err != nil {
		t.Fatalf("首次建日表失败: %v", err)
	}
	if name != "metric_sample_20260711" {
		t.Fatalf("日表名不符，实际 %s", name)
	}
	if !db.Migrator().HasTable(name) {
		t.Fatalf("首次调用后日表应已存在")
	}

	// 手动删表后再调：命中缓存则短路、不重建——HasTable 应仍为 false，证明缓存生效。
	if err := db.Migrator().DropTable(name); err != nil {
		t.Fatalf("删表失败: %v", err)
	}
	name2, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day)
	if err != nil {
		t.Fatalf("缓存命中调用不应报错: %v", err)
	}
	if name2 != name {
		t.Fatalf("缓存命中应返回同名，实际 %s", name2)
	}
	if db.Migrator().HasTable(name) {
		t.Fatalf("缓存应短路建表——删表后不重建，实际表又存在，说明未命中缓存")
	}
}

// TestEnsureDailyTableIsolatesByDB 校验缓存按 db 身份隔离：同名日表在不同库需各自建。
func TestEnsureDailyTableIsolatesByDB(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db1 := openMemSQLite(t, "daily_iso_1")
	db2 := openMemSQLite(t, "daily_iso_2")
	day := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

	name, err := EnsureDailyTable(db1, &model.MetricSampleV2{}, day)
	if err != nil {
		t.Fatalf("db1 建表失败: %v", err)
	}
	// db2 用同一日期：缓存键含 db 身份，不应命中 db1 的缓存，必须在 db2 真正建表。
	if _, err := EnsureDailyTable(db2, &model.MetricSampleV2{}, day); err != nil {
		t.Fatalf("db2 建表失败: %v", err)
	}
	if !db2.Migrator().HasTable(name) {
		t.Fatalf("db2 应独立建出日表 %s（缓存未按 db 隔离会漏建）", name)
	}
}

// TestEnsureDailyTableCrossDaySameDB 校验同库跨日建多张日表不冲突（composite 空名索引按表名自动命名）。
func TestEnsureDailyTableCrossDaySameDB(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db := openMemSQLite(t, "daily_crossday")
	d1 := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)

	n1, err := EnsureDailyTable(db, &model.MetricSampleV2{}, d1)
	if err != nil {
		t.Fatalf("建 d1 表失败: %v", err)
	}
	n2, err := EnsureDailyTable(db, &model.MetricSampleV2{}, d2)
	if err != nil {
		t.Fatalf("建 d2 表失败（索引名跨日冲突？）: %v", err)
	}
	if n1 == n2 || !db.Migrator().HasTable(n1) || !db.Migrator().HasTable(n2) {
		t.Fatalf("跨日应建出两张不同日表，实际 n1=%s n2=%s", n1, n2)
	}
}
