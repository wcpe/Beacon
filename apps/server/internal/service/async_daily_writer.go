package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// 写入通道工程默认值（§8 待定 13：均为压测可调的工程参数，非契约，故为常量不入设置 store）。
const (
	// 每路由有界队列容量（批）：满即背压（指标路由回 429），保护控制面不被写入压垮（数据不丢在 agent 侧）。
	defaultDailyWriteQueueCapacity = 4096
	// 每路由写入协程数：多写并发消费同一路由队列。
	defaultDailyWriteWorkers = 2
	// 单次 flush 的攒批行数上限：攒够即刷。
	dailyWriteFlushRows = 200
	// 单次 flush 的攒批超时：不足行数但超时也刷，控制入库时延。
	dailyWriteFlushInterval = 500 * time.Millisecond
	// flush 失败的有限退避重试次数（仍失败则丢弃该 flush 并计数）。
	dailyWriteFlushMaxRetries = 3
)

// writeBatch 是入队的一批行：rows 实际为注册路由的 []T，n 为行数（供攒批计数，不必解开 any）。
type writeBatch struct {
	rows any
	n    int
}

// writerRoute 是单个路由（一类实体日表）的独立队列与 flush 适配：各路由各自攒批、互不阻塞。
type writerRoute struct {
	kind  string
	queue chan writeBatch
	// flush 把攒好的批串接为类型化行并写库（由 RegisterFlusher 的泛型适配器提供），返回被去重的行数。
	flush func(batches []writeBatch) (int, error)
}

// AsyncDailyWriter 是异步日表写入通道（FR-144，见 §4.3）：按路由键把不同实体（指标批 / 健康快照 /
// 调度决策）分发到各自的有界队列 + 后台写入池，共享背压与批量语义；跨日拆表由各路由 flusher 内部处理。
//
// 不变量：
//   - 请求 goroutine 不碰 DB——只经 EnqueueRows 非阻塞投递，DB IO 全在后台 worker。
//   - 队列满不丢——EnqueueRows 返回 false，由上层背压（指标路由回 429，agent 保留缓冲重试）。
//   - 各路由独立队列、独立攒批，互不阻塞；flush 失败 WARN + 有限退避重试，仍失败丢弃该 flush
//     并累计丢弃计数暴露 /system（错误不静默，ADR-0057）。
//   - 路由经 RegisterFlusher 在装配期一次性注册，Start 后不再增减。
type AsyncDailyWriter struct {
	mu      sync.RWMutex
	routes  map[string]*writerRoute
	started bool

	// 以下为全路由共享的工程参数（测试可在注册 / 启动前调小加速）。
	queueCapacity int
	workers       int
	flushRows     int
	flushInterval time.Duration
	maxRetries    int
	// discarded 累计全路由因多次重试仍失败而丢弃的行数（原子读写，供自观测暴露）。
	discarded int64
}

// NewAsyncDailyWriter 构造写入通道（工程默认参数），随后经 RegisterFlusher 注册各路由、Start 启动。
func NewAsyncDailyWriter() *AsyncDailyWriter {
	return &AsyncDailyWriter{
		routes:        make(map[string]*writerRoute),
		queueCapacity: defaultDailyWriteQueueCapacity,
		workers:       defaultDailyWriteWorkers,
		flushRows:     dailyWriteFlushRows,
		flushInterval: dailyWriteFlushInterval,
		maxRetries:    dailyWriteFlushMaxRetries,
	}
}

// RegisterFlusher 注册一个路由：kind 为路由键，flush 为该实体的幂等批量写函数
// （通常为 repository 的 FlushDaily 方法值，跨日拆表在其内部处理），返回被唯一键去重的行数。
// 只在装配期调用；重复注册或 Start 后注册均属装配错误，直接 panic 快速失败。
func RegisterFlusher[T any](w *AsyncDailyWriter, kind string, flush func(rows []T) (int, error)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		panic(fmt.Sprintf("写入通道已启动，禁止再注册路由 %q", kind))
	}
	if _, dup := w.routes[kind]; dup {
		panic(fmt.Sprintf("写入通道路由 %q 重复注册", kind))
	}
	w.routes[kind] = &writerRoute{
		kind:  kind,
		queue: make(chan writeBatch, w.queueCapacity),
		flush: adaptTypedFlush(kind, flush),
	}
}

// adaptTypedFlush 把类型化批量写函数适配为对 writeBatch 的 flush：串接各批为一个类型化切片后一次写库。
func adaptTypedFlush[T any](kind string, flush func(rows []T) (int, error)) func(batches []writeBatch) (int, error) {
	return func(batches []writeBatch) (int, error) {
		total := 0
		for _, b := range batches {
			total += b.n
		}
		rows := make([]T, 0, total)
		for _, b := range batches {
			typed, ok := b.rows.([]T)
			if !ok {
				// 入队类型与注册类型不符属编程错误：记 ERROR 跳过该批，不让坏批拖垮写入协程。
				slog.Error("写入通道批类型与路由注册类型不符，已跳过该批", "路由", kind)
				continue
			}
			rows = append(rows, typed...)
		}
		return flush(rows)
	}
}

