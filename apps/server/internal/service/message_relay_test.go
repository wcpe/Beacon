package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// captureEnqueuer 捕获落库的终态记录，供断言。
type captureEnqueuer struct {
	mu      sync.Mutex
	records []model.MessageRecord
	full    bool // 置 true 模拟队列满
}

func (c *captureEnqueuer) Enqueue(rs []model.MessageRecord) bool {
	if c.full {
		return false
	}
	c.mu.Lock()
	c.records = append(c.records, rs...)
	c.mu.Unlock()
	return true
}

func (c *captureEnqueuer) byID(id string) (model.MessageRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Trace.MessageID == id {
			return r, true
		}
	}
	return model.MessageRecord{}, false
}

// newTestRelay 构造带可控时钟与小档位的中转。
func newTestRelay(sink *captureEnqueuer, nowMs *int64) *MessageRelay {
	r := NewMessageRelay(sink)
	r.now = func() time.Time { return time.UnixMilli(*nowMs).UTC() }
	r.ttl = 30 * time.Second
	r.ackTimeout = 10 * time.Second
	r.maxDispatch = 3
	return r
}

func serverMsg(id string, ns uint, src, target string, createdMs int64, payload string) IncomingMessage {
	return IncomingMessage{
		MessageID: id, NamespaceID: ns, SourceServerID: src, MsgType: "chat",
		TargetKind: model.MsgTargetKindServer, TargetServerID: target,
		ResolvedNamespaceID: ns, ResolvedServerID: target,
		Payload: payload, PayloadSize: len(payload), SentAtMs: createdMs - 1, CreatedAtMs: createdMs,
	}
}

// TestRelayAcceptPollDeliver 校验 accept→poll(dispatched)→ack(delivered) 全链路 hops 与耗时。
func TestRelayAcceptPollDeliver(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(1_000_000)
	r := newTestRelay(sink, &now)

	r.Accept(serverMsg("mid-1", 1, "game-1", "game-2", now, "hello"))
	got := r.Poll(context.Background(), 1, "game-2", 0, 10)
	if len(got) != 1 || got[0].MessageID != "mid-1" || got[0].Payload != "hello" {
		t.Fatalf("poll 应取到 mid-1 带 payload，实际 %+v", got)
	}

	now += 20
	applied, ignored := r.Ack(1, "game-2", []AckResult{{MessageID: "mid-1", Status: model.MsgStatusDelivered, DeliveredAtMs: now}})
	if applied != 1 || ignored != 0 {
		t.Fatalf("ack 应 applied=1 ignored=0，实际 %d/%d", applied, ignored)
	}
	rec, ok := sink.byID("mid-1")
	if !ok || rec.Trace.Status != model.MsgStatusDelivered {
		t.Fatalf("应落 delivered 终态记录，实际 %+v ok=%v", rec.Trace.Status, ok)
	}
	if rec.Trace.HopCount != 1 {
		t.Fatalf("经控制面单跳中转 hop_count 应为 1，实际 %d", rec.Trace.HopCount)
	}
	if rec.Trace.DurationMs == nil || *rec.Trace.DurationMs != 20 {
		t.Fatalf("全链路耗时应为 20ms，实际 %v", rec.Trace.DurationMs)
	}
	if rec.Payload == nil || rec.Payload.Payload != "hello" || rec.Payload.SHA256 == "" {
		t.Fatalf("应落 payload 且带 sha256，实际 %+v", rec.Payload)
	}
	var hops []hopEvent
	if err := json.Unmarshal([]byte(rec.Trace.Hops), &hops); err != nil || len(hops) != 4 {
		t.Fatalf("hops 应为 sent/received/dispatched/delivered 四段，实际 %s err=%v", rec.Trace.Hops, err)
	}
	if hops[0].Event != hopSent || hops[3].Event != hopDelivered {
		t.Fatalf("hops 首尾应为 sent/delivered，实际 %s..%s", hops[0].Event, hops[3].Event)
	}
}

