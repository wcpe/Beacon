//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestConnQueryMySQL 真 MySQL：connId 直查 + 跨日并表游标分页（opened_at 降序）+ serverId 多列过滤可移植。
func TestConnQueryMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5b_conn")
	repo := NewConnDetailRepository(db)

	dayD := time.Date(2026, 7, 11, 20, 0, 0, 0, time.UTC).UnixMilli()
	dayN := time.Date(2026, 7, 12, 2, 0, 0, 0, time.UTC).UnixMilli()

	seeds := []int64{dayD, dayD + 1000, dayN, dayN + 1000}
	for i, ms := range seeds {
		cid := uuidV7At(ms, string(rune('a'+i)))
		if _, err := repo.FlushDaily([]model.ConnEvent{openEvent(cid, 1, "proxy-1", "p", ms)}); err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}

	// connId 直查免时间范围。
	directID := uuidV7At(dayD, "a")
	if row, err := repo.FindByConnID(directID); err != nil || row == nil || row.ConnID != directID {
		t.Fatalf("MySQL connId 直查应命中，实际 %v err=%v", row, err)
	}

	// 跨日游标分页：首页取 D+1 两条（降序），次页取 D 两条。
	q := ConnQuery{ServerID: "proxy-1", FromMs: dayD - 3600_000, ToMs: dayN + 3600_000, Limit: 2}
	page1, hasMore1, err := repo.QueryConnections(q)
	if err != nil || !hasMore1 || len(page1) != 2 || page1[0].OpenedAt.UnixMilli() != dayN+1000 {
		t.Fatalf("MySQL 跨日首页不符: len=%d hasMore=%v err=%v", len(page1), hasMore1, err)
	}
	q.Offset = 2
	page2, hasMore2, err := repo.QueryConnections(q)
	if err != nil || hasMore2 || len(page2) != 2 || page2[1].OpenedAt.UnixMilli() != dayD {
		t.Fatalf("MySQL 跨日次页不符: len=%d hasMore=%v err=%v", len(page2), hasMore2, err)
	}
}

// TestConnStatsMySQL 真 MySQL -count 防脆：与窗口重叠会话投影可移植（含 closed_at IS NULL OR ≥ 语义）。
func TestConnStatsMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5b_conn")
	repo := NewConnDetailRepository(db)
	winFrom := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	winTo := winFrom + 30*60_000

	preOpen := uuidV7At(winFrom-20*60_000, "s1")
	inMs := winFrom + 5*60_000
	inCid := uuidV7At(inMs, "s2")
	if _, err := repo.FlushDaily([]model.ConnEvent{
		openEvent(preOpen, 1, "proxy-1", "a", winFrom-20*60_000),
		openEvent(inCid, 1, "proxy-1", "b", inMs),
		closeEvent(inCid, 1, "proxy-1", "b", inMs, inMs+1000),
	}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	rows, err := repo.ScanConnStats("proxy-1", winFrom, winTo)
	if err != nil || len(rows) != 2 {
		t.Fatalf("MySQL 存量投影应 2 条，实际 len=%d err=%v", len(rows), err)
	}
}
