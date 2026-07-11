package service

import (
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
)

// fakeSnapshotQuerier / fakeSeriesQuerier 是日表查询替身（单测不连 DB）。
type fakeSnapshotQuerier struct {
	rows []model.HealthSnapshot
	got  struct {
		serverID     string
		fromMs, toMs int64
	}
}

func (f *fakeSnapshotQuerier) QueryRange(serverID string, fromMs, toMs int64) ([]model.HealthSnapshot, error) {
	f.got.serverID, f.got.fromMs, f.got.toMs = serverID, fromMs, toMs
	return f.rows, nil
}

type fakeSeriesQuerier struct {
	rows []model.MetricSampleV2
}

func (f *fakeSeriesQuerier) QueryRange(_ []string, _, _ int64) ([]model.MetricSampleV2, error) {
	return f.rows, nil
}

// queryView 构造一条视图。
func queryView(ns uint, serverID, kind, zone, level string, schedulable bool, reasons []string) healthview.View {
	return healthview.View{
		NamespaceID: ns, Namespace: "prod", ServerID: serverID, Kind: kind, ZoneName: zone,
		Score: 90, Level: level, Schedulable: schedulable, Reasons: reasons,
		WeightsRev: 1, ComputedAtMs: 1_000,
	}
}

// newQueryFixture 装配查询服务（预置视图 + 替身日表查询）。
func newQueryFixture(views []healthview.View) (*HealthQueryService, *fakeSnapshotQuerier, *metricwindow.Store) {
	store := healthview.NewStore()
	store.ReplaceAll(views)
	window := metricwindow.New(metricwindow.DefaultCapacity)
	snapshots := &fakeSnapshotQuerier{}
	svc := NewHealthQueryService(store, window, snapshots, &fakeSeriesQuerier{})
	svc.now = func() time.Time { return time.UnixMilli(10_000_000).UTC() }
	return svc, snapshots, window
}

// TestListHealthFiltersAndPaging 校验筛选（namespaceId/zone/level/schedulable/keyword）+ 稳定排序 + 分页。
func TestListHealthFiltersAndPaging(t *testing.T) {
	svc, _, _ := newQueryFixture([]healthview.View{
		queryView(1, "b-1", model.ServerKindBackend, "area-1", healthview.LevelHealthy, true, nil),
		queryView(1, "b-2", model.ServerKindBackend, "area-2", healthview.LevelDegraded, true, nil),
		queryView(2, "a-1", model.ServerKindBackend, "area-1", healthview.LevelUnhealthy, false, []string{"unhealthy"}),
		queryView(1, "p-1", model.ServerKindProxy, "", healthview.LevelHealthy, false, []string{"kind_not_schedulable"}),
	})

	// 无筛选：全量 + (namespaceId, serverId) 稳定排序 + reasons 恒非 nil + 未分配 zone 为 null。
	items, total, err := svc.ListHealth(ListHealthParams{})
	if err != nil || total != 4 || len(items) != 4 {
		t.Fatalf("全量应 4 条: %v %d", err, total)
	}
	if items[0].ServerID != "b-1" || items[3].NamespaceID != 2 {
		t.Fatalf("应按 (namespaceId, serverId) 排序，实际 %+v", items)
	}
	if items[0].Reasons == nil || items[0].ZoneName == nil || *items[0].ZoneName != "area-1" {
		t.Fatalf("reasons 应非 nil、zoneName 应带值，实际 %+v", items[0])
	}
	if items[2].ZoneName != nil {
		t.Fatalf("未分配 zone 应为 null，实际 %+v", items[2])
	}

	// 各筛选维度。
	if items, _, _ = svc.ListHealth(ListHealthParams{NamespaceID: 2}); len(items) != 1 || items[0].ServerID != "a-1" {
		t.Fatalf("namespaceId 筛选不符: %+v", items)
	}
	if items, _, _ = svc.ListHealth(ListHealthParams{Zone: "area-2"}); len(items) != 1 || items[0].ServerID != "b-2" {
		t.Fatalf("zone 筛选不符: %+v", items)
	}
	if items, _, _ = svc.ListHealth(ListHealthParams{Level: healthview.LevelDegraded}); len(items) != 1 {
		t.Fatalf("level 筛选不符: %+v", items)
	}
	yes := true
	if _, total, _ = svc.ListHealth(ListHealthParams{Schedulable: &yes}); total != 2 {
		t.Fatalf("schedulable=true 应 2 条，实际 %d", total)
	}
	if items, _, _ = svc.ListHealth(ListHealthParams{Keyword: "B-"}); len(items) != 2 {
		t.Fatalf("keyword 大小写不敏感筛选应 2 条，实际 %+v", items)
	}

	// 分页：page=2/pageSize=3 → 第 4 条；越界页 → 空页但 total 不变。
	if items, total, _ = svc.ListHealth(ListHealthParams{Page: 2, PageSize: 3}); total != 4 || len(items) != 1 {
		t.Fatalf("分页不符: total=%d items=%d", total, len(items))
	}
	if items, total, _ = svc.ListHealth(ListHealthParams{Page: 9, PageSize: 3}); total != 4 || len(items) != 0 {
		t.Fatalf("越界页应空页: total=%d items=%d", total, len(items))
	}
}

