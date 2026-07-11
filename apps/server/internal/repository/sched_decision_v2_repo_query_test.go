package repository

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// seedSchedQueryRows 造两日决策数据：今日 3 行（1 失败 + 1 降级补报）、昨日 2 行。
// 返回 (今日零点毫秒, 昨日零点毫秒)。
func seedSchedQueryRows(t *testing.T, db *gorm.DB) (todayMs, yesterdayMs int64) {
	t.Helper()
	repo := NewSchedDecisionV2Repository(db)
	// 锚定 UTC 日零点（而非 now 相对偏移）：任何时刻运行行都稳定归属今日 / 昨日两表，不跨日漂移。
	today := utcDayStart(time.Now().UTC().UnixMilli()).Add(time.Second)
	yesterday := today.AddDate(0, 0, -1)

	rows := []model.SchedDecisionV2{
		// 今日：成功（ns1 / area-1 / 选中 s-a）
		func() model.SchedDecisionV2 {
			r := schedRowAt("q-t1", today.UnixMilli())
			r.ChosenServerID = "s-a"
			return r
		}(),
		// 今日：失败 no_candidate（ns1 / area-2）
		func() model.SchedDecisionV2 {
			r := schedRowAt("q-t2", today.Add(time.Minute).UnixMilli())
			r.ZoneName = "area-2"
			r.ChosenServerID = ""
			r.ChosenScore = -1
			r.FailReason = "no_candidate"
			return r
		}(),
		// 今日：降级补报（ns2 / requester req-9）
		func() model.SchedDecisionV2 {
			r := schedRowAt("q-t3", today.Add(2*time.Minute).UnixMilli())
			r.NamespaceID = 2
			r.RequesterServerID = "req-9"
			r.Source = model.SchedSourceLocalFallback
			return r
		}(),
		// 昨日：成功 ×2（ns1）
		schedRowAt("q-y1", yesterday.UnixMilli()),
		func() model.SchedDecisionV2 {
			r := schedRowAt("q-y2", yesterday.Add(time.Minute).UnixMilli())
			r.ChosenServerID = "s-b"
			return r
		}(),
	}
	if _, err := repo.FlushDaily(rows); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	return today.UnixMilli(), yesterday.UnixMilli()
}

// queryWindow 返回覆盖近两日的查询窗口。
func queryWindow() (fromMs, toMs int64) {
	now := time.Now().UTC()
	return now.AddDate(0, 0, -2).UnixMilli(), now.UnixMilli()
}

// TestSchedQueryRangeFiltersAndPaging 跨日并表查询：过滤（ns/zone/serverId/result）+ 降序 + 跨表分页。
func TestSchedQueryRangeFiltersAndPaging(t *testing.T) {
	db := openRepoSQLite(t, "sched_v2_query")
	seedSchedQueryRows(t, db)
	repo := NewSchedDecisionV2Repository(db)
	fromMs, toMs := queryWindow()
	base := SchedDecisionQuery{FromMs: fromMs, ToMs: toMs, Limit: 20}

	// 无过滤：全部 5 行，ts_ms 降序（今日在前）。
	rows, total, err := repo.QueryRange(base)
	if err != nil || total != 5 || len(rows) != 5 {
		t.Fatalf("全量应 5 行，实际 total=%d len=%d err=%v", total, len(rows), err)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].TsMs < rows[i].TsMs {
			t.Fatalf("应按 ts_ms 降序，实际 %d < %d", rows[i-1].TsMs, rows[i].TsMs)
		}
	}

	// 跨表分页：页大小 2，第 2 页应跨过今日最后一行进入昨日。
	page2 := base
	page2.Offset, page2.Limit = 2, 2
	rows, total, err = repo.QueryRange(page2)
	if err != nil || total != 5 || len(rows) != 2 {
		t.Fatalf("第 2 页应 2 行 total=5，实际 total=%d len=%d err=%v", total, len(rows), err)
	}
	if rows[0].TraceID != "q-t1" || rows[1].TraceID != "q-y2" {
		t.Fatalf("第 2 页应跨日 [q-t1, q-y2]，实际 [%s, %s]", rows[0].TraceID, rows[1].TraceID)
	}

	cases := []struct {
		name   string
		mutate func(*SchedDecisionQuery)
		want   int64
	}{
		{"按 namespaceId", func(q *SchedDecisionQuery) { q.NamespaceID = 2 }, 1},
		{"按 zone", func(q *SchedDecisionQuery) { q.Zone = "area-2" }, 1},
		{"按 serverId 匹配 chosen", func(q *SchedDecisionQuery) { q.ServerID = "s-b" }, 1},
		{"按 serverId 匹配 requester", func(q *SchedDecisionQuery) { q.ServerID = "req-9" }, 1},
		{"result=failed", func(q *SchedDecisionQuery) { q.Result = "failed" }, 1},
		{"result=success", func(q *SchedDecisionQuery) { q.Result = "success" }, 4},
	}
	for _, c := range cases {
		q := base
		c.mutate(&q)
		_, total, err := repo.QueryRange(q)
		if err != nil || total != c.want {
			t.Fatalf("%s 应 %d 行，实际 %d err=%v", c.name, c.want, total, err)
		}
	}
}

