package service

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

// countingCmdCounter 是 commandCounter 替身：记录被调用次数，用于证明「抓取路径不查库」。
//
// 必须加锁：刷新器在后台 goroutine 里调用它，而测试主线程会读 calls 做断言——
// 无锁会在 -race 下报数据竞争（本用例最初就是如此被检出的）。
type countingCmdCounter struct {
	mu     sync.Mutex
	calls  int
	counts map[string]int
	err    error
}

func (c *countingCmdCounter) CountByStatus() (map[string]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.counts, nil
}

// callCount 读已发生的调用次数（加锁）。
func (c *countingCmdCounter) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// recordingSink 是 commandCountSink 替身：记录被写入的计数与时间戳。同样需加锁（见上）。
type recordingSink struct {
	mu       sync.Mutex
	writes   int
	byStatus map[string]int
	nowUnix  float64
}

func (s *recordingSink) SetCommandStatusSnapshot(byStatus map[string]int, nowUnix float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	s.byStatus = byStatus
	s.nowUnix = nowUnix
}

// snapshot 读当前记录（加锁）。
func (s *recordingSink) snapshot() (int, map[string]int, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes, s.byStatus, s.nowUnix
}

// TestSnapshotWithoutCommandCountsDoesNotTouchDB 锁定「抓取路径不触达 DB」这一硬约束（见 ADR-0091）。
//
// 为什么必须锁死：命令队列深度源自一次 DB GROUP BY。抓取端点默认无鉴权且不设限流，
// 若把该查询放进抓取路径，任何能访问该端口的人都能按任意频率驱动数据库查询——
// 等于把 DB 负载交给外部请求速率决定。本用例用计数替身直接证明：多次调用抓取侧方法，
// CountByStatus 的调用次数必须**恒为 0**。
//
// 判别力：若有人日后图省事把 CountByStatus 加回 SnapshotWithoutCommandCounts（或让
// 抓取侧改调 Snapshot），本用例立即变红。
func TestSnapshotWithoutCommandCountsDoesNotTouchDB(t *testing.T) {
	cc := &countingCmdCounter{counts: map[string]int{"pending": 3}}
	svc := NewObservabilityService(
		fakeDBStats{stats: sql.DBStats{MaxOpenConnections: 4, OpenConnections: 2, InUse: 1, Idle: 1}},
		newObsRegistry(t, 2),
		fakeWaiter(1), fakeWaiter(2), fakeWaiter(3), fakeWaiter(4),
		cc,
	)

	for i := 0; i < 5; i++ {
		snap := svc.SnapshotWithoutCommandCounts()
		// 内存类字段照常可用（抓取侧仍要看到连接池与长轮询）。
		if snap.DBPool.MaxOpenConnections != 4 {
			t.Fatalf("连接池统计应可用，实际 %+v", snap.DBPool)
		}
		if snap.Longpoll.Total != 1+2+3+4 {
			t.Fatalf("长轮询合计应为 10，实际 %d", snap.Longpoll.Total)
		}
		// 命令队列在抓取侧恒为空（由后台刷新器单独填充指标缓存）。
		if len(snap.CommandByStatus) != 0 {
			t.Fatalf("抓取侧不应包含命令队列计数，实际 %v", snap.CommandByStatus)
		}
	}
	if got := cc.callCount(); got != 0 {
		t.Fatalf("抓取路径不得查库：5 次调用后 CountByStatus 应为 0 次，实际 %d 次", got)
	}

	// 对照：管理台自观测页仍走 Snapshot（允许查库，且只查一次）。
	_ = svc.Snapshot()
	if got := cc.callCount(); got != 1 {
		t.Fatalf("Snapshot 应查一次库，实际 %d 次", got)
	}
}

// TestCommandCountRefresherWritesSnapshot 验证后台刷新器把队列深度与时间戳写入指标缓存。
func TestCommandCountRefresherWritesSnapshot(t *testing.T) {
	cc := &countingCmdCounter{counts: map[string]int{"pending": 7, "failed": 2}}
	sink := &recordingSink{}
	r := NewCommandCountRefresher(cc, sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	// 首轮应立即刷新（不等第一个 tick）：否则启动后前 15s 抓取只能看到空缓存，
	// 会把「还没采过」误读成「队列为空」。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if w, _, _ := sink.snapshot(); w > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	writes, byStatus, nowUnix := sink.snapshot()
	if writes == 0 {
		t.Fatal("刷新器应在启动时立即写入一次（首轮不等待 tick）")
	}
	if byStatus["pending"] != 7 {
		t.Fatalf("应写入 pending=7，实际 %v", byStatus)
	}
	if nowUnix <= 0 {
		t.Fatalf("应写入正的刷新时间戳，实际 %v", nowUnix)
	}
}

// TestCommandCountRefresherKeepsOldValueOnError 验证采集失败时**不写缓存**。
//
// 关键点：失败若写入空 map，看板上会显示「队列为空」——一个假的好消息，
// 恰好掩盖了"采集本身坏了"这个真问题。保留旧值则表现为数值停滞，
// 配合 beacon_self_commands_snapshot_timestamp_seconds 可判断陈旧度。
func TestCommandCountRefresherKeepsOldValueOnError(t *testing.T) {
	cc := &countingCmdCounter{err: errCmdCountFailed}
	sink := &recordingSink{}
	r := NewCommandCountRefresher(cc, sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	time.Sleep(200 * time.Millisecond) // 给首轮足够时间执行
	if writes, _, _ := sink.snapshot(); writes != 0 {
		t.Fatalf("采集失败时不应写入缓存（避免把「查不到」写成「队列空」），实际写入 %d 次", writes)
	}
	if cc.callCount() == 0 {
		t.Fatal("刷新器应确实尝试过采集")
	}
}

var errCmdCountFailed = &cmdCountErr{}

type cmdCountErr struct{}

func (*cmdCountErr) Error() string { return "模拟查库失败" }
