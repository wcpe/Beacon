package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// fakeDailyFlusher 是写入通道持久化窄依赖的测试替身：记录 flush 的行、可注入错误、每次成功 flush 发信号。
type fakeDailyFlusher struct {
	mu      sync.Mutex
	flushed [][]model.MetricSampleV2
	calls   int
	err     error
	done    chan struct{}
}

func (f *fakeDailyFlusher) FlushDaily(rows []model.MetricSampleV2) (int, error) {
	f.mu.Lock()
	f.calls++
	if f.err != nil {
		f.mu.Unlock()
		return 0, f.err
	}
	cp := make([]model.MetricSampleV2, len(rows))
	copy(cp, rows)
	f.flushed = append(f.flushed, cp)
	f.mu.Unlock()
	if f.done != nil {
		f.done <- struct{}{}
	}
	return 0, nil
}

func (f *fakeDailyFlusher) totalRows() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.flushed {
		n += len(b)
	}
	return n
}

// TestWriterEnqueueFull 校验队列满时 EnqueueRows 返回 false（背压信号），空批为安全空操作。
func TestWriterEnqueueFull(t *testing.T) {
	w := NewAsyncDailyWriter()
	w.queueCapacity = 1
	RegisterFlusher(w, RouteKindMetricSample, (&fakeDailyFlusher{}).FlushDaily)

	if !EnqueueRows(w, RouteKindMetricSample, []model.MetricSampleV2(nil)) {
		t.Fatalf("空批应为安全空操作返回 true")
	}
	if !EnqueueRows(w, RouteKindMetricSample, []model.MetricSampleV2{{ServerID: "s1"}}) {
		t.Fatalf("首次入队应成功")
	}
	if EnqueueRows(w, RouteKindMetricSample, []model.MetricSampleV2{{ServerID: "s2"}}) {
		t.Fatalf("队列满时应返回 false")
	}
}

// TestWriterEnqueueUnregisteredRoute 校验未注册路由入队返回 false（装配错误不 panic、不吞批）。
func TestWriterEnqueueUnregisteredRoute(t *testing.T) {
	w := NewAsyncDailyWriter()
	if EnqueueRows(w, "no_such_route", []model.MetricSampleV2{{ServerID: "s1"}}) {
		t.Fatalf("未注册路由应返回 false")
	}
}

// TestWriterFlushOnSize 校验攒够行数即 flush（一批 200 行触发）。
func TestWriterFlushOnSize(t *testing.T) {
	sink := &fakeDailyFlusher{done: make(chan struct{}, 4)}
	w := NewAsyncDailyWriter()
	RegisterFlusher(w, RouteKindMetricSample, sink.FlushDaily)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	rows := make([]model.MetricSampleV2, w.flushRows)
	if !EnqueueRows(w, RouteKindMetricSample, rows) {
		t.Fatalf("入队失败")
	}
	select {
	case <-sink.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("攒够行数应触发 flush，超时未 flush")
	}
	if got := sink.totalRows(); got != w.flushRows {
		t.Fatalf("应 flush %d 行，实际 %d", w.flushRows, got)
	}
}

// TestWriterFlushOnTimeout 校验不足行数但超时也 flush。
func TestWriterFlushOnTimeout(t *testing.T) {
	sink := &fakeDailyFlusher{done: make(chan struct{}, 4)}
	w := NewAsyncDailyWriter()
	w.flushInterval = 50 * time.Millisecond // 缩短超时加速测试
	RegisterFlusher(w, RouteKindMetricSample, sink.FlushDaily)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	EnqueueRows(w, RouteKindMetricSample, []model.MetricSampleV2{{ServerID: "s1"}, {ServerID: "s2"}}) // 仅 2 行，不足 200
	select {
	case <-sink.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("超时应触发 flush，未 flush")
	}
	if got := sink.totalRows(); got != 2 {
		t.Fatalf("超时应 flush 2 行，实际 %d", got)
	}
}

// TestWriterRouteIsolation 校验攒批按路由键分桶：两路由各自 flush 各自的行，互不串批。
func TestWriterRouteIsolation(t *testing.T) {
	sinkA := &fakeDailyFlusher{done: make(chan struct{}, 4)}
	sinkB := &fakeDailyFlusher{done: make(chan struct{}, 4)}
	w := NewAsyncDailyWriter()
	w.flushInterval = 50 * time.Millisecond // 缩短超时加速测试
	RegisterFlusher(w, "route_a", sinkA.FlushDaily)
	RegisterFlusher(w, "route_b", sinkB.FlushDaily)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	EnqueueRows(w, "route_a", []model.MetricSampleV2{{ServerID: "a1"}})
	EnqueueRows(w, "route_b", []model.MetricSampleV2{{ServerID: "b1"}, {ServerID: "b2"}})
	for _, done := range []chan struct{}{sinkA.done, sinkB.done} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("两路由都应各自 flush，超时未 flush")
		}
	}
	if got := sinkA.totalRows(); got != 1 {
		t.Fatalf("路由 a 应 flush 1 行，实际 %d", got)
	}
	if got := sinkB.totalRows(); got != 2 {
		t.Fatalf("路由 b 应 flush 2 行，实际 %d", got)
	}
}

// TestWriterDiscardOnRepeatedFailure 校验多次重试仍失败即丢弃并累计计数（暴露 /system）。
func TestWriterDiscardOnRepeatedFailure(t *testing.T) {
	sink := &fakeDailyFlusher{err: errors.New("库已停")}
	w := NewAsyncDailyWriter()
	w.maxRetries = 1 // 缩短重试加速测试
	RegisterFlusher(w, RouteKindMetricSample, sink.FlushDaily)

	rt := w.routes[RouteKindMetricSample]
	w.flushWithRetry(rt, []writeBatch{{rows: make([]model.MetricSampleV2, 3), n: 3}}, 3)

	if w.Discarded() != 3 {
		t.Fatalf("多次重试仍失败应丢弃 3 行，实际 %d", w.Discarded())
	}
	if sink.calls != 2 {
		t.Fatalf("应尝试 2 次（首次 + 1 重试），实际 %d", sink.calls)
	}
}

// TestWriterFlushSuccessNoDiscard 校验成功 flush 不计丢弃。
func TestWriterFlushSuccessNoDiscard(t *testing.T) {
	sink := &fakeDailyFlusher{}
	w := NewAsyncDailyWriter()
	RegisterFlusher(w, RouteKindMetricSample, sink.FlushDaily)

	rt := w.routes[RouteKindMetricSample]
	w.flushWithRetry(rt, []writeBatch{{rows: make([]model.MetricSampleV2, 5), n: 5}}, 5)
	if w.Discarded() != 0 {
		t.Fatalf("成功 flush 不应计丢弃，实际 %d", w.Discarded())
	}
	if got := sink.totalRows(); got != 5 {
		t.Fatalf("应 flush 5 行，实际 %d", got)
	}
}
