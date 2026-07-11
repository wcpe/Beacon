package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// fakeFlusher 是写入池持久化窄依赖的测试替身：记录 flush 的行、可注入错误、每次成功 flush 发信号。
type fakeFlusher struct {
	mu      sync.Mutex
	flushed [][]model.MetricSampleV2
	calls   int
	err     error
	done    chan struct{}
}

func (f *fakeFlusher) FlushDaily(rows []model.MetricSampleV2) (int, error) {
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

func (f *fakeFlusher) totalRows() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.flushed {
		n += len(b)
	}
	return n
}

// TestWriterEnqueueFull 校验队列满时 Enqueue 返回 false（背压信号），空批为安全空操作。
func TestWriterEnqueueFull(t *testing.T) {
	w := NewMetricIngestWriter(&fakeFlusher{})
	w.queue = make(chan []model.MetricSampleV2, 1)

	if !w.Enqueue(nil) {
		t.Fatalf("空批应为安全空操作返回 true")
	}
	if !w.Enqueue([]model.MetricSampleV2{{ServerID: "s1"}}) {
		t.Fatalf("首次入队应成功")
	}
	if w.Enqueue([]model.MetricSampleV2{{ServerID: "s2"}}) {
		t.Fatalf("队列满时应返回 false")
	}
}

// TestWriterFlushOnSize 校验攒够行数即 flush（一批 200 行触发）。
func TestWriterFlushOnSize(t *testing.T) {
	sink := &fakeFlusher{done: make(chan struct{}, 4)}
	w := NewMetricIngestWriter(sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	rows := make([]model.MetricSampleV2, w.flushRows)
	if !w.Enqueue(rows) {
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
	sink := &fakeFlusher{done: make(chan struct{}, 4)}
	w := NewMetricIngestWriter(sink)
	w.flushInterval = 50 * time.Millisecond // 缩短超时加速测试
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	w.Enqueue([]model.MetricSampleV2{{ServerID: "s1"}, {ServerID: "s2"}}) // 仅 2 行，不足 200
	select {
	case <-sink.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("超时应触发 flush，未 flush")
	}
	if got := sink.totalRows(); got != 2 {
		t.Fatalf("超时应 flush 2 行，实际 %d", got)
	}
}

// TestWriterDiscardOnRepeatedFailure 校验多次重试仍失败即丢弃并累计计数（暴露 /system）。
func TestWriterDiscardOnRepeatedFailure(t *testing.T) {
	sink := &fakeFlusher{err: errors.New("库已停")}
	w := NewMetricIngestWriter(sink)
	w.maxRetries = 1 // 缩短重试加速测试

	w.flushWithRetry(make([]model.MetricSampleV2, 3))

	if w.Discarded() != 3 {
		t.Fatalf("多次重试仍失败应丢弃 3 行，实际 %d", w.Discarded())
	}
	if sink.calls != 2 {
		t.Fatalf("应尝试 2 次（首次 + 1 重试），实际 %d", sink.calls)
	}
}

// TestWriterFlushSuccessNoDiscard 校验成功 flush 不计丢弃。
func TestWriterFlushSuccessNoDiscard(t *testing.T) {
	sink := &fakeFlusher{}
	w := NewMetricIngestWriter(sink)
	w.flushWithRetry(make([]model.MetricSampleV2, 5))
	if w.Discarded() != 0 {
		t.Fatalf("成功 flush 不应计丢弃，实际 %d", w.Discarded())
	}
}
