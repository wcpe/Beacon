//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestMessageQueryMySQL 真 MySQL：messageId 直查 + payload 分表直查 + 跨日游标分页 + 关联跨午夜直查可移植。
func TestMessageQueryMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5b_msg")
	repo := NewMessageRepository(db)

	// ① 元数据 + payload 分表直查。
	ms := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	dropDailyIT(t, db, "msg_trace", time.UnixMilli(ms).UTC())
	dropDailyIT(t, db, "msg_payload", time.UnixMilli(ms).UTC())
	mid := uuidV7At(ms, "m0")
	if _, err := repo.FlushDaily([]model.MessageRecord{traceRecord(mid, 1, "game-1", true)}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	if trace, err := repo.FindByMessageID(mid); err != nil || trace == nil || trace.MessageID != mid {
		t.Fatalf("MySQL messageId 直查应命中，实际 %v err=%v", trace, err)
	}
	if pl, err := repo.FindPayload(mid); err != nil || pl == nil || pl.Payload != "hello" {
		t.Fatalf("MySQL payload 直查应命中，实际 %v err=%v", pl, err)
	}

	// ② 跨日游标分页。
	dayD := time.Date(2026, 7, 8, 22, 0, 0, 0, time.UTC).UnixMilli()
	dayN := time.Date(2026, 7, 9, 1, 0, 0, 0, time.UTC).UnixMilli()
	dropDailyIT(t, db, "msg_trace", time.UnixMilli(dayD).UTC())
	dropDailyIT(t, db, "msg_trace", time.UnixMilli(dayN).UTC())
	dropDailyIT(t, db, "msg_payload", time.UnixMilli(dayD).UTC())
	dropDailyIT(t, db, "msg_payload", time.UnixMilli(dayN).UTC())
	for i, t0 := range []int64{dayD, dayD + 1000, dayN, dayN + 1000} {
		id := uuidV7At(t0, "c"+string(rune('a'+i)))
		if _, err := repo.FlushDaily([]model.MessageRecord{msgTraceAt(id, "game-7", "game-8", model.MsgStatusDelivered, "")}); err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}
	q := MessageQuery{ServerID: "game-7", FromMs: dayD - 3600_000, ToMs: dayN + 3600_000, Limit: 2}
	page1, hasMore1, err := repo.QueryMessages(q)
	if err != nil || !hasMore1 || len(page1) != 2 || page1[0].CreatedAt.UnixMilli() != dayN+1000 {
		t.Fatalf("MySQL 跨日首页不符: len=%d hasMore=%v err=%v", len(page1), hasMore1, err)
	}
}

// TestMessageCorrelationMySQL 真 MySQL -count 防脆：关联跨午夜按 correlationId 得往返两条。
func TestMessageCorrelationMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5b_msg")
	repo := NewMessageRepository(db)
	reqMs := time.Date(2026, 7, 6, 23, 59, 30, 0, time.UTC).UnixMilli()
	respMs := time.Date(2026, 7, 7, 0, 0, 10, 0, time.UTC).UnixMilli()
	dropDailyIT(t, db, "msg_trace", time.UnixMilli(reqMs).UTC())
	dropDailyIT(t, db, "msg_trace", time.UnixMilli(respMs).UTC())
	dropDailyIT(t, db, "msg_payload", time.UnixMilli(reqMs).UTC())
	dropDailyIT(t, db, "msg_payload", time.UnixMilli(respMs).UTC())

	reqID := uuidV7At(reqMs, "rq")
	respID := uuidV7At(respMs, "rp")
	if _, err := repo.FlushDaily([]model.MessageRecord{
		msgTraceAt(reqID, "game-1", "game-2", model.MsgStatusDelivered, reqID),
		msgTraceAt(respID, "game-2", "game-1", model.MsgStatusDelivered, reqID),
	}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	rows, err := repo.FindByCorrelationID(reqID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("MySQL 跨午夜关联应 2 条，实际 len=%d err=%v", len(rows), err)
	}
	if corr, err := repo.FindCorrelated(reqID, reqID); err != nil || corr == nil || corr.MessageID != respID {
		t.Fatalf("MySQL 关联查找应得响应 %s，实际 %v err=%v", respID, corr, err)
	}
}