// TestSchedQueryRangeSkipsMissingTables 范围含不存在日表：跳过缺表不隐式建表、不报错。
func TestSchedQueryRangeSkipsMissingTables(t *testing.T) {
	db := openRepoSQLite(t, "sched_v2_missing")
	repo := NewSchedDecisionV2Repository(db)
	fromMs, toMs := queryWindow()

	rows, total, err := repo.QueryRange(SchedDecisionQuery{FromMs: fromMs, ToMs: toMs, Limit: 20})
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("空库应 0 行不报错，实际 total=%d err=%v", total, err)
	}
	// 查询侧严禁隐式建表：查询后日表仍不存在。
	if db.Migrator().HasTable("sched_decision_" + time.Now().UTC().Format("20060102")) {
		t.Fatal("查询不应隐式建当日表")
	}
}

// TestSchedFindByTraceID 详情逆序逐日表查：今日 / 昨日命中，未知 trace 返回 nil。
func TestSchedFindByTraceID(t *testing.T) {
	db := openRepoSQLite(t, "sched_v2_find")
	seedSchedQueryRows(t, db)
	repo := NewSchedDecisionV2Repository(db)

	row, err := repo.FindByTraceID("q-t2", 60)
	if err != nil || row == nil || row.FailReason != "no_candidate" {
		t.Fatalf("今日 trace 应命中，实际 %+v err=%v", row, err)
	}
	row, err = repo.FindByTraceID("q-y1", 60)
	if err != nil || row == nil || row.ChosenServerID != "s-1" {
		t.Fatalf("昨日 trace 应命中，实际 %+v err=%v", row, err)
	}
	row, err = repo.FindByTraceID("q-nope", 60)
	if err != nil || row != nil {
		t.Fatalf("未知 trace 应返回 nil，实际 %+v err=%v", row, err)
	}
}

// TestSchedSummarize 聚合：总数 / 成功数 / 失败原因分布 / 降级补报数（跨日累加）。
func TestSchedSummarize(t *testing.T) {
	db := openRepoSQLite(t, "sched_v2_sum")
	seedSchedQueryRows(t, db)
	repo := NewSchedDecisionV2Repository(db)
	fromMs, toMs := queryWindow()

	agg, err := repo.Summarize(fromMs, toMs)
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if agg.Total != 5 || agg.SuccessCount != 4 || agg.FallbackCount != 1 {
		t.Fatalf("聚合计数不符: %+v", agg)
	}
	if agg.FailReasonCounts["no_candidate"] != 1 || len(agg.FailReasonCounts) != 1 {
		t.Fatalf("失败原因分布不符: %+v", agg.FailReasonCounts)
	}
}
