package service

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
)

// 查询侧工程参数（§5.2：时序 / 快照查询按时间范围并表，防无界扫描）。
const (
	// 未给 from 时默认回看窗口
	defaultQueryRangeMs = int64(3_600_000)
	// 单次查询允许的最大时间跨度（覆盖快照 30 天保留期，防按年扫日表）
	maxQueryRangeMs = int64(31*24) * 3_600_000
	// series 默认聚合桶（秒）
	defaultSeriesStepSec = 60
	// series 最小聚合桶（秒）：底层即 5s 批行，更小无意义
	minSeriesStepSec = 5
)

// healthSnapshotQuerier 是快照日表查询的窄依赖（由 repository.HealthSnapshotRepository 实现）。
type healthSnapshotQuerier interface {
	QueryRange(serverID string, fromMs, toMs int64) ([]model.HealthSnapshot, error)
}

// metricSeriesQuerier 是指标日表查询的窄依赖（由 repository.MetricSampleV2Repository 实现）。
type metricSeriesQuerier interface {
	QueryRange(serverIDs []string, fromMs, toMs int64) ([]model.MetricSampleV2, error)
}

// HealthQueryService 提供健康与指标的管理面只读查询（FR-147，见 §5.2）：
// 实时读走内存（healthview + metricwindow），回放读走日表（快照 / 指标，缺表跳过、严禁隐式建表）。
type HealthQueryService struct {
	views     *healthview.Store
	window    *metricwindow.Store
	snapshots healthSnapshotQuerier
	series    metricSeriesQuerier
	now       func() time.Time
}

