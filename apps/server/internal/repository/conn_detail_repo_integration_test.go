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

// dropDailyIT 删掉某 UTC 日的日表，给集成用例干净起点（表跨运行持久，须先清）。
func dropDailyIT(t *testing.T, db *gorm.DB, base string, day time.Time) string {
	t.Helper()
	name := store.DailyTableName(base, day)
	_ = db.Migrator().DropTable(name)
	return name
}

// TestConnFlushDailyMySQL 真 MySQL：会话行 open 插入 / close upsert 更新、DATETIME(3) 与可空列可移植、跨日定位 open 日表。
func TestConnFlushDailyMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5a_conn")
	repo := NewConnDetailRepository(db)

	// 先清齐本用例用到的三张日表（同库跨运行持久；须在任何 EnsureDailyTable 之前，避免进程缓存与物理表不一致）。
	openedMs := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	crossOpenMs := time.Date(2026, 7, 8, 23, 59, 0, 0, time.UTC).UnixMilli() // 与上面不同日，避免同表被中途 drop
	name := dropDailyIT(t, db, "conn_detail", time.UnixMilli(openedMs).UTC())
	crossName := dropDailyIT(t, db, "conn_detail", time.UnixMilli(crossOpenMs).UTC())
	nextName := dropDailyIT(t, db, "conn_detail", time.UnixMilli(crossOpenMs+2*3600*1000).UTC())
	cid := uuidV7At(openedMs, "it1")

	if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(cid, 1, "proxy-1", "alice", openedMs)}); err != nil {
		t.Fatalf("open 写入失败: %v", err)
	}
	if _, err := repo.FlushDaily([]model.ConnEvent{closeEvent(cid, 1, "proxy-1", "alice", openedMs, openedMs+5000)}); err != nil {
		t.Fatalf("close 写入失败: %v", err)
	}
	if n := countDaily(t, db, name); n != 1 {
		t.Fatalf("close 应更新同一行、总数仍 1，实际 %d", n)
	}
	row := fetchConn(t, db, name, cid)
	if row.Status != model.ConnStatusClosed || row.ClosedAt == nil || row.DurationMs == nil || *row.DurationMs != 5000 {
		t.Fatalf("MySQL 下 close 会话行 / 可空列不符: %+v", row)
	}
	if row.FirstBackendServerID != "game-1" || row.LastBackendServerID != "game-2" {
		t.Fatalf("close 应保留 open 首后端并落末后端: %+v", row)
	}

	// 跨日：open 于 D、close 于 D+1，行留在 open 日表且被闭合，不在 D+1 建表。
	ccid := uuidV7At(crossOpenMs, "it2")
	if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(ccid, 1, "proxy-1", "carol", crossOpenMs)}); err != nil {
		t.Fatalf("跨日 open 失败: %v", err)
	}
	if _, err := repo.FlushDaily([]model.ConnEvent{closeEvent(ccid, 1, "proxy-1", "carol", crossOpenMs, crossOpenMs+2*3600*1000)}); err != nil {
		t.Fatalf("跨日 close 失败: %v", err)
	}
	if r := fetchConn(t, db, crossName, ccid); r.Status != model.ConnStatusClosed {
		t.Fatalf("跨日 close 应闭合 open 日表内的行，实际 %s", r.Status)
	}
	if db.Migrator().HasTable(nextName) {
		t.Fatalf("跨日 close 不应在 D+1 日表另建物理表")
	}
}

// TestConnFlushDailyMySQLStable 真 MySQL 关键路径 -count 防脆：open→close upsert 幂等重放不增行、不改错状态。
func TestConnFlushDailyMySQLStable(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5a_conn")
	repo := NewConnDetailRepository(db)
	openedMs := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC).UnixMilli()
	name := dropDailyIT(t, db, "conn_detail", time.UnixMilli(openedMs).UTC())
	cid := uuidV7At(openedMs, "its")

	batch := []model.ConnEvent{
		openEvent(cid, 1, "proxy-1", "dan", openedMs),
		closeEvent(cid, 1, "proxy-1", "dan", openedMs, openedMs+1000),
	}
	if _, err := repo.FlushDaily(batch); err != nil {
		t.Fatalf("首写失败: %v", err)
	}
	if _, err := repo.FlushDaily(batch); err != nil { // 重放
		t.Fatalf("重放失败: %v", err)
	}
	if n := countDaily(t, db, name); n != 1 {
		t.Fatalf("重放后应仍 1 行，实际 %d", n)
	}
	if r := fetchConn(t, db, name, cid); r.Status != model.ConnStatusClosed {
		t.Fatalf("重放后应仍 closed，实际 %s", r.Status)
	}
}

// TestConnOrphanAndListOpenMySQL 真 MySQL：孤儿对账跨日 UPDATE + open 行投影 SELECT 可移植。
func TestConnOrphanAndListOpenMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5a_conn")
	repo := NewConnDetailRepository(db)
	base := time.Now().UTC().Add(-2 * time.Hour).UnixMilli() // 落今日表，供 retention 回溯命中
	name := dropDailyIT(t, db, "conn_detail", time.UnixMilli(base).UTC())

	oldCid := uuidV7At(base, "ito")
	freshCid := uuidV7At(base+3600*1000, "itf")
	if _, err := repo.FlushDaily([]model.ConnEvent{
		openEvent(oldCid, 1, "proxy-9", "old", base),
		openEvent(freshCid, 1, "proxy-9", "new", base+3600*1000),
	}); err != nil {
		t.Fatalf("写 open 失败: %v", err)
	}

	before := time.UnixMilli(base + 1800*1000).UTC()
	n, err := repo.CloseOrphans(1, "proxy-9", before, 8)
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应补 close 1 条孤儿，实际 %d", n)
	}
	if r := fetchConn(t, db, name, oldCid); r.Status != model.ConnStatusClosed || r.CloseKind != model.ConnCloseKindProxyShutdown {
		t.Fatalf("旧孤儿应补 close 为 proxy_shutdown，实际 %s/%s", r.Status, r.CloseKind)
	}

	open, err := repo.ListOpenConnections(8)
	if err != nil {
		t.Fatalf("列 open 失败: %v", err)
	}
	var freshFound bool
	for _, oc := range open {
		if oc.ConnID == freshCid && oc.PlayerUUID == "new" && oc.ProxyServerID == "proxy-9" {
			freshFound = true
		}
		if oc.ConnID == oldCid {
			t.Fatalf("已 close 的孤儿不应出现在 open 投影中")
		}
	}
	if !freshFound {
		t.Fatalf("鲜活 open 会话应出现在 open 投影中")
	}
}
