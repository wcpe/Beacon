package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

func traceRecord(msgID string, ns uint, src string, withPayload bool) model.MessageRecord {
	ms, _ := store.TimeMsFromUUIDv7(msgID)
	rec := model.MessageRecord{
		Trace: model.MsgTrace{
			MessageID: msgID, NamespaceID: ns, SourceServerID: src, MsgType: "chat",
			TargetKind: model.MsgTargetKindServer, TargetServerID: "game-2",
			ResolvedServerID: "game-2", Status: model.MsgStatusDelivered,
			CreatedAt: time.UnixMilli(ms).UTC(), HopCount: 1, Hops: "[]",
		},
	}
	if withPayload {
		rec.Trace.PayloadSize = 5
		rec.Trace.PayloadStored = true
		rec.Payload = &model.MsgPayload{
			MessageID: msgID, Payload: "hello", SHA256: "abc", Size: 5,
			CreatedAt: time.UnixMilli(ms).UTC(),
		}
	}
	return rec
}

// TestMessageFlushWritesBothTablesSameTx 校验 trace 与 payload 同事务写两表、各落一行。
func TestMessageFlushWritesBothTablesSameTx(t *testing.T) {
	db := openRepoSQLite(t, "msg_twotable")
	repo := NewMessageRepository(db)
	ms := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	mid := uuidV7At(ms, "m1")
	traceTbl := store.DailyTableName("msg_trace", time.UnixMilli(ms).UTC())
	payloadTbl := store.DailyTableName("msg_payload", time.UnixMilli(ms).UTC())

	if _, err := repo.FlushDaily([]model.MessageRecord{traceRecord(mid, 1, "game-1", true)}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	if got := countDaily(t, db, traceTbl); got != 1 {
		t.Fatalf("msg_trace 应落 1 行，实际 %d", got)
	}
	if got := countDaily(t, db, payloadTbl); got != 1 {
		t.Fatalf("msg_payload 应落 1 行，实际 %d", got)
	}
}

// TestMessageFlushNoPayload 校验 payload_stored=false 时只写 trace、不建 / 不写 payload 行。
func TestMessageFlushNoPayload(t *testing.T) {
	db := openRepoSQLite(t, "msg_nopayload")
	repo := NewMessageRepository(db)
	ms := time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC).UnixMilli()
	mid := uuidV7At(ms, "m2")
	payloadTbl := store.DailyTableName("msg_payload", time.UnixMilli(ms).UTC())

	if _, err := repo.FlushDaily([]model.MessageRecord{traceRecord(mid, 1, "game-1", false)}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	// payload 日表由 EnsureDailyTable 建出（空表），但不应有行。
	if got := countDaily(t, db, payloadTbl); got != 0 {
		t.Fatalf("无 payload 时 msg_payload 不应有行，实际 %d", got)
	}
}

// TestMessageFlushIdempotent 校验 message_id 冲突去重：重放同批不增行、返回去重数。
func TestMessageFlushIdempotent(t *testing.T) {
	db := openRepoSQLite(t, "msg_idem")
	repo := NewMessageRepository(db)
	ms := time.Date(2026, 7, 11, 14, 0, 0, 0, time.UTC).UnixMilli()
	mid := uuidV7At(ms, "m3")
	traceTbl := store.DailyTableName("msg_trace", time.UnixMilli(ms).UTC())
	batch := []model.MessageRecord{traceRecord(mid, 1, "game-1", true)}

	if _, err := repo.FlushDaily(batch); err != nil {
		t.Fatalf("首写失败: %v", err)
	}
	dup, err := repo.FlushDaily(batch)
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if dup != 1 {
		t.Fatalf("重放应去重 1，实际 %d", dup)
	}
	if got := countDaily(t, db, traceTbl); got != 1 {
		t.Fatalf("重放后应仍 1 行，实际 %d", got)
	}
}

// TestMessageFlushCrossDay 校验一批跨日消息按 message_id 内嵌时间各落对应日表。
func TestMessageFlushCrossDay(t *testing.T) {
	db := openRepoSQLite(t, "msg_crossday")
	repo := NewMessageRepository(db)
	d1 := time.Date(2026, 7, 11, 23, 0, 0, 0, time.UTC).UnixMilli()
	d2 := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC).UnixMilli()
	m1 := uuidV7At(d1, "m4")
	m2 := uuidV7At(d2, "m5")
	tbl1 := store.DailyTableName("msg_trace", time.UnixMilli(d1).UTC())
	tbl2 := store.DailyTableName("msg_trace", time.UnixMilli(d2).UTC())

	if _, err := repo.FlushDaily([]model.MessageRecord{
		traceRecord(m1, 1, "game-1", false),
		traceRecord(m2, 1, "game-1", false),
	}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	if countDaily(t, db, tbl1) != 1 || countDaily(t, db, tbl2) != 1 {
		t.Fatalf("跨日应各落 1 行")
	}
}