// NewHealthQueryService 构造查询服务。
func NewHealthQueryService(views *healthview.Store, window *metricwindow.Store,
	snapshots healthSnapshotQuerier, series metricSeriesQuerier) *HealthQueryService {
	return &HealthQueryService{
		views: views, window: window, snapshots: snapshots, series: series,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// HealthItemView 是健康视图列表项（json 形状对齐 contracts HealthItem）。
type HealthItemView struct {
	ServerID    string   `json:"serverId"`
	NamespaceID uint     `json:"namespaceId"`
	Kind        string   `json:"kind"`
	ZoneName    *string  `json:"zoneName"`
	Score       int      `json:"score"`
	Level       string   `json:"level"`
	Schedulable bool     `json:"schedulable"`
	Reasons     []string `json:"reasons"`
	SampledAtMs int64    `json:"sampledAtMs"`
}

// HealthDetailView 是单服健康详情（json 形状对齐 contracts HealthDetail：列表项 + 因子分解 + 权重版本）。
type HealthDetailView struct {
	HealthItemView
	Factors    []HealthFactorView `json:"factors"`
	WeightsRev int                `json:"weightsRev"`
}

// ListHealthParams 是健康列表的筛选 / 分页参数（参数名对齐 devmock：namespaceId/zone/level/schedulable/keyword）。
type ListHealthParams struct {
	NamespaceID uint // 0 = 不筛
	Zone        string
	Level       string
	Schedulable *bool
	Keyword     string // serverId 子串匹配
	Page        int
	PageSize    int
}

// ListHealth 内存实时列出健康视图（筛选 + 稳定排序 + 分页），返回当页与总数。
func (s *HealthQueryService) ListHealth(p ListHealthParams) ([]HealthItemView, int, error) {
	views := s.views.List()
	sort.Slice(views, func(i, j int) bool {
		if views[i].NamespaceID != views[j].NamespaceID {
			return views[i].NamespaceID < views[j].NamespaceID
		}
		return views[i].ServerID < views[j].ServerID
	})
	filtered := make([]HealthItemView, 0, len(views))
	for i := range views {
		if !matchHealthFilter(&views[i], p) {
			continue
		}
		filtered = append(filtered, healthItemOf(&views[i]))
	}
	total := len(filtered)
	start := pageOffset(p.Page, p.PageSize)
	if start >= total {
		return []HealthItemView{}, total, nil
	}
	end := start + pageSize(p.PageSize)
	if end > total {
		end = total
	}
	return filtered[start:end], total, nil
}

// matchHealthFilter 判断单视图是否命中筛选条件。
func matchHealthFilter(v *healthview.View, p ListHealthParams) bool {
	if p.NamespaceID != 0 && v.NamespaceID != p.NamespaceID {
		return false
	}
	if p.Zone != "" && v.ZoneName != p.Zone {
		return false
	}
	if p.Level != "" && v.Level != p.Level {
		return false
	}
	if p.Schedulable != nil && v.Schedulable != *p.Schedulable {
		return false
	}
	if p.Keyword != "" && !containsFold(v.ServerID, p.Keyword) {
		return false
	}
	return true
}

// HealthDetail 取单服健康详情（含因子分解与权重版本）。serverId 跨 namespace 重名时取 namespaceId 最小者；
// 无视图（不在册）回 404。
func (s *HealthQueryService) HealthDetail(serverID string) (*HealthDetailView, error) {
	if serverID == "" {
		return nil, apperr.ErrInvalidParam
	}
	var hit *healthview.View
	views := s.views.List()
	for i := range views {
		if views[i].ServerID != serverID {
			continue
		}
		if hit == nil || views[i].NamespaceID < hit.NamespaceID {
			hit = &views[i]
		}
	}
	if hit == nil {
		return nil, apperr.ErrInstanceNotFound
	}
	return &HealthDetailView{
		HealthItemView: healthItemOf(hit),
		Factors:        factorViewsOf(hit.Factors),
		WeightsRev:     hit.WeightsRev,
	}, nil
}

// HealthSnapshotPointView 是快照回放点（json 形状对齐 contracts HealthSnapshotPoint）。
type HealthSnapshotPointView struct {
	TsMs        int64    `json:"tsMs"`
	Score       int      `json:"score"`
	Level       string   `json:"level"`
	Schedulable bool     `json:"schedulable"`
	Reasons     []string `json:"reasons"`
	WeightsRev  int      `json:"weightsRev"`
}

// HealthSnapshots 查健康快照回放（serverId 必填；from/to 缺省为最近 1 小时；跨日并表、缺表跳过）。
func (s *HealthQueryService) HealthSnapshots(serverID string, fromMs, toMs int64) ([]HealthSnapshotPointView, error) {
	if serverID == "" {
		return nil, apperr.ErrInvalidParam
	}
	fromMs, toMs, err := s.normalizeQueryRange(fromMs, toMs)
	if err != nil {
		return nil, err
	}
	rows, err := s.snapshots.QueryRange(serverID, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	points := make([]HealthSnapshotPointView, 0, len(rows))
	for i := range rows {
		points = append(points, snapshotPointOf(&rows[i]))
	}
	return points, nil
}

// MetricsKindCountView 是分角色实例计数（total 在册 / online 活性正常即非 lost）。
type MetricsKindCountView struct {
	Total  int `json:"total"`
	Online int `json:"online"`
}

// MetricsByKindView 是 proxy / backend 两角色计数。
type MetricsByKindView struct {
	Proxy   MetricsKindCountView `json:"proxy"`
	Backend MetricsKindCountView `json:"backend"`
}

// LevelDistributionView 是健康等级分布。
type LevelDistributionView struct {
	Healthy   int `json:"healthy"`
	Degraded  int `json:"degraded"`
	Unhealthy int `json:"unhealthy"`
}

// SchedulableCountView 是可调度计数。
type SchedulableCountView struct {
	Yes int `json:"yes"`
	No  int `json:"no"`
}

// MetricsSummaryView 是集群聚合概览（json 形状对齐 contracts MetricsSummary）。
type MetricsSummaryView struct {
	GeneratedAt       time.Time             `json:"generatedAt"`
	ByKind            MetricsByKindView     `json:"byKind"`
	PlayersOnline     int                   `json:"playersOnline"`
	AvgTps            float64               `json:"avgTps"`
	AvgCPUPct         float64               `json:"avgCpuPct"`
	LevelDistribution LevelDistributionView `json:"levelDistribution"`
	Schedulable       SchedulableCountView  `json:"schedulable"`
}

// MetricsSummary 内存实时聚合集群概览：计数走健康视图，均值走 60s 指标窗口最新批。
func (s *HealthQueryService) MetricsSummary() MetricsSummaryView {
	out := MetricsSummaryView{GeneratedAt: s.now()}
	views := s.views.List()
	var tpsSum, cpuSum float64
	var tpsN, cpuN int
	for i := range views {
		v := &views[i]
		online := !containsReason(v.Reasons, healthview.ReasonLost)
		countKind(&out.ByKind, v.Kind, online)
		countLevel(&out.LevelDistribution, v.Level)
		if v.Schedulable {
			out.Schedulable.Yes++
		} else {
			out.Schedulable.No++
		}
		if !online {
			continue
		}
		if v.Kind == model.ServerKindBackend {
			out.PlayersOnline += v.OnlineCount
		}
		latest, ok := s.window.Latest(v.NamespaceID, v.ServerID)
		if !ok {
			continue
		}
		if v.Kind == model.ServerKindBackend {
			tpsSum += latest.TPSAvg
			tpsN++
		}
		if latest.CPUPctAvg >= 0 {
			cpuSum += latest.CPUPctAvg
			cpuN++
		}
	}
	if tpsN > 0 {
		out.AvgTps = roundTo1(tpsSum / float64(tpsN))
	}
	if cpuN > 0 {
		out.AvgCPUPct = roundTo1(cpuSum / float64(cpuN))
	}
	return out
}

// MetricsSeriesPointView 是时序点（json 形状对齐 contracts MetricsSeriesPoint）。
type MetricsSeriesPointView struct {
	TsMs         int64   `json:"tsMs"`
	CPUPctAvg    float64 `json:"cpuPctAvg"`
	CPUPctMax    float64 `json:"cpuPctMax"`
	MemUsedMbAvg float64 `json:"memUsedMbAvg"`
	TPSAvg       float64 `json:"tpsAvg"`
	TPSMin       float64 `json:"tpsMin"`
	OnlineAvg    int     `json:"onlineAvg"`
	OnlineMax    int     `json:"onlineMax"`
}

// MetricsSeriesEntryView 是单服时序。
type MetricsSeriesEntryView struct {
	ServerID string                   `json:"serverId"`
	Points   []MetricsSeriesPointView `json:"points"`
}

// MetricsSeriesView 是时序响应（json 形状对齐 contracts MetricsSeriesResponse）。
type MetricsSeriesView struct {
	StepSec int                      `json:"stepSec"`
	Series  []MetricsSeriesEntryView `json:"series"`
}

// MetricsSeriesParams 是时序查询参数。
type MetricsSeriesParams struct {
	ServerIDs []string // 必填（禁全量扫）
	FromMs    int64    // 0 = 缺省（to − 1h）
	ToMs      int64    // 0 = 缺省（now）
	StepSec   int      // 0 = 缺省 60；下限 5
}

// MetricsSeries 查单服 / 多服指标时序：日表跨日并表 + 按 step 服务端桶聚合（avg/max/min）。
func (s *HealthQueryService) MetricsSeries(p MetricsSeriesParams) (*MetricsSeriesView, error) {
	if len(p.ServerIDs) == 0 {
		return nil, apperr.ErrInvalidParam
	}
	fromMs, toMs, err := s.normalizeQueryRange(p.FromMs, p.ToMs)
	if err != nil {
		return nil, err
	}
	stepSec := p.StepSec
	if stepSec <= 0 {
		stepSec = defaultSeriesStepSec
	}
	if stepSec < minSeriesStepSec {
		stepSec = minSeriesStepSec
	}
	rows, err := s.series.QueryRange(p.ServerIDs, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	byServer := make(map[string][]model.MetricSampleV2, len(p.ServerIDs))
	for i := range rows {
		byServer[rows[i].ServerID] = append(byServer[rows[i].ServerID], rows[i])
	}
	out := &MetricsSeriesView{StepSec: stepSec, Series: make([]MetricsSeriesEntryView, 0, len(p.ServerIDs))}
	for _, serverID := range p.ServerIDs {
		out.Series = append(out.Series, MetricsSeriesEntryView{
			ServerID: serverID,
			Points:   aggregateSeriesPoints(byServer[serverID], fromMs, int64(stepSec)*1000),
		})
	}
	return out, nil
}

// normalizeQueryRange 归一化查询时间范围：to 缺省 now、from 缺省 to−1h；
// from>to 或跨度超上限（31 天，防按年扫日表）回参数错误。
func (s *HealthQueryService) normalizeQueryRange(fromMs, toMs int64) (int64, int64, error) {
	if toMs <= 0 {
		toMs = s.now().UnixMilli()
	}
	if fromMs <= 0 {
		fromMs = toMs - defaultQueryRangeMs
	}
	if fromMs > toMs || toMs-fromMs > maxQueryRangeMs {
		return 0, 0, apperr.ErrInvalidParam
	}
	return fromMs, toMs, nil
}

// seriesBucket 是聚合中的单桶累加器。
type seriesBucket struct {
	cpuSum, cpuMax, memSum, tpsSum, tpsMin float64
	onlineSum, onlineMax                   int
	cpuN, n                                int
}

// aggregateSeriesPoints 把单服 5s 聚合行按 step 桶聚合为时序点（纯函数）：
// avg 列取桶内均值（cpu 剔除 <0 毛刺批）、max 列取桶内最大、tpsMin 取桶内最小；桶按 tsMs 升序输出。
func aggregateSeriesPoints(rows []model.MetricSampleV2, fromMs, stepMs int64) []MetricsSeriesPointView {
	if len(rows) == 0 {
		return []MetricsSeriesPointView{}
	}
	buckets := make(map[int64]*seriesBucket)
	for i := range rows {
		r := &rows[i]
		ts := fromMs + (r.BucketStartMs-fromMs)/stepMs*stepMs
		b := buckets[ts]
		if b == nil {
			b = &seriesBucket{tpsMin: math.MaxFloat64}
			buckets[ts] = b
		}
		if r.CPUPctAvg >= 0 {
			b.cpuSum += r.CPUPctAvg
			b.cpuN++
		}
		if r.CPUPctMax > b.cpuMax {
			b.cpuMax = r.CPUPctMax
		}
		b.memSum += r.MemUsedMbAvg
		b.tpsSum += r.TPSAvg
		if r.TPSMin < b.tpsMin {
			b.tpsMin = r.TPSMin
		}
		b.onlineSum += r.OnlineAvg
		if r.OnlineMax > b.onlineMax {
			b.onlineMax = r.OnlineMax
		}
		b.n++
	}
	keys := make([]int64, 0, len(buckets))
	for ts := range buckets {
		keys = append(keys, ts)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	points := make([]MetricsSeriesPointView, 0, len(keys))
	for _, ts := range keys {
		b := buckets[ts]
		point := MetricsSeriesPointView{
			TsMs:         ts,
			CPUPctMax:    roundTo1(b.cpuMax),
			MemUsedMbAvg: roundTo1(b.memSum / float64(b.n)),
			TPSAvg:       roundTo1(b.tpsSum / float64(b.n)),
			TPSMin:       roundTo1(b.tpsMin),
			OnlineAvg:    int(math.Round(float64(b.onlineSum) / float64(b.n))),
			OnlineMax:    b.onlineMax,
		}
		if b.cpuN > 0 {
			point.CPUPctAvg = roundTo1(b.cpuSum / float64(b.cpuN))
		}
		points = append(points, point)
	}
	return points
}

// healthItemOf 把内存视图转为列表项（reasons 恒非 nil，zoneName 未分配为 null）。
func healthItemOf(v *healthview.View) HealthItemView {
	item := HealthItemView{
		ServerID: v.ServerID, NamespaceID: v.NamespaceID, Kind: v.Kind,
		Score: v.Score, Level: v.Level, Schedulable: v.Schedulable,
		Reasons: v.Reasons, SampledAtMs: v.ComputedAtMs,
	}
	if item.Reasons == nil {
		item.Reasons = []string{}
	}
	if v.ZoneName != "" {
		zone := v.ZoneName
		item.ZoneName = &zone
	}
	return item
}

// snapshotPointOf 把快照行转为回放点（reasons json 解析失败按空数组，行不因此丢弃）。
func snapshotPointOf(row *model.HealthSnapshot) HealthSnapshotPointView {
	reasons := []string{}
	_ = json.Unmarshal([]byte(row.Reasons), &reasons)
	if reasons == nil {
		reasons = []string{}
	}
	return HealthSnapshotPointView{
		TsMs: row.TsMs, Score: row.Score, Level: row.Level,
		Schedulable: row.Schedulable, Reasons: reasons, WeightsRev: row.WeightsRev,
	}
}

// countKind 按角色累计在册 / 在线计数。
func countKind(byKind *MetricsByKindView, kind string, online bool) {
	target := &byKind.Backend
	if kind == model.ServerKindProxy {
		target = &byKind.Proxy
	}
	target.Total++
	if online {
		target.Online++
	}
}

// countLevel 按等级累计分布。
func countLevel(dist *LevelDistributionView, level string) {
	switch level {
	case healthview.LevelHealthy:
		dist.Healthy++
	case healthview.LevelDegraded:
		dist.Degraded++
	default:
		dist.Unhealthy++
	}
}

// roundTo1 保留 1 位小数（响应稳定可读）。
func roundTo1(x float64) float64 {
	return math.Round(x*10) / 10
}

// containsFold 判断 s 是否含子串 sub（不区分大小写，serverId 关键字筛选用）。
func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 'a' - 'A'
		}
		return b
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