// TestRelayAckFailed 校验回执失败落 failed 终态带原因。
func TestRelayAckFailed(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(2_000_000)
	r := newTestRelay(sink, &now)
	r.Accept(serverMsg("mid-2", 1, "game-1", "game-2", now, ""))
	r.Poll(context.Background(), 1, "game-2", 0, 10)
	r.Ack(1, "game-2", []AckResult{{MessageID: "mid-2", Status: model.MsgStatusFailed, Reason: "boom", DeliveredAtMs: now}})
	rec, _ := sink.byID("mid-2")
	if rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != "boom" {
		t.Fatalf("应落 failed(boom)，实际 %s/%s", rec.Trace.Status, rec.Trace.FailReason)
	}
	if rec.Trace.PayloadStored { // 空 payload 不落
		t.Fatalf("空 payload 不应标 stored")
	}
}

// TestRelayTTLExpire 校验 accepted 超 TTL 无人取走 → expired(ttl_expired)。
func TestRelayTTLExpire(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(3_000_000)
	r := newTestRelay(sink, &now)
	r.Accept(serverMsg("mid-3", 1, "game-1", "game-2", now, "x"))
	now += 31_000
	r.Sweep()
	rec, ok := sink.byID("mid-3")
	if !ok || rec.Trace.Status != model.MsgStatusExpired || rec.Trace.FailReason != model.MsgFailTTLExpired {
		t.Fatalf("应 expired(ttl_expired)，实际 %+v ok=%v", rec.Trace, ok)
	}
}

// TestRelayRequeueThenAckTimeout 校验 dispatched 超时重投至多 2 次、仍无回执 → failed(ack_timeout)。
func TestRelayRequeueThenAckTimeout(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(4_000_000)
	r := newTestRelay(sink, &now)
	r.Accept(serverMsg("mid-4", 1, "game-1", "game-2", now, ""))

	// 首次取走（dispatch 1）
	if len(r.Poll(context.Background(), 1, "game-2", 0, 10)) != 1 {
		t.Fatalf("首次应取到")
	}
	// 超时重投 → dispatch 2
	now += 11_000
	r.Sweep()
	if len(r.Poll(context.Background(), 1, "game-2", 0, 10)) != 1 {
		t.Fatalf("重投后应能再取（dispatch 2）")
	}
	// 超时重投 → dispatch 3
	now += 11_000
	r.Sweep()
	if len(r.Poll(context.Background(), 1, "game-2", 0, 10)) != 1 {
		t.Fatalf("重投后应能再取（dispatch 3）")
	}
	// dispatch 3 仍无回执超时 → failed(ack_timeout)
	now += 11_000
	r.Sweep()
	rec, ok := sink.byID("mid-4")
	if !ok || rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != model.MsgFailAckTimeout {
		t.Fatalf("3 次下发耗尽应 failed(ack_timeout)，实际 %+v ok=%v", rec.Trace, ok)
	}
}

// TestRelayQueueOverflow 校验队列溢出淘汰最旧为 expired(queue_overflow)。
func TestRelayQueueOverflow(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(5_000_000)
	r := newTestRelay(sink, &now)
	r.queueCap = 2
	r.Accept(serverMsg("old", 1, "game-1", "game-2", now, ""))
	r.Accept(serverMsg("mid", 1, "game-1", "game-2", now, ""))
	r.Accept(serverMsg("new", 1, "game-1", "game-2", now, "")) // 溢出，淘汰 old
	rec, ok := sink.byID("old")
	if !ok || rec.Trace.Status != model.MsgStatusExpired || rec.Trace.FailReason != model.MsgFailQueueOverflow {
		t.Fatalf("最旧应被淘汰为 expired(queue_overflow)，实际 %+v ok=%v", rec.Trace, ok)
	}
	// 队列应剩 mid、new
	got := r.Poll(context.Background(), 1, "game-2", 0, 10)
	if len(got) != 2 || got[0].MessageID != "mid" || got[1].MessageID != "new" {
		t.Fatalf("溢出后应剩 mid,new，实际 %+v", got)
	}
}

