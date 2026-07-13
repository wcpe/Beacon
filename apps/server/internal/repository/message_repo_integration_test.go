//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestMessageFlushDailyMySQL 真 MySQL：msg_trace 与 msg_payload 同事务写两表、TEXT/可空列可移植、跨日拆表、幂等去重。
// 残留日表由 testsupport.OpenTestDB 统一清理（跨运行持久，无需各测试自清）。
func TestMessageFlushDailyMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5a_msg")
	repo := NewMessageRepository(db)

	ms := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	traceName := store.DailyTableName("msg_trace", time.UnixMilli(ms).UTC())
	payloadName := store.DailyTableName("msg_payload", time.UnixMilli(ms).UTC())
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
	n1 := store.DailyTableName("msg_trace", time.UnixMilli(d1).UTC())
	n2 := store.DailyTableName("msg_trace", time.UnixMilli(d2).UTC())

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

// TestMessageBroadcastColumnMigrationMySQL 真 MySQL：升级当日存量 msg_trace 日表（缺广播聚合列）
// 被 EnsureDailyTable 触达时补列（GORM 加列、零方言），随后广播聚合行落库、聚合计数回读一致（FR-180）。
//
// 用「按当前模型建表后 DropColumn 掉新列」模拟旧版二进制建出的表形态；
// 日表日期按纳秒时刻取唯一日，保证 -count=2 复跑时不撞进程内建表缓存（每轮都是全新日表）。
func TestMessageBroadcastColumnMigrationMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p5a_msg")
	repo := NewMessageRepository(db)

	day := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(time.Now().UnixNano()%3000))
	ms := day.Add(12 * time.Hour).UnixMilli()
	traceName := store.DailyTableName("msg_trace", day)
	t.Cleanup(func() { _ = db.Migrator().DropTable(traceName) })

	// ① 直接按当前模型建出日表（绕过 EnsureDailyTable 缓存），再删掉 5 个新列，模拟旧版存量表。
	if err := db.Table(traceName).Migrator().CreateTable(&model.MsgTrace{}); err != nil {
		t.Fatalf("建模拟存量表失败: %v", err)
	}
	for _, col := range []string{"target_zone", "fanout_total", "delivered_count", "failed_count", "expired_count"} {
		if err := db.Table(traceName).Migrator().DropColumn(&model.MsgTrace{}, col); err != nil {
			t.Fatalf("模拟旧表删列 %s 失败: %v", col, err)
		}
	}
	if db.Table(traceName).Migrator().HasColumn(&model.MsgTrace{}, "fanout_total") {
		t.Fatalf("预置失败：模拟旧表不应含 fanout_total 列")
	}

	// ② 广播聚合行落库：EnsureDailyTable 触达存量表应先补列、事务内插入成功。
	bid := uuidV7At(ms, "bm1")
	if _, err := repo.FlushDaily([]model.MessageRecord{broadcastTraceRecord(bid, 1, "game-1", "zone-a")}); err != nil {
		t.Fatalf("存量表补列后广播聚合行应落库成功: %v", err)
	}
	if !db.Table(traceName).Migrator().HasColumn(&model.MsgTrace{}, "fanout_total") {
		t.Fatalf("存量表应已被补上 fanout_total 列")
	}

	// ③ 回读聚合计数与 zone（MySQL 可空列可移植）。
	var row model.MsgTrace
	if err := db.Table(traceName).Where("message_id = ?", bid).Take(&row).Error; err != nil {
		t.Fatalf("回读广播聚合行失败: %v", err)
	}
	if row.TargetKind != model.MsgTargetKindBroadcast || row.TargetZone == nil || *row.TargetZone != "zone-a" {
		t.Fatalf("广播行 target_zone 不符: %+v", row)
	}
	if row.FanoutTotal == nil || *row.FanoutTotal != 3 || row.DeliveredCount == nil || *row.DeliveredCount != 2 ||
		row.FailedCount == nil || *row.FailedCount != 1 || row.ExpiredCount == nil || *row.ExpiredCount != 0 {
		t.Fatalf("广播聚合计数回读不符: %+v", row)
	}
}
