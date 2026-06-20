package service

import (
	"context"
	"log/slog"
	"time"

	"beacon/internal/model"
	"beacon/internal/runtime"
)

// metricSink 是采样器对持久化的窄依赖（由 repository.MetricSampleRepository 实现）：
// 只需批量写样本与按 cutoff 清理，便于以测试替身验证调用时序与参数。
type metricSink interface {
	InsertBatch(samples []model.MetricSample) error
	DeleteBefore(cutoff time.Time) (int64, error)
}

// MetricSampler 是负载指标采样器（FR-32，单后台 goroutine）：按间隔对在线实例采样落库形成历史趋势，
// 并按保留期滚动清理过期样本。快照在 registry 锁内取（List 返回深拷贝），DB IO 全在锁外（守 runtime 锁 / DB IO 在锁外）。
type MetricSampler struct {
	registry  *runtime.Registry
	sink      metricSink
	interval  time.Duration
	retention time.Duration
}

// NewMetricSampler 构造采样器。
func NewMetricSampler(registry *runtime.Registry, sink metricSink, interval, retention time.Duration) *MetricSampler {
	return &MetricSampler{registry: registry, sink: sink, interval: interval, retention: retention}
}

// Run 启动采样 + 清理循环，直到 ctx 取消。采样与保留期清理共用同一 ticker：
// 每个 tick 先采样落库，再做一次过期清理（频次足够、实现最简，不另起定时器）。
func (s *MetricSampler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	slog.Info("指标采样已启动", "采样间隔", s.interval.String(), "保留期", s.retention.String())
	for {
		select {
		case <-ctx.Done():
			slog.Info("指标采样已停止")
			return
		case now := <-ticker.C:
			s.sampleOnce(now.UTC())
			s.cleanupOnce(now.UTC())
		}
	}
}

// sampleOnce 执行一轮采样：在锁内取在线实例快照（List 深拷贝），锁外转样本批量写库。
// 返回本轮采样的实例数（无在线实例时为 0、不触发写入）。
func (s *MetricSampler) sampleOnce(at time.Time) int {
	// registry.List 在 RLock 内完成并返回深拷贝，返回后已脱离锁；后续转样本与写库均在锁外。
	insts := s.registry.List(runtime.Filter{Status: runtime.StatusOnline})
	if len(insts) == 0 {
		return 0
	}
	samples := make([]model.MetricSample, 0, len(insts))
	for _, in := range insts {
		samples = append(samples, model.MetricSample{
			Namespace: in.Namespace, ServerID: in.ServerID, Role: in.Role, SampledAt: at,
			PlayerCount: in.PlayerCount, TPS: in.TPS,
			MemUsed: in.MemUsed, MemMax: in.MemMax, CpuLoad: in.CpuLoad,
			// bc 专属指标（FR-34）：bukkit 实例 Proxy 为零值，照写不特判（聚合按 role 区分）。
			ProxyConn: in.Proxy.OnlineConnections, ThreadCount: in.Proxy.ThreadCount, UptimeMs: in.Proxy.UptimeMs,
			BackendUp: in.Proxy.BackendUp, BackendTotal: in.Proxy.BackendTotal,
			BackendAvgLatencyMs: in.Proxy.BackendAvgLatencyMs,
		})
	}
	if err := s.sink.InsertBatch(samples); err != nil {
		slog.Error("批量写指标样本失败", "样本数", len(samples), "错误", err)
		return len(insts)
	}
	slog.Debug("指标采样落库", "样本数", len(samples))
	return len(insts)
}

// cleanupOnce 执行一轮保留期清理：删除早于 at-保留期 的样本，控制表体量。
func (s *MetricSampler) cleanupOnce(at time.Time) {
	cutoff := at.Add(-s.retention)
	deleted, err := s.sink.DeleteBefore(cutoff)
	if err != nil {
		slog.Error("清理过期指标样本失败", "cutoff", cutoff, "错误", err)
		return
	}
	if deleted > 0 {
		slog.Debug("清理过期指标样本", "删除条数", deleted, "cutoff", cutoff)
	}
}
