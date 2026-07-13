//go:build integration

package service

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestHealthQueryDailyTablesMySQL 真 MySQL：series 跨日并表真查（缺表日跳过、按 step 聚合）+
// snapshots 真查（json 回读）+ 查询侧绝不隐式建表。
func TestHealthQueryDailyTablesMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "fr147_query")
	metricRepo := repository.NewMetricSampleV2Repository(db)
	snapshotRepo := repository.NewHealthSnapshotRepository(db)
	svc := NewHealthQueryService(healthview.NewStore(), metricwindow.New(metricwindow.DefaultCapacity), snapshotRepo, metricRepo)

	// 固定远期两相邻 UTC 日（避开其它用例的「今天」表）；中缺一日验证缺表跳过。
	// 残留日表由 testsupport.OpenTestDB 统一清理（跨运行持久，无需各测试自清）。
	d1 := time.Date(2032, 1, 10, 23, 59, 0, 0, time.UTC)
	d3 := time.Date(2032, 1, 12, 0, 1, 0, 0, time.UTC)

	// 指标：d1 两行（同 60s 桶）+ d3 一行，d2 无表。
	metricRows := []model.MetricSampleV2{
		{NamespaceID: 1, ServerID: "q-s1", Kind: model.ServerKindBackend, BucketStartMs: d1.UnixMilli(), SampleCount: 5, CPUPctAvg: 30, CPUPctMax: 40, TPSAvg: 20, TPSMin: 19, OnlineAvg: 50, OnlineMax: 55},
		{NamespaceID: 1, ServerID: "q-s1", Kind: model.ServerKindBackend, BucketStartMs: d1.UnixMilli() + 5_000, SampleCount: 5, CPUPctAvg: 50, CPUPctMax: 60, TPSAvg: 18, TPSMin: 17, OnlineAvg: 70, OnlineMax: 80},
		{NamespaceID: 1, ServerID: "q-s1", Kind: model.ServerKindBackend, BucketStartMs: d3.UnixMilli(), SampleCount: 5, CPUPctAvg: 40, CPUPctMax: 45, TPSAvg: 19, TPSMin: 18, OnlineAvg: 60, OnlineMax: 62},
	}
	if _, err := metricRepo.FlushDaily(metricRows); err != nil {
		t.Fatalf("指标预置失败: %v", err)
	}
	series, err := svc.MetricsSeries(MetricsSeriesParams{
		ServerIDs: []string{"q-s1"},
		FromMs:    d1.UnixMilli() - 1_000, ToMs: d3.UnixMilli() + 1_000, StepSec: 60,
	})
	if err != nil {
		t.Fatalf("series 查询失败: %v", err)
	}
	if len(series.Series) != 1 || len(series.Series[0].Points) != 2 {
		t.Fatalf("跨日并表应聚 2 桶，实际 %+v", series.Series)
	}
	p0 := series.Series[0].Points[0]
	if p0.CPUPctAvg != 40 || p0.CPUPctMax != 60 || p0.TPSMin != 17 || p0.OnlineMax != 80 {
		t.Fatalf("首桶聚合口径不符: %+v", p0)
	}

	// 快照：d1 两行真查回读（含 reasons json）。
	snapRows := []model.HealthSnapshot{
		{TsMs: d1.UnixMilli(), NamespaceID: 1, ServerID: "q-s1", Kind: model.ServerKindBackend, Score: 92, Level: "healthy", Schedulable: true, Reasons: "[]", Factors: "[]", WeightsRev: 1},
		{TsMs: d1.UnixMilli() + 30_000, NamespaceID: 1, ServerID: "q-s1", Kind: model.ServerKindBackend, Score: 0, Level: "unhealthy", Schedulable: false, Reasons: `["lost","unhealthy"]`, Factors: "[]", WeightsRev: 1},
	}
	if _, err := snapshotRepo.FlushDaily(snapRows); err != nil {
		t.Fatalf("快照预置失败: %v", err)
	}
	points, err := svc.HealthSnapshots("q-s1", d1.UnixMilli()-1_000, d3.UnixMilli(), false)
	if err != nil {
		t.Fatalf("snapshots 查询失败: %v", err)
	}
	if len(points) != 2 || points[0].Score != 92 || len(points[1].Reasons) != 2 || points[1].Reasons[0] != "lost" {
		t.Fatalf("快照回放点不符: %+v", points)
	}

	// 查询侧绝不隐式建表：中间缺失日与查询范围外的日表在查询后均不存在。
	missingDay := d1.AddDate(0, 0, 1)
	for _, name := range []string{
		store.DailyTableName("metric_sample", missingDay),
		store.DailyTableName("health_snapshot", missingDay),
		store.DailyTableName("health_snapshot", d3),
	} {
		if db.Migrator().HasTable(name) {
			t.Fatalf("查询侧不应隐式建表：%s 不应存在", name)
		}
	}
}
