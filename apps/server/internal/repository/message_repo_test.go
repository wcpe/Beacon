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

// 不可信 ID 造数用样例（真机接入验收实测命中的 UUIDv4 随机 ID：前 48 位不是时间，
// 若按 ID 时间定日表会被写进 msg_trace_51580917 这类永不进入查询窗口的垃圾日表）。
const (
	untrustedMsgID = "5b84d1a0-7faa-4db0-a5df-362d302ea1cb"
	opaqueMsgID    = "mid-not-a-uuid"
)

// untrustedRecord 构造一条「message_id 内嵌时间不可信」的终态记录：日表只能由控制面接收时刻决定。
func untrustedRecord(msgID string, createdAtMs int64, payload string) model.MessageRecord {
	createdAt := time.UnixMilli(createdAtMs).UTC()
	rec := model.MessageRecord{
		Trace: model.MsgTrace{
			MessageID: msgID, NamespaceID: 1, SourceServerID: "game-1", MsgType: "chat",
			TargetKind: model.MsgTargetKindServer, TargetServerID: "game-2", ResolvedServerID: "game-2",
			Status: model.MsgStatusExpired, FailReason: model.MsgFailTTLExpired,
			CreatedAt: createdAt, HopCount: 1, Hops: "[]", PayloadSize: len(payload), PayloadStored: payload != "",
		},
	}
	if payload != "" {
		rec.Payload = &model.MsgPayload{
			MessageID: msgID, Payload: payload, SHA256: "sum", Size: len(payload), CreatedAt: createdAt,
		}
	}
	return rec
}

// TestMessageFlushUntrustedIDRoutesByCreatedAt 校验非 UUIDv7 的 message_id（实测命中的 UUIDv4 随机 ID）
// 不再按随机位推导日表：终态行落在控制面接收时刻当日表，ID 推导出的垃圾日表根本不产生。
func TestMessageFlushUntrustedIDRoutesByCreatedAt(t *testing.T) {
	db := openRepoSQLite(t, "msg_untrusted_id")
	repo := NewMessageRepository(db)
	createdMs := time.Date(2026, 10, 4, 7, 1, 11, 511_000_000, time.UTC).UnixMilli()
	receiveTbl := store.DailyTableName("msg_trace", time.UnixMilli(createdMs).UTC())
	receivePayloadTbl := store.DailyTableName("msg_payload", time.UnixMilli(createdMs).UTC())
	// ID 前 48 位被当成时间时的日表（旧行为落表处）——本用例要求它不存在。
	bogusMs, _ := store.TimeMsFromUUIDv7(untrustedMsgID)
	bogusTbl := store.DailyTableName("msg_trace", time.UnixMilli(bogusMs).UTC())
	if bogusTbl == receiveTbl {
		t.Fatalf("用例前提不成立：样例 ID 的随机位恰好落在接收当日")
	}

	if _, err := repo.FlushDaily([]model.MessageRecord{untrustedRecord(untrustedMsgID, createdMs, `{"text":"接入验收测试"}`)}); err != nil {
		t.Fatalf("写失败: %v", err)
	}
	if got := countDaily(t, db, receiveTbl); got != 1 {
		t.Fatalf("终态行应落控制面接收时刻日表 %s，实际 %d 行", receiveTbl, got)
	}
	if got := countDaily(t, db, receivePayloadTbl); got != 1 {
		t.Fatalf("payload 行应与 trace 同日落表，实际 %d 行", got)
	}
	if db.Migrator().HasTable(bogusTbl) {
		t.Fatalf("不应存在按随机位推导的垃圾日表 %s", bogusTbl)
	}
}

// TestMessageFlushOpaqueIDNotDropped 校验连 48 位时间都取不出来的 message_id 也不再被静默跳过，同样按接收时刻落表。
func TestMessageFlushOpaqueIDNotDropped(t *testing.T) {
	db := openRepoSQLite(t, "msg_opaque_id")
	repo := NewMessageRepository(db)
	createdMs := time.Date(2026, 10, 4, 7, 1, 11, 511_000_000, time.UTC).UnixMilli()
	tbl := store.DailyTableName("msg_trace", time.UnixMilli(createdMs).UTC())

	dup, err := repo.FlushDaily([]model.MessageRecord{untrustedRecord(opaqueMsgID, createdMs, "")})
	if err != nil {
		t.Fatalf("写失败: %v", err)
	}
	if dup != 0 {
		t.Fatalf("不可解析 ID 不应计入去重（旧行为会把整行丢弃并计去重），实际 %d", dup)
	}
	if got := countDaily(t, db, tbl); got != 1 {
		t.Fatalf("不可解析 ID 的终态行应落接收时刻日表 %s，实际 %d 行", tbl, got)
	}
}

