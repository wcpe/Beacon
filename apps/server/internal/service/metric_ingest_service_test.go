package service

import (
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
)

// fakeEnqueuer 是写入池测试替身：可切换「队列满」并记录入队的批。
type fakeEnqueuer struct {
	full     bool
	enqueued [][]model.MetricSampleV2
}

func (f *fakeEnqueuer) Enqueue(rows []model.MetricSampleV2) bool {
	if f.full {
		return false
	}
	cp := make([]model.MetricSampleV2, len(rows))
	copy(cp, rows)
	f.enqueued = append(f.enqueued, cp)
	return true
}

// newIngestSvc 构造接收服务（固定时钟，供时钟偏移可控）。
func newIngestSvc(enq metricEnqueuer, now time.Time) *MetricIngestService {
	s := NewMetricIngestService(metricwindow.New(metricwindow.DefaultCapacity), enq)
	s.now = func() time.Time { return now }
	return s
}

func backendIdentity() agentauth.Identity {
	return agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "lobby-1", Kind: model.ServerKindBackend, IdentityID: "id-1"}
}

func proxyIdentity() agentauth.Identity {
	return agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "proxy-1", Kind: model.ServerKindProxy, IdentityID: "id-2"}
}

// TestIngestNormalizeBackend 校验 backend 上报清 proxy 列（conn/backend 清 0、rtt=-1），保留 tps/online。
func TestIngestNormalizeBackend(t *testing.T) {
	enq := &fakeEnqueuer{}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)

	res, err := svc.Ingest(MetricReportParams{
		Identity:    backendIdentity(),
		AgentTimeMs: now.UnixMilli(),
		Samples: []MetricReportSample{{
			BucketStartMs: 5000, SampleCount: 5,
			TPSAvg: 19.8, TPSMin: 18, OnlineAvg: 40, OnlineMax: 42, MaxOnline: 100,
			// 下列 proxy 专属字段应被清除。
			ConnAvg: 999, ConnMax: 1000, BackendUp: 3, BackendTotal: 4, BackendRttMsAvg: 12.3,
			ReportRttMs: 8,
		}},
	})
	if err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	if res.Accepted != 1 || res.Deduplicated != 0 {
		t.Fatalf("应接收 1、去重 0，实际 %+v", res)
	}
	row := enq.enqueued[0][0]
	if row.ConnAvg != 0 || row.ConnMax != 0 || row.BackendUp != 0 || row.BackendTotal != 0 {
		t.Fatalf("backend 应清 proxy 列为 0，实际 %+v", row)
	}
	if row.BackendRttMsAvg != -1 {
		t.Fatalf("backend 的 backend_rtt_ms_avg 应为 -1，实际 %v", row.BackendRttMsAvg)
	}
	if row.TPSAvg != 19.8 || row.OnlineMax != 42 || row.MaxOnline != 100 || row.ReportRttMs != 8 {
		t.Fatalf("backend 应保留 tps/online/maxOnline/reportRtt，实际 %+v", row)
	}
	// 权威身份落库，不信任请求体。
	if row.NamespaceID != 1 || row.ServerID != "lobby-1" || row.Kind != model.ServerKindBackend {
		t.Fatalf("行应归属权威身份，实际 %+v", row)
	}
}

// TestIngestNormalizeProxy 校验 proxy 上报清 backend 列（tps/online/maxOnline 清 0），保留 conn/backend。
func TestIngestNormalizeProxy(t *testing.T) {
	enq := &fakeEnqueuer{}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)

	_, err := svc.Ingest(MetricReportParams{
		Identity:    proxyIdentity(),
		AgentTimeMs: now.UnixMilli(),
		Samples: []MetricReportSample{{
			BucketStartMs: 5000, SampleCount: 5,
			ConnAvg: 500, ConnMax: 600, BackendUp: 3, BackendTotal: 4, BackendRttMsAvg: 12.3, ReportRttMs: 8,
			// 下列 backend 专属字段应被清除。
			TPSAvg: 20, TPSMin: 19, OnlineAvg: 40, OnlineMax: 42, MaxOnline: 100,
		}},
	})
	if err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	row := enq.enqueued[0][0]
	if row.TPSAvg != 0 || row.TPSMin != 0 || row.OnlineAvg != 0 || row.OnlineMax != 0 || row.MaxOnline != 0 {
		t.Fatalf("proxy 应清 backend 列为 0，实际 %+v", row)
	}
	if row.ConnMax != 600 || row.BackendUp != 3 || row.BackendRttMsAvg != 12.3 {
		t.Fatalf("proxy 应保留 conn/backend/rtt，实际 %+v", row)
	}
}

