package service

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
)

// newTrendTestService 构造一个背靠内存 sqlite 的 MetricService，供趋势时间窗校验单测（不依赖 MySQL/DSN）。
func newTrendTestService(t *testing.T) *MetricService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.MetricSample{}); err != nil {
		t.Fatalf("迁移 metric_sample 失败: %v", err)
	}
	// 清表，避免共享内存库残留串扰。
	if err := db.Exec("DELETE FROM metric_sample").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return NewMetricService(runtime.NewRegistry(), repository.NewMetricSampleRepository(db))
}

// newTrendTestServiceWithDB 同 newTrendTestService，但额外返回底层 *gorm.DB 以便单测播种样本。
func newTrendTestServiceWithDB(t *testing.T) (*MetricService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.MetricSample{}); err != nil {
		t.Fatalf("迁移 metric_sample 失败: %v", err)
	}
	if err := db.Exec("DELETE FROM metric_sample").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return NewMetricService(runtime.NewRegistry(), repository.NewMetricSampleRepository(db)), db
}

// TestTrendEmptyNamespaceAggregatesAllEnvironments 验证 X1：namespace 为空时趋势聚合全部环境样本，
// 不再返回参数错误，且同桶内跨环境样本被合并（总人数求和）。
func TestTrendEmptyNamespaceAggregatesAllEnvironments(t *testing.T) {
	svc, db := newTrendTestServiceWithDB(t)
	at := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	// 两个环境各一条同一时刻的样本，落同一桶。
	seed := []model.MetricSample{
		{Namespace: "prod", ServerID: "p-1", Role: roleBukkit, SampledAt: at, PlayerCount: 30, TPS: 20.0, MemUsed: 100, MemMax: 1000, CPULoad: 0.4},
		{Namespace: "test", ServerID: "t-1", Role: roleBukkit, SampledAt: at, PlayerCount: 12, TPS: 18.0, MemUsed: 300, MemMax: 1000, CPULoad: 0.6},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("播种样本失败: %v", err)
	}

	// 空 namespace + 覆盖该时刻的自定义窗。
	points, err := svc.Trend(TrendQuery{
		From: at.Add(-time.Minute), To: at.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("空 namespace 趋势不应报错，实际 %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("两环境同桶应聚合为 1 点，实际 %d", len(points))
	}
	// 跨环境总人数求和：30+12=42；平均 TPS=(20+18)/2=19。
	if points[0].TotalPlayers != 42 {
		t.Fatalf("跨环境总人数应为 42，实际 %d", points[0].TotalPlayers)
	}
	if !floatEq(points[0].AvgTPS, 19.0) {
		t.Fatalf("跨环境平均 TPS 应为 19.0，实际 %v", points[0].AvgTPS)
	}
}

// TestTrendNamespaceFiltersWhenProvided 验证给定 namespace 时只返回该环境样本（空 namespace 才聚合全部）。
func TestTrendNamespaceFiltersWhenProvided(t *testing.T) {
	svc, db := newTrendTestServiceWithDB(t)
	at := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	seed := []model.MetricSample{
		{Namespace: "prod", ServerID: "p-1", Role: roleBukkit, SampledAt: at, PlayerCount: 30, TPS: 20.0, MemUsed: 100, MemMax: 1000, CPULoad: 0.4},
		{Namespace: "test", ServerID: "t-1", Role: roleBukkit, SampledAt: at, PlayerCount: 12, TPS: 18.0, MemUsed: 300, MemMax: 1000, CPULoad: 0.6},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("播种样本失败: %v", err)
	}

	points, err := svc.Trend(TrendQuery{Namespace: "prod", From: at.Add(-time.Minute), To: at.Add(time.Minute)})
	if err != nil {
		t.Fatalf("指定 namespace 趋势不应报错，实际 %v", err)
	}
	if len(points) != 1 || points[0].TotalPlayers != 30 {
		t.Fatalf("指定 prod 应只含其样本（人数 30），实际 %+v", points)
	}
}

// TestTrendRejectsOversizedCustomWindow 自定义 from/to 跨度超过上限（7 天）时拒绝，返回参数错误。
// 防超大区间把 metric_sample 全量加载入内存。
func TestTrendRejectsOversizedCustomWindow(t *testing.T) {
	svc := newTrendTestService(t)
	to := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	// 跨度 = 上限 + 1 秒，刚好越界。
	from := to.Add(-(maxCustomWindow + time.Second))

	_, err := svc.Trend(TrendQuery{Namespace: "prod", From: from, To: to})
	if !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("超限自定义时间窗应返回 ErrInvalidParam，实际 %v", err)
	}
}

// TestTrendAcceptsCustomWindowAtLimit 自定义 from/to 跨度恰为上限时通过（边界内不拒绝）。
func TestTrendAcceptsCustomWindowAtLimit(t *testing.T) {
	svc := newTrendTestService(t)
	to := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	// 跨度恰等于上限，应放行。
	from := to.Add(-maxCustomWindow)

	points, err := svc.Trend(TrendQuery{Namespace: "prod", From: from, To: to})
	if err != nil {
		t.Fatalf("上限内自定义时间窗不应被拒，实际 %v", err)
	}
	// 空库下无样本，应返回空序列（非 nil 校验在 Downsample 已保证）。
	if len(points) != 0 {
		t.Fatalf("空库应返回空趋势序列，实际 %d 点", len(points))
	}
}
