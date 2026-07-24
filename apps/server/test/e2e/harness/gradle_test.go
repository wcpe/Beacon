//go:build e2e

package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const helperProcessEnv = "BEACON_E2E_HELPER_PROCESS"

func TestGradleHelperProcess(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		return
	}
	switch os.Getenv("BEACON_E2E_HELPER_MODE") {
	case "exit-zero":
		os.Exit(0)
	case "exit-seven":
		os.Exit(7)
	case "delay-zero":
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	case "delay-half":
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	case "block":
		time.Sleep(time.Hour)
	default:
		os.Exit(2)
	}
}

func TestGradleProcReportsNaturalEarlyExit(t *testing.T) {
	proc := startHelperGradleProc(t, "exit-seven", ":agent-e2e:servePaper")
	waitGradleDone(t, proc)

	err := proc.CheckEarlyExit()
	if err == nil {
		t.Fatal("自然早退应返回错误")
	}
	for _, want := range []string{proc.task, "exit status 7", proc.stdoutPath, proc.stderrPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("早退错误缺少 %q：%v", want, err)
		}
	}
	if !filepath.IsAbs(proc.stdoutPath) || !filepath.IsAbs(proc.stderrPath) {
		t.Fatalf("日志路径必须为绝对路径：stdout=%s stderr=%s", proc.stdoutPath, proc.stderrPath)
	}
	movedLog := proc.stdoutPath + ".moved"
	if err := os.Rename(proc.stdoutPath, movedLog); err != nil {
		t.Fatalf("waiter 完成后应关闭日志句柄：%v", err)
	}
	if err := os.Rename(movedLog, proc.stdoutPath); err != nil {
		t.Fatalf("恢复测试日志文件名失败：%v", err)
	}

	proc.Stop()
	proc.Stop()
}

func TestGradleProcReportsZeroExitBeforeStop(t *testing.T) {
	proc := startHelperGradleProc(t, "exit-zero", ":agent-e2e:serveProxy")
	waitGradleDone(t, proc)

	err := proc.CheckEarlyExit()
	if err == nil || !strings.Contains(err.Error(), "退出码 0") {
		t.Fatalf("就绪前零码退出也应视为早退：%v", err)
	}
	proc.Stop()
}

func TestGradleProcStopIsConcurrentAndIdempotent(t *testing.T) {
	proc := startHelperGradleProc(t, "block", ":agent-e2e:serveDirectory")

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proc.Stop()
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("并发 Stop 未及时完成")
	}
	if err := proc.CheckEarlyExit(); err != nil {
		t.Fatalf("主动 Stop 不应报告早退：%v", err)
	}
	proc.Stop()
}

func TestGradleProcStopFallsBackAfterKillTreeFailure(t *testing.T) {
	proc := startHelperGradleProc(t, "block", ":agent-e2e:serveProxy")
	proc.killTreeFn = func(*exec.Cmd) error { return errors.New("注入整树终止失败") }
	rootCalled := false
	proc.killRootFn = func(process *os.Process) error {
		rootCalled = true
		return process.Kill()
	}

	err := proc.Stop()
	if !rootCalled {
		t.Fatal("整树终止失败后应尝试根进程 Kill 回退")
	}
	if err == nil || !strings.Contains(err.Error(), "注入整树终止失败") {
		t.Fatalf("Stop 应保留整树终止错误并返回中文诊断：%v", err)
	}
	waitGradleDone(t, proc)
}

func TestGradleProcStopWaitIsBounded(t *testing.T) {
	proc := startHelperGradleProc(t, "block", ":agent-e2e:serveDirectory")
	proc.killTreeFn = func(*exec.Cmd) error { return errors.New("注入整树终止失败") }
	proc.killRootFn = func(*os.Process) error { return errors.New("注入根进程终止失败") }
	proc.stopWaitTimeout = 100 * time.Millisecond

	started := time.Now()
	err := proc.Stop()
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("Stop 应在有界等待后返回，实际耗时 %s：%v", elapsed, err)
	}
	if err == nil {
		t.Fatal("终止与等待均失败时 Stop 应返回诊断")
	}
	for _, want := range []string{"等待进程退出超时", "注入整树终止失败", "注入根进程终止失败", proc.stdoutPath, proc.stderrPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Stop 诊断缺少 %q：%v", want, err)
		}
	}

	if err := proc.cmd.Process.Kill(); err != nil {
		t.Fatalf("清理测试根进程失败：%v", err)
	}
	waitGradleDone(t, proc)
}

func TestWaitForConditionStopsOnGradleExit(t *testing.T) {
	proc := startHelperGradleProc(t, "delay-zero", ":agent-e2e:servePaper")
	started := time.Now()
	err := WaitForCondition(5*time.Second, 2*time.Second, proc, func(context.Context) bool { return false })
	if err == nil {
		t.Fatal("Gradle 早退时等待应失败")
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("guard 应在一个轮询周期内失败，实际耗时 %s：%v", elapsed, err)
	}
	for _, want := range []string{proc.task, proc.stdoutPath, proc.stderrPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("guard 错误缺少 %q：%v", want, err)
		}
	}
	proc.Stop()
}

func startHelperGradleProc(t *testing.T, mode, task string) *GradleProc {
	t.Helper()
	dir := t.TempDir()
	outLog := filepath.Join(dir, "helper.out.log")
	errLog := filepath.Join(dir, "helper.err.log")
	env := append(os.Environ(), helperProcessEnv+"=1", "BEACON_E2E_HELPER_MODE="+mode)
	cmd, outFile, errFile, err := spawn(dir, os.Args[0], []string{"-test.run=^TestGradleHelperProcess$"}, env, outLog, errLog)
	if err != nil {
		t.Fatalf("启动测试子进程失败：%v", err)
	}
	return newGradleProc(task, cmd, outFile, errFile, outLog, errLog)
}

func waitGradleDone(t *testing.T, proc *GradleProc) {
	t.Helper()
	select {
	case <-proc.Done():
	case <-time.After(5 * time.Second):
		proc.Stop()
		t.Fatal("等待测试子进程退出超时")
	}
}
