package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
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
	svc, relay, sink, rs, trust, _ := newMsgSvcWithViews(t)
	return svc, relay, sink, rs, trust
}

// newMsgSvcWithViews 构造带健康视图存储的消息服务（广播寻址测试用真 healthview.Store）。
func newMsgSvcWithViews(t *testing.T) (*MessageService, *MessageRelay, *captureEnqueuer, *roster.Store, fakeTrust, *healthview.Store) {
	t.Helper()
	sink := &captureEnqueuer{}
	now := time.Now().UTC().UnixMilli()
	nowPtr := &now
	relay := newTestRelay(sink, nowPtr)
	rs := roster.NewStore()
	trust := fakeTrust{allowed: map[[2]uint]bool{}}
	views := healthview.NewStore()
	svc := NewMessageService(relay, rs, trust, views)
	svc.now = func() time.Time { return time.UnixMilli(*nowPtr).UTC() }
	return svc, relay, sink, rs, trust, views
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

// onlineView 构造一条健康视图（广播寻址测试造数用）。
func onlineView(ns uint, serverID, kind, zone string, reasons ...string) healthview.View {
	return healthview.View{NamespaceID: ns, Namespace: "prod", ServerID: serverID, Kind: kind, ZoneName: zone, Reasons: reasons}
}

// broadcastParams 构造广播发送入参。
func broadcastParams(msgID string, id agentauth.Identity, zone string) MessageSendParams {
	return MessageSendParams{
		Identity: id, MessageID: msgID, MsgType: "announce",
		TargetKind: model.MsgTargetKindBroadcast, TargetZone: zone,
		Payload: "hi-all", SentAtMs: time.Now().UTC().UnixMilli(),
	}
}

// TestMessageSendBroadcastFanoutNamespace 校验无 zone 广播投本 namespace 全部在线服
// （backend + proxy，含发送者自身），其它 namespace 的在线服绝不进集合（跨 ns 广播结构性拒绝）。
func TestMessageSendBroadcastFanoutNamespace(t *testing.T) {
	svc, relay, _, _, _, views := newMsgSvcWithViews(t)
	views.ReplaceAll([]healthview.View{
		onlineView(1, "game-1", model.ServerKindBackend, "z1"), // 发送者自身
		onlineView(1, "game-2", model.ServerKindBackend, "z2"),
		onlineView(1, "proxy-1", model.ServerKindProxy, ""),
		onlineView(2, "game-9", model.ServerKindBackend, ""), // 别的 namespace，不该收到
	})
	mid := uuid7(time.Now().UTC().UnixMilli(), "b1")
	res, err := svc.Send(broadcastParams(mid, backendID(1, "game-1"), ""))
	if err != nil || res.Status != model.MsgStatusAccepted {
		t.Fatalf("广播应 accepted，实际 %+v err=%v", res, err)
	}
	for _, sid := range []string{"game-1", "game-2", "proxy-1"} {
		got := relay.Poll(context.Background(), 1, sid, 0, 10)
		if len(got) != 1 || got[0].MessageID != mid || !got[0].Broadcast {
			t.Fatalf("ns1 在线服 %s 应收到带广播标记的消息，实际 %+v", sid, got)
		}
	}
	if got := relay.Poll(context.Background(), 2, "game-9", 0, 10); len(got) != 0 {
		t.Fatalf("跨 namespace 广播一律拒绝：ns2 的 game-9 不应收到，实际 %+v", got)
	}
}

// TestMessageSendBroadcastZoneFilter 校验 targetZone 只投该 zone 当前在线服。
func TestMessageSendBroadcastZoneFilter(t *testing.T) {
	svc, relay, _, _, _, views := newMsgSvcWithViews(t)
	views.ReplaceAll([]healthview.View{
		onlineView(1, "game-1", model.ServerKindBackend, "z1"),
		onlineView(1, "game-2", model.ServerKindBackend, "z2"),
		onlineView(1, "game-3", model.ServerKindBackend, "z1"),
	})
	mid := uuid7(time.Now().UTC().UnixMilli(), "b2")
	res, err := svc.Send(broadcastParams(mid, backendID(1, "game-1"), "z1"))
	if err != nil || res.Status != model.MsgStatusAccepted {
		t.Fatalf("zone 广播应 accepted，实际 %+v err=%v", res, err)
	}
	for _, sid := range []string{"game-1", "game-3"} {
		if got := relay.Poll(context.Background(), 1, sid, 0, 10); len(got) != 1 {
			t.Fatalf("z1 的 %s 应收到，实际 %+v", sid, got)
		}
	}
	if got := relay.Poll(context.Background(), 1, "game-2", 0, 10); len(got) != 0 {
		t.Fatalf("z2 的 game-2 不应收到 z1 定向广播，实际 %+v", got)
	}
}

// TestMessageSendBroadcastSkipsUnreachable 校验在线口径：lost / pending_confirm / disabled 不进
// fan-out；draining / unhealthy 等仅调度劣势的实例仍可收广播。
func TestMessageSendBroadcastSkipsUnreachable(t *testing.T) {
	svc, relay, sink, _, _, views := newMsgSvcWithViews(t)
	views.ReplaceAll([]healthview.View{
		onlineView(1, "game-1", model.ServerKindBackend, ""),
		onlineView(1, "game-lost", model.ServerKindBackend, "", healthview.ReasonLost),
		onlineView(1, "game-pending", model.ServerKindBackend, "", healthview.ReasonPendingConfirm),
		onlineView(1, "game-disabled", model.ServerKindBackend, "", healthview.ReasonDisabled),
		onlineView(1, "game-draining", model.ServerKindBackend, "", healthview.ReasonDraining),
		onlineView(1, "game-unhealthy", model.ServerKindBackend, "", healthview.ReasonUnhealthy),
	})
	mid := uuid7(time.Now().UTC().UnixMilli(), "b3")
	if _, err := svc.Send(broadcastParams(mid, backendID(1, "game-1"), "")); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	for _, sid := range []string{"game-1", "game-draining", "game-unhealthy"} {
		if got := relay.Poll(context.Background(), 1, sid, 0, 10); len(got) != 1 {
			t.Fatalf("在线实例 %s 应收到广播，实际 %+v", sid, got)
		}
	}
	for _, sid := range []string{"game-lost", "game-pending", "game-disabled"} {
		if got := relay.Poll(context.Background(), 1, sid, 0, 10); len(got) != 0 {
			t.Fatalf("不可达实例 %s 不应进 fan-out，实际 %+v", sid, got)
		}
	}
	// fan-out 集合 = 3（可达者），聚合行终态后可见——此处只验证入队面，聚合面由 relay 测试锚定。
	_ = sink
}

// TestMessageSendBroadcastEmptyOnline 校验空在线集合 → 200 status=failed 且落 failed(no_online_target) 聚合行。
func TestMessageSendBroadcastEmptyOnline(t *testing.T) {
	svc, _, sink, _, _, _ := newMsgSvcWithViews(t)
	mid := uuid7(time.Now().UTC().UnixMilli(), "b4")
	res, err := svc.Send(broadcastParams(mid, backendID(1, "game-1"), ""))
	if err != nil || res.Status != model.MsgStatusFailed {
		t.Fatalf("空在线集合应 200 failed，实际 %+v err=%v", res, err)
	}
	rec, ok := sink.byID(mid)
	if !ok || rec.Trace.FailReason != model.MsgFailNoOnlineTarget || rec.Trace.FanoutTotal == nil || *rec.Trace.FanoutTotal != 0 {
		t.Fatalf("应落 failed(no_online_target) 且 fanout=0，实际 %+v ok=%v", rec.Trace, ok)
	}
}

// TestMessageSendBroadcastZoneRecordedInTrace 校验 zone 广播的聚合行带 target_zone。
func TestMessageSendBroadcastZoneRecordedInTrace(t *testing.T) {
	svc, _, sink, _, _, _ := newMsgSvcWithViews(t)
	mid := uuid7(time.Now().UTC().UnixMilli(), "b5")
	// 无在线服：直接终态落库，最快取到聚合行验证 zone 字段。
	if _, err := svc.Send(broadcastParams(mid, backendID(1, "game-1"), "zone-abc")); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	rec, ok := sink.byID(mid)
	if !ok || rec.Trace.TargetZone == nil || *rec.Trace.TargetZone != "zone-abc" {
		t.Fatalf("聚合行应带 target_zone=zone-abc，实际 %+v ok=%v", rec.Trace, ok)
	}
}

// TestMessageSendBroadcastValidation 校验广播参数校验：targetZone 超列宽拒 400；
// 非广播寻址携带 targetZone 亦拒 400（targetZone 是广播专属键）。
func TestMessageSendBroadcastValidation(t *testing.T) {
	svc, _, _, _, _, _ := newMsgSvcWithViews(t)
	long := strings.Repeat("z", maxTargetZoneLen+1)
	if _, err := svc.Send(broadcastParams(uuid7(time.Now().UTC().UnixMilli(), "b6"), backendID(1, "game-1"), long)); err != apperr.ErrInvalidParam {
		t.Fatalf("targetZone 超长应拒 400，实际 %v", err)
	}
	p := sendParams(uuid7(time.Now().UTC().UnixMilli(), "b7"), backendID(1, "game-1"))
	p.TargetZone = "z1" // 定向消息带 targetZone
	if _, err := svc.Send(p); err != apperr.ErrInvalidParam {
		t.Fatalf("定向消息携带 targetZone 应拒 400，实际 %v", err)
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
