package repository

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

func openRepoSQLite(t *testing.T, name string) *gorm.DB {
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

// bucketMs 返回某 UTC 日 hour:00 对应的 5s 桶起点毫秒。
func bucketMs(day time.Time, offsetSec int) int64 {
	return day.Add(time.Duration(offsetSec) * time.Second).UnixMilli()
}

func rowAt(server string, bucket int64) model.MetricSampleV2 {
	return model.MetricSampleV2{
		NamespaceID: 1, ServerID: server, Kind: model.ServerKindBackend,
		BucketStartMs: bucket, SampleCount: 5, TPSAvg: 20, CPUPctAvg: 30,
	}
}

func countDaily(t *testing.T, db *gorm.DB, name string) int64 {
	t.Helper()
	var n int64
	if err := db.Table(name).Count(&n).Error; err != nil {
		t.Fatalf("统计 %s 行数失败: %v", name, err)
	}
	return n
}

// TestFlushDailyIdempotentDedup 校验唯一键 (server_id, bucket_start_ms) 幂等去重：重放同批被去重、行数不增。
func TestFlushDailyIdempotentDedup(t *testing.T) {
	db := openRepoSQLite(t, "metric_v2_dedup")
	repo := NewMetricSampleV2Repository(db)
	day := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	name := store.DailyTableName("metric_sample", day)

	batch := []model.MetricSampleV2{
		rowAt("s1", bucketMs(day, 0)),
		rowAt("s1", bucketMs(day, 5)),
		rowAt("s2", bucketMs(day, 0)),
	}
	dedup, err := repo.FlushDaily(batch)
	if err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if dedup != 0 {
		t.Fatalf("首次写入不应有去重，实际 %d", dedup)
	}
	if got := countDaily(t, db, name); got != 3 {
		t.Fatalf("首次应落 3 行，实际 %d", got)
	}

	// 重放：同批再写，唯一键冲突全去重、行数不增。
	dedup2, err := repo.FlushDaily(batch)
	if err != nil {
		t.Fatalf("重放写入失败: %v", err)
	}
	if dedup2 != 3 {
		t.Fatalf("重放应全部去重（deduplicated=3），实际 %d", dedup2)
	}
	if got := countDaily(t, db, name); got != 3 {
		t.Fatalf("重放后行数应仍为 3，实际 %d", got)
	}

	// 部分新 + 部分重放：仅新桶入库，重叠去重。
	mixed := []model.MetricSampleV2{
		rowAt("s1", bucketMs(day, 0)),  // 重放
		rowAt("s1", bucketMs(day, 10)), // 新
	}
	dedup3, err := repo.FlushDaily(mixed)
	if err != nil {
		t.Fatalf("混合写入失败: %v", err)
	}
	if dedup3 != 1 {
		t.Fatalf("混合应去重 1 条，实际 %d", dedup3)
	}
	if got := countDaily(t, db, name); got != 4 {
		t.Fatalf("混合后行数应为 4，实际 %d", got)
	}
}

// TestFlushDailyCrossDaySplit 校验跨日批自动拆分：不同 UTC 日的行分别落各自当日表。
func TestFlushDailyCrossDaySplit(t *testing.T) {
	db := openRepoSQLite(t, "metric_v2_crossday")
	repo := NewMetricSampleV2Repository(db)
	d1 := time.Date(2026, 7, 11, 23, 59, 55, 0, time.UTC)
	d2 := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	n1 := store.DailyTableName("metric_sample", d1)
	n2 := store.DailyTableName("metric_sample", d2)

	batch := []model.MetricSampleV2{
		rowAt("s1", d1.UnixMilli()),                    // 落 07-11 表
		rowAt("s1", d2.UnixMilli()),                    // 落 07-12 表
		rowAt("s1", d2.Add(5*time.Second).UnixMilli()), // 落 07-12 表
	}
	if _, err := repo.FlushDaily(batch); err != nil {
		t.Fatalf("跨日写入失败: %v", err)
	}
	if !db.Migrator().HasTable(n1) || !db.Migrator().HasTable(n2) {
		t.Fatalf("跨日应各建一张日表：%s / %s", n1, n2)
	}
	if got := countDaily(t, db, n1); got != 1 {
		t.Fatalf("07-11 表应 1 行，实际 %d", got)
	}
	if got := countDaily(t, db, n2); got != 2 {
		t.Fatalf("07-12 表应 2 行，实际 %d", got)
	}
}

// TestFlushDailyEmpty 空批为安全空操作。
func TestFlushDailyEmpty(t *testing.T) {
	db := openRepoSQLite(t, "metric_v2_empty")
	repo := NewMetricSampleV2Repository(db)
	dedup, err := repo.FlushDaily(nil)
	if err != nil || dedup != 0 {
		t.Fatalf("空批应无错、去重 0，实际 err=%v dedup=%d", err, dedup)
	}
}
