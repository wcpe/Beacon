package service

import (
	"context"
	"sort"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

const (
	// maxPayloadBytes 消息 payload 上限（默认 64KB，超限拒发不截断，spec §3.4）。
	maxPayloadBytes = 64 * 1024
	// maxMsgTypeLen / maxCorrelationLen / maxTargetZoneLen 字段长度上限（对齐 msg_trace 列宽，防坏行毒化落库）。
	maxMsgTypeLen     = 64
	maxCorrelationLen = 36
	maxTargetZoneLen  = 64
	// maxPollWaitSec / maxPollBatch 长轮询参数上限（spec §5.1）。
	maxPollWaitSec = 25
	maxPollBatch   = 50
	// maxAckResults 单次回执结果数上限（对齐单次下发上限）。
	maxAckResults = maxPollBatch
)

// namespaceTrustChecker 是消息服务对 namespace 信任的窄依赖（跨域放行须 capability=message，spec §4.2）。
type namespaceTrustChecker interface {
	NamespaceTrustAllowed(from, to uint, capability string) bool
}

// onlineViewSource 是广播寻址对「当前在线服集合」的窄依赖（FR-180 / ADR-0065）：
// 由 healthview.Store（健康计算轮维护的全实例内存视图，注册 / 健康内存事实）实现，
// List 返回深拷贝快照，读取无锁嵌套、不碰 DB。
type onlineViewSource interface {
	List() []healthview.View
}

// MessageService 是跨服消息 agent 面服务（FR-149/150/180，见 spec §4.2）：
// send 校验 + 寻址解析 + 跨域信任 + 交中转；poll / ack 转发中转。请求 goroutine 全程不碰 DB
// （寻址走内存名册 / 健康视图、信任走内存信任集、落库由中转在终态经异步通道完成）。
type MessageService struct {
	relay  *MessageRelay
	roster *roster.Store
	trust  namespaceTrustChecker
	views  onlineViewSource // 广播 fan-out 的在线服集合来源（健康视图内存真源）
	now    func() time.Time
}

// NewMessageService 构造消息服务。views 供广播寻址解析当前在线服集合（healthview.Store）。
func NewMessageService(relay *MessageRelay, rosterStore *roster.Store, trust namespaceTrustChecker, views onlineViewSource) *MessageService {
	return &MessageService{
		relay:  relay,
		roster: rosterStore,
		trust:  trust,
		views:  views,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// MessageSendParams 是一次发送的入参（身份为中间件注入的权威绑定）。
type MessageSendParams struct {
	Identity         agentauth.Identity
	MessageID        string
	MsgType          string
	TargetKind       string
	TargetServerID   string
	TargetPlayerUUID string
	TargetZone       string // 广播 zone 级定向（仅 targetKind=broadcast 时可选，FR-180）
	CorrelationID    string
	Payload          string
	PayloadRaw       bool
	SentAtMs         int64
}

// MessageSendResult 是一次发送的处理结果（对齐 spec §5.1 的 200 响应 messageId / status）。
type MessageSendResult struct {
	MessageID string
	Status    string
}

// Send 受理一条跨服消息：校验 → 寻址解析 → 跨域信任 → 交中转入队。
//
// 返回约定（spec §5.1）：正常入队 200 {status:accepted}；payload 超限 400 payload_too_large；
// 跨域无信任 403（并记 failed 落库）；目标玩家不在线 / 广播无在线目标 200 {status:failed}（记 failed 落库）。
func (s *MessageService) Send(p MessageSendParams) (MessageSendResult, error) {
	if err := validateSendParams(p); err != nil {
		return MessageSendResult{}, err
	}
	nowMs := s.now().UnixMilli()
	sentAtMs := p.SentAtMs
	if sentAtMs <= 0 {
		// sentAt 缺省回退 message_id 内嵌 UUIDv7 时间（≈ 发出时刻），保证 sent 链路事件时间完整（spec §3.3）。
		sentAtMs, _ = store.TimeMsFromUUIDv7(p.MessageID)
	}
	msg := IncomingMessage{
		MessageID: p.MessageID, NamespaceID: p.Identity.NamespaceID, SourceServerID: p.Identity.ServerID,
		MsgType: p.MsgType, TargetKind: p.TargetKind, TargetServerID: p.TargetServerID,
		TargetPlayer: p.TargetPlayerUUID, TargetZone: p.TargetZone, CorrelationID: p.CorrelationID,
		Payload: p.Payload, PayloadRaw: p.PayloadRaw, PayloadSize: len(p.Payload),
		SentAtMs: sentAtMs, CreatedAtMs: nowMs,
	}
	if p.TargetKind == model.MsgTargetKindBroadcast {
		return s.sendBroadcast(msg), nil
	}
	failReason, err := s.resolveTarget(&msg)
	if err != nil {
		return MessageSendResult{}, err
	}
	if failReason != "" {
		// 寻址即判失败（玩家不在线）：记 failed 落库，200 {status:failed}。
		s.relay.RecordTerminal(msg, model.MsgStatusFailed, failReason)
		return MessageSendResult{MessageID: p.MessageID, Status: model.MsgStatusFailed}, nil
	}
	s.relay.Accept(msg)
	return MessageSendResult{MessageID: p.MessageID, Status: model.MsgStatusAccepted}, nil
}

// sendBroadcast 受理广播（FR-180 / ADR-0065）：按当前在线服集合解析接收方后交中转 fan-out。
// 广播无 targetNamespace 概念、恒限定发送者 namespace（跨 namespace 广播不存在于 wire，结构上即被拒绝）；
// 在线集合为空 → 200 {status:failed}（中转已落 failed(no_online_target) 聚合行）。
func (s *MessageService) sendBroadcast(msg IncomingMessage) MessageSendResult {
	msg.ResolvedNamespaceID = msg.NamespaceID
	targets := s.resolveBroadcastTargets(msg.NamespaceID, msg.TargetZone)
	if !s.relay.AcceptBroadcast(msg, targets) {
		return MessageSendResult{MessageID: msg.MessageID, Status: model.MsgStatusFailed}
	}
	return MessageSendResult{MessageID: msg.MessageID, Status: model.MsgStatusAccepted}
}

// resolveBroadcastTargets 按健康视图内存事实解析广播接收方集合（含发送者自身，升序保证确定性）：
// 无 targetZone = 发送者 namespace 全部在线服（backend + proxy）；有 targetZone = 该 zone 当前在线服。
//
// 「在线」口径：视图存在且不含 lost / pending_confirm / disabled 原因——失联的实例取不走消息、
// 未确认 / 禁用的身份过不了 agent 鉴权，投了必然过期，故不纳入 fan-out；draining / unhealthy /
// unassigned 等只是调度劣势，实例仍在线可收广播（可丢语义下的最大可达集合）。
func (s *MessageService) resolveBroadcastTargets(namespaceID uint, targetZone string) []string {
	views := s.views.List()
	targets := make([]string, 0, len(views))
	for i := range views {
		v := &views[i]
		if v.NamespaceID != namespaceID {
			continue // 广播恒本 namespace：其它 namespace 的在线服绝不进集合
		}
		if targetZone != "" && v.ZoneName != targetZone {
			continue
		}
		if broadcastUnreachable(v.Reasons) {
			continue
		}
		targets = append(targets, v.ServerID)
	}
	sort.Strings(targets)
	return targets
}

// broadcastUnreachable 判定实例是否对广播不可达（lost / pending_confirm / disabled 之一即不可达）。
func broadcastUnreachable(reasons []string) bool {
	for _, reason := range reasons {
		switch reason {
		case healthview.ReasonLost, healthview.ReasonPendingConfirm, healthview.ReasonDisabled:
			return true
		}
	}
	return false
}

// resolveTarget 解析投递目标与跨域信任，回填 msg 的 Resolved* / CrossNamespace 字段。
// 返回 (failReason, err)：failReason 非空表示寻址即判失败（上层记 failed 落库、200 status=failed）；
// err 非空表示拒绝（跨域无信任 403，已在此记 failed 落库）。
//
//   - server 目标：同 namespace 内定向，resolved = targetServerId（离线由 TTL 过期，不即时判失败）。
//   - player 目标：查内存名册；不在线 → failReason=player_not_online；跨 namespace → 须 capability=message
//     信任，无信任返回 403。
func (s *MessageService) resolveTarget(msg *IncomingMessage) (string, error) {
	if msg.TargetKind == model.MsgTargetKindServer {
		msg.ResolvedNamespaceID = msg.NamespaceID
		msg.ResolvedServerID = msg.TargetServerID
		return "", nil
	}
	loc, ok := s.roster.Resolve(msg.TargetPlayer)
	if !ok {
		return model.MsgFailPlayerNotOnline, nil
	}
	msg.Resolved = true
	msg.ResolvedNamespaceID = loc.NamespaceID
	msg.ResolvedServerID = loc.ServerID
	if loc.NamespaceID != msg.NamespaceID {
		msg.CrossNamespace = true
		targetNs := loc.NamespaceID
		msg.TargetNamespaceID = &targetNs
		if !s.trust.NamespaceTrustAllowed(msg.NamespaceID, loc.NamespaceID, model.NamespaceTrustCapabilityMessage) {
			// 跨域无信任：记 failed 落库后 403 拒绝（spec §4.2/§7）。
			s.relay.RecordTerminal(*msg, model.MsgStatusFailed, model.MsgFailNamespaceNoTrust)
			return "", apperr.ErrMessageCrossNamespaceNoTrust
		}
	}
	return "", nil
}

// PollMessages 长轮询取本服待投消息（clamp waitSec/max 到上限）；空返回长度 0（handler 回 204）。
func (s *MessageService) PollMessages(ctx context.Context, id agentauth.Identity, waitSec, limit int) []DispatchedMessage {
	if waitSec > maxPollWaitSec {
		waitSec = maxPollWaitSec
	}
	if waitSec < 0 {
		waitSec = 0
	}
	if limit > maxPollBatch {
		limit = maxPollBatch
	}
	if limit <= 0 {
		limit = maxPollBatch
	}
	return s.relay.Poll(ctx, id.NamespaceID, id.ServerID, waitSec, limit)
}

// AckMessages 批量回执转发中转；超上限截断保护。返回 applied / ignored。
func (s *MessageService) AckMessages(id agentauth.Identity, results []AckResult) (applied, ignored int) {
	if len(results) > maxAckResults {
		results = results[:maxAckResults]
	}
	return s.relay.Ack(id.NamespaceID, id.ServerID, results)
}

// validateSendParams 校验发送请求字段：messageId 为 UUIDv7、msgType 非空且不超列宽、目标齐备、
// targetZone 仅随广播出现且不超列宽、payload 不超上限。
func validateSendParams(p MessageSendParams) error {
	if _, ok := store.TimeMsFromUUIDv7(p.MessageID); !ok {
		return apperr.ErrInvalidParam
	}
	if p.MsgType == "" || len(p.MsgType) > maxMsgTypeLen || len(p.CorrelationID) > maxCorrelationLen {
		return apperr.ErrInvalidParam
	}
	if !model.IsValidMsgTargetKind(p.TargetKind) {
		return apperr.ErrInvalidParam
	}
	if p.TargetKind == model.MsgTargetKindServer && p.TargetServerID == "" {
		return apperr.ErrInvalidParam
	}
	if p.TargetKind == model.MsgTargetKindPlayer && p.TargetPlayerUUID == "" {
		return apperr.ErrInvalidParam
	}
	if p.TargetKind != model.MsgTargetKindBroadcast && p.TargetZone != "" {
		return apperr.ErrInvalidParam // targetZone 是广播专属键，定向 / 按玩家寻址不接受
	}
	if len(p.TargetZone) > maxTargetZoneLen {
		return apperr.ErrInvalidParam
	}
	if len(p.Payload) > maxPayloadBytes {
		return apperr.ErrPayloadTooLarge
	}
	return nil
}