// TestIngestWindowDedup 校验 60s 窗口重放去重：同批再报 accepted=0、deduplicated=批数，且不重复入队。
func TestIngestWindowDedup(t *testing.T) {
	enq := &fakeEnqueuer{}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)
	samples := []MetricReportSample{
		{BucketStartMs: 5000, SampleCount: 5, TPSAvg: 20},
		{BucketStartMs: 10000, SampleCount: 5, TPSAvg: 20},
	}
	p := MetricReportParams{Identity: backendIdentity(), AgentTimeMs: now.UnixMilli(), Samples: samples}

	res1, _ := svc.Ingest(p)
	if res1.Accepted != 2 || res1.Deduplicated != 0 {
		t.Fatalf("首次应接收 2，实际 %+v", res1)
	}
	res2, _ := svc.Ingest(p)
	if res2.Accepted != 0 || res2.Deduplicated != 2 {
		t.Fatalf("重放应全部去重，实际 %+v", res2)
	}
	if len(enq.enqueued) != 1 {
		t.Fatalf("重放不应重复入队，实际入队 %d 次", len(enq.enqueued))
	}
}

// TestIngestWithinBatchDup 校验批内重复桶被去重、仅入队一次。
func TestIngestWithinBatchDup(t *testing.T) {
	enq := &fakeEnqueuer{}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)
	res, err := svc.Ingest(MetricReportParams{
		Identity: backendIdentity(), AgentTimeMs: now.UnixMilli(),
		Samples: []MetricReportSample{
			{BucketStartMs: 5000, SampleCount: 5},
			{BucketStartMs: 5000, SampleCount: 5}, // 批内重复
			{BucketStartMs: 10000, SampleCount: 5},
		},
	})
	if err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	if res.Accepted != 2 || res.Deduplicated != 1 {
		t.Fatalf("批内重复应去重 1，实际 %+v", res)
	}
	if len(enq.enqueued[0]) != 2 {
		t.Fatalf("应仅入队 2 行，实际 %d", len(enq.enqueued[0]))
	}
}

// TestIngestClockSkew 校验时钟偏移超阈值整批 400。
func TestIngestClockSkew(t *testing.T) {
	enq := &fakeEnqueuer{}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)
	// agent 时钟比控制面早 6 分钟，超 5 分钟阈值。
	skewed := now.Add(-6 * time.Minute).UnixMilli()
	_, err := svc.Ingest(MetricReportParams{
		Identity: backendIdentity(), AgentTimeMs: skewed,
		Samples: []MetricReportSample{{BucketStartMs: 5000, SampleCount: 5}},
	})
	if !errors.Is(err, apperr.ErrClockSkewTooLarge) {
		t.Fatalf("应返回 clock_skew_too_large，实际 %v", err)
	}
	if len(enq.enqueued) != 0 {
		t.Fatalf("时钟偏移拒绝不应入队")
	}
}

// TestIngestQueueFull 校验队列满回 429，且窗口未被改（agent 重发可正常接收）。
func TestIngestQueueFull(t *testing.T) {
	enq := &fakeEnqueuer{full: true}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)
	p := MetricReportParams{
		Identity: backendIdentity(), AgentTimeMs: now.UnixMilli(),
		Samples: []MetricReportSample{{BucketStartMs: 5000, SampleCount: 5}},
	}
	_, err := svc.Ingest(p)
	if !errors.Is(err, apperr.ErrMetricsIngestBusy) {
		t.Fatalf("队列满应返回 metrics_ingest_busy，实际 %v", err)
	}
	// 队列恢复后同批应能正常接收（证明 429 未污染窗口）。
	enq.full = false
	res, err := svc.Ingest(p)
	if err != nil || res.Accepted != 1 || res.Deduplicated != 0 {
		t.Fatalf("429 后窗口应未变、重发应正常接收，实际 err=%v res=%+v", err, res)
	}
}

// TestIngestBindingMismatch 校验请求体自报 serverId 与权威身份不一致时拒绝。
func TestIngestBindingMismatch(t *testing.T) {
	enq := &fakeEnqueuer{}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)
	_, err := svc.Ingest(MetricReportParams{
		Identity: backendIdentity(), BodyServerID: "someone-else", AgentTimeMs: now.UnixMilli(),
		Samples: []MetricReportSample{{BucketStartMs: 5000, SampleCount: 5}},
	})
	if !errors.Is(err, apperr.ErrIdentityBindingMismatch) {
		t.Fatalf("serverId 不一致应拒绝，实际 %v", err)
	}
}

// TestIngestBatchTooLarge 校验超单批上限（120）拒绝。
func TestIngestBatchTooLarge(t *testing.T) {
	enq := &fakeEnqueuer{}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)
	big := make([]MetricReportSample, maxSamplesPerReport+1)
	for i := range big {
		big[i] = MetricReportSample{BucketStartMs: int64((i + 1) * 5000), SampleCount: 5}
	}
	_, err := svc.Ingest(MetricReportParams{Identity: backendIdentity(), AgentTimeMs: now.UnixMilli(), Samples: big})
	if !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("超批上限应 400，实际 %v", err)
	}
}

// TestIngestEmptySamples 空样本为安全空操作，返回 202 accepted=0。
func TestIngestEmptySamples(t *testing.T) {
	enq := &fakeEnqueuer{}
	now := time.UnixMilli(1_000_000_000_000).UTC()
	svc := newIngestSvc(enq, now)
	res, err := svc.Ingest(MetricReportParams{Identity: backendIdentity(), AgentTimeMs: now.UnixMilli()})
	if err != nil || res.Accepted != 0 || res.Deduplicated != 0 {
		t.Fatalf("空样本应空操作，实际 err=%v res=%+v", err, res)
	}
}
