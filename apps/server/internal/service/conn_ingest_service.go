package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// RouteKindConnEvent 是连接采集事件在异步日表写入通道中的路由键（FR-145，见 spec §4.1/§4.3）。
const RouteKindConnEvent = "conn_event"

const (
	// maxConnEventsPerBatch 单批上报事件数上限（spec §4.1/§5.1）。
	maxConnEventsPerBatch = 500
	// connClockSkewReject agent 上报时钟与控制面偏移超此值即丢弃该事件（防错误分片，spec §3.1）。
	connClockSkewReject = 24 * time.Hour
	// connClockSkewWarn 偏移超此值记 WARN（倒逼校时，spec §3.1）。
	connClockSkewWarn = 5 * time.Minute
	// connReconcileRetentionDays 孤儿对账 / 名册重建回溯的日表天数窗口。
	connReconcileRetentionDays = 8
)

// connEventEnqueuer 是采集服务对异步写入池的窄依赖：非阻塞投递一批连接事件，队列满返回 false。
type connEventEnqueuer interface {
	Enqueue(rows []model.ConnEvent) bool
}

// ConnEventEnqueuer 把泛化异步日表写入通道绑定到 conn_event 路由（装配用）。
type ConnEventEnqueuer struct {
	Writer *AsyncDailyWriter
}

// Enqueue 非阻塞投递一批连接事件；队列满返回 false（上层据此回 429 背压）。
func (e ConnEventEnqueuer) Enqueue(rows []model.ConnEvent) bool {
	return EnqueueRows(e.Writer, RouteKindConnEvent, rows)
}

// connOrphanReconciler 是采集服务对连接明细仓库的窄依赖：孤儿对账 + 名册重建读原语（后台 / 启动期用）。
type connOrphanReconciler interface {
	CloseOrphans(namespaceID uint, proxyServerID string, before time.Time, retentionDays int) (int64, error)
	ListOpenConnections(retentionDays int) ([]repository.OpenConn, error)
}

// ConnEventInput 是一条采集事件的服务层入参（handler 解码 + 时间归一为毫秒后交服务）。
type ConnEventInput struct {
	Kind               string
	ConnID             string
	PlayerUUID         string
	PlayerName         string
	ClientIP           string
	ProtocolVersion    int
	OpenedAtMs         int64
	ClosedAtMs         int64
	CloseKind          string
	CloseReason        string
	FirstBackend       string
	LastBackend        string
	BackendSwitchCount int
}

// ConnBatchParams 是一次连接批量上报的入参（身份为中间件注入的权威绑定）。
type ConnBatchParams struct {
	Identity     agentauth.Identity
	BootID       string
	DroppedCount int
	Events       []ConnEventInput
}

// ConnBatchResult 是一次上报的处理结果（对齐 spec §5.1 的 202 响应 accepted / duplicated）。
type ConnBatchResult struct {
	Accepted   int
	Duplicated int
}

// reconcileReq 是一次孤儿对账请求（proxy 检出新 bootId 触发，交后台 goroutine 执行 DB 补 close）。
type reconcileReq struct {
	namespaceID   uint
	proxyServerID string
	before        time.Time
}

// bootKey 是 boot 追踪的定位键。
type bootKey struct {
	namespaceID   uint
	proxyServerID string
}

// ConnIngestService 是控制面连接明细接收端（FR-145，见 spec §4.1）：
// 请求线程只鉴权 + 结构校验 + 非阻塞入队 + 更新内存名册（纯内存），DB IO（落库 / 对账）全在后台。
//
// 同时驱动「玩家 → 所在服」名册（open 登记 / close 摘除）与孤儿会话对账（proxy 新 bootId 首见触发）。
type ConnIngestService struct {
	enqueue    connEventEnqueuer
	roster     *roster.Store
	reconciler connOrphanReconciler
	now        func() time.Time

	reconcileCh chan reconcileReq

	bootMu sync.Mutex
	boots  map[bootKey]string
}

// NewConnIngestService 构造接收服务；reconciler 供后台孤儿对账与启动期名册重建。
func NewConnIngestService(enqueue connEventEnqueuer, rosterStore *roster.Store, reconciler connOrphanReconciler) *ConnIngestService {
	return &ConnIngestService{
		enqueue:     enqueue,
		roster:      rosterStore,
		reconciler:  reconciler,
		now:         func() time.Time { return time.Now().UTC() },
		reconcileCh: make(chan reconcileReq, 64),
		boots:       make(map[bootKey]string),
	}
}

