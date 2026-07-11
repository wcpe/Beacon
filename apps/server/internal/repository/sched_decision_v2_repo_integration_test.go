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
