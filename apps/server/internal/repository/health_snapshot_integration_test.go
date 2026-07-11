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

// dropSnapshotDaily 删掉某 UTC 日的快照日表，给集成用例干净起点（表在库间持久，跨运行须先清）。
func dropSnapshotDaily(t *testing.T, db *gorm.DB, day time.Time) string {
	t.Helper()
	name := store.DailyTableName("health_snapshot", day)
	_ = db.Migrator().DropTable(name)
	return name
}

// snapshotRowAt 构造一行快照（ts 毫秒）。
func snapshotRowAt(tsMs int64, serverID string, score int) model.HealthSnapshot {
	return model.HealthSnapshot{
		TsMs: tsMs, NamespaceID: 1, ServerID: serverID, Kind: model.ServerKindBackend,
		Score: score, Level: "healthy", Schedulable: true,
		Reasons: "[]", Factors: "[]", WeightsRev: 1,
	}
}

// TestHealthSnapshotFlushDailyMySQL 真 MySQL：快照日表按需建 + 批量落库可查 + 跨日批自动拆分。
func TestHealthSnapshotFlushDailyMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "fr147_snapshot")
	repo := NewHealthSnapshotRepository(db)

	// 当日批：按需建表 + 行落库可查（含 reasons/factors json 文本回读）。
	now := time.Now().UTC()
	name := dropSnapshotDaily(t, db, now)
	rows := []model.HealthSnapshot{
		snapshotRowAt(now.UnixMilli(), "it-h1", 95),
		snapshotRowAt(now.UnixMilli(), "it-h2", 42),
	}
	rows[1].Level = "unhealthy"
	rows[1].Schedulable = false
	rows[1].Reasons = `["unhealthy"]`
	if _, err := repo.FlushDaily(rows); err != nil {
		t.Fatalf("快照落库失败: %v", err)
	}
	if !db.Migrator().HasTable(name) {
		t.Fatalf("当日快照表 %s 应已按需建出", name)
	}
	var got []model.HealthSnapshot
	if err := db.Table(name).Where("server_id = ?", "it-h2").Order("ts_ms ASC").Find(&got).Error; err != nil {
		t.Fatalf("回查失败: %v", err)
	}
	if len(got) != 1 || got[0].Score != 42 || got[0].Reasons != `["unhealthy"]` || got[0].Schedulable {
		t.Fatalf("回查行不符，实际 %+v", got)
	}

	// 跨日批：按 ts_ms 拆分两表写入。
	d1 := time.Date(2031, 5, 5, 23, 59, 55, 0, time.UTC)
	d2 := time.Date(2031, 5, 6, 0, 0, 5, 0, time.UTC)
	n1 := dropSnapshotDaily(t, db, d1)
	n2 := dropSnapshotDaily(t, db, d2)
	cross := []model.HealthSnapshot{
		snapshotRowAt(d1.UnixMilli(), "it-h1", 90),
		snapshotRowAt(d2.UnixMilli(), "it-h1", 91),
		snapshotRowAt(d2.Add(30*time.Second).UnixMilli(), "it-h1", 92),
	}
	if _, err := repo.FlushDaily(cross); err != nil {
		t.Fatalf("跨日写入失败: %v", err)
	}
	var c1, c2 int64
	if err := db.Table(n1).Count(&c1).Error; err != nil {
		t.Fatalf("查 %s 失败: %v", n1, err)
	}
	if err := db.Table(n2).Count(&c2).Error; err != nil {
		t.Fatalf("查 %s 失败: %v", n2, err)
	}
	if c1 != 1 || c2 != 2 {
		t.Fatalf("跨日拆分应 1/2 行，实际 %d/%d", c1, c2)
	}
}
