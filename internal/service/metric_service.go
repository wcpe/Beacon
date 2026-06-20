package service

import (
	"time"

	"beacon/internal/apperr"
	"beacon/internal/repository"
	"beacon/internal/runtime"
)

// 预设趋势时间窗（FR-32）：管理台按近 1h / 6h / 24h 出图。
const (
	windowLastHour   = "1h"
	windowLast6Hours = "6h"
	windowLast24H    = "24h"
)

// trendTargetBuckets 是趋势降采样的目标点数上界：按窗长 / 此值定桶大小，避免点数过多压垮出图。
const trendTargetBuckets = 120

// maxCustomWindow 是自定义 from/to 时间窗的跨度上限（7 天）：超限拒绝，避免超大区间把
// metric_sample 全量 Find 入内存再降采样（撞「大批量禁一次性全加载」）。与默认保留期（168h=7d）对齐——
// 早于保留期的样本已被滚动清理，查更长区间本就读不到数据。
const maxCustomWindow = 7 * 24 * time.Hour

// presetWindows 把预设窗口名映射到时间跨度。
var presetWindows = map[string]time.Duration{
	windowLastHour:   time.Hour,
	windowLast6Hours: 6 * time.Hour,
	windowLast24H:    24 * time.Hour,
}

// TrendQuery 是趋势查询入参（FR-32）。
type TrendQuery struct {
	Namespace string        // 可选，空则聚合全部环境
	ServerID  string        // 可选，空则聚合该 namespace 全部子服
	Window    string        // 预设窗口名（1h/6h/24h）；与 From/To 二选一，优先 From/To
	From      time.Time     // 可选自定义窗起（与 To 配对）
	To        time.Time     // 可选自定义窗止
	Bucket    time.Duration // 可选降采样粒度；<=0 时按窗长 / trendTargetBuckets 自动取
}

// MetricService 编排负载指标的聚合（实时读注册表）与趋势（查 metric_sample 降采样）。
// 不持 GORM：趋势查询经 repository，聚合 / 降采样为纯函数。
type MetricService struct {
	registry *runtime.Registry
	repo     *repository.MetricSampleRepository
	now      func() time.Time // 便于测试注入时钟；默认 UTC now
}

// NewMetricService 构造服务。
func NewMetricService(registry *runtime.Registry, repo *repository.MetricSampleRepository) *MetricService {
	return &MetricService{registry: registry, repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// Summary 返回某 namespace 当前快照聚合（实时读内存注册表的在线实例）。
// namespace 为空时聚合全部环境的在线实例（管理台总览）。
func (s *MetricService) Summary(namespace string) Summary {
	insts := s.registry.List(runtime.Filter{Namespace: namespace, Status: runtime.StatusOnline})
	return Summarize(insts)
}

// Trend 按时间窗 + 可选 serverId 查询 metric_sample，降采样为时间序列点。
// namespace 可选：为空时聚合全部环境（管理台总览）；窗口要么给合法预设，要么给 from<=to 的自定义区间。
func (s *MetricService) Trend(q TrendQuery) ([]TrendPoint, error) {
	from, to, err := s.resolveWindow(q)
	if err != nil {
		return nil, err
	}
	samples, err := s.repo.Query(q.Namespace, q.ServerID, from, to)
	if err != nil {
		return nil, err
	}
	bucket := q.Bucket
	if bucket <= 0 {
		bucket = autoBucket(to.Sub(from))
	}
	return Downsample(samples, bucket), nil
}

// resolveWindow 解析时间窗：优先显式 [From,To]；否则按预设窗口名取 [now-跨度, now]。
func (s *MetricService) resolveWindow(q TrendQuery) (time.Time, time.Time, error) {
	if !q.From.IsZero() || !q.To.IsZero() {
		if q.From.IsZero() || q.To.IsZero() || q.To.Before(q.From) {
			return time.Time{}, time.Time{}, apperr.ErrInvalidParam
		}
		// 跨度上限守卫：超大区间会一次性全量加载样本，拒绝之（见 maxCustomWindow）。
		if q.To.Sub(q.From) > maxCustomWindow {
			return time.Time{}, time.Time{}, apperr.ErrInvalidParam
		}
		return q.From, q.To, nil
	}
	span, ok := presetWindows[q.Window]
	if !ok {
		return time.Time{}, time.Time{}, apperr.ErrInvalidParam
	}
	now := s.now()
	return now.Add(-span), now, nil
}

// autoBucket 按窗长 / 目标点数取桶大小，至少 1 秒（防过细）。
func autoBucket(span time.Duration) time.Duration {
	if span <= 0 {
		return time.Second
	}
	b := span / trendTargetBuckets
	if b < time.Second {
		return time.Second
	}
	return b
}