// TestMessageDirectLookupFallsBackToReceiveDay 校验按 message_id 直查在首选日表未命中时回退到最近日表：
// 不可信 ID 的行按接收时刻落表后，发送方拿 ID 仍能查到最终去向（元数据 + payload）。
func TestMessageDirectLookupFallsBackToReceiveDay(t *testing.T) {
	db := openRepoSQLite(t, "msg_direct_lookup")
	repo := NewMessageRepository(db)
	createdMs := time.Date(2026, 10, 4, 7, 1, 11, 511_000_000, time.UTC).UnixMilli()
	if _, err := repo.FlushDaily([]model.MessageRecord{untrustedRecord(untrustedMsgID, createdMs, `{"text":"接入验收测试"}`)}); err != nil {
		t.Fatalf("写失败: %v", err)
	}

	row, err := repo.FindByMessageID(untrustedMsgID)
	if err != nil || row == nil {
		t.Fatalf("按 message_id 直查应回退命中，实际 row=%v err=%v", row, err)
	}
	if row.Status != model.MsgStatusExpired || row.FailReason != model.MsgFailTTLExpired {
		t.Fatalf("回退命中的终态应 expired(ttl_expired)，实际 %s/%s", row.Status, row.FailReason)
	}
	pl, err := repo.FindPayload(untrustedMsgID)
	if err != nil || pl == nil || pl.Payload != `{"text":"接入验收测试"}` {
		t.Fatalf("payload 直查应回退命中，实际 pl=%v err=%v", pl, err)
	}
	if miss, err := repo.FindByMessageID("11111111-2222-3333-4444-555555555555"); err != nil || miss != nil {
		t.Fatalf("确实不存在的消息应返回 nil，实际 %v err=%v", miss, err)
	}
}

// TestMessageListQuerySeesUntrustedIDRow 校验管理面列表查询路径（serverId + 时间窗跨日并表）能看到
// 不可信 ID 的终态行——保证「未投递消息」在发送方 / 运维的可查询面（/admin/v2/messages）里可见。
func TestMessageListQuerySeesUntrustedIDRow(t *testing.T) {
	db := openRepoSQLite(t, "msg_list_sees")
	repo := NewMessageRepository(db)
	createdMs := time.Date(2026, 10, 4, 7, 1, 11, 511_000_000, time.UTC).UnixMilli()
	if _, err := repo.FlushDaily([]model.MessageRecord{untrustedRecord(untrustedMsgID, createdMs, "")}); err != nil {
		t.Fatalf("写失败: %v", err)
	}

	rows, _, err := repo.QueryMessages(MessageQuery{
		ServerID: "game-2", Status: model.MsgStatusExpired,
		FromMs: createdMs - time.Hour.Milliseconds(), ToMs: createdMs + time.Hour.Milliseconds(), Limit: 10,
	})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(rows) != 1 || rows[0].MessageID != untrustedMsgID {
		t.Fatalf("列表查询应命中该过期消息，实际 %+v", rows)
	}
}

// TestMessageCorrelationLookupFallsBack 校验 RPC 关联查询（correlationId 直查与详情页 correlated 对手）
// 在 ID 内嵌时间不可信时的有界回退：UUIDv4 随机 ID 的往返两条也不因错表而丢结果。
func TestMessageCorrelationLookupFallsBack(t *testing.T) {
	db := openRepoSQLite(t, "msg_corr_fallback")
	repo := NewMessageRepository(db)
	createdMs := time.Date(2026, 10, 4, 7, 1, 11, 511_000_000, time.UTC).UnixMilli()
	// 请求消息：UUIDv4 随机 ID，correlation_id 自引用；响应消息：另一随机 ID，correlation_id 指回请求。
	reqID := untrustedMsgID
	respID := "d5a46cf0-bc44-4d40-8d40-91b7238e9b3d"
	req := untrustedRecord(reqID, createdMs, "")
	req.Trace.CorrelationID = reqID
	resp := untrustedRecord(respID, createdMs+50, "")
	resp.Trace.CorrelationID = reqID
	if _, err := repo.FlushDaily([]model.MessageRecord{req, resp}); err != nil {
		t.Fatalf("写失败: %v", err)
	}

	rows, err := repo.FindByCorrelationID(reqID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("按 correlationId 直查应回退命中往返两条，实际 %d 条 err=%v", len(rows), err)
	}
	pair, err := repo.FindCorrelated(reqID, reqID)
	if err != nil || pair == nil || pair.MessageID != respID {
		t.Fatalf("correlated 对手应回退命中响应消息 %s，实际 %+v err=%v", respID, pair, err)
	}
}

