package service

import (
	"errors"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// TestValidateRangeFilter 条件查询防护：缺范围 / 倒置 / 超 168h 均拒；无 selector 但有合法时间窗放行（全局近期）。
func TestValidateRangeFilter(t *testing.T) {
	const hour = int64(3_600_000)
	cases := []struct {
		name        string
		hasSelector bool
		from, to    int64
		wantErr     bool
	}{
		{"无 selector 有时间窗（全局近期）", false, 1, 1 + hour, false},
		{"缺 from", true, 0, 100, true},
		{"缺 to", true, 100, 0, true},
		{"倒置", true, 200, 100, true},
		{"超 168h", true, 1, 1 + 169*hour, true},
		{"恰 168h 合法", true, 1, 1 + 168*hour, false},
		{"正常 1h", true, 1, 1 + hour, false},
		{"无 selector 缺时间窗", false, 0, 0, true},
	}
	for _, c := range cases {
		err := validateRangeFilter(c.hasSelector, c.from, c.to)
		if c.wantErr && !errors.Is(err, apperr.ErrQueryGuardViolation) {
			t.Fatalf("%s：应 ErrQueryGuardViolation，实际 %v", c.name, err)
		}
		if !c.wantErr && err != nil {
			t.Fatalf("%s：应放行，实际 %v", c.name, err)
		}
	}
}

// TestClampAndCursor 分页大小 / 偏移规整与下一游标推导。
func TestClampAndCursor(t *testing.T) {
	if clampLimit(0) != 50 || clampLimit(-1) != 50 {
		t.Fatalf("默认分页大小应 50")
	}
	if clampLimit(500) != 200 {
		t.Fatalf("分页上限应收敛到 200")
	}
	if clampLimit(30) != 30 {
		t.Fatalf("合法分页应原样")
	}
	if clampOffset(-5) != 0 {
		t.Fatalf("负偏移应归 0")
	}
	if nextCursorOf(0, 50, false) != "" {
		t.Fatalf("无下一页应空串")
	}
	if nextCursorOf(50, 50, true) != "100" {
		t.Fatalf("下一游标应 offset+limit=100")
	}
}

// TestAggregateByEdge 边聚合：失败率 / p95 / 样本上限 / top 原因；未解析目标归 (未解析)。
func TestAggregateByEdge(t *testing.T) {
	d1, d2 := int64(100), int64(300)
	rows := []repository.MsgStatRow{
		{MessageID: "m1", SourceServerID: "a", ResolvedServerID: "b", Status: model.MsgStatusDelivered, DurationMs: &d1},
		{MessageID: "m2", SourceServerID: "a", ResolvedServerID: "b", Status: model.MsgStatusFailed, FailReason: "ack_timeout"},
		{MessageID: "m3", SourceServerID: "a", ResolvedServerID: "b", Status: model.MsgStatusExpired, FailReason: "ttl_expired", DurationMs: &d2},
		{MessageID: "m4", SourceServerID: "c", ResolvedServerID: "", Status: model.MsgStatusFailed, FailReason: "player_not_online"},
	}
	edges := aggregateByEdge(rows)
	if len(edges) != 2 {
		t.Fatalf("应 2 条边，实际 %d", len(edges))
	}
	// a→b：total3、failed1、expired1、failRate=(1+1)/3≈66.7。
	var ab, cUnresolved *MsgEdgeStat
	for i := range edges {
		switch edges[i].SourceServerID {
		case "a":
			ab = &edges[i]
		case "c":
			cUnresolved = &edges[i]
		}
	}
	if ab == nil || ab.Total != 3 || ab.Failed != 1 || ab.Expired != 1 {
		t.Fatalf("a→b 计数不符: %+v", ab)
	}
	if ab.FailRatePercent < 66.6 || ab.FailRatePercent > 66.8 {
		t.Fatalf("a→b 失败率应≈66.7，实际 %v", ab.FailRatePercent)
	}
	if len(ab.SampleMessageIDs) != 2 {
		t.Fatalf("a→b 应 2 条失败样本，实际 %v", ab.SampleMessageIDs)
	}
	if cUnresolved == nil || cUnresolved.ResolvedServerID != unresolvedEdgeTarget {
		t.Fatalf("未解析目标应归 (未解析)，实际 %+v", cUnresolved)
	}
}

// TestAggregateByType 类型聚合：total / failed 计数，failed+expired 计失败，按 total 降序。
func TestAggregateByType(t *testing.T) {
	rows := []repository.MsgStatRow{
		{MsgType: "chat", Status: model.MsgStatusDelivered},
		{MsgType: "chat", Status: model.MsgStatusFailed},
		{MsgType: "tp", Status: model.MsgStatusExpired},
	}
	types := aggregateByType(rows)
	if len(types) != 2 || types[0].MsgType != "chat" || types[0].Total != 2 || types[0].Failed != 1 {
		t.Fatalf("类型聚合不符: %+v", types)
	}
	if types[1].MsgType != "tp" || types[1].Failed != 1 {
		t.Fatalf("expired 应计失败: %+v", types[1])
	}
}

// TestP95AndPercent p95 分位与百分比取整口径。
func TestP95AndPercent(t *testing.T) {
	if p95Of(nil) != 0 {
		t.Fatalf("空耗时 p95 应 0")
	}
	if got := p95Of([]int64{10, 20, 30, 40, 50}); got != 50 {
		t.Fatalf("5 元素 p95 应取末位 50，实际 %d", got)
	}
	if got := roundPercent1(1, 3); got < 33.2 || got > 33.4 {
		t.Fatalf("1/3 应≈33.3，实际 %v", got)
	}
	if roundPercent1(0, 0) != 0 {
		t.Fatalf("0/0 应 0")
	}
}