// TestHealthDetailLookup 校验详情：命中带因子与权重版本；重名取 namespaceId 最小；不存在 404。
func TestHealthDetailLookup(t *testing.T) {
	dup1 := queryView(3, "dup", model.ServerKindBackend, "z", healthview.LevelHealthy, true, nil)
	dup2 := queryView(1, "dup", model.ServerKindBackend, "z", healthview.LevelHealthy, true, nil)
	dup2.Factors = []healthview.Factor{{Factor: "cpu", Raw: 30, Normalized: 100, Weight: 20, Applicable: true}}
	dup2.WeightsRev = 7
	svc, _, _ := newQueryFixture([]healthview.View{dup1, dup2})

	detail, err := svc.HealthDetail("dup")
	if err != nil {
		t.Fatalf("详情失败: %v", err)
	}
	if detail.NamespaceID != 1 || detail.WeightsRev != 7 || len(detail.Factors) != 1 {
		t.Fatalf("应取 namespaceId 最小者并带因子分解，实际 %+v", detail)
	}
	if _, err := svc.HealthDetail("ghost"); !errors.Is(err, apperr.ErrInstanceNotFound) {
		t.Fatalf("不存在应 404，实际 %v", err)
	}
	if _, err := svc.HealthDetail(""); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("空 serverId 应参数错误，实际 %v", err)
	}
}

// TestHealthSnapshotsQuery 校验快照回放：serverId 必填、默认最近 1h、行转点（reasons json 解析）。
func TestHealthSnapshotsQuery(t *testing.T) {
	svc, snapshots, _ := newQueryFixture(nil)
	snapshots.rows = []model.HealthSnapshot{
		{TsMs: 1, Score: 90, Level: "healthy", Schedulable: true, Reasons: "[]", WeightsRev: 2},
		{TsMs: 2, Score: 0, Level: "unhealthy", Schedulable: false, Reasons: `["lost","unhealthy"]`, WeightsRev: 2},
	}

	if _, err := svc.HealthSnapshots("", 0, 0); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺 serverId 应参数错误，实际 %v", err)
	}
	points, err := svc.HealthSnapshots("s1", 0, 0)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if snapshots.got.serverID != "s1" || snapshots.got.toMs != 10_000_000 || snapshots.got.fromMs != 10_000_000-3_600_000 {
		t.Fatalf("默认范围应为最近 1h，实际 %+v", snapshots.got)
	}
	if len(points) != 2 || len(points[0].Reasons) != 0 || len(points[1].Reasons) != 2 || points[1].Reasons[0] != "lost" {
		t.Fatalf("快照点不符: %+v", points)
	}

	// 范围校验：from > to / 跨度超 31 天均拒绝。
	if _, err := svc.HealthSnapshots("s1", 200, 100); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("from>to 应参数错误，实际 %v", err)
	}
	if _, err := svc.HealthSnapshots("s1", 1, 1+maxQueryRangeMs+1); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("超 31 天应参数错误，实际 %v", err)
	}
}

