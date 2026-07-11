package service

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 写入池工程默认值（§8 待定 13：均为压测可调的工程参数，非契约，故为常量不入设置 store）。
const (
	// 有界队列容量（批）：满即背压 429，保护控制面不被写入压垮（数据不丢在 agent 侧）。
	defaultMetricQueueCapacity = 4096
	// 写入协程数：多写并发消费同一队列。
	defaultMetricWriters = 2
	// 单次 flush 的攒批行数上限：攒够即刷。
	metricFlushRows = 200
	// 单次 flush 的攒批超时：不足行数但超时也刷，控制入库时延。
	metricFlushInterval = 500 * time.Millisecond
	// flush 失败的有限退避重试次数（仍失败则丢弃该 flush 并计数）。
	metricFlushMaxRetries = 3
)

// metricRowFlusher 是写入池对持久化的窄依赖（由 repository.MetricSampleV2Repository 实现）：
// 幂等批量写一批（可能跨日）聚合行到各自当日表，返回被唯一键去重的行数。抽成接口便于单测注入替身。
type metricRowFlusher interface {
	FlushDaily(rows []model.MetricSampleV2) (int, error)
}

// MetricIngestWriter 是 P4 指标异步入库的有界队列 + 后台写入池（FR-144，见 §4.3）。
//
// 不变量：
//   - 请求 goroutine 不碰 DB——只经 Enqueue 非阻塞投递，DB IO 全在后台 worker。
//   - 队列满不丢——Enqueue 返回 false，由上层回 429 背压 agent，agent 保留缓冲重试。
//   - flush 失败 WARN + 有限退避重试；仍失败丢弃该 flush 并累计丢弃计数暴露 /system（错误不静默，ADR-0057）。
type MetricIngestWriter struct {
	queue         chan []model.MetricSampleV2
	flusher       metricRowFlusher
	workers       int
	flushRows     int
	flushInterval time.Duration
	maxRetries    int
	// discarded 累计因多次重试仍失败而丢弃的行数（原子读写，供自观测暴露）。
	discarded int64
}

// NewMetricIngestWriter 构造写入池（工程默认参数）。
func NewMetricIngestWriter(flusher metricRowFlusher) *MetricIngestWriter {
	return &MetricIngestWriter{
		queue:         make(chan []model.MetricSampleV2, defaultMetricQueueCapacity),
		flusher:       flusher,
		workers:       defaultMetricWriters,
		flushRows:     metricFlushRows,
		flushInterval: metricFlushInterval,
		maxRetries:    metricFlushMaxRetries,
	}
}

// Workers 返回应启动的写入协程数（main 据此 go w.Run(ctx) 若干次）。
func (w *MetricIngestWriter) Workers() int { return w.workers }

// Enqueue 非阻塞投递一批聚合行；队列满返回 false（上层据此回 429 背压）。空批为安全空操作。
func (w *MetricIngestWriter) Enqueue(rows []model.MetricSampleV2) bool {
	if len(rows) == 0 {
		return true
	}
	select {
	case w.queue <- rows:
		return true
	default:
		return false
	}
}

// Discarded 返回累计丢弃行数（自观测；写入多次重试仍失败被丢弃的行）。
func (w *MetricIngestWriter) Discarded() int64 {
	return atomic.LoadInt64(&w.discarded)
}

// Run 启动单个写入 worker，从队列取批、攒到行数上限或超时即 flush，直到 ctx 取消。
// main 按 Workers() 起多个 goroutine 共享同一队列。
func (w *MetricIngestWriter) Run(ctx context.Context) {
	slog.Info("指标写入协程已启动", "攒批行数", w.flushRows, "攒批超时", w.flushInterval.String())
	buf := make([]model.MetricSampleV2, 0, w.flushRows*2)
	timer := time.NewTimer(w.flushInterval)
	defer timer.Stop()

	flush := func() {
		if len(buf) == 0 {
			return
		}
		w.flushWithRetry(buf)
		buf = buf[:0]
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
			w.drainOnShutdown(&buf, flush)
			slog.Info("指标写入协程已停止")
			return
		case batch := <-w.queue:
			buf = append(buf, batch...)
			if len(buf) >= w.flushRows {
				flush()
				resetTimer()
			}
		case <-timer.C:
			flush()
			timer.Reset(w.flushInterval)
		}
	}
}

// drainOnShutdown 关停时把队列中剩余批并入 buffer 并 flush，尽力不丢已接收数据。
func (w *MetricIngestWriter) drainOnShutdown(buf *[]model.MetricSampleV2, flush func()) {
	for {
		select {
		case batch := <-w.queue:
			*buf = append(*buf, batch...)
		default:
			flush()
			return
		}
	}
}

// flushWithRetry 一次事务批插一批行；失败 WARN + 有限退避重试，仍失败丢弃并累计计数。
func (w *MetricIngestWriter) flushWithRetry(rows []model.MetricSampleV2) {
	var lastErr error
	for attempt := 0; attempt <= w.maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(flushBackoff(attempt))
		}
		dedup, err := w.flusher.FlushDaily(rows)
		if err == nil {
			if dedup > 0 {
				slog.Debug("指标批量写入完成（含重放去重）", "行数", len(rows), "去重数", dedup)
			}
			return
		}
		lastErr = err
		slog.Warn("指标批量写入失败，将退避重试", "行数", len(rows), "尝试", attempt+1, "错误", err)
	}
	discarded := atomic.AddInt64(&w.discarded, int64(len(rows)))
	slog.Warn("指标批量写入多次重试仍失败，丢弃本次 flush", "行数", len(rows), "累计丢弃行数", discarded, "错误", lastErr)
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
