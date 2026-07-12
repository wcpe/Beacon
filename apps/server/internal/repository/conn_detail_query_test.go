package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestFindByConnID 直查：命中返回行、非 UUIDv7 / 缺表 / 无行均 (nil,nil)。
func TestFindByConnID(t *testing.T) {
	db := openRepoSQLite(t, "conn_q_find")
	repo := NewConnDetailRepository(db)
	openedMs := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	cid := uuidV7At(openedMs, "f1")
	if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(cid, 1, "proxy-1", "p", openedMs)}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	row, err := repo.FindByConnID(cid)
	if err != nil || row == nil || row.ConnID != cid {
		t.Fatalf("应命中，实际 row=%v err=%v", row, err)
	}
	// 非 UUIDv7 → nil；同日无此 id → nil。
	if r, _ := repo.FindByConnID("not-a-uuid"); r != nil {
		t.Fatalf("非 UUIDv7 应 nil")
	}
	if r, _ := repo.FindByConnID(uuidV7At(openedMs, "zz")); r != nil {
		t.Fatalf("无此行应 nil")
	}
}

// TestQueryConnectionsCrossDayCursor 跨日并表 + 游标：D 与 D+1 两日会话按 opened_at 降序跨表合并，游标短路取页。
func TestQueryConnectionsCrossDayCursor(t *testing.T) {
	db := openRepoSQLite(t, "conn_q_cross")
	repo := NewConnDetailRepository(db)
	dayD := time.Date(2026, 7, 11, 20, 0, 0, 0, time.UTC).UnixMilli()
	dayN := time.Date(2026, 7, 12, 2, 0, 0, 0, time.UTC).UnixMilli()
	// D 日两条、D+1 日两条（opened_at 递增）。
	seeds := []int64{dayD, dayD + 1000, dayN, dayN + 1000}
	for i, ms := range seeds {
		cid := uuidV7At(ms, string(rune('a'+i)))
		if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(cid, 1, "proxy-1", "p", ms)}); err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}
	q := ConnQuery{ServerID: "proxy-1", FromMs: dayD - 3600_000, ToMs: dayN + 3600_000, Offset: 0, Limit: 2}
	page1, hasMore1, err := repo.QueryConnections(q)
	if err != nil || !hasMore1 || len(page1) != 2 {
		t.Fatalf("首页应 2 条且有下一页，实际 len=%d hasMore=%v err=%v", len(page1), hasMore1, err)
	}
	// 降序：最新的 D+1 日两条在前。
	if page1[0].OpenedAt.UnixMilli() != dayN+1000 || page1[1].OpenedAt.UnixMilli() != dayN {
		t.Fatalf("跨日降序合并顺序不符: %d,%d", page1[0].OpenedAt.UnixMilli(), page1[1].OpenedAt.UnixMilli())
	}
	q.Offset = 2
	page2, hasMore2, err := repo.QueryConnections(q)
	if err != nil || hasMore2 || len(page2) != 2 {
		t.Fatalf("次页应 2 条且无下一页，实际 len=%d hasMore=%v err=%v", len(page2), hasMore2, err)
	}
	if page2[0].OpenedAt.UnixMilli() != dayD+1000 || page2[1].OpenedAt.UnixMilli() != dayD {
		t.Fatalf("次页应为 D 日两条降序: %d,%d", page2[0].OpenedAt.UnixMilli(), page2[1].OpenedAt.UnixMilli())
	}
}

// TestQueryConnectionsServerFilter serverId 匹配 proxy / 首后端 / 末后端任一。
func TestQueryConnectionsServerFilter(t *testing.T) {
	db := openRepoSQLite(t, "conn_q_filter")
	repo := NewConnDetailRepository(db)
	base := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC).UnixMilli()
	cid := uuidV7At(base, "s1")
	// close 携带 first=game-1 / last=game-2。
	if _, err := repo.FlushDaily([]model.ConnEvent{
		openEvent(cid, 1, "proxy-1", "p", base),
		closeEvent(cid, 1, "proxy-1", "p", base, base+1000),
	}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	q := ConnQuery{FromMs: base - 3600_000, ToMs: base + 3600_000, Limit: 10}
	for _, sid := range []string{"proxy-1", "game-1", "game-2"} {
		q.ServerID = sid
		rows, _, err := repo.QueryConnections(q)
		if err != nil || len(rows) != 1 {
			t.Fatalf("serverId=%s 应命中会话，实际 len=%d err=%v", sid, len(rows), err)
		}
	}
	q.ServerID = "game-9"
	if rows, _, _ := repo.QueryConnections(q); len(rows) != 0 {
		t.Fatalf("无关 serverId 不应命中")
	}
}

// TestScanConnStats 存量投影：与窗口重叠的会话（含窗口前建立仍在线者）被取出。
func TestScanConnStats(t *testing.T) {
	db := openRepoSQLite(t, "conn_q_stats")
	repo := NewConnDetailRepository(db)
	winFrom := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	winTo := winFrom + 30*60_000
	// 窗口前建立、窗口内仍 open（存量）。
	preOpen := uuidV7At(winFrom-20*60_000, "p1")
	if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(preOpen, 1, "proxy-1", "a", winFrom-20*60_000)}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	// 窗口内建立并断开。
	inMs := winFrom + 5*60_000
	inCid := uuidV7At(inMs, "p2")
	if _, err := repo.FlushDaily([]model.ConnEvent{
		openEvent(inCid, 1, "proxy-1", "b", inMs),
		closeEvent(inCid, 1, "proxy-1", "b", inMs, inMs+1000),
	}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	rows, err := repo.ScanConnStats("proxy-1", winFrom, winTo)
	if err != nil || len(rows) != 2 {
		t.Fatalf("应取窗口重叠 2 条（含窗口前存量），实际 len=%d err=%v", len(rows), err)
	}
}
