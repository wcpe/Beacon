package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
)

// RouteKindMessageTrace 是消息终态记录在异步日表写入通道中的路由键（FR-149/150，见 spec §3.4/§4.2）：
// 消息生命周期在控制面内存中转维护，只在终态一次性把 msg_trace + msg_payload 合并记录经此路由落库。
const RouteKindMessageTrace = "message_trace"

// 消息中转可靠性档位默认值（spec §4.2/§8 待定 5：走运维设置热更，本阶段为常量工程默认）。
const (
	// defaultMsgTTL accepted 停留超此值无人取走 → expired。
	defaultMsgTTL = 30 * time.Second
	// defaultMsgAckTimeout dispatched 后超此值无回执 → 重投或 ack_timeout。
	defaultMsgAckTimeout = 10 * time.Second
	// defaultMsgMaxDispatch 单条消息最多下发次数（首次 + 2 次重投）。
	defaultMsgMaxDispatch = 3
	// defaultMsgQueueCap 每服内存投递队列容量，溢出淘汰最旧。
	defaultMsgQueueCap = 1000
	// defaultMsgSweepInterval 后台清理轮周期（推进 TTL / 重投 / 超时）。
	defaultMsgSweepInterval = 2 * time.Second
)

// messageRecordEnqueuer 是中转对异步写入池的窄依赖：非阻塞投递一批终态消息记录，队列满返回 false。
type messageRecordEnqueuer interface {
	Enqueue(records []model.MessageRecord) bool
}

// MessageRecordEnqueuer 把泛化异步日表写入通道绑定到 message_trace 路由（装配用）。
type MessageRecordEnqueuer struct {
	Writer *AsyncDailyWriter
}

// Enqueue 非阻塞投递一批终态消息记录；队列满返回 false。
func (e MessageRecordEnqueuer) Enqueue(records []model.MessageRecord) bool {
	return EnqueueRows(e.Writer, RouteKindMessageTrace, records)
}

