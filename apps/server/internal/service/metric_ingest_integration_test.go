//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// dropDaily 删掉某 UTC 日的指标日表，给集成用例干净起点（表在库间持久，跨运行须先清）。
func dropDaily(t *testing.T, db *gorm.DB, day time.Time) string {
	t.Helper()
	name := store.DailyTableName("metric_sample", day)
	_ = db.Migrator().DropTable(name)
	return name
}

// waitDailyRows 轮询等待某日表行数达到期望（异步写入池 flush 后落库）。
func waitDailyRows(t *testing.T, db *gorm.DB, name string, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !db.Migrator().HasTable(name) {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var n int64
		if err := db.Table(name).Count(&n).Error; err == nil && n == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	var n int64
	if db.Migrator().HasTable(name) {
		_ = db.Table(name).Count(&n).Error
	}
	t.Fatalf("超时：%s 期望 %d 行，实际 %d", name, want, n)
}

// TestMetricIngestAsyncWriteAndReplayDedupMySQL 真 MySQL：日表按需建 + 异步批量事务写 + 唯一键幂等去重（重放）。
func TestMetricIngestAsyncWriteAndReplayDedupMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "metric_ingest")
	now := time.Now().UTC()
	name := dropDaily(t, db, now)

	repo := repository.NewMetricSampleV2Repository(db)
	window := metricwindow.New(metricwindow.DefaultCapacity)
	writer := NewMetricIngestWriter(repo)
	writer.flushInterval = 100 * time.Millisecond // 加速 flush
	svc := NewMetricIngestService(window, writer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go writer.Run(ctx)

	id := agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "int-s1", Kind: model.ServerKindBackend}
	bucket := (now.UnixMilli() / 5000) * 5000
	samples := []MetricReportSample{
		{BucketStartMs: bucket, SampleCount: 5, TPSAvg: 20, CPUPctAvg: 30},
		{BucketStartMs: bucket + 5000, SampleCount: 5, TPSAvg: 19, CPUPctAvg: 35},
	}
	res, err := svc.Ingest(MetricReportParams{Identity: id, AgentTimeMs: now.UnixMilli(), Samples: samples})
	if err != nil {
		t.Fatalf("首次上报失败: %v", err)
	}
	if res.Accepted != 2 || res.Deduplicated != 0 {
		t.Fatalf("首次应接收 2，实际 %+v", res)
	}
	waitDailyRows(t, db, name, 2)

	// 重放同批：60s 窗口去重（synchronously accepted=0），日表行数不增（DB 唯一键幂等兜底）。
	res2, err := svc.Ingest(MetricReportParams{Identity: id, AgentTimeMs: now.UnixMilli(), Samples: samples})
	if err != nil {
		t.Fatalf("重放上报失败: %v", err)
	}
	if res2.Accepted != 0 || res2.Deduplicated != 2 {
		t.Fatalf("重放应全部去重，实际 %+v", res2)
	}
	// 直接经 repo 重放（绕过窗口）验证 DB 唯一键幂等：deduplicated 计数正确、行数不增。
	rows := []model.MetricSampleV2{
		{NamespaceID: 1, ServerID: "int-s1", Kind: model.ServerKindBackend, BucketStartMs: bucket, SampleCount: 5},
	}
	dedup, err := repo.FlushDaily(rows)
	if err != nil {
		t.Fatalf("repo 重放失败: %v", err)
	}
	if dedup != 1 {
		t.Fatalf("DB 唯一键应去重 1，实际 %d", dedup)
	}
	waitDailyRows(t, db, name, 2)
}

// TestMetricIngestQueueFull429MySQL 真 MySQL 装配下队列满回 429（不启 worker、填满队列）。
func TestMetricIngestQueueFull429MySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "metric_ingest")
	now := time.Now().UTC()
	dropDaily(t, db, now)

	repo := repository.NewMetricSampleV2Repository(db)
	window := metricwindow.New(metricwindow.DefaultCapacity)
	writer := NewMetricIngestWriter(repo)
	writer.queue = make(chan []model.MetricSampleV2, 1) // 极小队列
	// 不启动 worker：队列不会被消费。
	writer.Enqueue([]model.MetricSampleV2{{ServerID: "filler"}}) // 填满

	svc := NewMetricIngestService(window, writer)
	id := agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "int-s2", Kind: model.ServerKindBackend}
	bucket := (now.UnixMilli() / 5000) * 5000
	_, err := svc.Ingest(MetricReportParams{
		Identity: id, AgentTimeMs: now.UnixMilli(),
		Samples: []MetricReportSample{{BucketStartMs: bucket, SampleCount: 5}},
	})
	if err != apperr.ErrMetricsIngestBusy {
		t.Fatalf("队列满应回 metrics_ingest_busy，实际 %v", err)
	}
}

// TestMetricFlushCrossDaySplitMySQL 真 MySQL：跨日批自动拆分到各自当日表。
func TestMetricFlushCrossDaySplitMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "metric_ingest")
	// 取两个相邻 UTC 日（用远期固定日期避免与其它用例的「今天」表撞行）。
	d1 := time.Date(2031, 3, 3, 23, 59, 55, 0, time.UTC)
	d2 := time.Date(2031, 3, 4, 0, 0, 0, 0, time.UTC)
	n1 := dropDaily(t, db, d1)
	n2 := dropDaily(t, db, d2)

	repo := repository.NewMetricSampleV2Repository(db)
	rows := []model.MetricSampleV2{
		{NamespaceID: 1, ServerID: "cd-s1", Kind: model.ServerKindBackend, BucketStartMs: d1.UnixMilli(), SampleCount: 5},
		{NamespaceID: 1, ServerID: "cd-s1", Kind: model.ServerKindBackend, BucketStartMs: d2.UnixMilli(), SampleCount: 5},
		{NamespaceID: 1, ServerID: "cd-s1", Kind: model.ServerKindBackend, BucketStartMs: d2.Add(5 * time.Second).UnixMilli(), SampleCount: 5},
	}
	if _, err := repo.FlushDaily(rows); err != nil {
		t.Fatalf("跨日写入失败: %v", err)
	}
	waitDailyRows(t, db, n1, 1)
	waitDailyRows(t, db, n2, 2)
}
