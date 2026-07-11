package service

import (
	"reflect"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// TestIngestSelfHealthBackfill 校验上报响应回填自身健康视图（FR-147，§5.1 self）：
// 未注入存储 / 尚无视图 → nil；有视图 → 回填 score/level/schedulable/reasons；空样本请求同样回填。
func TestIngestSelfHealthBackfill(t *testing.T) {
	now := time.UnixMilli(1_000_000_000_000).UTC()
	id := backendIdentity()
	samples := []MetricReportSample{{BucketStartMs: 5000, SampleCount: 5, TPSAvg: 19.8}}

	// 未注入视图存储：self 恒 nil（向后兼容占位语义）。
	svc := newIngestSvc(&fakeEnqueuer{}, now)
	res, err := svc.Ingest(MetricReportParams{Identity: id, AgentTimeMs: now.UnixMilli(), Samples: samples})
	if err != nil || res.Self != nil {
		t.Fatalf("未注入存储 self 应为 nil: %v %+v", err, res.Self)
	}

	// 注入存储但尚无该实例视图：nil。
	svc = newIngestSvc(&fakeEnqueuer{}, now)
	views := healthview.NewStore()
	svc.SetHealthViews(views)
	res, err = svc.Ingest(MetricReportParams{Identity: id, AgentTimeMs: now.UnixMilli(), Samples: samples})
	if err != nil || res.Self != nil {
		t.Fatalf("尚无视图 self 应为 nil: %v %+v", err, res.Self)
	}

	// 有视图：回填四字段；reasons 保持非 nil。
	views.ReplaceAll([]healthview.View{{
		NamespaceID: id.NamespaceID, Namespace: id.Namespace, ServerID: id.ServerID, Kind: id.Kind,
		Score: 42, Level: healthview.LevelDegraded, Schedulable: true, Reasons: nil,
	}})
	res, err = svc.Ingest(MetricReportParams{Identity: id, AgentTimeMs: now.UnixMilli(),
		Samples: []MetricReportSample{{BucketStartMs: 10_000, SampleCount: 5}}})
	if err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	want := &SelfHealthView{Score: 42, Level: healthview.LevelDegraded, Schedulable: true, Reasons: []string{}}
	if !reflect.DeepEqual(res.Self, want) {
		t.Fatalf("self 应回填视图，期望 %+v 实际 %+v", want, res.Self)
	}

	// 空样本请求（纯活性心跳语义）同样回填 self。
	res, err = svc.Ingest(MetricReportParams{Identity: id, AgentTimeMs: now.UnixMilli()})
	if err != nil || res.Self == nil || res.Self.Score != 42 {
		t.Fatalf("空样本请求也应回填 self: %v %+v", err, res.Self)
	}

	// 视图属他服不串台：换个 serverId 上报 → nil。
	other := proxyIdentity()
	res, err = svc.Ingest(MetricReportParams{Identity: other, AgentTimeMs: now.UnixMilli()})
	if err != nil || res.Self != nil {
		t.Fatalf("他服不应串台取到视图: %v %+v", err, res.Self)
	}
}