// TestMetricsSummaryAggregation 校验概览聚合：分角色计数（online=非 lost）、level 分布、
// schedulable 计数、玩家合计与均值走 60s 窗口最新批。
func TestMetricsSummaryAggregation(t *testing.T) {
	okView := queryView(1, "b-1", model.ServerKindBackend, "z", healthview.LevelHealthy, true, nil)
	okView.OnlineCount = 60
	degView := queryView(1, "b-2", model.ServerKindBackend, "z", healthview.LevelDegraded, true, nil)
	degView.OnlineCount = 40
	lostView := queryView(1, "b-3", model.ServerKindBackend, "z", healthview.LevelUnhealthy, false, []string{"lost", "unhealthy"})
	lostView.OnlineCount = 999 // lost 不应计入玩家合计
	proxyView := queryView(1, "p-1", model.ServerKindProxy, "", healthview.LevelHealthy, false, []string{"kind_not_schedulable"})

	svc, _, window := newQueryFixture([]healthview.View{okView, degView, lostView, proxyView})
	window.Upsert(metricwindow.Sample{NamespaceID: 1, ServerID: "b-1", BucketStartMs: 5000, TPSAvg: 20, CPUPctAvg: 30})
	window.Upsert(metricwindow.Sample{NamespaceID: 1, ServerID: "b-2", BucketStartMs: 5000, TPSAvg: 18, CPUPctAvg: -1})
	window.Upsert(metricwindow.Sample{NamespaceID: 1, ServerID: "p-1", BucketStartMs: 5000, CPUPctAvg: 50})

	got := svc.MetricsSummary()
	if got.ByKind.Backend.Total != 3 || got.ByKind.Backend.Online != 2 || got.ByKind.Proxy.Total != 1 || got.ByKind.Proxy.Online != 1 {
		t.Fatalf("分角色计数不符: %+v", got.ByKind)
	}
	if got.LevelDistribution != (LevelDistributionView{Healthy: 2, Degraded: 1, Unhealthy: 1}) {
		t.Fatalf("等级分布不符: %+v", got.LevelDistribution)
	}
	if got.Schedulable != (SchedulableCountView{Yes: 2, No: 2}) {
		t.Fatalf("可调度计数不符: %+v", got.Schedulable)
	}
	if got.PlayersOnline != 100 {
		t.Fatalf("玩家合计应 100（lost 不计），实际 %d", got.PlayersOnline)
	}
	if !almostEq(got.AvgTps, 19) {
		t.Fatalf("平均 TPS 应 19，实际 %v", got.AvgTps)
	}
	if !almostEq(got.AvgCPUPct, 40) {
		t.Fatalf("平均 CPU 应 40（b-2 的 -1 剔除），实际 %v", got.AvgCPUPct)
	}
}

// TestMetricsSeriesBucketAggregation 校验时序桶聚合纯函数：avg/max/min 口径、cpu<0 剔除、桶升序。
func TestMetricsSeriesBucketAggregation(t *testing.T) {
	fromMs := int64(0)
	stepMs := int64(60_000)
	rows := []model.MetricSampleV2{
		{BucketStartMs: 5_000, CPUPctAvg: 30, CPUPctMax: 45, MemUsedMbAvg: 2000, TPSAvg: 20, TPSMin: 19.5, OnlineAvg: 50, OnlineMax: 55},
		{BucketStartMs: 30_000, CPUPctAvg: -1, CPUPctMax: 50, MemUsedMbAvg: 2200, TPSAvg: 18, TPSMin: 17, OnlineAvg: 70, OnlineMax: 80},
		{BucketStartMs: 65_000, CPUPctAvg: 60, CPUPctMax: 70, MemUsedMbAvg: 2100, TPSAvg: 19, TPSMin: 18.5, OnlineAvg: 60, OnlineMax: 66},
	}
	points := aggregateSeriesPoints(rows, fromMs, stepMs)
	if len(points) != 2 || points[0].TsMs != 0 || points[1].TsMs != 60_000 {
		t.Fatalf("应聚为升序 2 桶，实际 %+v", points)
	}
	p0 := points[0]
	if !almostEq(p0.CPUPctAvg, 30) { // -1 批剔除后只剩 30
		t.Fatalf("cpuPctAvg 应剔除 -1 后为 30，实际 %v", p0.CPUPctAvg)
	}
	if !almostEq(p0.CPUPctMax, 50) || !almostEq(p0.TPSAvg, 19) || !almostEq(p0.TPSMin, 17) {
		t.Fatalf("max/avg/min 口径不符: %+v", p0)
	}
	if p0.OnlineAvg != 60 || p0.OnlineMax != 80 || !almostEq(p0.MemUsedMbAvg, 2100) {
		t.Fatalf("在线/内存聚合不符: %+v", p0)
	}
	if len(aggregateSeriesPoints(nil, fromMs, stepMs)) != 0 {
		t.Fatal("空行应返回空点集")
	}
}

// TestMetricsSeriesParams 校验时序参数：serverId 必填、step 缺省 60 / 下限 5、空数据服务器仍出空序列。
func TestMetricsSeriesParams(t *testing.T) {
	svc, _, _ := newQueryFixture(nil)
	if _, err := svc.MetricsSeries(MetricsSeriesParams{}); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺 serverId 应参数错误，实际 %v", err)
	}
	out, err := svc.MetricsSeries(MetricsSeriesParams{ServerIDs: []string{"s1", "s2"}})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if out.StepSec != defaultSeriesStepSec || len(out.Series) != 2 || out.Series[1].ServerID != "s2" || len(out.Series[1].Points) != 0 {
		t.Fatalf("缺省 step 与空序列不符: %+v", out)
	}
	if out, _ = svc.MetricsSeries(MetricsSeriesParams{ServerIDs: []string{"s1"}, StepSec: 1}); out.StepSec != minSeriesStepSec {
		t.Fatalf("step 应钳到下限 5，实际 %d", out.StepSec)
	}
}