// hopEvent 是 msg_trace.hops 链路事件（json 数组元素，spec §3.3）。
type hopEvent struct {
	Seq    int    `json:"seq"`
	Node   string `json:"node"`
	Event  string `json:"event"`
	At     string `json:"at"`
	CostMs int64  `json:"costMs,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// hops 链路事件枚举（spec §3.3）。
const (
	hopSent       = "sent"       // 源 agent 发出
	hopReceived   = "received"   // 控制面收到
	hopResolved   = "resolved"   // 按玩家寻址解析出目标服
	hopDispatched = "dispatched" // 目标取走
	hopDelivered  = "delivered"  // 目标业务 handler 处理完
	hopFailed     = "failed"     // 任一环失败
)

// nodeBeacon 是控制面在 hops 中的节点名。
const nodeBeacon = "beacon"

// IncomingMessage 是一条经校验待中转的消息元数据（由消息服务解析 / 鉴权后构造，spec §4.2）。
type IncomingMessage struct {
	MessageID           string
	NamespaceID         uint // 来源 namespace
	SourceServerID      string
	MsgType             string
	TargetKind          string
	TargetServerID      string
	TargetPlayer        string
	TargetZone          string // 广播 zone 级定向的 zone 名（target_kind=broadcast 时可选，FR-180）
	ResolvedNamespaceID uint   // 投递目标所在 namespace（跨域玩家寻址时 ≠ 来源）
	ResolvedServerID    string // 投递目标服
	TargetNamespaceID   *uint  // 跨域目标 namespace（同域为 nil）
	CrossNamespace      bool
	Resolved            bool // 是否按玩家寻址解析（决定是否记 resolved hop）
	CorrelationID       string
	Payload             string
	PayloadSize         int
	SentAtMs            int64 // 源 agent 发出时刻
	CreatedAtMs         int64 // 控制面接收时刻
}

// serverKey 是每服投递队列的定位键（serverId 仅 namespace 内唯一，须带 ns 区分）。
type serverKey struct {
	namespaceID uint
	serverID    string
}

// liveKey 是活消息索引键：广播把同一 messageID fan-out 到多个目标，须按（消息, 目标）二元定位。
type liveKey struct {
	messageID string
	target    serverKey
}

// liveMessage 是中转中一条消息投递目标的内存活状态（accepted / dispatched）；终态即出内存。
// 定向消息即消息本身（终态直接落单行）；广播的每个目标各一条，终态计入共享聚合（agg）。
type liveMessage struct {
	meta           IncomingMessage
	targetKey      serverKey
	status         string // accepted / dispatched
	acceptedAtMs   int64  // 首次进入队列时刻（TTL 基准）
	lastEnqueuedMs int64  // 最近一次进入 accepted 时刻（重投基准）
	dispatchedAtMs int64
	dispatchCount  int
	hops           []hopEvent
	agg            *broadcastAgg // 广播目标指向共享聚合；定向为 nil
}

// key 返回本目标在活消息索引中的键。
func (m *liveMessage) key() liveKey {
	return liveKey{messageID: m.meta.MessageID, target: m.targetKey}
}

// broadcastAgg 是一条广播在中转中的聚合状态（FR-180 / ADR-0065）：fan-out 后各目标独立走
// dispatched / ack / TTL / 重投状态机，终态逐一计入本聚合；全部目标终态后一次性产出单行聚合记录
// （一条广播只落一行 msg_trace，防 ×N 写放大）。仅在持有 MessageRelay.mu 时读写，不另设锁。
type broadcastAgg struct {
	meta      IncomingMessage // 广播元数据与 payload（只存一份）
	fanout    int             // fan-out 目标总数
	remaining int             // 尚未终态的目标数，减到 0 即收口落库
	delivered int
	failed    int
	expired   int
	reasons   map[string]int // 目标失败 / 过期原因计数（聚合行 fail_reason 取出现最多者）
	// hops 概要链路：sent / received / dispatched（首个目标取走）/ delivered|failed（收口），
	// 不逐目标展开（防 TEXT 膨胀，ADR-0065 决策 3）。
	hops              []hopEvent
	firstDispatchedMs int64 // 首个目标被取走时刻（聚合行 dispatched_at）
	lastDeliveredMs   int64 // 最后一次 delivered 回执时刻（聚合行 delivered_at / duration 基准）
}

// newBroadcastAgg 构造广播聚合状态（remaining=fanout，前置链路 sent/received 与定向同构）。
func newBroadcastAgg(m IncomingMessage, fanout int) *broadcastAgg {
	return &broadcastAgg{
		meta: m, fanout: fanout, remaining: fanout,
		reasons: make(map[string]int),
		hops:    seedHops(m),
	}
}

// serverQueue 是单服的待取（accepted）消息队列（FIFO，最旧在前）。
type serverQueue struct {
	pending []*liveMessage
}

// MessageRelay 是跨服消息控制面中转（FR-149，见 spec §4.2、ADR-0063）：
// 每服内存投递队列 + accepted→dispatched→delivered/failed/expired 状态机 + TTL / 重投后台清理。
//
// 独立 Mutex 保护队列与索引，锁内纯内存操作、绝不碰 DB；终态记录经异步写入通道落库（锁外投递）。
// 长轮询唤醒复用 longpoll.Hub（按 namespaceID 字符串 + serverId 索引 waiter）。
type MessageRelay struct {
	mu     sync.Mutex
	queues map[serverKey]*serverQueue
	all    map[liveKey]*liveMessage // (messageID, 目标) -> 活消息（accepted / dispatched）

	hub     *longpoll.Hub
	enqueue messageRecordEnqueuer
	now     func() time.Time

	ttl         time.Duration
	ackTimeout  time.Duration
	maxDispatch int
	queueCap    int
}

// NewMessageRelay 构造中转（工程默认档位）；enqueue 为终态记录异步入库通道。
func NewMessageRelay(enqueue messageRecordEnqueuer) *MessageRelay {
	return &MessageRelay{
		queues:      make(map[serverKey]*serverQueue),
		all:         make(map[liveKey]*liveMessage),
		hub:         longpoll.NewHub(),
		enqueue:     enqueue,
		now:         func() time.Time { return time.Now().UTC() },
		ttl:         defaultMsgTTL,
		ackTimeout:  defaultMsgAckTimeout,
		maxDispatch: defaultMsgMaxDispatch,
		queueCap:    defaultMsgQueueCap,
	}
}

// Accept 受理一条定向消息入目标服队列（状态 accepted）；队列溢出即淘汰最旧为 expired(queue_overflow)。
// 记 sent / received /（按玩家寻址时）resolved 链路事件。返回后唤醒目标服 poll waiter。
func (r *MessageRelay) Accept(m IncomingMessage) {
	msg := &liveMessage{
		meta:           m,
		targetKey:      serverKey{namespaceID: m.ResolvedNamespaceID, serverID: m.ResolvedServerID},
		status:         model.MsgStatusAccepted,
		acceptedAtMs:   m.CreatedAtMs,
		lastEnqueuedMs: m.CreatedAtMs,
		hops:           seedHops(m),
	}
	r.mu.Lock()
	evicted := r.enqueueLocked(msg)
	r.all[msg.key()] = msg
	r.mu.Unlock()

	r.persist(evicted)
	r.hub.Notify(hubNS(msg.targetKey.namespaceID), []string{msg.targetKey.serverID})
}

// AcceptBroadcast 受理一条广播消息（FR-180 / ADR-0065）：向已解析的在线目标集合逐服入既有投递
// 队列（每目标独立走 dispatched / ack / TTL / 重投 / 溢出规则），一条广播只维护一份聚合状态、
// 全部目标终态后一次性落单行聚合记录——整体收口由各目标的有限时限（TTL / 重投上限）自然保证，
// 无需独立的整体计时器。targets 为空（无在线目标）时直接落聚合终态行 failed(no_online_target)
// 并返回 false（上层据此回 200 status=failed）。
func (r *MessageRelay) AcceptBroadcast(m IncomingMessage, targetServerIDs []string) bool {
	if len(targetServerIDs) == 0 {
		// 空 fan-out：聚合状态未共享，无须持锁即可收口。
		r.persist([]model.MessageRecord{r.finishBroadcast(newBroadcastAgg(m, 0))})
		return false
	}
	agg := newBroadcastAgg(m, len(targetServerIDs))
	var evicted []model.MessageRecord
	r.mu.Lock()
	for _, sid := range targetServerIDs {
		msg := &liveMessage{
			meta:           m,
			targetKey:      serverKey{namespaceID: m.NamespaceID, serverID: sid},
			status:         model.MsgStatusAccepted,
			acceptedAtMs:   m.CreatedAtMs,
			lastEnqueuedMs: m.CreatedAtMs,
			agg:            agg,
		}
		evicted = append(evicted, r.enqueueLocked(msg)...)
		r.all[msg.key()] = msg
	}
	r.mu.Unlock()

	r.persist(evicted)
	r.hub.Notify(hubNS(m.NamespaceID), targetServerIDs)
	return true
}

// enqueueLocked 把消息目标放入其投递队列（锁内）：容量满先淘汰最旧为 expired(queue_overflow)。
// 返回本次淘汰产生的待落库终态记录（0 或 1 条；被淘汰的广播目标计入其聚合，收口时才有记录）。
func (r *MessageRelay) enqueueLocked(msg *liveMessage) []model.MessageRecord {
	q := r.queues[msg.targetKey]
	if q == nil {
		q = &serverQueue{}
		r.queues[msg.targetKey] = q
	}
	var out []model.MessageRecord
	if len(q.pending) >= r.queueCap {
		oldest := q.pending[0]
		q.pending = q.pending[1:]
		delete(r.all, oldest.key())
		if rec, ok := r.settleLocked(oldest, model.MsgStatusExpired, model.MsgFailQueueOverflow, 0); ok {
			out = append(out, rec)
		}
	}
	q.pending = append(q.pending, msg)
	return out
}

// RecordTerminal 直接把一条未入队的消息按终态落库（发送期即判定失败 / 过期：玩家不在线、跨域无信任等）。
// 记 sent / received /（解析时）resolved + failed/expired 事件，不进投递队列。
func (r *MessageRelay) RecordTerminal(m IncomingMessage, status, failReason string) {
	msg := &liveMessage{meta: m, status: model.MsgStatusAccepted, hops: seedHops(m)}
	rec := r.terminate(msg, status, failReason)
	r.persist([]model.MessageRecord{rec})
}

// DispatchedMessage 是长轮询下发给目标 agent 的一条消息（对齐 spec §5.1 poll 响应，camelCase 在 handler 层）。
type DispatchedMessage struct {
	MessageID      string
	MsgType        string
	SourceServerID string
	CorrelationID  string
	Payload        string
	CreatedAtMs    int64
	Broadcast      bool // 广播投递标记（additive 键，FR-180）：agent 据此路由 topic 订阅分发
}

// Poll 长轮询取本服待投消息：先即时排空，无则登记 waiter 再排空（防丢唤醒），仍无则等待至超时。
// 取走即置 dispatched、记 dispatched 事件、携 payload 返回。
func (r *MessageRelay) Poll(ctx context.Context, namespaceID uint, serverID string, waitSec, limit int) []DispatchedMessage {
	key := serverKey{namespaceID: namespaceID, serverID: serverID}
	if msgs := r.drain(key, limit); len(msgs) > 0 {
		return msgs
	}
	w := r.hub.Register(hubNS(namespaceID), serverID)
	defer r.hub.Deregister(w)
	// 登记后再排空一次：消除「登记前入队」的丢唤醒窗口（与配置长轮询同口径）。
	if msgs := r.drain(key, limit); len(msgs) > 0 {
		return msgs
	}
	if waitSec <= 0 {
		return nil
	}
	if w.Wait(ctx, time.Duration(waitSec)*time.Second) {
		return r.drain(key, limit)
	}
	return nil
}

// drain 从某服队列取至多 limit 条 accepted，置 dispatched 并返回下发形态（锁内纯内存）。
func (r *MessageRelay) drain(key serverKey, limit int) []DispatchedMessage {
	if limit <= 0 {
		return nil
	}
	nowMs := r.now().UnixMilli()
	out := make([]DispatchedMessage, 0, limit)
	r.mu.Lock()
	q := r.queues[key]
	if q != nil {
		n := min(limit, len(q.pending))
		for i := 0; i < n; i++ {
			msg := q.pending[i]
			msg.status = model.MsgStatusDispatched
			msg.dispatchedAtMs = nowMs
			msg.dispatchCount++
			if msg.agg != nil {
				// 广播链路只记 fan-out 概要：首个目标取走时记一次 dispatched，不逐目标展开（防 TEXT 膨胀）。
				if msg.agg.firstDispatchedMs == 0 {
					msg.agg.firstDispatchedMs = nowMs
					msg.agg.hops = appendHopEvent(msg.agg.hops, nodeBeacon, hopDispatched, nowMs, "")
				}
			} else {
				msg.appendHop(nodeBeacon, hopDispatched, nowMs, "")
			}
			out = append(out, DispatchedMessage{
				MessageID: msg.meta.MessageID, MsgType: msg.meta.MsgType,
				SourceServerID: msg.meta.SourceServerID, CorrelationID: msg.meta.CorrelationID,
				Payload: msg.meta.Payload, CreatedAtMs: msg.meta.CreatedAtMs,
				Broadcast: msg.agg != nil,
			})
		}
		q.pending = q.pending[n:]
	}
	r.mu.Unlock()
	return out
}

// AckResult 是一条回执（spec §5.1 ack 请求元素）。
type AckResult struct {
	MessageID     string
	Status        string // delivered / failed
	Reason        string
	DeliveredAtMs int64
	HandlerCostMs int
}

// Ack 批量回执：delivered → 落 delivered 终态、failed → 落 failed 终态；未知 / 非本服 / 非 dispatched 计入 ignored。
// 广播目标按（messageId, 本服）定位其独立投递态，终态计入聚合、收口时才产出聚合行。
func (r *MessageRelay) Ack(namespaceID uint, serverID string, results []AckResult) (applied, ignored int) {
	key := serverKey{namespaceID: namespaceID, serverID: serverID}
	var records []model.MessageRecord
	r.mu.Lock()
	for _, res := range results {
		k := liveKey{messageID: res.MessageID, target: key}
		msg, ok := r.all[k]
		if !ok || msg.status != model.MsgStatusDispatched {
			ignored++
			continue
		}
		delete(r.all, k)
		var rec model.MessageRecord
		var settled bool
		if res.Status == model.MsgStatusDelivered {
			rec, settled = r.settleLocked(msg, model.MsgStatusDelivered, "", res.DeliveredAtMs)
		} else {
			reason := res.Reason
			if reason == "" {
				reason = model.MsgFailHandlerError
			}
			rec, settled = r.settleLocked(msg, model.MsgStatusFailed, reason, 0)
		}
		if settled {
			records = append(records, rec)
		}
		applied++
	}
	r.mu.Unlock()
	r.persist(records)
	return applied, ignored
}

// Sweep 推进一轮状态机（后台清理，spec §4.2）：accepted 超 TTL → expired；dispatched 超时 → 重投或 ack_timeout；
// 重投后未被再取走亦超时 → ack_timeout。终态记录锁外落库、重投服锁外唤醒。
func (r *MessageRelay) Sweep() {
	nowMs := r.now().UnixMilli()
	var records []model.MessageRecord
	requeued := make(map[serverKey]struct{})
	appendSettled := func(rec model.MessageRecord, ok bool) {
		if ok {
			records = append(records, rec)
		}
	}
	r.mu.Lock()
	for k, msg := range r.all {
		switch msg.status {
		case model.MsgStatusAccepted:
			if msg.dispatchCount == 0 {
				if nowMs-msg.acceptedAtMs > r.ttl.Milliseconds() {
					r.removePending(msg)
					delete(r.all, k)
					appendSettled(r.settleLocked(msg, model.MsgStatusExpired, model.MsgFailTTLExpired, 0))
				}
			} else if nowMs-msg.lastEnqueuedMs > r.ackTimeout.Milliseconds() {
				// 已重投但无人再取走：目标已离开，判 ack_timeout。
				r.removePending(msg)
				delete(r.all, k)
				appendSettled(r.settleLocked(msg, model.MsgStatusFailed, model.MsgFailAckTimeout, 0))
			}
		case model.MsgStatusDispatched:
			if nowMs-msg.dispatchedAtMs > r.ackTimeout.Milliseconds() {
				if msg.dispatchCount < r.maxDispatch {
					// 重投：回到目标服队列头部（尽快再取），状态复位 accepted。
					msg.status = model.MsgStatusAccepted
					msg.lastEnqueuedMs = nowMs
					r.requeueFront(msg)
					requeued[msg.targetKey] = struct{}{}
				} else {
					delete(r.all, k)
					appendSettled(r.settleLocked(msg, model.MsgStatusFailed, model.MsgFailAckTimeout, 0))
				}
			}
		}
	}
	r.mu.Unlock()
	r.persist(records)
	for key := range requeued {
		r.hub.Notify(hubNS(key.namespaceID), []string{key.serverID})
	}
}

// Run 后台清理轮：每 defaultMsgSweepInterval 推进一次，随 ctx 取消退出。
func (r *MessageRelay) Run(ctx context.Context) {
	slog.Info("消息中转清理轮已启动", "TTL", r.ttl.String(), "重投超时", r.ackTimeout.String(), "最大下发次数", r.maxDispatch)
	ticker := time.NewTicker(defaultMsgSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("消息中转清理轮已停止")
			return
		case <-ticker.C:
			r.Sweep()
		}
	}
}

// removePending 从目标服队列摘除某 accepted 消息（TTL / ack_timeout 终态前调用）。
func (r *MessageRelay) removePending(msg *liveMessage) {
	q := r.queues[msg.targetKey]
	if q == nil {
		return
	}
	for i, m := range q.pending {
		if m == msg {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			return
		}
	}
}

// requeueFront 把重投消息放回目标服队列头部（尽快再取走）。
func (r *MessageRelay) requeueFront(msg *liveMessage) {
	q := r.queues[msg.targetKey]
	if q == nil {
		q = &serverQueue{}
		r.queues[msg.targetKey] = q
	}
	q.pending = append([]*liveMessage{msg}, q.pending...)
}

// settleLocked 把一个投递目标置终态并返回需落库的记录（锁内调用；空 fan-out 收口除外不经此）：
// 定向消息 → 本消息的单行终态记录（恒返回 ok=true）；广播目标 → 计入共享聚合，
// 最后一个目标终态时返回聚合行（其余返回 ok=false）。deliveredMs 仅 status=delivered 时有意义（≤0 按当前时钟）。
func (r *MessageRelay) settleLocked(msg *liveMessage, status, failReason string, deliveredMs int64) (model.MessageRecord, bool) {
	if msg.agg != nil {
		msg.status = status
		return r.settleBroadcastLocked(msg.agg, status, failReason, deliveredMs)
	}
	if status == model.MsgStatusDelivered {
		return r.terminateDelivered(msg, deliveredMs), true
	}
	return r.terminate(msg, status, failReason), true
}

// settleBroadcastLocked 把广播的一个目标终态计入聚合；全部目标到齐（remaining=0）即收口产出聚合行。
func (r *MessageRelay) settleBroadcastLocked(agg *broadcastAgg, status, failReason string, deliveredMs int64) (model.MessageRecord, bool) {
	switch status {
	case model.MsgStatusDelivered:
		agg.delivered++
		if deliveredMs <= 0 {
			deliveredMs = r.now().UnixMilli()
		}
		if deliveredMs > agg.lastDeliveredMs {
			agg.lastDeliveredMs = deliveredMs
		}
	case model.MsgStatusExpired:
		agg.expired++
		agg.reasons[failReason]++
	default:
		agg.failed++
		agg.reasons[failReason]++
	}
	agg.remaining--
	if agg.remaining > 0 {
		return model.MessageRecord{}, false
	}
	return r.finishBroadcast(agg), true
}

// finishBroadcast 广播收口：按聚合口径定整行状态、补链路终事件、构造单行聚合记录。
// 共享中的聚合须在锁内调用；空 fan-out 的本地聚合（未与任何目标共享）可锁外调用。
//
// 整行 status 口径（ADR-0065 决策 3 的落地约定，测试逐一锚定）：
//   - 任一目标 delivered → delivered（部分失败仍算送达，失败面经 failed_count / expired_count 可见）；
//   - fan-out 集合为空 → failed(no_online_target)；
//   - 全军覆没（无一 delivered）→ expired 数多于 failed 取 expired、否则取 failed（平手偏 failed），
//     fail_reason 取各目标原因中出现最多者（同数取字典序小者）。
func (r *MessageRelay) finishBroadcast(agg *broadcastAgg) model.MessageRecord {
	nowMs := r.now().UnixMilli()
	var status, failReason string
	switch {
	case agg.delivered > 0:
		status = model.MsgStatusDelivered
		agg.hops = appendHopEvent(agg.hops, nodeBeacon, hopDelivered, agg.lastDeliveredMs, "")
	case agg.fanout == 0:
		status, failReason = model.MsgStatusFailed, model.MsgFailNoOnlineTarget
		agg.hops = appendHopEvent(agg.hops, nodeBeacon, hopFailed, nowMs, failReason)
	default:
		status = model.MsgStatusFailed
		if agg.expired > agg.failed {
			status = model.MsgStatusExpired
		}
		failReason = topReasonOf(agg.reasons)
		agg.hops = appendHopEvent(agg.hops, nodeBeacon, hopFailed, nowMs, failReason)
	}
	return buildBroadcastRecord(agg, status, failReason)
}

// topReasonOf 取原因计数中出现最多者（同数取字典序小者）；空计数返回空串。
func topReasonOf(counts map[string]int) string {
	var top string
	var topCount int
	for reason, count := range counts {
		if count > topCount || (count == topCount && (top == "" || reason < top)) {
			top, topCount = reason, count
		}
	}
	return top
}

// terminate 把定向消息置终态（failed/expired）、补 failed 事件、构造落库记录（不改索引，由调用方删）。
func (r *MessageRelay) terminate(msg *liveMessage, status, failReason string) model.MessageRecord {
	nowMs := r.now().UnixMilli()
	msg.status = status
	if status != model.MsgStatusDelivered {
		node := nodeBeacon
		if failReason == model.MsgFailAckTimeout || failReason == model.MsgFailHandlerError {
			node = msg.meta.ResolvedServerID // 回执 / 送达环失败归在目标节点
			if node == "" {
				node = msg.meta.TargetServerID
			}
		}
		msg.appendHopReason(node, hopFailed, nowMs, failReason)
	}
	return buildRecord(msg, status, failReason, nil, nil)
}

// terminateDelivered 把 dispatched 定向消息按回执置 delivered、补 delivered 事件、结算全链路耗时。
func (r *MessageRelay) terminateDelivered(msg *liveMessage, deliveredMs int64) model.MessageRecord {
	if deliveredMs <= 0 {
		deliveredMs = r.now().UnixMilli()
	}
	node := msg.meta.ResolvedServerID
	if node == "" {
		node = msg.meta.TargetServerID
	}
	msg.appendHop(node, hopDelivered, deliveredMs, "")
	duration := deliveredMs - msg.meta.CreatedAtMs
	deliveredAt := time.UnixMilli(deliveredMs).UTC()
	return buildRecord(msg, model.MsgStatusDelivered, "", &deliveredAt, &duration)
}

// persist 把终态记录经异步写入通道落库；队列满 / 未装配记 WARN，不阻塞中转（错误不静默，ADR-0057）。
func (r *MessageRelay) persist(records []model.MessageRecord) {
	if len(records) == 0 || r.enqueue == nil {
		return
	}
	if !r.enqueue.Enqueue(records) {
		ids := make([]string, 0, len(records))
		for _, rec := range records {
			ids = append(ids, rec.Trace.MessageID)
		}
		slog.Warn("消息终态记录写入队列已满，本批被丢弃", "条数", len(records), "messageIds", ids)
	}
}

// seedHops 构造消息前置链路事件：sent（源发出）→ received（控制面收到）→（按玩家寻址时）resolved。
func seedHops(m IncomingMessage) []hopEvent {
	hops := []hopEvent{
		{Seq: 0, Node: m.SourceServerID, Event: hopSent, At: isoMs(m.SentAtMs)},
		{Seq: 1, Node: nodeBeacon, Event: hopReceived, At: isoMs(m.CreatedAtMs), CostMs: costBetween(m.SentAtMs, m.CreatedAtMs)},
	}
	if m.Resolved {
		hops = append(hops, hopEvent{Seq: 2, Node: nodeBeacon, Event: hopResolved, At: isoMs(m.CreatedAtMs)})
	}
	return hops
}

// appendHop 追加一条链路事件，costMs 为与上一事件的间隔（毫秒）。
func (m *liveMessage) appendHop(node, event string, atMs int64, reason string) {
	m.appendHopReason(node, event, atMs, reason)
}

// appendHopReason 追加带原因的链路事件。
func (m *liveMessage) appendHopReason(node, event string, atMs int64, reason string) {
	m.hops = appendHopEvent(m.hops, node, event, atMs, reason)
}

// appendHopEvent 在链路事件序列上追加一条事件（seq 递增，costMs 为与上一事件的间隔毫秒）。
func appendHopEvent(hops []hopEvent, node, event string, atMs int64, reason string) []hopEvent {
	var prevMs int64
	if len(hops) > 0 {
		prevMs = msFromISO(hops[len(hops)-1].At)
	}
	return append(hops, hopEvent{
		Seq: len(hops), Node: node, Event: event, At: isoMs(atMs),
		CostMs: costBetween(prevMs, atMs), Reason: reason,
	})
}

// buildRecord 把定向活消息映射为终态落库记录（元数据 + 可选 payload）。
func buildRecord(msg *liveMessage, status, failReason string, deliveredAt *time.Time, duration *int64) model.MessageRecord {
	m := msg.meta
	createdAt := time.UnixMilli(m.CreatedAtMs).UTC()
	trace := model.MsgTrace{
		MessageID: m.MessageID, NamespaceID: m.NamespaceID, SourceServerID: m.SourceServerID,
		MsgType: m.MsgType, TargetKind: m.TargetKind, TargetServerID: m.TargetServerID,
		TargetPlayer: m.TargetPlayer, ResolvedServerID: m.ResolvedServerID,
		TargetNamespaceID: m.TargetNamespaceID, CrossNamespace: m.CrossNamespace,
		CorrelationID: m.CorrelationID, Status: status, FailReason: failReason,
		CreatedAt: createdAt, DeliveredAt: deliveredAt, DurationMs: duration,
		HopCount: hopCount(msg.hops), Hops: marshalHops(msg.hops),
		PayloadSize: m.PayloadSize, PayloadStored: m.PayloadSize > 0,
	}
	if msg.dispatchedAtMs > 0 {
		dispatchedAt := time.UnixMilli(msg.dispatchedAtMs).UTC()
		trace.DispatchedAt = &dispatchedAt
	}
	rec := model.MessageRecord{Trace: trace}
	rec.Payload = payloadRowOf(m, createdAt)
	return rec
}

// buildBroadcastRecord 把广播聚合状态映射为单行聚合落库记录（FR-180）：
// 聚合计数列（fanout/delivered/failed/expired）恒非空；delivered_at / duration 以最后一次
// delivered 回执为基准（无送达为 NULL）；payload 只存一份（payload_stored 语义不变）。
func buildBroadcastRecord(agg *broadcastAgg, status, failReason string) model.MessageRecord {
	m := agg.meta
	createdAt := time.UnixMilli(m.CreatedAtMs).UTC()
	fanout, delivered, failed, expired := agg.fanout, agg.delivered, agg.failed, agg.expired
	trace := model.MsgTrace{
		MessageID: m.MessageID, NamespaceID: m.NamespaceID, SourceServerID: m.SourceServerID,
		MsgType: m.MsgType, TargetKind: m.TargetKind,
		FanoutTotal: &fanout, DeliveredCount: &delivered, FailedCount: &failed, ExpiredCount: &expired,
		CorrelationID: m.CorrelationID, Status: status, FailReason: failReason,
		CreatedAt: createdAt,
		HopCount:  hopCount(agg.hops), Hops: marshalHops(agg.hops),
		PayloadSize: m.PayloadSize, PayloadStored: m.PayloadSize > 0,
	}
	if m.TargetZone != "" {
		zone := m.TargetZone
		trace.TargetZone = &zone
	}
	if agg.firstDispatchedMs > 0 {
		dispatchedAt := time.UnixMilli(agg.firstDispatchedMs).UTC()
		trace.DispatchedAt = &dispatchedAt
	}
	if agg.delivered > 0 && agg.lastDeliveredMs > 0 {
		deliveredAt := time.UnixMilli(agg.lastDeliveredMs).UTC()
		duration := agg.lastDeliveredMs - m.CreatedAtMs
		trace.DeliveredAt = &deliveredAt
		trace.DurationMs = &duration
	}
	rec := model.MessageRecord{Trace: trace}
	rec.Payload = payloadRowOf(m, createdAt)
	return rec
}

// payloadRowOf 构造消息的 payload 行（payload 为空返回 nil，不落 msg_payload）。
func payloadRowOf(m IncomingMessage, createdAt time.Time) *model.MsgPayload {
	if m.PayloadSize <= 0 {
		return nil
	}
	sum := sha256.Sum256([]byte(m.Payload))
	return &model.MsgPayload{
		MessageID: m.MessageID, Payload: m.Payload,
		SHA256: hex.EncodeToString(sum[:]), Size: m.PayloadSize, CreatedAt: createdAt,
	}
}

// hopCount 统计承担转发职责的节点数：出现 received / resolved / dispatched 事件的去重节点（经控制面单跳恒 1）。
func hopCount(hops []hopEvent) int {
	nodes := make(map[string]struct{})
	for _, h := range hops {
		switch h.Event {
		case hopReceived, hopResolved, hopDispatched:
			nodes[h.Node] = struct{}{}
		}
	}
	return len(nodes)
}

// marshalHops 序列化链路事件为 json 数组文本（仅基础字段，不会失败；异常时回退空数组）。
func marshalHops(hops []hopEvent) string {
	b, err := json.Marshal(hops)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// costBetween 计算两毫秒时刻的正向间隔（负值归 0，防时钟回拨脏值）。
func costBetween(prevMs, curMs int64) int64 {
	if prevMs <= 0 || curMs < prevMs {
		return 0
	}
	return curMs - prevMs
}

// isoMs 把毫秒时刻格式化为 UTC ISO8601（毫秒精度）。
func isoMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// msFromISO 解析 isoMs 产出的时间文本回毫秒（供 costMs 计算）；失败返回 0。
func msFromISO(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse("2006-01-02T15:04:05.000Z", s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// hubNS 把 namespaceID 转为 longpoll.Hub 的 namespace 键（与 poll 登记侧一致）。
func hubNS(namespaceID uint) string {
	return strconv.FormatUint(uint64(namespaceID), 10)
}
