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

// broadcastTraceRecord 构造一条广播聚合行（FR-180：一条广播只落一行，聚合计数非空）。
func broadcastTraceRecord(msgID string, ns uint, src, zone string) model.MessageRecord {
	ms, _ := store.TimeMsFromUUIDv7(msgID)
	fanout, delivered, failed, expired := 3, 2, 1, 0
	rec := model.MessageRecord{
		Trace: model.MsgTrace{
			MessageID: msgID, NamespaceID: ns, SourceServerID: src, MsgType: "announce",
			TargetKind:  model.MsgTargetKindBroadcast,
			FanoutTotal: &fanout, DeliveredCount: &delivered, FailedCount: &failed, ExpiredCount: &expired,
			Status: model.MsgStatusDelivered, CreatedAt: time.UnixMilli(ms).UTC(), HopCount: 1, Hops: "[]",
		},
	}
	if zone != "" {
		rec.Trace.TargetZone = &zone
	}
	return rec
}

// TestMessageFlushBroadcastAggregateRow 校验广播聚合行落库：聚合列写入回读一致、
// 无 zone 时 target_zone 为 NULL、定向行的聚合列保持 NULL（可空列语义，FR-180）。
func TestMessageFlushBroadcastAggregateRow(t *testing.T) {
	db := openRepoSQLite(t, "msg_broadcast_agg")
	repo := NewMessageRepository(db)
	ms := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC).UnixMilli()
	bidZone := uuidV7At(ms, "b1")
	bidAll := uuidV7At(ms+1, "b2")
	directed := uuidV7At(ms+2, "b3")
	traceTbl := store.DailyTableName("msg_trace", time.UnixMilli(ms).UTC())

	if _, err := repo.FlushDaily([]model.MessageRecord{
		broadcastTraceRecord(bidZone, 1, "game-1", "zone-a"),
		broadcastTraceRecord(bidAll, 1, "game-1", ""),
		traceRecord(directed, 1, "game-1", false),
	}); err != nil {
		t.Fatalf("写广播聚合行失败: %v", err)
	}

	var zoneRow model.MsgTrace
	if err := db.Table(traceTbl).Where("message_id = ?", bidZone).Take(&zoneRow).Error; err != nil {
		t.Fatalf("回读 zone 广播行失败: %v", err)
	}
	if zoneRow.TargetKind != model.MsgTargetKindBroadcast || zoneRow.TargetZone == nil || *zoneRow.TargetZone != "zone-a" {
		t.Fatalf("zone 广播行 target_zone 不符: %+v", zoneRow)
	}
	if zoneRow.FanoutTotal == nil || *zoneRow.FanoutTotal != 3 ||
		zoneRow.DeliveredCount == nil || *zoneRow.DeliveredCount != 2 ||
		zoneRow.FailedCount == nil || *zoneRow.FailedCount != 1 ||
		zoneRow.ExpiredCount == nil || *zoneRow.ExpiredCount != 0 {
		t.Fatalf("广播聚合计数回读不符: %+v", zoneRow)
	}

	var allRow model.MsgTrace
	if err := db.Table(traceTbl).Where("message_id = ?", bidAll).Take(&allRow).Error; err != nil {
		t.Fatalf("回读全 ns 广播行失败: %v", err)
	}
	if allRow.TargetZone != nil {
		t.Fatalf("无 zone 广播行 target_zone 应为 NULL，实际 %v", *allRow.TargetZone)
	}

	var dirRow model.MsgTrace
	if err := db.Table(traceTbl).Where("message_id = ?", directed).Take(&dirRow).Error; err != nil {
		t.Fatalf("回读定向行失败: %v", err)
	}
	if dirRow.TargetZone != nil || dirRow.FanoutTotal != nil || dirRow.DeliveredCount != nil ||
		dirRow.FailedCount != nil || dirRow.ExpiredCount != nil {
		t.Fatalf("定向行的广播聚合列应全为 NULL，实际 %+v", dirRow)
	}
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
