package main

import (
	"errors"
	"testing"

	"github.com/wcpe/Beacon/internal/exitcode"
)

// fakeRunner 以预设的退出码序列模拟一连串子进程运行，记录被调用次数。
// 当调用次数超过序列长度时返回最后一个码（便于模拟"持续崩溃"）。
type fakeRunner struct {
	codes   []int
	err     error
	calls   int
	swaps   int
	swapErr error
}

func (f *fakeRunner) run() (int, error) {
	idx := f.calls
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if idx >= len(f.codes) {
		return f.codes[len(f.codes)-1], nil
	}
	return f.codes[idx], nil
}

func (f *fakeRunner) swap() error {
	f.swaps++
	return f.swapErr
}

// newTestSupervisor 构造一个用假运行器 + 即时退避（不真睡）的监督器。
func newTestSupervisor(r *fakeRunner) *supervisor {
	return &supervisor{
		runChild:    r.run,
		swapBinary:  r.swap,
		maxRestarts: 3,
		sleep:       func() {}, // 测试里不真睡，避免拖慢
	}
}

// 正常退出（码 0）：launcher 跟随退出、不重启。
func TestSupervisor_NormalExitStops(t *testing.T) {
	r := &fakeRunner{codes: []int{exitcode.OK}}
	sup := newTestSupervisor(r)

	code := sup.run()

	if r.calls != 1 {
		t.Fatalf("正常退出应只运行子进程一次，实际 %d 次", r.calls)
	}
	if code != exitcode.OK {
		t.Fatalf("launcher 应以 0 退出，实际 %d", code)
	}
	if r.swaps != 0 {
		t.Fatalf("正常退出不应触发换二进制，实际 %d 次", r.swaps)
	}
}

// 崩溃（码 1）后再正常退出：应重启一次，第二次正常退出即停。
func TestSupervisor_CrashThenRecover(t *testing.T) {
	r := &fakeRunner{codes: []int{exitcode.Crash, exitcode.OK}}
	sup := newTestSupervisor(r)

	code := sup.run()

	if r.calls != 2 {
		t.Fatalf("崩溃后应重启一次共运行两次，实际 %d 次", r.calls)
	}
	if code != exitcode.OK {
		t.Fatalf("最终正常退出应以 0 退出，实际 %d", code)
	}
}

// 信号退出码（130 SIGINT / 137 SIGKILL）视作崩溃同样重启。
func TestSupervisor_SignalExitTreatedAsCrash(t *testing.T) {
	r := &fakeRunner{codes: []int{130, 137, exitcode.OK}}
	sup := newTestSupervisor(r)

	code := sup.run()

	if r.calls != 3 {
		t.Fatalf("信号退出应按崩溃重启，期望运行 3 次，实际 %d 次", r.calls)
	}
	if code != exitcode.OK {
		t.Fatalf("最终正常退出应以 0 退出，实际 %d", code)
	}
}

// 连续崩溃超过上限：停并以崩溃码退出，重启次数不超过 maxRestarts。
func TestSupervisor_CrashLoopHitsCap(t *testing.T) {
	r := &fakeRunner{codes: []int{exitcode.Crash}} // 永远崩溃
	sup := newTestSupervisor(r)                    // maxRestarts=3

	code := sup.run()

	// 首次运行 + 3 次重启 = 4 次运行；之后达上限停。
	if r.calls != 4 {
		t.Fatalf("崩溃循环应在 1 次初始 + %d 次重启后停，期望运行 4 次，实际 %d 次", sup.maxRestarts, r.calls)
	}
	if code == exitcode.OK {
		t.Fatalf("超过重启上限应以非 0 退出，实际 %d", code)
	}
}

// 请求更新重启会重置连续失败计数：崩溃数次后一次更新重启清零，再崩溃不应立即触发上限。
// （正常退出 0 会直接终结 launcher，故"恢复重置"语义只在更新重启这条不终结的路径上可验。）
func TestSupervisor_UpdateRestartResetsCounter(t *testing.T) {
	// 崩溃 2 次（计数到 2）→ 更新重启（计数清零、换二进制）→ 再崩溃 2 次（重新从 1 计起）→ 正常退出。
	r := &fakeRunner{codes: []int{
		exitcode.Crash, exitcode.Crash, exitcode.RequestUpdateRestart,
		exitcode.Crash, exitcode.Crash, exitcode.OK,
	}}
	sup := newTestSupervisor(r) // maxRestarts=3，全程连续崩溃从未达 3 因被更新重启打断

	code := sup.run()

	if code != exitcode.OK {
		t.Fatalf("更新重启重置计数后未触发上限，应以 0 退出，实际 %d", code)
	}
	if r.calls != 6 {
		t.Fatalf("应完整跑完 6 段，实际 %d 次", r.calls)
	}
	if r.swaps != 1 {
		t.Fatalf("中途一次更新重启应换二进制一次，实际 %d 次", r.swaps)
	}
}

// 请求更新重启码 + 换二进制成功：换一次后重启，再正常退出即停。
func TestSupervisor_RequestUpdateSwapsAndRestarts(t *testing.T) {
	r := &fakeRunner{codes: []int{exitcode.RequestUpdateRestart, exitcode.OK}}
	sup := newTestSupervisor(r)

	code := sup.run()

	if r.swaps != 1 {
		t.Fatalf("请求更新重启应触发一次换二进制，实际 %d 次", r.swaps)
	}
	if r.calls != 2 {
		t.Fatalf("换二进制后应重启共运行两次，实际 %d 次", r.calls)
	}
	if code != exitcode.OK {
		t.Fatalf("换后正常退出应以 0 退出，实际 %d", code)
	}
}

// 请求更新重启但换二进制失败：回退按旧二进制重启（不计入崩溃上限），不疯狂换。
func TestSupervisor_RequestUpdateSwapFailFallsBack(t *testing.T) {
	r := &fakeRunner{codes: []int{exitcode.RequestUpdateRestart, exitcode.OK}, swapErr: errors.New("pending 缺失")}
	sup := newTestSupervisor(r)

	code := sup.run()

	if r.swaps != 1 {
		t.Fatalf("应尝试一次换二进制，实际 %d 次", r.swaps)
	}
	if r.calls != 2 {
		t.Fatalf("换失败应回退按旧二进制重启共运行两次，实际 %d 次", r.calls)
	}
	if code != exitcode.OK {
		t.Fatalf("回退后正常退出应以 0 退出，实际 %d", code)
	}
}

// 启动子进程本身失败（无法 exec）视作崩溃，按崩溃策略计数重启直至上限。
func TestSupervisor_RunErrorTreatedAsCrash(t *testing.T) {
	r := &fakeRunner{err: errors.New("无法启动")}
	sup := newTestSupervisor(r)

	code := sup.run()

	if r.calls != 4 {
		t.Fatalf("启动失败应按崩溃重启至上限，期望 4 次，实际 %d 次", r.calls)
	}
	if code == exitcode.OK {
		t.Fatalf("启动持续失败应以非 0 退出，实际 %d", code)
	}
}