// Ingest 处理一次连接批量上报：结构校验 → 逐事件时钟偏移护栏 → 批内去重 → 非阻塞入队 → 更名册 → boot 对账。
//
// proxy 专用（非 proxy 身份拒 400）；事件数超上限 / 结构非法拒 400；入队满回 429（不改名册，agent 退避重报）。
func (s *ConnIngestService) Ingest(p ConnBatchParams) (ConnBatchResult, error) {
	if p.Identity.Kind != model.ServerKindProxy {
		return ConnBatchResult{}, apperr.ErrInvalidParam
	}
	if len(p.Events) > maxConnEventsPerBatch {
		return ConnBatchResult{}, apperr.ErrInvalidParam
	}
	if p.DroppedCount > 0 {
		// agent 本地缓冲溢出丢弃样本——记 WARN 让「丢了多少」可见（错误不静默，ADR-0057）。
		slog.Warn("agent 连接采集缓冲有事件被丢弃",
			"namespace", p.Identity.Namespace, "proxyServerId", p.Identity.ServerID, "丢弃数", p.DroppedCount)
	}

	nowMs := s.now().UnixMilli()
	events := make([]model.ConnEvent, 0, len(p.Events))
	seen := make(map[string]struct{}, len(p.Events))
	duplicated := 0
	for _, in := range p.Events {
		if !model.IsValidConnEventKind(in.Kind) || in.PlayerUUID == "" {
			return ConnBatchResult{}, apperr.ErrInvalidParam
		}
		connMs, ok := store.TimeMsFromUUIDv7(in.ConnID)
		if !ok {
			return ConnBatchResult{}, apperr.ErrInvalidParam
		}
		if !model.IsValidConnCloseKind(in.CloseKind) {
			return ConnBatchResult{}, apperr.ErrInvalidParam
		}
		if skewDropped := s.guardClockSkew(p.Identity, in.ConnID, connMs, nowMs); skewDropped {
			continue
		}
		dedupKey := in.Kind + "\x00" + in.ConnID
		if _, dup := seen[dedupKey]; dup {
			duplicated++
			continue
		}
		seen[dedupKey] = struct{}{}
		events = append(events, s.toConnEvent(p.Identity, in, connMs))
	}

	if len(events) > 0 {
		if !s.enqueue.Enqueue(events) {
			// 队列满：不改名册、回 429 背压，agent 退避后重报（数据不丢在 agent 侧）。
			return ConnBatchResult{}, apperr.ErrConnIngestBusy
		}
		s.applyRoster(events)
	}
	s.trackBoot(p.Identity, p.BootID, s.now())
	return ConnBatchResult{Accepted: len(events), Duplicated: duplicated}, nil
}

// guardClockSkew 校验 conn_id 内嵌时间与控制面偏移：超 24h 丢弃该事件（防写错误分片）、超 5min 记 WARN。
func (s *ConnIngestService) guardClockSkew(id agentauth.Identity, connID string, connMs, nowMs int64) (dropped bool) {
	skew := nowMs - connMs
	if skew < 0 {
		skew = -skew
	}
	if skew > connClockSkewReject.Milliseconds() {
		slog.Warn("连接事件时钟偏移过大，已丢弃",
			"namespace", id.Namespace, "proxyServerId", id.ServerID, "connId", connID, "偏移毫秒", skew)
		return true
	}
	if skew > connClockSkewWarn.Milliseconds() {
		slog.Warn("连接事件时钟偏移偏大",
			"namespace", id.Namespace, "proxyServerId", id.ServerID, "偏移毫秒", skew)
	}
	return false
}

