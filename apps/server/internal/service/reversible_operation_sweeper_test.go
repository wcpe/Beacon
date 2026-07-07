package service

import (
	"testing"
	"time"
)

// fakeRevOpExpirer 是清理器的测试替身：记录 ExpireStale 收到的 before、返回预置过期条数与窗口。
type fakeRevOpExpirer struct {
	window     int
	expiredN   int64
	gotBefore  time.Time
	calledOnce bool
}

func (f *fakeRevOpExpirer) WindowHours() int { return f.window }
func (f *fakeRevOpExpirer) ExpireStale(before time.Time) (int64, error) {
	f.gotBefore = before
	f.calledOnce = true
	return f.expiredN, nil
}

// sweepOnce 用当前窗口算 before=now-窗口 并调 ExpireStale。
func TestReversibleOpSweeper_SweepOnceUsesWindow(t *testing.T) {
	fake := &fakeRevOpExpirer{window: 6, expiredN: 2}
	sw := NewReversibleOperationSweeper(fake)
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)

	sw.sweepOnce(now)

	if !fake.calledOnce {
		t.Fatalf("sweepOnce 应调用 ExpireStale")
	}
	want := now.Add(-6 * time.Hour)
	if !fake.gotBefore.Equal(want) {
		t.Fatalf("before 应为 now-窗口(%v), got %v", want, fake.gotBefore)
	}
}
