package service

import (
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
)

// fakeConnEnqueuer 捕获入队的连接事件，可模拟队列满。
type fakeConnEnqueuer struct {
	events []model.ConnEvent
	full   bool
}

func (f *fakeConnEnqueuer) Enqueue(rows []model.ConnEvent) bool {
	if f.full {
		return false
	}
	f.events = append(f.events, rows...)
	return true
}

// fakeReconciler 记录对账调用并提供名册重建数据。
type fakeReconciler struct {
	mu         sync.Mutex
	closeCalls []reconcileReq
	openConns  []repository.OpenConn
}

func (f *fakeReconciler) CloseOrphans(nsID uint, proxy string, before time.Time, retentionDays int) (int64, error) {
	f.mu.Lock()
	f.closeCalls = append(f.closeCalls, reconcileReq{namespaceID: nsID, proxyServerID: proxy, before: before})
	f.mu.Unlock()
	return 1, nil
}

func (f *fakeReconciler) ListOpenConnections(retentionDays int) ([]repository.OpenConn, error) {
	return f.openConns, nil
}

func proxyID(ns uint, server string) agentauth.Identity {
	return agentauth.Identity{NamespaceID: ns, Namespace: "prod", ServerID: server, Kind: model.ServerKindProxy}
}

func connInput(kind, connID, player, firstBackend string) ConnEventInput {
	return ConnEventInput{Kind: kind, ConnID: connID, PlayerUUID: player, FirstBackend: firstBackend}
}

// uuid7 构造内嵌指定毫秒时间的 UUIDv7 文本（测试用，seq 保后半段唯一）。
func uuid7(ms int64, seq string) string {
	const d = "0123456789abcdef"
	h := func(b byte) string { return string([]byte{d[b>>4], d[b&0x0f]}) }
	p := h(byte(ms>>40)) + h(byte(ms>>32)) + h(byte(ms>>24)) + h(byte(ms>>16)) + "-" + h(byte(ms>>8)) + h(byte(ms))
	return p + "-7abc-8def-" + (seq + "000000000000")[:12]
}

func newConnSvc(enq *fakeConnEnqueuer, rec *fakeReconciler) (*ConnIngestService, *roster.Store) {
	rs := roster.NewStore()
	s := NewConnIngestService(enq, rs, rec)
	return s, rs
}

// TestConnIngestAcceptsAndDrivesRoster 校验 open 登记名册、close 摘除名册、accepted 计数正确。
func TestConnIngestAcceptsAndDrivesRoster(t *testing.T) {
	enq := &fakeConnEnqueuer{}
	s, rs := newConnSvc(enq, &fakeReconciler{})
	nowMs := time.Now().UTC().UnixMilli()

	res, err := s.Ingest(ConnBatchParams{
		Identity: proxyID(1, "proxy-1"), BootID: "boot-a",
		Events: []ConnEventInput{connInput(model.ConnEventKindOpen, uuid7(nowMs, "a1"), "alice", "game-3")},
	})
	if err != nil || res.Accepted != 1 {
		t.Fatalf("open 应 accepted=1，实际 %+v err=%v", res, err)
	}
	if loc, ok := rs.Resolve("alice"); !ok || loc.ServerID != "game-3" {
		t.Fatalf("open 应登记 alice→game-3，实际 %+v ok=%v", loc, ok)
	}

	if _, err := s.Ingest(ConnBatchParams{
		Identity: proxyID(1, "proxy-1"), BootID: "boot-a",
		Events: []ConnEventInput{{Kind: model.ConnEventKindClose, ConnID: uuid7(nowMs, "a1"), PlayerUUID: "alice", ClosedAtMs: nowMs + 1000}},
	}); err != nil {
		t.Fatalf("close 失败: %v", err)
	}
	if _, ok := rs.Resolve("alice"); ok {
		t.Fatalf("close 应摘除 alice")
	}
	if len(enq.events) != 2 {
		t.Fatalf("应入队 2 条事件，实际 %d", len(enq.events))
	}
}

// TestConnIngestQueueFull429 校验入队满回 429、名册不变。
func TestConnIngestQueueFull429(t *testing.T) {
	enq := &fakeConnEnqueuer{full: true}
	s, rs := newConnSvc(enq, &fakeReconciler{})
	nowMs := time.Now().UTC().UnixMilli()
	_, err := s.Ingest(ConnBatchParams{
		Identity: proxyID(1, "proxy-1"), BootID: "boot-a",
		Events: []ConnEventInput{connInput(model.ConnEventKindOpen, uuid7(nowMs, "b1"), "bob", "game-1")},
	})
	if err != apperr.ErrConnIngestBusy {
		t.Fatalf("队列满应回 conn_ingest_busy，实际 %v", err)
	}
	if _, ok := rs.Resolve("bob"); ok {
		t.Fatalf("429 时名册不应更新")
	}
}