// EnqueueRows 非阻塞投递一批行到指定路由；队列满返回 false（上层据此背压）。空批为安全空操作。
// 路由未注册属装配错误：记 ERROR 并返回 false（请求路径上保持进程安全，不 panic）。
func EnqueueRows[T any](w *AsyncDailyWriter, kind string, rows []T) bool {
	if len(rows) == 0 {
		return true
	}
	w.mu.RLock()
	rt := w.routes[kind]
	w.mu.RUnlock()
	if rt == nil {
		slog.Error("写入通道路由未注册，拒绝入队", "路由", kind, "行数", len(rows))
		return false
	}
	select {
	case rt.queue <- writeBatch{rows: rows, n: len(rows)}:
		return true
	default:
		return false
	}
}

// Discarded 返回累计丢弃行数（全路由合计；写入多次重试仍失败被丢弃的行，供 /system 自观测）。
func (w *AsyncDailyWriter) Discarded() int64 {
	return atomic.LoadInt64(&w.discarded)
}

// Start 为全部已注册路由各启动 workers 个写入协程，随 ctx 取消优雅退出。
// 须在全部 RegisterFlusher 完成后调用一次（main 装配尾声）。
func (w *AsyncDailyWriter) Start(ctx context.Context) {
	w.mu.Lock()
	w.started = true
	routes := make([]*writerRoute, 0, len(w.routes))
	for _, rt := range w.routes {
		routes = append(routes, rt)
	}
	w.mu.Unlock()
	for _, rt := range routes {
		for i := 0; i < w.workers; i++ {
			go w.runRoute(ctx, rt)
		}
	}
}

// runRoute 是单路由单 worker 主循环：取批、攒到行数上限或超时即 flush，直到 ctx 取消。
// 同路由多 worker 共享同一队列；不同路由各自攒批，互不阻塞。
func (w *AsyncDailyWriter) runRoute(ctx context.Context, rt *writerRoute) {
	slog.Info("日表写入协程已启动", "路由", rt.kind, "攒批行数", w.flushRows, "攒批超时", w.flushInterval.String())
	buf := make([]writeBatch, 0, 8)
	bufRows := 0
	timer := time.NewTimer(w.flushInterval)
	defer timer.Stop()

	flush := func() {
		if bufRows == 0 {
			return
		}
		w.flushWithRetry(rt, buf, bufRows)
		buf = buf[:0]
		bufRows = 0
	}
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(w.flushInterval)
	}

	for {
		select {
		case <-ctx.Done():
			// 关停：把 buffer 与队列里剩余批尽力落盘一次后退出。
			drainOnShutdown(rt.queue, &buf, &bufRows, flush)
			slog.Info("日表写入协程已停止", "路由", rt.kind)
			return
		case batch := <-rt.queue:
			buf = append(buf, batch)
			bufRows += batch.n
			if bufRows >= w.flushRows {
				flush()
				resetTimer()
			}
		case <-timer.C:
			flush()
			timer.Reset(w.flushInterval)
		}
	}
}

// drainOnShutdown 关停时把该路由队列中剩余批并入 buffer 并 flush，尽力不丢已接收数据。
func drainOnShutdown(queue chan writeBatch, buf *[]writeBatch, bufRows *int, flush func()) {
	for {
		select {
		case batch := <-queue:
			*buf = append(*buf, batch)
			*bufRows += batch.n
		default:
			flush()
			return
		}
	}
}

// flushWithRetry 一次调用路由 flusher 批插攒好的批；失败 WARN + 有限退避重试，仍失败丢弃并累计计数。
func (w *AsyncDailyWriter) flushWithRetry(rt *writerRoute, batches []writeBatch, rows int) {
	var lastErr error
	for attempt := 0; attempt <= w.maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(flushBackoff(attempt))
		}
		dedup, err := rt.flush(batches)
		if err == nil {
			if dedup > 0 {
				slog.Debug("日表批量写入完成（含重放去重）", "路由", rt.kind, "行数", rows, "去重数", dedup)
			}
			return
		}
		lastErr = err
		slog.Warn("日表批量写入失败，将退避重试", "路由", rt.kind, "行数", rows, "尝试", attempt+1, "错误", err)
	}
	discarded := atomic.AddInt64(&w.discarded, int64(rows))
	slog.Warn("日表批量写入多次重试仍失败，丢弃本次 flush", "路由", rt.kind, "行数", rows, "累计丢弃行数", discarded, "错误", lastErr)
}

// flushBackoff 计算第 attempt 次重试前的退避时长（50ms / 200ms / 500ms，有界不指数爆炸）。
func flushBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 50 * time.Millisecond
	case 2:
		return 200 * time.Millisecond
	default:
		return 500 * time.Millisecond
	}
}
