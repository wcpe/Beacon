package repository

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// uuidV7At 构造一个高 48 位内嵌指定毫秒时间戳的 UUIDv7 文本（测试用，后半段用 seq 区分不同连接）。
func uuidV7At(ms int64, seq string) string {
	const d = "0123456789abcdef"
	h2 := func(b byte) string { return string([]byte{d[b>>4], d[b&0x0f]}) }
	p := h2(byte(ms>>40)) + h2(byte(ms>>32)) + h2(byte(ms>>24)) + h2(byte(ms>>16)) +
		"-" + h2(byte(ms>>8)) + h2(byte(ms))
	// 补足 8-4-4-4-12 后半段；seq 落末段保证唯一（≤12 字符）
	tail := (seq + "000000000000")[:12]
	return p + "-7abc-8def-" + tail
}

func openEvent(connID string, ns uint, proxy, player string, openedMs int64) model.ConnEvent {
	return model.ConnEvent{
		Kind: model.ConnEventKindOpen, ConnID: connID, NamespaceID: ns,
		ProxyServerID: proxy, PlayerUUID: player, PlayerName: "Steve",
		ClientIP: "10.0.0.1", ProtocolVersion: 765, OpenedAtMs: openedMs,
		FirstBackend: "game-1",
	}
}

func closeEvent(connID string, ns uint, proxy, player string, openedMs, closedMs int64) model.ConnEvent {
	ev := openEvent(connID, ns, proxy, player, openedMs)
	ev.Kind = model.ConnEventKindClose
	ev.ClosedAtMs = closedMs
	ev.CloseKind = model.ConnCloseKindQuit
	ev.CloseReason = "disconnect"
	ev.LastBackend = "game-2"
	ev.BackendSwitchCount = 1
	return ev
}

func fetchConn(t *testing.T, db *gorm.DB, table, connID string) model.ConnDetail {
	t.Helper()
	var row model.ConnDetail
	if err := db.Table(table).Where("conn_id = ?", connID).First(&row).Error; err != nil {
		t.Fatalf("查连接行 %s 失败: %v", connID, err)
	}
	return row
}

// TestConnFlushOpenThenClose 校验 open 插入会话行、后续 close 更新同一行（会话行语义）。
func TestConnFlushOpenThenClose(t *testing.T) {
	db := openRepoSQLite(t, "conn_open_close")
	repo := NewConnDetailRepository(db)
	openedMs := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	closedMs := openedMs + 5000
	cid := uuidV7At(openedMs, "a1")
	table := store.DailyTableName("conn_detail", time.UnixMilli(openedMs).UTC())

	if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(cid, 1, "proxy-1", "p-uuid", openedMs)}); err != nil {
		t.Fatalf("写 open 失败: %v", err)
	}
	row := fetchConn(t, db, table, cid)
	if row.Status != model.ConnStatusOpen || row.ClosedAt != nil {
		t.Fatalf("open 后应为 open 且 closed_at 为空，实际 status=%s closedAt=%v", row.Status, row.ClosedAt)
	}

	if _, err := repo.FlushDaily([]model.ConnEvent{closeEvent(cid, 1, "proxy-1", "p-uuid", openedMs, closedMs)}); err != nil {
		t.Fatalf("写 close 失败: %v", err)
	}
	if got := countDaily(t, db, table); got != 1 {
		t.Fatalf("close 应更新同一行、总行数仍为 1，实际 %d", got)
	}
	row = fetchConn(t, db, table, cid)
	if row.Status != model.ConnStatusClosed {
		t.Fatalf("close 后应为 closed，实际 %s", row.Status)
	}
	if row.DurationMs == nil || *row.DurationMs != 5000 {
		t.Fatalf("close 应算时长 5000ms，实际 %v", row.DurationMs)
	}
	if row.LastBackendServerID != "game-2" || row.BackendSwitchCount != 1 || row.CloseKind != model.ConnCloseKindQuit {
		t.Fatalf("close 摘要字段未落齐: %+v", row)
	}
	if row.FirstBackendServerID != "game-1" {
		t.Fatalf("close 更新应保留 open 时段首后端 game-1，实际 %s", row.FirstBackendServerID)
	}
}

// TestConnFlushCrossDayCloseHitsOpenDay 校验 open 于 D 日、close 于 D+1 日：行留在 D 日表且被闭合。
func TestConnFlushCrossDayCloseHitsOpenDay(t *testing.T) {
	db := openRepoSQLite(t, "conn_crossday")
	repo := NewConnDetailRepository(db)
	openedMs := time.Date(2026, 7, 11, 23, 59, 0, 0, time.UTC).UnixMilli()
	closedMs := openedMs + 2*3600*1000 // 跨到 07-12
	cid := uuidV7At(openedMs, "b2")     // conn_id 内嵌 open 时间 → 恒定位 07-11 表
	openTable := store.DailyTableName("conn_detail", time.UnixMilli(openedMs).UTC())

	if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(cid, 1, "proxy-1", "p2", openedMs)}); err != nil {
		t.Fatalf("写 open 失败: %v", err)
	}
	if _, err := repo.FlushDaily([]model.ConnEvent{closeEvent(cid, 1, "proxy-1", "p2", openedMs, closedMs)}); err != nil {
		t.Fatalf("写 close 失败: %v", err)
	}
	row := fetchConn(t, db, openTable, cid)
	if row.Status != model.ConnStatusClosed {
		t.Fatalf("跨日 close 应闭合 open 日表内的行，实际 %s", row.Status)
	}
	// 不应在 07-12 表另建行
	nextTable := store.DailyTableName("conn_detail", time.UnixMilli(closedMs).UTC())
	if db.Migrator().HasTable(nextTable) {
		t.Fatalf("close 不应在 D+1 日表 %s 另建物理表", nextTable)
	}
}

