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

// TestEnsureDailyTableCacheRetainsDBIdentity 校验缓存键强持有数据库身份，而非仅保存可被复用的指针地址字符串。
// 这是关闭旧连接、打开新库后仍能正确建表的前提；多 DB / 归档隔离由上一个用例验证。
func TestEnsureDailyTableCacheRetainsDBIdentity(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db := openMemSQLite(t, "daily_identity")
	day := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

	name, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day)
	if err != nil {
		t.Fatalf("建日表失败: %v", err)
	}
	found := false
	ensuredDailyTables.Range(func(key, _ any) bool {
		cacheKey, ok := key.(dailyTableCacheKey)
		if ok && cacheKey.db == db && cacheKey.table == name {
			found = true
			return false
		}
		return true
	})
	if !found {
		t.Fatalf("日表缓存键应强持有数据库连接身份")
	}
}

// dailyProbeV1 模拟旧版模型（列较少）；与 dailyProbeV2 共用同一基表名，供加列迁移测试。
type dailyProbeV1 struct {
	ID string `gorm:"column:id;size:36;primaryKey"`
}

func (dailyProbeV1) TableName() string { return "daily_probe_migrate" }

// dailyProbeV2 模拟新版模型：在旧表基础上新增可空列（FR-180 广播聚合列的抽象化形态）。
type dailyProbeV2 struct {
	ID    string `gorm:"column:id;size:36;primaryKey"`
	Extra *int   `gorm:"column:extra"`
}

func (dailyProbeV2) TableName() string { return "daily_probe_migrate" }

// TestEnsureDailyTableAddsMissingColumns 校验存量日表（旧版二进制建出、缺新列）在进程重启后
// 首次触达时按当前模型补齐缺失列（GORM 加列、零方言），新列可写可读（FR-180 加列向后兼容）。
func TestEnsureDailyTableAddsMissingColumns(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db := openMemSQLite(t, "daily_addcol")
	day := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)

	// 旧版模型建出当日表（模拟升级前的二进制）。
	name, err := EnsureDailyTable(db, &dailyProbeV1{}, day)
	if err != nil {
		t.Fatalf("旧版建表失败: %v", err)
	}
	// 清缓存模拟进程重启（升级换新二进制后缓存为空、表已存在）。
	resetDailyTableCacheForTest()

	if _, err := EnsureDailyTable(db, &dailyProbeV2{}, day); err != nil {
		t.Fatalf("新版触达存量表应补列而非报错: %v", err)
	}
	if !db.Table(name).Migrator().HasColumn(&dailyProbeV2{}, "extra") {
		t.Fatalf("存量日表应被补上新增列 extra")
	}
	// 新列可写可读（升级当日聚合行落库不被缺列卡死）。
	extra := 7
	if err := db.Table(name).Create(&dailyProbeV2{ID: "row-1", Extra: &extra}).Error; err != nil {
		t.Fatalf("补列后写入含新列的行失败: %v", err)
	}
	var got dailyProbeV2
	if err := db.Table(name).Where("id = ?", "row-1").Take(&got).Error; err != nil || got.Extra == nil || *got.Extra != 7 {
		t.Fatalf("回读新列失败: %+v err=%v", got, err)
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
