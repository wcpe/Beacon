//go:build integration

package repository

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// dropSchedDaily 删掉某 UTC 日的决策日表，给集成用例干净起点（表在库间持久，跨运行须先清）。
func dropSchedDaily(t *testing.T, db *gorm.DB, day time.Time) string {
	t.Helper()
	name := store.DailyTableName("sched_decision", day)
	_ = db.Migrator().DropTable(name)
	return name
}

// TestSchedDecisionFlushDailyMySQL 真 MySQL：日表按需建 + 行落库 + trace_id 幂等去重 + 跨日拆表。
func TestSchedDecisionFlushDailyMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "fr146_repo")
	day1 := time.Date(2026, 7, 11, 23, 59, 59, 0, time.UTC)
	day2 := time.Date(2026, 7, 12, 0, 0, 1, 0, time.UTC)
	name1 := dropSchedDaily(t, db, day1)
	name2 := dropSchedDaily(t, db, day2)
	repo := NewSchedDecisionV2Repository(db)

	// 跨日批：day1 一行 + day2 两行 → 两张日表按需建、行各归其日。
	dedup, err := repo.FlushDaily([]model.SchedDecisionV2{
		schedRowAt("it-trace-d1", day1.UnixMilli()),
		schedRowAt("it-trace-d2a", day2.UnixMilli()),
		schedRowAt("it-trace-d2b", day2.Add(time.Second).UnixMilli()),
	})
	if err != nil {
		t.Fatalf("跨日批写入失败: %v", err)
	}
	if dedup != 0 {
		t.Fatalf("首次写入不应去重，实际 %d", dedup)
	}
	if !db.Migrator().HasTable(name1) || !db.Migrator().HasTable(name2) {
		t.Fatalf("日表应按需建齐：%s / %s", name1, name2)
	}
	if n := countDaily(t, db, name1); n != 1 {
		t.Fatalf("%s 应 1 行，实际 %d", name1, n)
	}
	if n := countDaily(t, db, name2); n != 2 {
		t.Fatalf("%s 应 2 行，实际 %d", name2, n)
	}

	// 重放（含一条新行）→ 旧 trace 去重、新行落库、旧行数不重复增长。
	dedup, err = repo.FlushDaily([]model.SchedDecisionV2{
		schedRowAt("it-trace-d1", day1.UnixMilli()),
		schedRowAt("it-trace-d2a", day2.UnixMilli()),
		schedRowAt("it-trace-d2c", day2.Add(2*time.Second).UnixMilli()),
	})
	if err != nil {
		t.Fatalf("重放写入失败: %v", err)
	}
	if dedup != 2 {
		t.Fatalf("重放应去重 2，实际 %d", dedup)
	}
	if n := countDaily(t, db, name1); n != 1 {
		t.Fatalf("重放后 %s 应仍 1 行，实际 %d", name1, n)
	}
	if n := countDaily(t, db, name2); n != 3 {
		t.Fatalf("重放后 %s 应 3 行（新增 1），实际 %d", name2, n)
	}

	// 行内容回读：字段按列落库（抽查 excluded json 文本与 chosen）。
	var got model.SchedDecisionV2
	if err := db.Table(name1).Where("trace_id = ?", "it-trace-d1").Take(&got).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Excluded != "[]" || got.ChosenServerID != "s-1" || got.ChosenScore != 90 {
		t.Fatalf("回读行内容不符: %+v", got)
	}
}

// TestSchedDecisionQueryMySQL 真 MySQL：跨日并表列表过滤 / 分页、详情逆序查表、概览聚合真查。
func TestSchedDecisionQueryMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "fr146_repo")
	now := time.Now().UTC()
	dropSchedDaily(t, db, now)
	dropSchedDaily(t, db, now.AddDate(0, 0, -1))
	seedSchedQueryRows(t, db)
	repo := NewSchedDecisionV2Repository(db)
	fromMs, toMs := queryWindow()

	// 列表：全量降序 + 跨表第 2 页 + 过滤抽查。
	rows, total, err := repo.QueryRange(SchedDecisionQuery{FromMs: fromMs, ToMs: toMs, Limit: 20})
	if err != nil || total != 5 || len(rows) != 5 {
		t.Fatalf("全量应 5 行，实际 total=%d len=%d err=%v", total, len(rows), err)
	}
	rows, _, err = repo.QueryRange(SchedDecisionQuery{FromMs: fromMs, ToMs: toMs, Offset: 2, Limit: 2})
	if err != nil || len(rows) != 2 || rows[0].TraceID != "q-t1" || rows[1].TraceID != "q-y2" {
		t.Fatalf("跨表第 2 页应 [q-t1, q-y2]，实际 %+v err=%v", rows, err)
	}
	_, total, err = repo.QueryRange(SchedDecisionQuery{FromMs: fromMs, ToMs: toMs, Limit: 20, Result: "failed"})
	if err != nil || total != 1 {
		t.Fatalf("result=failed 应 1 行，实际 %d err=%v", total, err)
	}
	_, total, err = repo.QueryRange(SchedDecisionQuery{FromMs: fromMs, ToMs: toMs, Limit: 20, ServerID: "req-9"})
	if err != nil || total != 1 {
		t.Fatalf("serverId=req-9 应 1 行，实际 %d err=%v", total, err)
	}

	// 详情：昨日表命中；未知 trace 未命中。
	row, err := repo.FindByTraceID("q-y1", 60)
	if err != nil || row == nil || row.TraceID != "q-y1" {
		t.Fatalf("昨日 trace 应命中，实际 %+v err=%v", row, err)
	}
	if row, err = repo.FindByTraceID("q-nope", 60); err != nil || row != nil {
		t.Fatalf("未知 trace 应 nil，实际 %+v err=%v", row, err)
	}

	// 概览聚合：总数 / 成功 / 失败分布 / 降级数。
	agg, err := repo.Summarize(fromMs, toMs)
	if err != nil || agg.Total != 5 || agg.SuccessCount != 4 || agg.FallbackCount != 1 ||
		agg.FailReasonCounts["no_candidate"] != 1 {
		t.Fatalf("聚合不符: %+v err=%v", agg, err)
	}
}