// TestRelayAckUnknownAndCrossServer 校验未知 messageId 与非本服 ack 计入 ignored。
func TestRelayAckUnknownAndCrossServer(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(6_000_000)
	r := newTestRelay(sink, &now)
	r.Accept(serverMsg("mid-6", 1, "game-1", "game-2", now, ""))
	r.Poll(context.Background(), 1, "game-2", 0, 10)
	// 未知 id + 另一台服冒领本服消息
	applied, ignored := r.Ack(1, "game-3", []AckResult{
		{MessageID: "nope", Status: model.MsgStatusDelivered},
		{MessageID: "mid-6", Status: model.MsgStatusDelivered}, // game-3 无权 ack game-2 的消息
	})
	if applied != 0 || ignored != 2 {
		t.Fatalf("应全部 ignored（未知 + 非本服），实际 applied=%d ignored=%d", applied, ignored)
	}
}

// TestRelayPollLongPollWakeup 校验空队列长轮询被 Accept 唤醒后取到消息。
func TestRelayPollLongPollWakeup(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(7_000_000)
	r := newTestRelay(sink, &now)
	done := make(chan []DispatchedMessage, 1)
	go func() {
		done <- r.Poll(context.Background(), 1, "game-2", 5, 10)
	}()
	// 稍候后投递，应唤醒挂起的 poll
	time.Sleep(50 * time.Millisecond)
	r.Accept(serverMsg("mid-7", 1, "game-1", "game-2", now, "hi"))
	select {
	case got := <-done:
		if len(got) != 1 || got[0].MessageID != "mid-7" {
			t.Fatalf("长轮询应被唤醒取到 mid-7，实际 %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("长轮询未被唤醒（超时）")
	}
}

// broadcastMsg 构造一条广播消息元数据（FR-180）。
func broadcastMsg(id string, ns uint, src, zone string, createdMs int64, payload string) IncomingMessage {
	return IncomingMessage{
		MessageID: id, NamespaceID: ns, SourceServerID: src, MsgType: "announce",
		TargetKind: model.MsgTargetKindBroadcast, TargetZone: zone,
		ResolvedNamespaceID: ns,
		Payload:             payload, PayloadSize: len(payload), SentAtMs: createdMs - 1, CreatedAtMs: createdMs,
	}
}

// recordCount 返回已捕获的落库记录总数。
func (c *captureEnqueuer) recordCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.records)
}

// TestRelayBroadcastAllDelivered 校验广播 fan-out 全部送达：各目标（含发送者自身）poll 到带
// Broadcast 标记的消息、逐目标 ack 后只落一行聚合记录（status=delivered、计数 / 链路 / 耗时正确、payload 一份）。
func TestRelayBroadcastAllDelivered(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(10_000_000)
	r := newTestRelay(sink, &now)

	targets := []string{"game-1", "game-2", "proxy-1"} // game-1 为发送者自身（含自身语义）
	if !r.AcceptBroadcast(broadcastMsg("bid-1", 1, "game-1", "", now, "hello-all"), targets) {
		t.Fatalf("非空目标集合应受理成功")
	}
	for _, sid := range targets {
		got := r.Poll(context.Background(), 1, sid, 0, 10)
		if len(got) != 1 || got[0].MessageID != "bid-1" || got[0].Payload != "hello-all" {
			t.Fatalf("目标 %s 应 poll 到广播消息，实际 %+v", sid, got)
		}
		if !got[0].Broadcast {
			t.Fatalf("广播下发应带 Broadcast 标记（目标 %s）", sid)
		}
	}
	// 非目标服冒领：不应 applied（复合键按（消息, 目标）定位）。
	if applied, ignored := r.Ack(1, "game-9", []AckResult{{MessageID: "bid-1", Status: model.MsgStatusDelivered}}); applied != 0 || ignored != 1 {
		t.Fatalf("非目标服 ack 应 ignored，实际 applied=%d ignored=%d", applied, ignored)
	}
	// 各目标逐一回执 delivered；全部到齐前不落任何记录。
	for i, sid := range targets {
		if sink.recordCount() != 0 {
			t.Fatalf("聚合收口前不应落库，实际已有 %d 条", sink.recordCount())
		}
		now += 10
		if applied, _ := r.Ack(1, sid, []AckResult{{MessageID: "bid-1", Status: model.MsgStatusDelivered, DeliveredAtMs: now}}); applied != 1 {
			t.Fatalf("目标 %s（第 %d 个）回执应 applied=1", sid, i)
		}
	}
	if sink.recordCount() != 1 {
		t.Fatalf("一条广播应只落一行聚合记录，实际 %d 行", sink.recordCount())
	}
	rec, _ := sink.byID("bid-1")
	assertBroadcastAllDelivered(t, rec)
}