// TestConnFlushOpenIdempotent 校验重放同一 open 批被 conn_id 去重、行数不增。
func TestConnFlushOpenIdempotent(t *testing.T) {
	db := openRepoSQLite(t, "conn_idem")
	repo := NewConnDetailRepository(db)
	openedMs := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC).UnixMilli()
	cid := uuidV7At(openedMs, "c3")
	batch := []model.ConnEvent{openEvent(cid, 1, "proxy-1", "p3", openedMs)}
	table := store.DailyTableName("conn_detail", time.UnixMilli(openedMs).UTC())

	if _, err := repo.FlushDaily(batch); err != nil {
		t.Fatalf("首写失败: %v", err)
	}
	dup, err := repo.FlushDaily(batch)
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if dup != 1 {
		t.Fatalf("重放同一 open 应去重 1，实际 %d", dup)
	}
	if got := countDaily(t, db, table); got != 1 {
		t.Fatalf("重放后行数应仍为 1，实际 %d", got)
	}
}

// TestConnFlushSameBatchOpenClose 校验同一批内 open+close 同 conn_id：先插后更、终为 closed。
func TestConnFlushSameBatchOpenClose(t *testing.T) {
	db := openRepoSQLite(t, "conn_samebatch")
	repo := NewConnDetailRepository(db)
	openedMs := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC).UnixMilli()
	cid := uuidV7At(openedMs, "d4")
	table := store.DailyTableName("conn_detail", time.UnixMilli(openedMs).UTC())

	batch := []model.ConnEvent{
		openEvent(cid, 1, "proxy-1", "p4", openedMs),
		closeEvent(cid, 1, "proxy-1", "p4", openedMs, openedMs+3000),
	}
	if _, err := repo.FlushDaily(batch); err != nil {
		t.Fatalf("同批 open+close 写失败: %v", err)
	}
	if got := countDaily(t, db, table); got != 1 {
		t.Fatalf("同批 open+close 应为 1 行，实际 %d", got)
	}
	if row := fetchConn(t, db, table, cid); row.Status != model.ConnStatusClosed {
		t.Fatalf("同批处理后应为 closed，实际 %s", row.Status)
	}
}

// TestConnCloseOrphans 校验孤儿对账：把某 proxy before 之前的 open 行补 close（proxy_shutdown），不动之后的。
func TestConnCloseOrphans(t *testing.T) {
	db := openRepoSQLite(t, "conn_orphan")
	repo := NewConnDetailRepository(db)
	base := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC).UnixMilli()
	old := openEvent(uuidV7At(base, "e5"), 1, "proxy-1", "old", base)          // 旧进程孤儿
	fresh := openEvent(uuidV7At(base+60000, "e6"), 1, "proxy-1", "new", base+60000) // 新进程鲜活
	if _, err := repo.FlushDaily([]model.ConnEvent{old, fresh}); err != nil {
		t.Fatalf("写 open 失败: %v", err)
	}
	before := time.UnixMilli(base + 30000).UTC() // 新 boot 检测时刻，介于两者之间
	n, err := repo.CloseOrphans(1, "proxy-1", before, 3)
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应补 close 1 条孤儿，实际 %d", n)
	}
	table := store.DailyTableName("conn_detail", time.UnixMilli(base).UTC())
	if row := fetchConn(t, db, table, old.ConnID); row.Status != model.ConnStatusClosed || row.CloseKind != model.ConnCloseKindProxyShutdown {
		t.Fatalf("旧孤儿应被补 close 为 proxy_shutdown，实际 status=%s kind=%s", row.Status, row.CloseKind)
	}
	if row := fetchConn(t, db, table, fresh.ConnID); row.Status != model.ConnStatusOpen {
		t.Fatalf("新进程鲜活连接不应被误闭，实际 %s", row.Status)
	}
}

// TestConnListOpen 校验名册重建读原语：只返回 open 行投影。
func TestConnListOpen(t *testing.T) {
	db := openRepoSQLite(t, "conn_listopen")
	repo := NewConnDetailRepository(db)
	base := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC).UnixMilli()
	openCid := uuidV7At(base, "f7")
	closedCid := uuidV7At(base+1000, "f8")
	if _, err := repo.FlushDaily([]model.ConnEvent{
		openEvent(openCid, 1, "proxy-1", "alice", base),
		closeEvent(closedCid, 1, "proxy-1", "bob", base+1000, base+2000),
	}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	open, err := repo.ListOpenConnections(3)
	if err != nil {
		t.Fatalf("列 open 失败: %v", err)
	}
	if len(open) != 1 || open[0].PlayerUUID != "alice" || open[0].FirstBackend != "game-1" {
		t.Fatalf("应只返回 1 条 open（alice），实际 %+v", open)
	}
}