// toConnEvent 把服务层入参映射为落库流转事件（namespace / proxyServerId 取权威身份；openedAt 缺省回退 conn_id 时间）。
func (s *ConnIngestService) toConnEvent(id agentauth.Identity, in ConnEventInput, connMs int64) model.ConnEvent {
	openedMs := in.OpenedAtMs
	if openedMs <= 0 {
		openedMs = connMs
	}
	return model.ConnEvent{
		Kind: in.Kind, ConnID: in.ConnID, NamespaceID: id.NamespaceID, ProxyServerID: id.ServerID,
		PlayerUUID: in.PlayerUUID, PlayerName: in.PlayerName, ClientIP: in.ClientIP,
		ProtocolVersion: in.ProtocolVersion, OpenedAtMs: openedMs, ClosedAtMs: in.ClosedAtMs,
		CloseKind: in.CloseKind, CloseReason: in.CloseReason,
		FirstBackend: in.FirstBackend, LastBackend: in.LastBackend, BackendSwitchCount: in.BackendSwitchCount,
	}
}

// applyRoster 按已受理事件更新内存名册：open 登记（解析服 = 首后端，缺省回退 proxy）、close 摘除。
func (s *ConnIngestService) applyRoster(events []model.ConnEvent) {
	for _, ev := range events {
		if ev.Kind == model.ConnEventKindClose {
			s.roster.ApplyClose(ev.PlayerUUID, ev.ConnID)
			continue
		}
		resolved := ev.FirstBackend
		if resolved == "" {
			resolved = ev.ProxyServerID
		}
		s.roster.ApplyOpen(ev.NamespaceID, ev.PlayerUUID, ev.ConnID, resolved)
	}
}

// trackBoot 追踪某 proxy 的 bootId：本进程内首见的「不同」bootId（proxy 重启）触发孤儿对账，
// 非阻塞投递到后台队列（请求线程不碰 DB）。首次见（本进程无记录）不对账——避免控制面重启后误闭鲜活会话。
func (s *ConnIngestService) trackBoot(id agentauth.Identity, bootID string, at time.Time) {
	if bootID == "" {
		return
	}
	key := bootKey{namespaceID: id.NamespaceID, proxyServerID: id.ServerID}
	s.bootMu.Lock()
	prev, seen := s.boots[key]
	changed := seen && prev != bootID
	s.boots[key] = bootID
	s.bootMu.Unlock()
	if !changed {
		return
	}
	select {
	case s.reconcileCh <- reconcileReq{namespaceID: id.NamespaceID, proxyServerID: id.ServerID, before: at}:
	default:
		slog.Warn("孤儿对账队列已满，本次 proxy 重启对账被丢弃", "namespace", id.Namespace, "proxyServerId", id.ServerID)
	}
}

// Run 后台孤儿对账 worker：消费 proxy 重启对账请求，把其旧 bootId 下 status=open 的孤儿会话补 close。
// 随 ctx 取消退出。DB 写在此后台执行，请求线程全程不碰 DB。
func (s *ConnIngestService) Run(ctx context.Context) {
	slog.Info("连接明细孤儿对账 worker 已启动")
	for {
		select {
		case <-ctx.Done():
			slog.Info("连接明细孤儿对账 worker 已停止")
			return
		case req := <-s.reconcileCh:
			n, err := s.reconciler.CloseOrphans(req.namespaceID, req.proxyServerID, req.before, connReconcileRetentionDays)
			if err != nil {
				slog.Warn("孤儿会话对账失败", "proxyServerId", req.proxyServerID, "错误", err)
				continue
			}
			if n > 0 {
				slog.Info("孤儿会话已补 close", "proxyServerId", req.proxyServerID, "补 close 数", n)
			}
		}
	}
}

// RebuildRoster 进程启动期从 status=open 连接行重建内存名册（spec §4.1）。best-effort，失败仅 WARN。
func (s *ConnIngestService) RebuildRoster() {
	open, err := s.reconciler.ListOpenConnections(connReconcileRetentionDays)
	if err != nil {
		slog.Warn("名册重建读 open 连接失败，名册留空待新连接填充", "错误", err)
		return
	}
	entries := make([]roster.RebuildEntry, 0, len(open))
	for _, oc := range open {
		resolved := oc.FirstBackend
		if resolved == "" {
			resolved = oc.ProxyServerID
		}
		entries = append(entries, roster.RebuildEntry{
			PlayerUUID: oc.PlayerUUID, ConnID: oc.ConnID,
			NamespaceID: oc.NamespaceID, ServerID: resolved,
		})
	}
	s.roster.RebuildFrom(entries)
	slog.Info("玩家名册已重建", "在线玩家数", len(entries))
}