// assertBroadcastAllDelivered 断言全部送达的广播聚合行：状态 / 计数 / 耗时 / 单跳 / payload / 概要链路。
func assertBroadcastAllDelivered(t *testing.T, rec model.MessageRecord) {
	t.Helper()
	if rec.Trace.Status != model.MsgStatusDelivered || rec.Trace.FailReason != "" {
		t.Fatalf("全部送达应 status=delivered 无原因，实际 %s/%s", rec.Trace.Status, rec.Trace.FailReason)
	}
	if rec.Trace.TargetKind != model.MsgTargetKindBroadcast || rec.Trace.TargetZone != nil {
		t.Fatalf("聚合行应 target_kind=broadcast 且无 zone，实际 %+v", rec.Trace)
	}
	if rec.Trace.FanoutTotal == nil || *rec.Trace.FanoutTotal != 3 ||
		rec.Trace.DeliveredCount == nil || *rec.Trace.DeliveredCount != 3 ||
		rec.Trace.FailedCount == nil || *rec.Trace.FailedCount != 0 ||
		rec.Trace.ExpiredCount == nil || *rec.Trace.ExpiredCount != 0 {
		t.Fatalf("聚合计数应 3/3/0/0，实际 %+v", rec.Trace)
	}
	if rec.Trace.DurationMs == nil || *rec.Trace.DurationMs != 30 {
		t.Fatalf("duration 应按最后一次 delivered 结算为 30ms，实际 %v", rec.Trace.DurationMs)
	}
	if rec.Trace.HopCount != 1 {
		t.Fatalf("广播经控制面单跳 hop_count 应为 1，实际 %d", rec.Trace.HopCount)
	}
	if rec.Payload == nil || rec.Payload.Payload != "hello-all" {
		t.Fatalf("payload 应只存一份，实际 %+v", rec.Payload)
	}
	var hops []hopEvent
	if err := json.Unmarshal([]byte(rec.Trace.Hops), &hops); err != nil || len(hops) != 4 {
		t.Fatalf("聚合链路应为 sent/received/dispatched/delivered 概要四段，实际 %s err=%v", rec.Trace.Hops, err)
	}
	if hops[2].Event != hopDispatched || hops[3].Event != hopDelivered {
		t.Fatalf("概要链路后两段应为 dispatched/delivered，实际 %s/%s", hops[2].Event, hops[3].Event)
	}
}

// TestRelayBroadcastPartialDelivered 校验部分送达口径：任一 delivered 即整行 delivered，
// 失败 / 过期面经计数可见（1 delivered + 1 failed + 1 TTL expired → delivered 1/1/1）。
func TestRelayBroadcastPartialDelivered(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(11_000_000)
	r := newTestRelay(sink, &now)

	r.AcceptBroadcast(broadcastMsg("bid-2", 1, "game-1", "", now, "x"), []string{"game-1", "game-2", "game-3"})
	// game-1 送达、game-2 回执失败、game-3 从不取走（TTL 过期）。
	r.Poll(context.Background(), 1, "game-1", 0, 10)
	r.Poll(context.Background(), 1, "game-2", 0, 10)
	r.Ack(1, "game-1", []AckResult{{MessageID: "bid-2", Status: model.MsgStatusDelivered, DeliveredAtMs: now + 5}})
	r.Ack(1, "game-2", []AckResult{{MessageID: "bid-2", Status: model.MsgStatusFailed, Reason: "boom"}})
	now += 31_000
	r.Sweep()

	rec, ok := sink.byID("bid-2")
	if !ok || rec.Trace.Status != model.MsgStatusDelivered {
		t.Fatalf("有任一 delivered 整行应 delivered，实际 %+v ok=%v", rec.Trace, ok)
	}
	if *rec.Trace.FanoutTotal != 3 || *rec.Trace.DeliveredCount != 1 || *rec.Trace.FailedCount != 1 || *rec.Trace.ExpiredCount != 1 {
		t.Fatalf("聚合计数应 3/1/1/1，实际 %+v", rec.Trace)
	}
}

