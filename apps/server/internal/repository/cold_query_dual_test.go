package repository

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 冷查询（includeArchived）双连接并表测试（FR-152，spec §4.4）：用两个内存 sqlite 库模拟热 / 冷库，
// 覆盖有序归并、主键去重保热侧、游标分页、默认查询不触归档、namespace 隔离不绕过。

// seedConn 造一条 open 会话行并落指定连接的当日表。
func seedConn(t *testing.T, db *gorm.DB, connID string, ns uint, proxy string, openedMs int64) {
	t.Helper()
	repo := NewConnDetailRepository(db)
	if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(connID, ns, proxy, "p", openedMs)}); err != nil {
		t.Fatalf("造连接数据失败: %v", err)
	}
}

// TestConnColdMergeDedupAndPaging 冷查询跨热 / 冷有序归并 + 主键去重保热侧 + keyset 游标分页。
func TestConnColdMergeDedupAndPaging(t *testing.T) {
	hotDB := openRepoSQLite(t, "conn_cold_hot")
	arcDB := openRepoSQLite(t, "conn_cold_arc")
	repo := NewConnDetailRepository(hotDB)
	repo.SetArchiveDB(arcDB)

	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	// 交错时间：热 t=500,300,100；冷 t=400,200；另一条 t=300 与热同主键（归档进行中两侧同存）。
	dupID := uuidV7At(base+300, "dup")
	seedConn(t, hotDB, uuidV7At(base+500, "h5"), 1, "proxy-1", base+500)
	seedConn(t, hotDB, dupID, 1, "proxy-1", base+300)
	seedConn(t, hotDB, uuidV7At(base+100, "h1"), 1, "proxy-1", base+100)
	seedConn(t, arcDB, uuidV7At(base+400, "a4"), 1, "proxy-1", base+400)
	seedConn(t, arcDB, uuidV7At(base+200, "a2"), 1, "proxy-1", base+200)
	seedConn(t, arcDB, dupID, 1, "proxy-1", base+300) // 与热同 conn_id，应去重保热侧

	q := ConnQuery{ServerID: "proxy-1", NamespaceID: 1, FromMs: base - 3600_000, ToMs: base + 3600_000}
	// 第一页 limit=2：应为 t=500,400（最新两条降序）、有下一页。
	page1, next1, err := repo.QueryConnectionsCold(q, "", 2)
	if err != nil {
		t.Fatalf("冷查询首页失败: %v", err)
	}
	if len(page1) != 2 || page1[0].OpenedAt.UnixMilli() != base+500 || page1[1].OpenedAt.UnixMilli() != base+400 {
		t.Fatalf("首页应为 500,400 降序，实际 %d,%d", page1[0].OpenedAt.UnixMilli(), page1[1].OpenedAt.UnixMilli())
	}
	if next1 == "" {
		t.Fatalf("首页应有下一页游标")
	}
	// 第二页：应为 t=300(dup,保热),200，有下一页（还剩 t=100）。
	page2, next2, err := repo.QueryConnectionsCold(q, next1, 2)
	if err != nil {
		t.Fatalf("冷查询次页失败: %v", err)
	}
	if len(page2) != 2 || page2[0].OpenedAt.UnixMilli() != base+300 || page2[1].OpenedAt.UnixMilli() != base+200 {
		t.Fatalf("次页应为 300,200，实际 %d,%d", page2[0].OpenedAt.UnixMilli(), page2[1].OpenedAt.UnixMilli())
	}
	// 第三页：应为 t=100，无下一页。
	page3, next3, err := repo.QueryConnectionsCold(q, next2, 2)
	if err != nil {
		t.Fatalf("冷查询末页失败: %v", err)
	}
	if len(page3) != 1 || page3[0].OpenedAt.UnixMilli() != base+100 || next3 != "" {
		t.Fatalf("末页应为单条 100 且无下一页，实际 len=%d next=%q", len(page3), next3)
	}
	// 去重验证：全量取应恰 5 条唯一（dup 只出现一次），无重复 conn_id。
	all, _, err := repo.QueryConnectionsCold(q, "", 100)
	if err != nil {
		t.Fatalf("冷查询全量失败: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("去重后应 5 条唯一，实际 %d", len(all))
	}
	seen := map[string]bool{}
	for _, c := range all {
		if seen[c.ConnID] {
			t.Fatalf("出现重复 conn_id：%s", c.ConnID)
		}
		seen[c.ConnID] = true
	}
}

// TestConnDefaultQueryIgnoresArchive 默认查询（不 includeArchived）绝不触碰归档库：归档独有行不得出现。
func TestConnDefaultQueryIgnoresArchive(t *testing.T) {
	hotDB := openRepoSQLite(t, "conn_def_hot")
	arcDB := openRepoSQLite(t, "conn_def_arc")
	repo := NewConnDetailRepository(hotDB)
	repo.SetArchiveDB(arcDB)

	base := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC).UnixMilli()
	seedConn(t, hotDB, uuidV7At(base+100, "h1"), 1, "proxy-1", base+100)
	arcOnly := uuidV7At(base+200, "a1")
	seedConn(t, arcDB, arcOnly, 1, "proxy-1", base+200)

	q := ConnQuery{ServerID: "proxy-1", NamespaceID: 1, FromMs: base - 3600_000, ToMs: base + 3600_000, Limit: 10}
	rows, _, err := repo.QueryConnections(q) // 默认热查询
	if err != nil {
		t.Fatalf("默认查询失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("默认查询应只见热库 1 条，实际 %d", len(rows))
	}
	for _, r := range rows {
		if r.ConnID == arcOnly {
			t.Fatalf("默认查询不得返回归档独有行 %s", arcOnly)
		}
	}
}

// TestConnColdNamespaceIsolation 冷查询 namespace 隔离不绕过：仅返回目标 namespace 的行（两侧均过滤）。
func TestConnColdNamespaceIsolation(t *testing.T) {
	hotDB := openRepoSQLite(t, "conn_ns_hot")
	arcDB := openRepoSQLite(t, "conn_ns_arc")
	repo := NewConnDetailRepository(hotDB)
	repo.SetArchiveDB(arcDB)

	base := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC).UnixMilli()
	// ns=1 各一条（热 / 冷），ns=2 各一条（热 / 冷）——应只见 ns=1 两条。
	seedConn(t, hotDB, uuidV7At(base+100, "h1"), 1, "proxy-1", base+100)
	seedConn(t, hotDB, uuidV7At(base+150, "h2"), 2, "proxy-1", base+150)
	seedConn(t, arcDB, uuidV7At(base+200, "a1"), 1, "proxy-1", base+200)
	seedConn(t, arcDB, uuidV7At(base+250, "a2"), 2, "proxy-1", base+250)

	q := ConnQuery{ServerID: "proxy-1", NamespaceID: 1, FromMs: base - 3600_000, ToMs: base + 3600_000}
	rows, _, err := repo.QueryConnectionsCold(q, "", 100)
	if err != nil {
		t.Fatalf("冷查询失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应只见 ns=1 的 2 条，实际 %d", len(rows))
	}
	for _, r := range rows {
		if r.NamespaceID != 1 {
			t.Fatalf("冷查询绕过了 namespace 隔离，返回 ns=%d", r.NamespaceID)
		}
	}
}

