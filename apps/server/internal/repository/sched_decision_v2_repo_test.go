package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// schedRowAt 构造一条最小决策行。
func schedRowAt(traceID string, tsMs int64) model.SchedDecisionV2 {
	return model.SchedDecisionV2{
		TraceID: traceID, TsMs: tsMs, NamespaceID: 1, RequesterServerID: "req-1",
		ZoneName: "area-1", Strategy: "highest_score", Source: "control_plane",
		CandidateCount: 2, Excluded: "[]", ChosenServerID: "s-1", ChosenScore: 90,
	}
}

// TestSchedFlushDailyIdempotentDedup 校验 trace_id 唯一键幂等去重：重放同批被去重、行数不增。
func TestSchedFlushDailyIdempotentDedup(t *testing.T) {
	db := openRepoSQLite(t, "sched_v2_dedup")
	repo := NewSchedDecisionV2Repository(db)
	day := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	name := store.DailyTableName("sched_decision", day)

	batch := []model.SchedDecisionV2{
		schedRowAt("trace-a", day.UnixMilli()),
		schedRowAt("trace-b", day.Add(time.Minute).UnixMilli()),
	}
	dedup, err := repo.FlushDaily(batch)
	if err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if dedup != 0 {
		t.Fatalf("首次写入不应去重，实际 %d", dedup)
	}
	if n := countDaily(t, db, name); n != 2 {
		t.Fatalf("应落 2 行，实际 %d", n)
	}

	// 重放同批（trace_id 相同、其余字段可不同）→ 全部去重、行数不增。
	replay := []model.SchedDecisionV2{
		schedRowAt("trace-a", day.UnixMilli()),
		schedRowAt("trace-b", day.Add(time.Minute).UnixMilli()),
	}
	replay[0].ChosenServerID = "s-other"
	dedup, err = repo.FlushDaily(replay)
	if err != nil {
		t.Fatalf("重放写入失败: %v", err)
	}
	if dedup != 2 {
		t.Fatalf("重放应去重 2，实际 %d", dedup)
	}
	if n := countDaily(t, db, name); n != 2 {
		t.Fatalf("重放后应仍 2 行，实际 %d", n)
	}
}

// TestSchedFlushDailyCrossDaySplit 跨日批按 ts_ms 拆分写入两张日表。
func TestSchedFlushDailyCrossDaySplit(t *testing.T) {
	db := openRepoSQLite(t, "sched_v2_crossday")
	repo := NewSchedDecisionV2Repository(db)
	day1 := time.Date(2026, 7, 11, 23, 59, 59, 0, time.UTC)
	day2 := time.Date(2026, 7, 12, 0, 0, 1, 0, time.UTC)

	dedup, err := repo.FlushDaily([]model.SchedDecisionV2{
		schedRowAt("trace-d1", day1.UnixMilli()),
		schedRowAt("trace-d2a", day2.UnixMilli()),
		schedRowAt("trace-d2b", day2.Add(time.Second).UnixMilli()),
	})
	if err != nil {
		t.Fatalf("跨日批写入失败: %v", err)
	}
	if dedup != 0 {
		t.Fatalf("跨日批不应去重，实际 %d", dedup)
	}
	name1 := store.DailyTableName("sched_decision", day1)
	name2 := store.DailyTableName("sched_decision", day2)
	if n := countDaily(t, db, name1); n != 1 {
		t.Fatalf("%s 应 1 行，实际 %d", name1, n)
	}
	if n := countDaily(t, db, name2); n != 2 {
		t.Fatalf("%s 应 2 行，实际 %d", name2, n)
	}
}
