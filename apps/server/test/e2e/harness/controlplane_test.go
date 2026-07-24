//go:build e2e

package harness

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFinalizeControlPlaneBlocksLeakedDynamicTokenAfterExplicitStop(t *testing.T) {
	configureArtifactSecretFiles(t)
	cp := startHelperControlPlane(t, "block")
	if err := cp.StopE(); err != nil {
		t.Fatalf("显式停止控制面失败：%v", err)
	}
	const token = "test-secret-cleanup-leak"
	captureStdout(t, func() {
		if err := registerArtifactSecret(token); err != nil {
			t.Fatalf("登记动态 token 失败：%v", err)
		}
	})
	logDir := filepath.Join(cp.repoRoot, ".tmp")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("创建日志目录失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "late-leak.log"), []byte(token), 0o600); err != nil {
		t.Fatalf("写入延迟泄漏日志失败：%v", err)
	}
	err := finalizeControlPlane(cp)
	if err == nil || !strings.Contains(err.Error(), "late-leak.log") {
		t.Fatalf("最终清理必须阻断控制面提前停止后的动态 token 泄漏：%v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("最终扫描错误不得泄漏 token：%v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "e2e-artifact-secret-leak")); err != nil {
		t.Fatalf("最终扫描必须创建归档阻断标记：%v", err)
	}
}

func TestControlPlaneStopEIsConcurrentAndIdempotent(t *testing.T) {
	cp := startHelperControlPlane(t, "block")

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- cp.StopE()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发 StopE 不应失败：%v", err)
		}
	}
	if err := cp.StopE(); err != nil {
		t.Fatalf("重复 StopE 不应失败：%v", err)
	}
}

func TestControlPlaneStopEReportsKillTreeFailure(t *testing.T) {
	cp := startHelperControlPlane(t, "block")
	cp.killTreeFn = func(*exec.Cmd) error { return errors.New("注入控制面整树终止失败") }
	rootCalled := false
	cp.killRootFn = func(process *os.Process) error {
		rootCalled = true
		return process.Kill()
	}

	err := cp.StopE()
	if !rootCalled {
		t.Fatal("整树终止失败后应尝试终止控制面根进程")
	}
	if err == nil || !strings.Contains(err.Error(), "注入控制面整树终止失败") {
		t.Fatalf("StopE 应保留整树终止错误：%v", err)
	}
}

func TestControlPlaneStopEWaitIsBounded(t *testing.T) {
	cp := startHelperControlPlane(t, "block")
	cp.killTreeFn = func(*exec.Cmd) error { return errors.New("注入控制面整树终止失败") }
	cp.killRootFn = func(*os.Process) error { return errors.New("注入控制面根进程终止失败") }
	cp.stopWaitTimeout = 100 * time.Millisecond

	started := time.Now()
	err := cp.StopE()
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("StopE 应有界返回，实际耗时 %s：%v", elapsed, err)
	}
	if err == nil {
		t.Fatal("终止与等待均失败时 StopE 应返回诊断")
	}
	for _, want := range []string{"等待进程退出超时", "注入控制面整树终止失败", "注入控制面根进程终止失败", cp.stdoutPath, cp.stderrPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("StopE 诊断缺少 %q：%v", want, err)
		}
	}

	if err := cp.cmd.Process.Kill(); err != nil {
		t.Fatalf("清理测试控制面根进程失败：%v", err)
	}
	waitControlPlaneDone(t, cp)
}

func TestControlPlaneReadinessStopsOnEarlyExit(t *testing.T) {
	cp := startHelperControlPlane(t, "delay-half")
	started := time.Now()

	err := waitControlPlaneReady("http://127.0.0.1:1", cp)
	if err == nil {
		t.Fatal("控制面启动期早退时就绪等待应失败")
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("控制面早退应在有界窗口内返回，实际耗时 %s：%v", elapsed, err)
	}
	for _, want := range []string{"控制面进程意外早退", cp.stdoutPath, cp.stderrPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("控制面早退诊断缺少 %q：%v", want, err)
		}
	}
	if stopErr := cp.StopE(); stopErr != nil {
		t.Fatalf("清理已早退控制面不应失败：%v", stopErr)
	}
}

func startHelperControlPlane(t *testing.T, mode string) *ControlPlane {
	t.Helper()
	dir := t.TempDir()
	outLog := filepath.Join(dir, "control-plane.out.log")
	errLog := filepath.Join(dir, "control-plane.err.log")
	env := append(os.Environ(), helperProcessEnv+"=1", "BEACON_E2E_HELPER_MODE="+mode)
	cmd, outFile, errFile, err := spawn(dir, os.Args[0], []string{"-test.run=^TestGradleHelperProcess$"}, env, outLog, errLog)
	if err != nil {
		t.Fatalf("启动测试控制面子进程失败：%v", err)
	}
	return newControlPlane(cmd, outFile, errFile, outLog, errLog, dir)
}

func waitControlPlaneDone(t *testing.T, cp *ControlPlane) {
	t.Helper()
	select {
	case <-cp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("等待测试控制面退出超时")
	}
}
