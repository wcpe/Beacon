package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
)

// fakeTrust 按预置集合判定跨域信任。
type fakeTrust struct {
	allowed map[[2]uint]bool
}

func (f fakeTrust) NamespaceTrustAllowed(from, to uint, capability string) bool {
	return capability == model.NamespaceTrustCapabilityMessage && f.allowed[[2]uint{from, to}]
}

func newMsgSvc(t *testing.T) (*MessageService, *MessageRelay, *captureEnqueuer, *roster.Store, fakeTrust) {
	t.Helper()
	sink := &captureEnqueuer{}
	now := time.Now().UTC().UnixMilli()
	nowPtr := &now
	relay := newTestRelay(sink, nowPtr)
	rs := roster.NewStore()
	trust := fakeTrust{allowed: map[[2]uint]bool{}}
	svc := NewMessageService(relay, rs, trust)
	svc.now = func() time.Time { return time.UnixMilli(*nowPtr).UTC() }
	return svc, relay, sink, rs, trust
}

func sendParams(msgID string, id agentauth.Identity) MessageSendParams {
	return MessageSendParams{
		Identity: id, MessageID: msgID, MsgType: "chat",
		TargetKind: model.MsgTargetKindServer, TargetServerID: "game-2",
		Payload: "hi", SentAtMs: time.Now().UTC().UnixMilli(),
	}
}

func backendID(ns uint, server string) agentauth.Identity {
	return agentauth.Identity{NamespaceID: ns, Namespace: "prod", ServerID: server, Kind: model.ServerKindBackend}
}

// TestMessageSendServerAccepted 校验 server 目标发送入队、状态 accepted、可被 poll 取走。
func TestMessageSendServerAccepted(t *testing.T) {
	svc, _, _, _, _ := newMsgSvc(t)
	mid := uuid7(time.Now().UTC().UnixMilli(), "s1")
	res, err := svc.Send(sendParams(mid, backendID(1, "game-1")))
	if err != nil || res.Status != model.MsgStatusAccepted {
		t.Fatalf("应 accepted，实际 %+v err=%v", res, err)
	}
	got := svc.relay.Poll(context.Background(), 1, "game-2", 0, 10)
	if len(got) != 1 || got[0].MessageID != mid {
		t.Fatalf("应能 poll 到该消息，实际 %+v", got)
	}
}

// TestMessageSendPayloadTooLarge 校验 payload 超限拒 400 payload_too_large。
func TestMessageSendPayloadTooLarge(t *testing.T) {
	svc, _, _, _, _ := newMsgSvc(t)
	p := sendParams(uuid7(time.Now().UTC().UnixMilli(), "s2"), backendID(1, "game-1"))
	p.Payload = strings.Repeat("x", maxPayloadBytes+1)
	if _, err := svc.Send(p); err != apperr.ErrPayloadTooLarge {
		t.Fatalf("超限应拒 payload_too_large，实际 %v", err)
	}
}

// TestMessageSendPlayerNotOnline 校验按玩家寻址不在线 → 200 status=failed(player_not_online) 且落库。
func TestMessageSendPlayerNotOnline(t *testing.T) {
	svc, _, sink, _, _ := newMsgSvc(t)
	mid := uuid7(time.Now().UTC().UnixMilli(), "s3")
	p := sendParams(mid, backendID(1, "game-1"))
	p.TargetKind = model.MsgTargetKindPlayer
	p.TargetServerID = ""
	p.TargetPlayerUUID = "ghost"
	res, err := svc.Send(p)
	if err != nil || res.Status != model.MsgStatusFailed {
		t.Fatalf("玩家不在线应 200 failed，实际 %+v err=%v", res, err)
	}
	rec, ok := sink.byID(mid)
	if !ok || rec.Trace.FailReason != model.MsgFailPlayerNotOnline {
		t.Fatalf("应落 failed(player_not_online)，实际 %+v ok=%v", rec.Trace, ok)
	}
}

// TestMessageSendPlayerSameNamespace 校验同域玩家在线解析 resolved 并入队。
func TestMessageSendPlayerSameNamespace(t *testing.T) {
	svc, _, _, rs, _ := newMsgSvc(t)
	rs.ApplyOpen(1, "alice", "c1", "game-9")
	mid := uuid7(time.Now().UTC().UnixMilli(), "s4")
	p := sendParams(mid, backendID(1, "game-1"))
	p.TargetKind = model.MsgTargetKindPlayer
	p.TargetServerID = ""
	p.TargetPlayerUUID = "alice"
	res, err := svc.Send(p)
	if err != nil || res.Status != model.MsgStatusAccepted {
		t.Fatalf("同域玩家在线应 accepted，实际 %+v err=%v", res, err)
	}
	if got := svc.relay.Poll(context.Background(), 1, "game-9", 0, 10); len(got) != 1 {
		t.Fatalf("应投递到 alice 所在 game-9，实际 %+v", got)
	}
}

