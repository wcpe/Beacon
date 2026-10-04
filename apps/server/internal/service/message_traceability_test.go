package service

import (
	"context"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// v4RandomMsgID 是真机接入验收实测命中的 message_id 形态（UUIDv4 随机 ID）：前 48 位不是时间，
// 旧实现据其「推导」出 msg_trace_51580917 这类永不进入查询窗口的日表，于是消息发出去就消失。
const v4RandomMsgID = "5b84d1a0-7faa-4db0-a5df-362d302ea1cb"

// repoEnqueuer 把中转终态记录直接写进真仓库（替代异步写入通道，测试无计时等待、确定性强）。
type repoEnqueuer struct {
	t    *testing.T
	repo *repository.MessageRepository
}

// Enqueue 同步落库；失败即测试失败（异步通道的「队列满丢弃」场景不在本用例覆盖面内）。
func (e repoEnqueuer) Enqueue(records []model.MessageRecord) bool {
	if _, err := e.repo.FlushDaily(records); err != nil {
		e.t.Fatalf("消息终态记录落库失败: %v", err)
		return false
	}
	return true
}

// newTraceableMsgSvc 构造「真仓库 + 可控时钟」的消息服务：中转终态记录直接进 sqlite 日表，
// 便于对「未投递消息是否可追踪」做端到端断言（send → 不 poll → TTL → 落库 → 查询）。
func newTraceableMsgSvc(t *testing.T, dbName string, nowMs *int64) (*MessageService, *MessageRelay, *repository.MessageRepository) {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + dbName + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })
	repo := repository.NewMessageRepository(db)
	relay := NewMessageRelay(repoEnqueuer{t: t, repo: repo})
	relay.now = func() time.Time { return time.UnixMilli(*nowMs).UTC() }
	relay.ttl = 30 * time.Second
	relay.ackTimeout = 10 * time.Second
	relay.maxDispatch = 3
	svc := NewMessageService(relay, roster.NewStore(), fakeTrust{allowed: map[[2]uint]bool{}}, healthview.NewStore())
	svc.now = func() time.Time { return time.UnixMilli(*nowMs).UTC() }
	return svc, relay, repo
}

// TestMessageNotDeliveredTraceable 校验「发送方被告知 accepted、但消息最终没送达」在数据上可见：
// 目标 agent 未启用 messaging（从不 poll）时，TTL 过后必须落一条可查询的 expired(ttl_expired) 终态行——
// 按 message_id 直查、按 serverId + 时间窗列表查询（/admin/v2/messages 口径）都能查到最终去向，
// payload 亦与元数据同日落表可查。两种 message_id 都覆盖：规范 UUIDv7 与实测命中的 UUIDv4 随机 ID。
func TestMessageNotDeliveredTraceable(t *testing.T) {
	cases := []struct {
		name   string
		dbName string
		msgID  string // 空表示用规范 UUIDv7
	}{
		{name: "规范UUIDv7", dbName: "msg_traceable_v7"},
		{name: "UUIDv4随机ID实测命中", dbName: "msg_traceable_v4", msgID: v4RandomMsgID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := time.Date(2026, 10, 4, 7, 1, 11, 0, time.UTC).UnixMilli()
			svc, relay, repo := newTraceableMsgSvc(t, c.dbName, &now)
			mid := c.msgID
			if mid == "" {
				mid = uuid7(now, "t1")
			}

			res, err := svc.Send(MessageSendParams{
				Identity: backendID(1, "game-1"), MessageID: mid, MsgType: "chat",
				TargetKind: model.MsgTargetKindServer, TargetServerID: "game-2",
				Payload: `{"text":"接入验收测试"}`, PayloadRaw: true, SentAtMs: now,
			})
			if err != nil || res.Status != model.MsgStatusAccepted {
				t.Fatalf("发送应 accepted，实际 %+v err=%v", res, err)
			}

			// 目标 agent 未启用 messaging：全程不 poll（无任何取走动作），等待超过 TTL 后推进清理轮。
			now += 31_000
			relay.Sweep()

			row, err := repo.FindByMessageID(mid)
			if err != nil {
				t.Fatalf("按 message_id 直查失败: %v", err)
			}
			if row == nil {
				t.Fatalf("未投递消息必须能在 msg_trace 里按 message_id 查到最终去向，实际查不到（发出去就消失）")
			}
			if row.Status != model.MsgStatusExpired || row.FailReason != model.MsgFailTTLExpired {
				t.Fatalf("未取走消息终态应为 expired(ttl_expired)，实际 %s/%s", row.Status, row.FailReason)
			}
			if row.DispatchedAt != nil {
				t.Fatalf("目标未 poll，不应有 dispatched_at，实际 %v", *row.DispatchedAt)
			}
			if !row.PayloadStored {
				t.Fatalf("payload 应随元数据落库（payload_stored=true），实际 %+v", row)
			}
			if pl, err := repo.FindPayload(mid); err != nil || pl == nil || pl.Payload != `{"text":"接入验收测试"}` {
				t.Fatalf("payload 应与元数据同日落表可查，实际 pl=%v err=%v", pl, err)
			}

			q := NewMessageQueryService(repo)
			page, err := q.List(ListMessagesParams{MessageID: mid})
			if err != nil || len(page.Items) != 1 || page.Items[0].Status != model.MsgStatusExpired {
				t.Fatalf("按 message_id 直查（/admin/v2/messages?messageId=）应看到 expired，实际 %+v err=%v", page.Items, err)
			}
			list, err := q.List(ListMessagesParams{
				ServerID: "game-2", Status: model.MsgStatusExpired,
				FromMs: now - time.Hour.Milliseconds(), ToMs: now + time.Hour.Milliseconds(),
			})
			if err != nil || len(list.Items) != 1 || list.Items[0].MessageID != mid {
				t.Fatalf("按 serverId + 时间窗列表查询应看到该过期消息，实际 %+v err=%v", list.Items, err)
			}
		})
	}
}

