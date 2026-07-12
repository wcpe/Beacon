package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// msgTraceAt 构造一条落日表的消息元数据记录（供查询测试灵活造数）。
func msgTraceAt(id, src, resolved, status, correlationID string) model.MessageRecord {
	ms, _ := store.TimeMsFromUUIDv7(id)
	return model.MessageRecord{Trace: model.MsgTrace{
		MessageID: id, NamespaceID: 1, SourceServerID: src, MsgType: "chat",
		TargetKind: model.MsgTargetKindServer, TargetServerID: resolved, ResolvedServerID: resolved,
		CorrelationID: correlationID, Status: status, CreatedAt: time.UnixMilli(ms).UTC(), HopCount: 1, Hops: "[]",
	}}
}

// TestFindByMessageIDAndPayload 直查元数据 + payload；缺 payload 表 / 非 UUIDv7 均 nil。
func TestFindByMessageIDAndPayload(t *testing.T) {
	db := openRepoSQLite(t, "msg_q_find")
	repo := NewMessageRepository(db)
	ms := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	mid := uuidV7At(ms, "f1")
	if _, err := repo.FlushDaily([]model.MessageRecord{traceRecord(mid, 1, "game-1", true)}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	trace, err := repo.FindByMessageID(mid)
	if err != nil || trace == nil || trace.MessageID != mid {
		t.Fatalf("应命中元数据，实际 %v err=%v", trace, err)
	}
	pl, err := repo.FindPayload(mid)
	if err != nil || pl == nil || pl.Payload != "hello" {
		t.Fatalf("应命中 payload，实际 %v err=%v", pl, err)
	}
	if r, _ := repo.FindByMessageID("bad"); r != nil {
		t.Fatalf("非 UUIDv7 应 nil")
	}
}

// TestQueryMessagesCrossDayCursor 跨日并表 + 游标：D 与 D+1 消息按 created_at 降序合并、游标短路取页。
func TestQueryMessagesCrossDayCursor(t *testing.T) {
	db := openRepoSQLite(t, "msg_q_cross")
	repo := NewMessageRepository(db)
	dayD := time.Date(2026, 7, 11, 22, 0, 0, 0, time.UTC).UnixMilli()
	dayN := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC).UnixMilli()
	seeds := []int64{dayD, dayD + 1000, dayN, dayN + 1000}
	for i, ms := range seeds {
		id := uuidV7At(ms, string(rune('a'+i)))
		if _, err := repo.FlushDaily([]model.MessageRecord{msgTraceAt(id, "game-1", "game-2", model.MsgStatusDelivered, "")}); err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}
	q := MessageQuery{ServerID: "game-1", FromMs: dayD - 3600_000, ToMs: dayN + 3600_000, Limit: 2}
	page1, hasMore1, err := repo.QueryMessages(q)
	if err != nil || !hasMore1 || len(page1) != 2 {
		t.Fatalf("首页应 2 条且有下一页，实际 len=%d hasMore=%v err=%v", len(page1), hasMore1, err)
	}
	if page1[0].CreatedAt.UnixMilli() != dayN+1000 || page1[1].CreatedAt.UnixMilli() != dayN {
		t.Fatalf("跨日降序顺序不符")
	}
	q.Offset = 2
	page2, hasMore2, err := repo.QueryMessages(q)
	if err != nil || hasMore2 || len(page2) != 2 {
		t.Fatalf("次页应 2 条且无下一页，实际 len=%d hasMore=%v err=%v", len(page2), hasMore2, err)
	}
}

// TestFindByCorrelationIDCrossMidnight 关联直查跨午夜：请求落 D、响应落 D+1，按 correlationId 得往返两条。
func TestFindByCorrelationIDCrossMidnight(t *testing.T) {
	db := openRepoSQLite(t, "msg_q_corr")
	repo := NewMessageRepository(db)
	reqMs := time.Date(2026, 7, 11, 23, 59, 30, 0, time.UTC).UnixMilli()
	respMs := time.Date(2026, 7, 12, 0, 0, 10, 0, time.UTC).UnixMilli()
	reqID := uuidV7At(reqMs, "rq")
	respID := uuidV7At(respMs, "rp")
	if _, err := repo.FlushDaily([]model.MessageRecord{
		msgTraceAt(reqID, "game-1", "game-2", model.MsgStatusDelivered, reqID),  // 请求自引用
		msgTraceAt(respID, "game-2", "game-1", model.MsgStatusDelivered, reqID), // 响应指回请求
	}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	rows, err := repo.FindByCorrelationID(reqID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("跨午夜按 correlationId 应得往返 2 条，实际 len=%d err=%v", len(rows), err)
	}
	// 关联查找：从请求（自引用）应找到响应对手。
	corr, err := repo.FindCorrelated(reqID, reqID)
	if err != nil || corr == nil || corr.MessageID != respID {
		t.Fatalf("请求应关联到响应 %s，实际 %v err=%v", respID, corr, err)
	}
}

// TestScanMessageStatsWindow 聚合投影只取窗口内消息（窗口外不计）。
func TestScanMessageStatsWindow(t *testing.T) {
	db := openRepoSQLite(t, "msg_q_stats")
	repo := NewMessageRepository(db)
	winFrom := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	winTo := winFrom + 30*60_000
	inID := uuidV7At(winFrom+60_000, "in")
	outID := uuidV7At(winFrom-2*3600_000, "out") // 窗口前，同日但时间外
	if _, err := repo.FlushDaily([]model.MessageRecord{
		msgTraceAt(inID, "game-1", "game-2", model.MsgStatusFailed, ""),
		msgTraceAt(outID, "game-1", "game-2", model.MsgStatusDelivered, ""),
	}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	rows, err := repo.ScanMessageStats(winFrom, winTo)
	if err != nil || len(rows) != 1 || rows[0].MessageID != inID {
		t.Fatalf("应只取窗口内 1 条，实际 %+v err=%v", rows, err)
	}
}