// TestMessageSendCrossNamespaceNoTrust 校验跨域玩家无信任 → 403 并记 failed。
func TestMessageSendCrossNamespaceNoTrust(t *testing.T) {
	svc, _, sink, rs, _ := newMsgSvc(t)
	rs.ApplyOpen(2, "bob", "c2", "game-5") // bob 在 ns2
	mid := uuid7(time.Now().UTC().UnixMilli(), "s5")
	p := sendParams(mid, backendID(1, "game-1")) // 源 ns1
	p.TargetKind = model.MsgTargetKindPlayer
	p.TargetServerID = ""
	p.TargetPlayerUUID = "bob"
	if _, err := svc.Send(p); err != apperr.ErrMessageCrossNamespaceNoTrust {
		t.Fatalf("跨域无信任应 403，实际 %v", err)
	}
	rec, ok := sink.byID(mid)
	if !ok || rec.Trace.Status != model.MsgStatusFailed || rec.Trace.FailReason != model.MsgFailNamespaceNoTrust || !rec.Trace.CrossNamespace {
		t.Fatalf("应落 failed(namespace_not_trusted) 且 cross_namespace=true，实际 %+v ok=%v", rec.Trace, ok)
	}
}

// TestMessageSendCrossNamespaceTrusted 校验跨域有信任放行、投递到目标服。
func TestMessageSendCrossNamespaceTrusted(t *testing.T) {
	svc, _, _, rs, trust := newMsgSvc(t)
	trust.allowed[[2]uint{1, 2}] = true
	rs.ApplyOpen(2, "bob", "c3", "game-5")
	mid := uuid7(time.Now().UTC().UnixMilli(), "s6")
	p := sendParams(mid, backendID(1, "game-1"))
	p.TargetKind = model.MsgTargetKindPlayer
	p.TargetServerID = ""
	p.TargetPlayerUUID = "bob"
	res, err := svc.Send(p)
	if err != nil || res.Status != model.MsgStatusAccepted {
		t.Fatalf("跨域有信任应 accepted，实际 %+v err=%v", res, err)
	}
	// 投递到 ns2 的 game-5 队列，由 ns2 的 agent poll 取走
	if got := svc.relay.Poll(context.Background(), 2, "game-5", 0, 10); len(got) != 1 {
		t.Fatalf("应投递到 ns2 game-5，实际 %+v", got)
	}
}

// TestMessageSendInvalidMessageID 校验 messageId 非 UUIDv7 拒 400。
func TestMessageSendInvalidMessageID(t *testing.T) {
	svc, _, _, _, _ := newMsgSvc(t)
	p := sendParams("not-a-uuid", backendID(1, "game-1"))
	if _, err := svc.Send(p); err != apperr.ErrInvalidParam {
		t.Fatalf("非法 messageId 应拒 400，实际 %v", err)
	}
}

// TestMessagePollAckRoundtrip 校验 poll 取走 → ack delivered 全链路。
func TestMessagePollAckRoundtrip(t *testing.T) {
	svc, _, sink, _, _ := newMsgSvc(t)
	mid := uuid7(time.Now().UTC().UnixMilli(), "s7")
	if _, err := svc.Send(sendParams(mid, backendID(1, "game-1"))); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	got := svc.PollMessages(context.Background(), backendID(1, "game-2"), 0, 100) // max 超上限被 clamp
	if len(got) != 1 {
		t.Fatalf("应 poll 到 1 条，实际 %d", len(got))
	}
	applied, ignored := svc.AckMessages(backendID(1, "game-2"),
		[]AckResult{{MessageID: mid, Status: model.MsgStatusDelivered, DeliveredAtMs: time.Now().UTC().UnixMilli()}})
	if applied != 1 || ignored != 0 {
		t.Fatalf("ack 应 applied=1，实际 %d/%d", applied, ignored)
	}
	if rec, ok := sink.byID(mid); !ok || rec.Trace.Status != model.MsgStatusDelivered {
		t.Fatalf("应落 delivered，实际 %+v ok=%v", rec.Trace, ok)
	}
}
