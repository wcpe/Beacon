package alert

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingAlerter 是「投递会卡住直到被放行」的通道，用于验证有界并发与关停 drain。
//
// 刻意不用 sleep 猜时序：进入 Notify 时给 started 一点信号，退出靠 release 闸门，
// 这样断言的是「实际并发度」，而不是「大概过了多久」。
type blockingAlerter struct {
	started atomic.Int32
	release chan struct{}
	once    sync.Once
	// done 记录成功投递（未被中断地走出 Notify）的条数。
	done atomic.Int32
}

func newBlockingAlerter() *blockingAlerter {
	return &blockingAlerter{release: make(chan struct{})}
}

func (a *blockingAlerter) Name() string { return "blocking" }

func (a *blockingAlerter) Notify(_ context.Context, _ Alert) error {
	a.started.Add(1)
	<-a.release
	a.done.Add(1)
	return nil
}

// unblock 放行所有投递。
func (a *blockingAlerter) unblock() { a.once.Do(func() { close(a.release) }) }

// countingAlerter 只计数，不阻塞。
type countingAlerter struct{ n atomic.Int32 }

func (a *countingAlerter) Name() string { return "counting" }

func (a *countingAlerter) Notify(_ context.Context, _ Alert) error {
	a.n.Add(1)
	return nil
}

// TestDispatchAsyncBoundedConcurrency 锁死护栏：异步扇出的并发度被钉在 worker 数上，
// 不会因为告警量增大而起等量 goroutine。
//
// 为什么重要：mass failure（一整批目标同时失败）时「每个告警一个 goroutine」会瞬时
// 起等量 goroutine + 等量 HTTP 连接，把旁路变成主路径的负担。
func TestDispatchAsyncBoundedConcurrency(t *testing.T) {
	blk := newBlockingAlerter()
	d := NewDispatcher(blk)
	defer func() {
		blk.unblock()
		_ = d.Shutdown(context.Background())
	}()

	// 投递远超 worker 数的告警：它们会分别进入 worker 或排队，但绝不该同时进入 Notify。
	const total = 60
	for i := 0; i < total; i++ {
		d.DispatchAsync(context.Background(), sampleAlert())
	}

	// 等到并发度稳定在 worker 数：只有 asyncWorkerCount 个能进入 Notify。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if blk.started.Load() >= asyncWorkerCount {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := blk.started.Load(); got != asyncWorkerCount {
		t.Fatalf("并发度应恰为 %d（worker 数），实际 %d", asyncWorkerCount, got)
	}

	// 再给一点时间，确认没有更多 goroutine 挤进来（并发度不随投递量增长）。
	time.Sleep(50 * time.Millisecond)
	if got := blk.started.Load(); got != asyncWorkerCount {
		t.Fatalf("并发度不应随投递量增长：仍应为 %d，实际 %d", asyncWorkerCount, got)
	}
}

// TestDispatchAsyncDropsWhenQueueFull 锁死护栏：队列满时**显式丢弃并计数**，不得阻塞调用方。
//
// 告警是旁路，宁可丢也不能反噬主循环；但「丢了却没人知道」不可接受，故必须计数。
func TestDispatchAsyncDropsWhenQueueFull(t *testing.T) {
	blk := newBlockingAlerter()
	d := NewDispatcher(blk)
	defer func() {
		blk.unblock()
		_ = d.Shutdown(context.Background())
	}()

	// 灌满队列：worker 全卡在 Notify，队列容量之外的全被丢弃。
	total := asyncWorkerCount + asyncQueueSize + 50
	done := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			d.DispatchAsync(context.Background(), sampleAlert())
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("DispatchAsync 不得阻塞调用方（队列满时应立即丢弃返回）")
	}

	if got := d.Dropped(); got == 0 {
		t.Fatalf("队列溢出必须显式计数，实际 Dropped()=0（共投递 %d）", total)
	}
}

