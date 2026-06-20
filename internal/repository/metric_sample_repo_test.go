package repository

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"beacon/internal/model"
)

// newMetricTestDB 打开内存 sqlite 并迁移 metric_sample，供时间窗查询 / 保留期清理单测（不依赖 MySQL/DSN）。
func newMetricTestDB(t *testing.T) *gorm.DB {
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
	return db
}

// TestInsertBatchAndRoundTrip 验证批量插入后字段可移植往返（含 int64 内存与 float64 CPU），并能整批写入。
func TestInsertBatchAndRoundTrip(t *testing.T) {
	r := NewMetricSampleRepository(newMetricTestDB(t))
	at := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		{Namespace: "prod", ServerID: "lobby-1", SampledAt: at, PlayerCount: 42, TPS: 19.9, MemUsed: 128 << 20, MemMax: 512 << 20, CpuLoad: 0.35},
		{Namespace: "prod", ServerID: "lobby-2", SampledAt: at, PlayerCount: 7, TPS: 20.0, MemUsed: 64 << 20, MemMax: 256 << 20, CpuLoad: -1.0},
	}
	if err := r.InsertBatch(samples); err != nil {
		t.Fatalf("批量插入失败: %v", err)
	}

	got, err := r.Query("prod", "", at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应查到 2 条样本，实际 %d", len(got))
	}
	// 校验 int64 与 float64 字段经 GORM 抽象往返无损（含 CPU 不可用哨兵 -1.0）。
	byServer := map[string]model.MetricSample{}
	for _, s := range got {
		byServer[s.ServerID] = s
	}
	s1 := byServer["lobby-1"]
	if s1.PlayerCount != 42 || s1.TPS != 19.9 || s1.MemUsed != 128<<20 || s1.MemMax != 512<<20 || s1.CpuLoad != 0.35 {
		t.Fatalf("lobby-1 字段往返错误：%+v", s1)
	}
	if byServer["lobby-2"].CpuLoad != -1.0 {
		t.Fatalf("CPU 不可用哨兵 -1.0 应往返无损，实际 %v", byServer["lobby-2"].CpuLoad)
	}
}

// TestInsertBatchEmptyNoop 空批不触发写入也不报错（采样无在线实例时的安全路径）。
func TestInsertBatchEmptyNoop(t *testing.T) {
	r := NewMetricSampleRepository(newMetricTestDB(t))
	if err := r.InsertBatch(nil); err != nil {
		t.Fatalf("空批应无操作且无错误，实际 %v", err)
	}
	if err := r.InsertBatch([]model.MetricSample{}); err != nil {
		t.Fatalf("空切片应无操作且无错误，实际 %v", err)
	}
}

// TestQueryByTimeWindow 验证仅返回时间窗内样本（含边界），窗外样本被排除。
func TestQueryByTimeWindow(t *testing.T) {
	r := NewMetricSampleRepository(newMetricTestDB(t))
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	seedMetric(t, r, "prod", "lobby-1", base.Add(-2*time.Hour)) // 窗外（早）
	seedMetric(t, r, "prod", "lobby-1", base)                   // 窗内（左边界）
	seedMetric(t, r, "prod", "lobby-1", base.Add(time.Hour))    // 窗内（右边界）
	seedMetric(t, r, "prod", "lobby-1", base.Add(2*time.Hour))  // 窗外（晚）

	got, err := r.Query("prod", "", base, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("时间窗 [base, base+1h] 应含 2 条（含边界），实际 %d", len(got))
	}
	// 结果应按时间升序便于出图。
	if !got[0].SampledAt.Before(got[1].SampledAt) {
		t.Fatalf("查询结果应按 sampledAt 升序，实际 %v / %v", got[0].SampledAt, got[1].SampledAt)
	}
}

// TestQueryByServerFilter 验证可选 serverId 过滤：指定 serverId 只返回该服样本，空则返回全部。
func TestQueryByServerFilter(t *testing.T) {
	r := NewMetricSampleRepository(newMetricTestDB(t))
	at := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	seedMetric(t, r, "prod", "lobby-1", at)
	seedMetric(t, r, "prod", "lobby-2", at)
	seedMetric(t, r, "dev", "lobby-1", at) // 不同 namespace 不应混入

	from, to := at.Add(-time.Minute), at.Add(time.Minute)
	if got, _ := r.Query("prod", "lobby-1", from, to); len(got) != 1 || got[0].ServerID != "lobby-1" {
		t.Fatalf("serverId=lobby-1 过滤应只返回 1 条，实际 %v", got)
	}
	if got, _ := r.Query("prod", "", from, to); len(got) != 2 {
		t.Fatalf("空 serverId 应返回该 namespace 全部 2 条，实际 %d", len(got))
	}
}

// TestQueryEmptyNamespaceAcrossEnvironments 验证 X1：namespace 为空时跨全部环境查询；非空时仅限该环境。
func TestQueryEmptyNamespaceAcrossEnvironments(t *testing.T) {
	r := NewMetricSampleRepository(newMetricTestDB(t))
	at := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	seedMetric(t, r, "prod", "p-1", at)
	seedMetric(t, r, "test", "t-1", at)

	from, to := at.Add(-time.Minute), at.Add(time.Minute)
	// 空 namespace：跨环境返回全部。
	if got, _ := r.Query("", "", from, to); len(got) != 2 {
		t.Fatalf("空 namespace 应跨环境返回全部 2 条，实际 %d", len(got))
	}
	// 非空 namespace：仅限该环境。
	if got, _ := r.Query("prod", "", from, to); len(got) != 1 || got[0].Namespace != "prod" {
		t.Fatalf("namespace=prod 应只返回该环境样本，实际 %v", got)
	}
}

// TestDeleteBefore 验证保留期清理：删除早于 cutoff 的样本，cutoff 当刻及之后保留。
func TestDeleteBefore(t *testing.T) {
	r := NewMetricSampleRepository(newMetricTestDB(t))
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	seedMetric(t, r, "prod", "lobby-1", base.Add(-3*time.Hour)) // 过期
	seedMetric(t, r, "prod", "lobby-1", base.Add(-2*time.Hour)) // 过期
	seedMetric(t, r, "prod", "lobby-1", base.Add(-time.Minute)) // 保留

	cutoff := base.Add(-time.Hour)
	deleted, err := r.DeleteBefore(cutoff)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("应删除 2 条过期样本，实际删除 %d", deleted)
	}
	remain, _ := r.Query("prod", "", base.Add(-24*time.Hour), base)
	if len(remain) != 1 {
		t.Fatalf("清理后应剩 1 条，实际 %d", len(remain))
	}
}

// seedMetric 写入一条样本（固定典型负载值，仅 sampledAt / serverId / namespace 变化）。
func seedMetric(t *testing.T, r *MetricSampleRepository, ns, serverID string, at time.Time) {
	t.Helper()
	if err := r.InsertBatch([]model.MetricSample{
		{Namespace: ns, ServerID: serverID, SampledAt: at, PlayerCount: 10, TPS: 20.0, MemUsed: 1 << 20, MemMax: 2 << 20, CpuLoad: 0.1},
	}); err != nil {
		t.Fatalf("写样本失败: %v", err)
	}
}
