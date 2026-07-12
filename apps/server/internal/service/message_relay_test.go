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
func newTestRelay(cap *captureEnqueuer, nowMs *int64) *MessageRelay {
	r := NewMessageRelay(cap)
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
	cap := &captureEnqueuer{}
	now := int64(1_000_000)
	r := newTestRelay(cap, &now)

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
	rec, ok := cap.byID("mid-1")
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
	cap := &captureEnqueuer{}
	now := int64(2_000_000)
	r := newTestRelay(cap, &now)
	r.Accept(serverMsg("mid-2", 1, "game-1", "game-2", now, ""))
	r.Poll(context.Background(), 1, "game-2", 0, 10)
	r.Ack(1, "game-2", []AckResult{{MessageID: "mid-2", Status: model.MsgStatusFailed, Reason: "boom", DeliveredAtMs: now}})
	rec, _ := cap.byID("mid-2")
	if rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != "boom" {
		t.Fatalf("应落 failed(boom)，实际 %s/%s", rec.Trace.Status, rec.Trace.FailReason)
	}
	if rec.Trace.PayloadStored { // 空 payload 不落
		t.Fatalf("空 payload 不应标 stored")
	}
}

// TestRelayTTLExpire 校验 accepted 超 TTL 无人取走 → expired(ttl_expired)。
func TestRelayTTLExpire(t *testing.T) {
	cap := &captureEnqueuer{}
	now := int64(3_000_000)
	r := newTestRelay(cap, &now)
	r.Accept(serverMsg("mid-3", 1, "game-1", "game-2", now, "x"))
	now += 31_000
	r.Sweep()
	rec, ok := cap.byID("mid-3")
	if !ok || rec.Trace.Status != model.MsgStatusExpired || rec.Trace.FailReason != model.MsgFailTTLExpired {
		t.Fatalf("应 expired(ttl_expired)，实际 %+v ok=%v", rec.Trace, ok)
	}
}

// TestRelayRequeueThenAckTimeout 校验 dispatched 超时重投至多 2 次、仍无回执 → failed(ack_timeout)。
func TestRelayRequeueThenAckTimeout(t *testing.T) {
	cap := &captureEnqueuer{}
	now := int64(4_000_000)
	r := newTestRelay(cap, &now)
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
	rec, ok := cap.byID("mid-4")
	if !ok || rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != model.MsgFailAckTimeout {
		t.Fatalf("3 次下发耗尽应 failed(ack_timeout)，实际 %+v ok=%v", rec.Trace, ok)
	}
}

// TestRelayQueueOverflow 校验队列溢出淘汰最旧为 expired(queue_overflow)。
func TestRelayQueueOverflow(t *testing.T) {
	cap := &captureEnqueuer{}
	now := int64(5_000_000)
	r := newTestRelay(cap, &now)
	r.queueCap = 2
	r.Accept(serverMsg("old", 1, "game-1", "game-2", now, ""))
	r.Accept(serverMsg("mid", 1, "game-1", "game-2", now, ""))
	r.Accept(serverMsg("new", 1, "game-1", "game-2", now, "")) // 溢出，淘汰 old
	rec, ok := cap.byID("old")
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
	cap := &captureEnqueuer{}
	now := int64(6_000_000)
	r := newTestRelay(cap, &now)
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
	cap := &captureEnqueuer{}
	now := int64(7_000_000)
	r := newTestRelay(cap, &now)
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

// TestRelayRecordTerminalDirect 校验发送期直接判失败（不入队）落 failed 终态。
func TestRelayRecordTerminalDirect(t *testing.T) {
	cap := &captureEnqueuer{}
	now := int64(8_000_000)
	r := newTestRelay(cap, &now)
	m := IncomingMessage{
		MessageID: "mid-8", NamespaceID: 1, SourceServerID: "game-1", MsgType: "rpc",
		TargetKind: model.MsgTargetKindPlayer, TargetPlayer: "ghost", Resolved: false,
		SentAtMs: now - 1, CreatedAtMs: now,
	}
	r.RecordTerminal(m, model.MsgStatusFailed, model.MsgFailPlayerNotOnline)
	rec, ok := cap.byID("mid-8")
	if !ok || rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != model.MsgFailPlayerNotOnline {
		t.Fatalf("应直接落 failed(player_not_online)，实际 %+v ok=%v", rec.Trace, ok)
	}
	// 不应进入任何队列
	if len(r.Poll(context.Background(), 1, "game-1", 0, 10)) != 0 {
		t.Fatalf("直接终态消息不应入队")
	}
}
