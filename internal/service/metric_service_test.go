package service

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
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