// TestConnIngestRejectsNonProxy 校验非 proxy 身份拒 400。
func TestConnIngestRejectsNonProxy(t *testing.T) {
	s, _ := newConnSvc(&fakeConnEnqueuer{}, &fakeReconciler{})
	id := proxyID(1, "game-1")
	id.Kind = model.ServerKindBackend
	if _, err := s.Ingest(ConnBatchParams{Identity: id, Events: nil}); err != apperr.ErrInvalidParam {
		t.Fatalf("非 proxy 应拒 400，实际 %v", err)
	}
}

// TestConnIngestClockSkewDrops 校验 conn_id 内嵌时间偏移超 24h 的事件被丢弃、不入队。
func TestConnIngestClockSkewDrops(t *testing.T) {
	enq := &fakeConnEnqueuer{}
	s, _ := newConnSvc(enq, &fakeReconciler{})
	staleMs := time.Now().UTC().Add(-48 * time.Hour).UnixMilli()
	res, err := s.Ingest(ConnBatchParams{
		Identity: proxyID(1, "proxy-1"), BootID: "boot-a",
		Events: []ConnEventInput{connInput(model.ConnEventKindOpen, uuid7(staleMs, "c1"), "carol", "game-1")},
	})
	if err != nil {
		t.Fatalf("不应报错（丢弃而非拒批）: %v", err)
	}
	if res.Accepted != 0 || len(enq.events) != 0 {
		t.Fatalf("超 24h 偏移事件应被丢弃，实际 accepted=%d 入队=%d", res.Accepted, len(enq.events))
	}
}

// TestConnIngestBootChangeTriggersReconcile 校验同 proxy 出现不同 bootId 触发孤儿对账入队。
func TestConnIngestBootChangeTriggersReconcile(t *testing.T) {
	s, _ := newConnSvc(&fakeConnEnqueuer{}, &fakeReconciler{})
	nowMs := time.Now().UTC().UnixMilli()
	base := ConnBatchParams{Identity: proxyID(1, "proxy-1"), Events: []ConnEventInput{connInput(model.ConnEventKindOpen, uuid7(nowMs, "d1"), "dan", "game-1")}}

	base.BootID = "boot-a"
	if _, err := s.Ingest(base); err != nil {
		t.Fatalf("首批失败: %v", err)
	}
	// 首见 boot-a 不应触发对账
	select {
	case <-s.reconcileCh:
		t.Fatalf("首见 bootId 不应触发对账")
	default:
	}

	base.BootID = "boot-b" // proxy 重启
	base.Events = []ConnEventInput{connInput(model.ConnEventKindOpen, uuid7(nowMs+10, "d2"), "dan", "game-1")}
	if _, err := s.Ingest(base); err != nil {
		t.Fatalf("次批失败: %v", err)
	}
	select {
	case req := <-s.reconcileCh:
		if req.proxyServerID != "proxy-1" || req.namespaceID != 1 {
			t.Fatalf("对账请求目标不符: %+v", req)
		}
	default:
		t.Fatalf("bootId 变更应触发对账入队")
	}
}

// TestConnRebuildRoster 校验从 open 连接行重建名册。
func TestConnRebuildRoster(t *testing.T) {
	rec := &fakeReconciler{openConns: []repository.OpenConn{
		{ConnID: "c1", NamespaceID: 1, ProxyServerID: "proxy-1", PlayerUUID: "erin", FirstBackend: "game-5"},
		{ConnID: "c2", NamespaceID: 1, ProxyServerID: "proxy-1", PlayerUUID: "frank"}, // 无后端 → 回退 proxy
	}}
	s, rs := newConnSvc(&fakeConnEnqueuer{}, rec)
	s.RebuildRoster()
	if loc, ok := rs.Resolve("erin"); !ok || loc.ServerID != "game-5" {
		t.Fatalf("应重建 erin→game-5，实际 %+v ok=%v", loc, ok)
	}
	if loc, ok := rs.Resolve("frank"); !ok || loc.ServerID != "proxy-1" {
		t.Fatalf("无后端应回退 proxy-1，实际 %+v ok=%v", loc, ok)
	}
}