// TestDispatchAsyncDeliversAfterUnblock 验证正常路径：放行后所有已入队的告警最终都被投递。
func TestDispatchAsyncDeliversAfterUnblock(t *testing.T) {
	cnt := &countingAlerter{}
	d := NewDispatcher(cnt)
	defer func() { _ = d.Shutdown(context.Background()) }()

	for i := 0; i < 20; i++ {
		d.DispatchAsync(context.Background(), sampleAlert())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cnt.n.Load() == 20 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("入队的告警应全部投递，实际 %d/20", cnt.n.Load())
}

// TestShutdownDrainsQueuedAlerts 锁死护栏：关停时**已接受但尚未发出**的告警必须投完。
//
// 为什么重要：优雅退出的那一刻往往正是最需要留痕的一批（e.g. 一批目标刚全部失败）。
// 若 Shutdown 只发停止信号而不 drain，这批告警会静默消失。
//
// 构造要点（这决定了测试是否真的有效）：必须让 worker **先卡住**，使告警确实积压在
// 队列里，再调 Shutdown。否则 worker 会在停止信号到达前就把队列处理空，
// 「不 drain」的实现也能通过——测试看似覆盖、实则测不到。
func TestShutdownDrainsQueuedAlerts(t *testing.T) {
	blk := newBlockingAlerter()
	d := NewDispatcher(blk)

	// 灌满全部 worker 位 + 额外排队：此时队列里必定有积压项。
	const total = asyncWorkerCount + 20
	for i := 0; i < total; i++ {
		d.DispatchAsync(context.Background(), sampleAlert())
	}
	// 等所有 worker 都卡在 Notify 上，确保积压已形成。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if blk.started.Load() >= asyncWorkerCount {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := blk.started.Load(); got != asyncWorkerCount {
		t.Fatalf("前置条件不成立：应有 %d 个 worker 卡住，实际 %d", asyncWorkerCount, got)
	}

	// 放行并同时关停：worker 必须把队列里的剩余项投完才退出。
	go func() {
		// 稍等让 Shutdown 先置停止信号，从而真正走到「drain 队列」这条路径。
		time.Sleep(20 * time.Millisecond)
		blk.unblock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown 应在期限内完成 drain，实际 %v", err)
	}
	// 全部投完：worker 退出前会把队列剩余项投递完毕。
	if got := blk.done.Load(); got != int32(total) {
		t.Fatalf("关停必须 drain 队列：应投递 %d 条，实际 %d", total, got)
	}
}

// TestShutdownRejectsNewAlertsAfterClose 锁死单向语义：关停后不再接收，且计入丢弃。
func TestShutdownRejectsNewAlertsAfterClose(t *testing.T) {
	cnt := &countingAlerter{}
	d := NewDispatcher(cnt)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown 应成功，实际 %v", err)
	}

	before := cnt.n.Load()
	d.DispatchAsync(context.Background(), sampleAlert())
	time.Sleep(50 * time.Millisecond)

	if got := cnt.n.Load(); got != before {
		t.Fatalf("关停后不得再投递：投递数不应从 %d 变为 %d", before, got)
	}
	if d.Dropped() == 0 {
		t.Fatal("关停后的投递必须计入丢弃，不得静默消失")
	}
}

// TestShutdownOnNeverUsedDispatcher 验证只用同步 Dispatch 的分发器也能安全关停。
//
// 惰性启动的代价：从未走异步路径时分发器没有 worker，Shutdown 必须立即返回而不是卡住。
func TestShutdownOnNeverUsedDispatcher(t *testing.T) {
	rec := &recordingAlerter{name: "sync"}
	d := NewDispatcher(rec)

	// 只走同步路径。
	d.Dispatch(context.Background(), sampleAlert())
	if len(rec.got) != 1 {
		t.Fatalf("同步 Dispatch 应立即投递，实际 %d", len(rec.got))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("未使用异步路径时 Shutdown 应立即成功，实际 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown 不应阻塞（无 worker 时应立即返回）")
	}
}

// TestShutdownIdempotent 验证重复关停安全（装配里两处 Dispatcher 都可能被关）。
func TestShutdownIdempotent(t *testing.T) {
	d := NewDispatcher(&countingAlerter{})
	d.DispatchAsync(context.Background(), sampleAlert())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("首次 Shutdown 应成功，实际 %v", err)
	}
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("重复 Shutdown 应安全返回，实际 %v", err)
	}
}

// TestNilDispatcherSafe 验证 nil 接收者安全（装配缺失时不得 panic）。
func TestNilDispatcherSafe(t *testing.T) {
	var d *Dispatcher
	d.DispatchAsync(context.Background(), sampleAlert()) // 不应 panic
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil 分发器 Shutdown 应返回 nil，实际 %v", err)
	}
	if got := d.Dropped(); got != 0 {
		t.Fatalf("nil 分发器 Dropped 应为 0，实际 %d", got)
	}
}