// TestRelayBroadcastAllFailedTakesMajority 校验全军覆没口径：无一 delivered 时按 failed/expired
// 多数定整行状态（平手偏 failed），fail_reason 取目标原因中出现最多者。
func TestRelayBroadcastAllFailedTakesMajority(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(12_000_000)
	r := newTestRelay(sink, &now)

	// 场景一：2 failed（handler boom）+ 1 expired → 整行 failed(boom)。
	r.AcceptBroadcast(broadcastMsg("bid-3", 1, "game-1", "", now, ""), []string{"game-1", "game-2", "game-3"})
	r.Poll(context.Background(), 1, "game-1", 0, 10)
	r.Poll(context.Background(), 1, "game-2", 0, 10)
	r.Ack(1, "game-1", []AckResult{{MessageID: "bid-3", Status: model.MsgStatusFailed, Reason: "boom"}})
	r.Ack(1, "game-2", []AckResult{{MessageID: "bid-3", Status: model.MsgStatusFailed, Reason: "boom"}})
	now += 31_000
	r.Sweep()
	rec, _ := sink.byID("bid-3")
	if rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != "boom" {
		t.Fatalf("failed 多数应整行 failed(boom)，实际 %s/%s", rec.Trace.Status, rec.Trace.FailReason)
	}

	// 场景二：1 failed + 2 expired → 整行 expired，原因取多数 ttl_expired。
	base := now + 1000
	r2 := newTestRelay(sink, &now)
	now = base
	r2.AcceptBroadcast(broadcastMsg("bid-4", 1, "game-1", "", now, ""), []string{"game-1", "game-2", "game-3"})
	r2.Poll(context.Background(), 1, "game-1", 0, 10)
	r2.Ack(1, "game-1", []AckResult{{MessageID: "bid-4", Status: model.MsgStatusFailed, Reason: "boom"}})
	now += 31_000
	r2.Sweep()
	rec4, _ := sink.byID("bid-4")
	if rec4.Trace.Status != model.MsgStatusExpired || rec4.Trace.FailReason != model.MsgFailTTLExpired {
		t.Fatalf("expired 多数应整行 expired(ttl_expired)，实际 %s/%s", rec4.Trace.Status, rec4.Trace.FailReason)
	}
	if *rec4.Trace.ExpiredCount != 2 || *rec4.Trace.FailedCount != 1 {
		t.Fatalf("聚合计数应 expired=2 failed=1，实际 %+v", rec4.Trace)
	}
}

// TestRelayBroadcastEmptyTargets 校验空在线集合：不入队、直接落聚合终态行 failed(no_online_target)、fanout=0。
func TestRelayBroadcastEmptyTargets(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(13_000_000)
	r := newTestRelay(sink, &now)

	if r.AcceptBroadcast(broadcastMsg("bid-5", 1, "game-1", "zone-x", now, "p"), nil) {
		t.Fatalf("空目标集合应返回 false")
	}
	rec, ok := sink.byID("bid-5")
	if !ok || rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != model.MsgFailNoOnlineTarget {
		t.Fatalf("应落 failed(no_online_target)，实际 %+v ok=%v", rec.Trace, ok)
	}
	if rec.Trace.FanoutTotal == nil || *rec.Trace.FanoutTotal != 0 || rec.Trace.TargetZone == nil || *rec.Trace.TargetZone != "zone-x" {
		t.Fatalf("聚合行应 fanout=0 且带 zone，实际 %+v", rec.Trace)
	}
	if len(r.Poll(context.Background(), 1, "game-1", 0, 10)) != 0 {
		t.Fatalf("空 fan-out 不应有任何入队消息")
	}
}