// TestMessageDeliveredPathUnchanged 校验目标正常 poll 时既有 delivered 路径语义不回归：
// 取走 → 回执成功 → 落 delivered 终态（含 dispatched_at / delivered_at / duration_ms）且按 ID 可查。
func TestMessageDeliveredPathUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC).UnixMilli()
	svc, relay, repo := newTraceableMsgSvc(t, "msg_delivered_unchanged", &now)
	mid := uuid7(now, "d1")

	if res, err := svc.Send(MessageSendParams{
		Identity: backendID(1, "game-1"), MessageID: mid, MsgType: "chat",
		TargetKind: model.MsgTargetKindServer, TargetServerID: "game-2",
		Payload: "hello", SentAtMs: now,
	}); err != nil || res.Status != model.MsgStatusAccepted {
		t.Fatalf("发送应 accepted，实际 %+v err=%v", res, err)
	}
	got := relay.Poll(context.Background(), 1, "game-2", 0, 10)
	if len(got) != 1 || got[0].MessageID != mid {
		t.Fatalf("正常 poll 应取到消息，实际 %+v", got)
	}
	now += 20
	if applied, ignored := relay.Ack(1, "game-2", []AckResult{{MessageID: mid, Status: model.MsgStatusDelivered, DeliveredAtMs: now}}); applied != 1 || ignored != 0 {
		t.Fatalf("ack 应 applied=1 ignored=0，实际 %d/%d", applied, ignored)
	}

	row, err := repo.FindByMessageID(mid)
	if err != nil || row == nil {
		t.Fatalf("delivered 行应可查，实际 %v err=%v", row, err)
	}
	if row.Status != model.MsgStatusDelivered || row.FailReason != "" {
		t.Fatalf("正常路径终态应为 delivered 且无原因，实际 %s/%s", row.Status, row.FailReason)
	}
	if row.DispatchedAt == nil || row.DeliveredAt == nil || row.DurationMs == nil || *row.DurationMs != 20 {
		t.Fatalf("delivered 行应带 dispatched_at / delivered_at / duration_ms=20，实际 %+v", row)
	}
}

// TestMessageSendSentAtFallbackTrusted 校验 sentAt 缺省回退只采用可信的 UUIDv7 内嵌时间：
// 随机 ID 的前 48 位不是时间，不得被当成发出时刻写进 hops（否则链路首段会显示 数千年后这类假时刻）。
func TestMessageSendSentAtFallbackTrusted(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC).UnixMilli()
	svc, _, _ := newTraceableMsgSvc(t, "msg_sentat_fallback", &now)

	// 规范 UUIDv7：无 sentAt 时回退 ID 内嵌时间（= 生成该 ID 的时刻）。
	v7 := uuid7(now-5_000, "s1")
	if _, err := svc.Send(MessageSendParams{
		Identity: backendID(1, "game-1"), MessageID: v7, MsgType: "chat",
		TargetKind: model.MsgTargetKindServer, TargetServerID: "game-2",
	}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	env := svc.relay.all[liveKey{messageID: v7, target: serverKey{namespaceID: 1, serverID: "game-2"}}]
	if env == nil || env.meta.SentAtMs != now-5_000 {
		t.Fatalf("规范 UUIDv7 应回退内嵌时间 %d，实际 %+v", now-5_000, env)
	}

	// 随机 ID：无可信内嵌时间，sentAt 留 0（sent 链路事件时间留空），不得把随机位当时间。
	v4 := v4RandomMsgID
	if _, err := svc.Send(MessageSendParams{
		Identity: backendID(1, "game-1"), MessageID: v4, MsgType: "chat",
		TargetKind: model.MsgTargetKindServer, TargetServerID: "game-2",
	}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	env = svc.relay.all[liveKey{messageID: v4, target: serverKey{namespaceID: 1, serverID: "game-2"}}]
	if env == nil || env.meta.SentAtMs != 0 {
		t.Fatalf("随机 ID 的 sentAt 应留 0（宁缺勿假），实际 %+v", env)
	}
}