// TestMessageDirectLookupIgnoresFutureDayTables 校验按 ID 直查的有界回退窗口只覆盖**真实日表**：
// 历史脏数据 / 随机位推出的日表（真机实测 msg_trace_97270109 这类公元数千年后的表名）日期晚于当前，
// 按日降序恰好排在真实日表之前——若不过滤，8 个窗口位会被它们全部占满，刚落进真实日表中的行
// 按 ID 直查**反而查不到**（审查发现的高危缺陷）。本用例正是修复前会失败的场景。
//
// 同时锁定窗口的「1 天容差」：建表任务预建的「明日」日表属正常，必须留在窗口里。
func TestMessageDirectLookupIgnoresFutureDayTables(t *testing.T) {
	db := openRepoSQLite(t, "msg_future_tables")
	repo := NewMessageRepository(db)
	traceBase := model.MsgTrace{}.TableName()
	nowDay := utcDayStart(time.Now().UTC().UnixMilli())

	// 未来垃圾日表：8 张（公元 9727 年，与真机命中的错表同类），trace / payload 各建一张——
	// 修复前它们按日降序排在最前，恰好吃掉整个回退窗口。
	for i := 0; i < msgLookupFallbackTables; i++ {
		day := time.Date(9727, 1, 1+i, 0, 0, 0, 0, time.UTC)
		if _, err := store.EnsureDailyTable(db, &model.MsgTrace{}, day); err != nil {
			t.Fatalf("建未来 msg_trace 日表失败: %v", err)
		}
		if _, err := store.EnsureDailyTable(db, &model.MsgPayload{}, day); err != nil {
			t.Fatalf("建未来 msg_payload 日表失败: %v", err)
		}
	}

	// 真实日表：明日（建表任务预建）+ 今日 + 前 6 天，共 8 张，恰好填满窗口；顺序即期望的窗口顺序。
	realTables := make([]string, 0, msgLookupFallbackTables)
	for i := 1; i >= -(msgLookupFallbackTables - 2); i-- {
		day := nowDay.AddDate(0, 0, i)
		name, err := store.EnsureDailyTable(db, &model.MsgTrace{}, day)
		if err != nil {
			t.Fatalf("建真实 msg_trace 日表失败: %v", err)
		}
		if _, err := store.EnsureDailyTable(db, &model.MsgPayload{}, day); err != nil {
			t.Fatalf("建真实 msg_payload 日表失败: %v", err)
		}
		realTables = append(realTables, name)
	}

	// 回退窗口只应包含这 8 张真实日表（含明日预建表），未来垃圾日表一律排除。
	got := recentDailyTables(db, traceBase, msgLookupFallbackTables, time.Now())
	if len(got) != len(realTables) {
		t.Fatalf("回退窗口应只含 %d 张真实日表，实际 %d 张：%v", len(realTables), len(got), got)
	}
	for i, name := range got {
		if name != realTables[i] {
			t.Fatalf("回退窗口第 %d 张应为真实日表 %s，实际 %s（未来日表必须被排除）", i+1, realTables[i], name)
		}
	}

	// 真实行：终态行按控制面接收时刻落「今日」日表（写侧口径），payload 同日落表。
	realID := untrustedMsgID
	if _, err := repo.FlushDaily([]model.MessageRecord{
		untrustedRecord(realID, time.Now().UTC().UnixMilli(), `{"text":"未来日表不得挤占回退窗口"}`),
	}); err != nil {
		t.Fatalf("写真实行失败: %v", err)
	}

	// 同名历史脏行：同一 message_id 在「今日」日表与未来垃圾日表各有一行，两行状态不同——
	// 命中的必须是真实日表那行（修复前窗口被垃圾日表占满，只会命中垃圾表那行）。
	shadowID := "d5a46cf0-bc44-4d40-8d40-91b7238e9b3d"
	shadowReal := untrustedRecord(shadowID, time.Now().UTC().UnixMilli(), "").Trace
	if err := db.Table(store.DailyTableName(traceBase, nowDay)).Create(&shadowReal).Error; err != nil {
		t.Fatalf("写今日影子行失败: %v", err)
	}
	shadowGarbage := untrustedRecord(shadowID, time.Now().UTC().UnixMilli(), "").Trace
	shadowGarbage.Status = model.MsgStatusDelivered
	if err := db.Table(store.DailyTableName(traceBase, time.Date(9727, 1, 8, 0, 0, 0, 0, time.UTC))).
		Create(&shadowGarbage).Error; err != nil {
		t.Fatalf("写垃圾日表影子行失败: %v", err)
	}

	row, err := repo.FindByMessageID(realID)
	if err != nil || row == nil {
		t.Fatalf("真实日表里的行必须能按 ID 直查命中（未来日表不得占满回退窗口），实际 row=%v err=%v", row, err)
	}
	if row.Status != model.MsgStatusExpired {
		t.Fatalf("命中的应是真实日表那行（expired），实际 %s", row.Status)
	}
	pl, err := repo.FindPayload(realID)
	if err != nil || pl == nil || pl.Payload != `{"text":"未来日表不得挤占回退窗口"}` {
		t.Fatalf("payload 也必须能按 ID 直查命中（未来 payload 日表不得占满回退窗口），实际 pl=%v err=%v", pl, err)
	}
	shadow, err := repo.FindByMessageID(shadowID)
	if err != nil || shadow == nil {
		t.Fatalf("同名影子行应命中真实日表那行，实际 row=%v err=%v", shadow, err)
	}
	if shadow.Status != model.MsgStatusExpired {
		t.Fatalf("同名影子行必须命中真实日表（expired），不得命中未来垃圾日表的错表数据（%s）", shadow.Status)
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
