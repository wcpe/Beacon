package service

import (
	"testing"
	"time"
)

// fakeCommandExpirer 是 CommandSweeper 的测试替身：记录三类清理各自被调用的次数与入参。
//
// 为什么需要它：issue #145 的根因正是「新增了一类瞬态正文，却没接进同一轮清扫」。
// 用真实库跑只能验证「清扫函数本身正确」，验证不了「清扫函数真的被定期调用」——
// 漏接线时函数再正确也永远不会执行，因此这里对调用本身做断言。
type fakeCommandExpirer struct {
	expireStaleBefore time.Time
	browseClearedAt   time.Time
	logClearedAt      time.Time

	expireStaleCalls int
	browseCalls      int
	logCalls         int
}

func (f *fakeCommandExpirer) ExpireStale(before time.Time) (int64, error) {
	f.expireStaleBefore = before
	f.expireStaleCalls++
	return 0, nil
}

func (f *fakeCommandExpirer) ClearExpiredBrowseResults(now time.Time) (int64, error) {
	f.browseClearedAt = now
	f.browseCalls++
	return 0, nil
}

func (f *fakeCommandExpirer) ClearExpiredLogContents(now time.Time) (int64, error) {
	f.logClearedAt = now
	f.logCalls++
	return 0, nil
}

// TestCommandSweeperClearsExpiredLogContents 一轮清扫必须同时回收浏览正文与日志正文。
//
// 断言的是接线而不是清理实现：日志正文与浏览正文同属 agent 回传瞬态，
// 只清扫其中一类就会出现「另一类永久滞留」——即 issue #145 的原始形态。
func TestCommandSweeperClearsExpiredLogContents(t *testing.T) {
	fake := &fakeCommandExpirer{}
	sweeper := NewCommandSweeper(fake)
	now := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

	sweeper.sweepOnce(now)

	if fake.expireStaleCalls != 1 {
		t.Fatalf("应调用一次 ExpireStale，实际 %d 次", fake.expireStaleCalls)
	}
	if fake.browseCalls != 1 {
		t.Fatalf("应调用一次 ClearExpiredBrowseResults，实际 %d 次", fake.browseCalls)
	}
	if fake.logCalls != 1 {
		t.Fatalf("应调用一次 ClearExpiredLogContents（漏接则日志正文永久滞留），实际 %d 次", fake.logCalls)
	}
	// 两类正文用同一个 now 判定到期，保持「一套口径」。
	if !fake.logClearedAt.Equal(now) || !fake.browseClearedAt.Equal(now) {
		t.Fatalf("两类正文应以同一个 now 判定到期：browse=%v log=%v want=%v", fake.browseClearedAt, fake.logClearedAt, now)
	}
	// ExpireStale 仍按陈旧阈值回溯，不能被改成同一个 now。
	if want := now.Add(-commandStaleAfter); !fake.expireStaleBefore.Equal(want) {
		t.Fatalf("ExpireStale 的 before 应为 now-陈旧阈值(%v)，实际 %v", want, fake.expireStaleBefore)
	}
}

// TestCommandSweeperKeepsGoingWhenOneCleanerFails 单项失败不得阻断其余瞬态的回收。
//
// 为什么重要：三类瞬态互不相关。若 ExpireStale 失败就 return，过期日志正文会跟着不清，
// 变成「一个故障拖住另外两类清理」。
func TestCommandSweeperKeepsGoingWhenOneCleanerFails(t *testing.T) {
	fake := &failingCommandExpirer{fakeCommandExpirer{expireStaleCalls: 0}}
	sweeper := NewCommandSweeper(fake)

	sweeper.sweepOnce(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))

	if fake.logCalls != 1 {
		t.Fatalf("ExpireStale 失败后仍应清扫日志正文，实际 %d 次", fake.logCalls)
	}
	if fake.browseCalls != 1 {
		t.Fatalf("ExpireStale 失败后仍应清扫浏览正文，实际 %d 次", fake.browseCalls)
	}
}

// failingCommandExpirer 让 ExpireStale 恒失败，其余两项正常。
type failingCommandExpirer struct {
	fakeCommandExpirer
}

func (f *failingCommandExpirer) ExpireStale(time.Time) (int64, error) {
	f.expireStaleCalls++
	return 0, errCommandSweepTest
}

// errCommandSweepTest 是清理器用例里的固定错误，避免为此引入新依赖。
var errCommandSweepTest = constSweepTestError("模拟清理失败")

type constSweepTestError string

func (e constSweepTestError) Error() string { return string(e) }