// TestRelayBroadcastRedispatchThenAckTimeout 校验广播目标沿用重投规则：取走不回执 → 重投 2 次 →
// 仍无回执按 ack_timeout 计入聚合并收口。
func TestRelayBroadcastRedispatchThenAckTimeout(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(14_000_000)
	r := newTestRelay(sink, &now)
	r.AcceptBroadcast(broadcastMsg("bid-6", 1, "game-1", "", now, ""), []string{"game-2"})

	for i := 0; i < 3; i++ { // 首发 + 2 次重投均取走且不回执
		if len(r.Poll(context.Background(), 1, "game-2", 0, 10)) != 1 {
			t.Fatalf("第 %d 次下发应能取到", i+1)
		}
		now += 11_000
		r.Sweep()
	}
	rec, ok := sink.byID("bid-6")
	if !ok || rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != model.MsgFailAckTimeout {
		t.Fatalf("重投用尽应整行 failed(ack_timeout)，实际 %+v ok=%v", rec.Trace, ok)
	}
	if *rec.Trace.FailedCount != 1 || *rec.Trace.FanoutTotal != 1 {
		t.Fatalf("聚合计数应 fanout=1 failed=1，实际 %+v", rec.Trace)
	}
}

// TestRelayBroadcastQueueOverflowCountsExpired 校验广播目标被队列溢出淘汰时计入 expired_count
// （每目标独立走溢出规则）。
func TestRelayBroadcastQueueOverflowCountsExpired(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(15_000_000)
	r := newTestRelay(sink, &now)
	r.queueCap = 1

	// 广播先占 game-2 队列，随后两条定向把它挤出（第一条挤出广播目标、第二条挤出第一条定向）。
	r.AcceptBroadcast(broadcastMsg("bid-7", 1, "game-1", "", now, ""), []string{"game-2"})
	r.Accept(serverMsg("dir-1", 1, "game-1", "game-2", now, ""))
	rec, ok := sink.byID("bid-7")
	if !ok || rec.Trace.Status != model.MsgStatusExpired || rec.Trace.FailReason != model.MsgFailQueueOverflow {
		t.Fatalf("被溢出淘汰的单目标广播应收口为 expired(queue_overflow)，实际 %+v ok=%v", rec.Trace, ok)
	}
	if *rec.Trace.ExpiredCount != 1 {
		t.Fatalf("expired_count 应为 1，实际 %+v", rec.Trace)
	}
}

// TestRelayRecordTerminalDirect 校验发送期直接判失败（不入队）落 failed 终态。
func TestRelayRecordTerminalDirect(t *testing.T) {
	sink := &captureEnqueuer{}
	now := int64(8_000_000)
	r := newTestRelay(sink, &now)
	m := IncomingMessage{
		MessageID: "mid-8", NamespaceID: 1, SourceServerID: "game-1", MsgType: "rpc",
		TargetKind: model.MsgTargetKindPlayer, TargetPlayer: "ghost", Resolved: false,
		SentAtMs: now - 1, CreatedAtMs: now,
	}
	r.RecordTerminal(m, model.MsgStatusFailed, model.MsgFailPlayerNotOnline)
	rec, ok := sink.byID("mid-8")
	if !ok || rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != model.MsgFailPlayerNotOnline {
		t.Fatalf("应直接落 failed(player_not_online)，实际 %+v ok=%v", rec.Trace, ok)
	}
	// 不应进入任何队列
	if len(r.Poll(context.Background(), 1, "game-1", 0, 10)) != 0 {
		t.Fatalf("直接终态消息不应入队")
	}
}
