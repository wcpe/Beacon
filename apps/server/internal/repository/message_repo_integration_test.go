//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestMessageFlushDailyMySQL 真 MySQL：msg_trace 与 msg_payload 同事务写两表、TEXT/可空列可移植、跨日拆表、幂等去重。
func TestMessageFlushDailyMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5a_msg")
	repo := NewMessageRepository(db)

	ms := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	traceName := dropDailyIT(t, db, "msg_trace", time.UnixMilli(ms).UTC())
	payloadName := dropDailyIT(t, db, "msg_payload", time.UnixMilli(ms).UTC())
	mid := uuidV7At(ms, "itm")

	if _, err := repo.FlushDaily([]model.MessageRecord{traceRecord(mid, 1, "game-1", true)}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	if countDaily(t, db, traceName) != 1 || countDaily(t, db, payloadName) != 1 {
		t.Fatalf("trace 与 payload 应各落 1 行（同事务）")
	}
	// 回读 trace 元数据与 payload：TEXT hops / payload、状态、hop_count 可移植落库。
	var trace model.MsgTrace
	if err := db.Table(traceName).Where("message_id = ?", mid).Take(&trace).Error; err != nil {
		t.Fatalf("回读 trace 失败: %v", err)
	}
	if trace.Status != model.MsgStatusDelivered || trace.Hops != "[]" || !trace.PayloadStored || trace.HopCount != 1 {
		t.Fatalf("MySQL 下 trace 元数据不符: %+v", trace)
	}
	var payload model.MsgPayload
	if err := db.Table(payloadName).Where("message_id = ?", mid).Take(&payload).Error; err != nil {
		t.Fatalf("回读 payload 失败: %v", err)
	}
	if payload.Payload != "hello" || payload.SHA256 != "abc" {
		t.Fatalf("MySQL 下 payload 不符: %+v", payload)
	}

	// 重放去重：message_id 冲突忽略、行数不增。
	dup, err := repo.FlushDaily([]model.MessageRecord{traceRecord(mid, 1, "game-1", true)})
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if dup != 1 || countDaily(t, db, traceName) != 1 {
		t.Fatalf("重放应去重 1、行数不增，实际 dup=%d cnt=%d", dup, countDaily(t, db, traceName))
	}

	// 分表分离：删掉 payload 日表模拟归档后，元数据仍可查（互不阻塞，spec §7）。
	if err := db.Migrator().DropTable(payloadName); err != nil {
		t.Fatalf("删 payload 表失败: %v", err)
	}
	if err := db.Table(traceName).Where("message_id = ?", mid).Take(&trace).Error; err != nil {
		t.Fatalf("payload 表缺失不应影响元数据查询: %v", err)
	}
}

// TestMessageFlushCrossDayMySQL 真 MySQL：一批跨日消息按 message_id 内嵌时间各落对应 trace 日表。
func TestMessageFlushCrossDayMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5a_msg")
	repo := NewMessageRepository(db)
	d1 := time.Date(2026, 7, 9, 23, 0, 0, 0, time.UTC).UnixMilli()
	d2 := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC).UnixMilli()
	n1 := dropDailyIT(t, db, "msg_trace", time.UnixMilli(d1).UTC())
	n2 := dropDailyIT(t, db, "msg_trace", time.UnixMilli(d2).UTC())

	if _, err := repo.FlushDaily([]model.MessageRecord{
		traceRecord(uuidV7At(d1, "cd1"), 1, "game-1", false),
		traceRecord(uuidV7At(d2, "cd2"), 1, "game-1", false),
	}); err != nil {
		t.Fatalf("跨日写失败: %v", err)
	}
	if countDaily(t, db, n1) != 1 || countDaily(t, db, n2) != 1 {
		t.Fatalf("跨日应各落 1 行 trace")
	}
}