// insertAudit 以显式 id / createdAt 落一条审计（模拟归档保留原主键 id，跨库唯一）。
func insertAudit(t *testing.T, db *gorm.DB, id uint, tsMs int64) {
	t.Helper()
	row := model.AuditLog{
		ID: id, Operator: "system", Action: "config.publish", TargetType: "config",
		TargetRef: "prod/area1/x", Result: "ok", CreatedAt: time.UnixMilli(tsMs).UTC(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("造审计数据失败: %v", err)
	}
}

// TestAuditColdMergeDedupNumericID 审计冷查询：单表跨热 / 冷归并 + 按数值 id 去重保热侧 + 游标分页。
func TestAuditColdMergeDedupNumericID(t *testing.T) {
	hotDB := openRepoSQLite(t, "audit_cold_hot")
	arcDB := openRepoSQLite(t, "audit_cold_arc")
	repo := NewAuditLogRepository(hotDB)
	repo.SetArchiveDB(arcDB)

	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	// 热：id=10(t+500) id=8(t+300) id=5(t+100)；冷：id=8(t+300 dup) id=6(t+200) id=3(t+50)。
	insertAudit(t, hotDB, 10, base+500)
	insertAudit(t, hotDB, 8, base+300)
	insertAudit(t, hotDB, 5, base+100)
	insertAudit(t, arcDB, 8, base+300) // 与热同 id：归档进行中，去重保热侧
	insertAudit(t, arcDB, 6, base+200)
	insertAudit(t, arcDB, 3, base+50)

	f := AuditFilter{From: time.UnixMilli(base - 1000).UTC(), To: time.UnixMilli(base + 1000).UTC()}
	all, _, err := repo.ListCold(f, "", 100)
	if err != nil {
		t.Fatalf("审计冷查询失败: %v", err)
	}
	// 去重后 5 条唯一 id，按 created_at desc：10,8,6,5,3。
	wantIDs := []uint{10, 8, 6, 5, 3}
	if len(all) != len(wantIDs) {
		t.Fatalf("去重后应 %d 条，实际 %d", len(wantIDs), len(all))
	}
	for i, id := range wantIDs {
		if all[i].ID != id {
			t.Fatalf("第 %d 位应为 id=%d，实际 %d", i, id, all[i].ID)
		}
	}
	// 分页：limit=2 首页 10,8；末行游标续查。
	p1, next1, err := repo.ListCold(f, "", 2)
	if err != nil || len(p1) != 2 || p1[0].ID != 10 || p1[1].ID != 8 || next1 == "" {
		t.Fatalf("首页应 10,8 且有游标，实际 %+v next=%q err=%v", ids(p1), next1, err)
	}
	p2, _, err := repo.ListCold(f, next1, 2)
	if err != nil || len(p2) != 2 || p2[0].ID != 6 || p2[1].ID != 5 {
		t.Fatalf("次页应 6,5，实际 %+v err=%v", ids(p2), err)
	}
}

// TestAuditColdNumericTiebreak 同一时刻多条：数值 id 降序（非字典序 "9">"10"）。
func TestAuditColdNumericTiebreak(t *testing.T) {
	hotDB := openRepoSQLite(t, "audit_tie_hot")
	arcDB := openRepoSQLite(t, "audit_tie_arc")
	repo := NewAuditLogRepository(hotDB)
	repo.SetArchiveDB(arcDB)
	ts := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	insertAudit(t, hotDB, 10, ts)
	insertAudit(t, arcDB, 9, ts)
	f := AuditFilter{From: time.UnixMilli(ts - 1000).UTC(), To: time.UnixMilli(ts + 1000).UTC()}
	all, _, err := repo.ListCold(f, "", 100)
	if err != nil || len(all) != 2 || all[0].ID != 10 || all[1].ID != 9 {
		t.Fatalf("同刻应按数值 id 降序 10,9，实际 %+v err=%v", ids(all), err)
	}
}

func ids(rows []model.AuditLog) []uint {
	out := make([]uint, len(rows))
	for i := range rows {
		out[i] = rows[i].ID
	}
	return out
}

// seedSnapshot 落一条健康快照到指定库的当日表。
func seedSnapshot(t *testing.T, db *gorm.DB, server string, tsMs int64) {
	t.Helper()
	repo := NewHealthSnapshotRepository(db)
	if _, err := repo.FlushDaily([]model.HealthSnapshot{{
		TsMs: tsMs, NamespaceID: 1, ServerID: server, Kind: model.ServerKindBackend,
		Score: 90, Level: "healthy", Schedulable: true, Reasons: "[]",
	}}); err != nil {
		t.Fatalf("造快照数据失败: %v", err)
	}
}

// TestHealthSnapshotColdUnionDedup 健康快照冷查询：区间并表 + 按 (server,ts) 去重保热侧、ts 升序。
func TestHealthSnapshotColdUnionDedup(t *testing.T) {
	hotDB := openRepoSQLite(t, "snap_cold_hot")
	arcDB := openRepoSQLite(t, "snap_cold_arc")
	repo := NewHealthSnapshotRepository(hotDB)
	repo.SetArchiveDB(arcDB)

	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	seedSnapshot(t, hotDB, "s1", base+100)
	seedSnapshot(t, hotDB, "s1", base+200)
	seedSnapshot(t, arcDB, "s1", base+200) // dup (s1,ts) 去重保热
	seedSnapshot(t, arcDB, "s1", base+300)
	seedSnapshot(t, arcDB, "s2", base+250) // 他服，serverId 过滤应排除

	rows, err := repo.QueryRangeCold("s1", base-1000, base+1000)
	if err != nil {
		t.Fatalf("快照冷查询失败: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("s1 去重后应 3 条，实际 %d", len(rows))
	}
	for i, want := range []int64{base + 100, base + 200, base + 300} {
		if rows[i].TsMs != want {
			t.Fatalf("第 %d 点应 ts=%d，实际 %d", i, want, rows[i].TsMs)
		}
	}
}

// TestMetricSeriesColdUnionDedup 指标时序冷查询：区间并表 + 按 (server,bucket) 去重保热侧、bucket 升序。
func TestMetricSeriesColdUnionDedup(t *testing.T) {
	hotDB := openRepoSQLite(t, "metric_cold_hot")
	arcDB := openRepoSQLite(t, "metric_cold_arc")
	repo := NewMetricSampleV2Repository(hotDB)
	repo.SetArchiveDB(arcDB)

	day := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	b1, b2, b3 := bucketMs(day, 5), bucketMs(day, 10), bucketMs(day, 15)
	hot := NewMetricSampleV2Repository(hotDB)
	arc := NewMetricSampleV2Repository(arcDB)
	if _, err := hot.FlushDaily([]model.MetricSampleV2{rowAt("s1", b1), rowAt("s1", b2)}); err != nil {
		t.Fatalf("造热指标失败: %v", err)
	}
	if _, err := arc.FlushDaily([]model.MetricSampleV2{rowAt("s1", b2), rowAt("s1", b3), rowAt("s2", b2)}); err != nil {
		t.Fatalf("造冷指标失败: %v", err)
	}
	rows, err := repo.QueryRangeCold([]string{"s1"}, b1-1000, b3+1000)
	if err != nil {
		t.Fatalf("指标冷查询失败: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("s1 去重后应 3 条，实际 %d", len(rows))
	}
	for i, want := range []int64{b1, b2, b3} {
		if rows[i].BucketStartMs != want {
			t.Fatalf("第 %d 桶应 %d，实际 %d", i, want, rows[i].BucketStartMs)
		}
	}
}
