//go:build e2e

// Package harness 封装真机 E2E 的跨平台进程与服务生命周期编排：
// 推导仓库根、构建控制面二进制、起停控制面、起停 Gradle 服务端（Paper/BungeeCord）、
// 以及登录与等实例 online 等 HTTP 助手。所有进程用进程组/进程树方式整树击杀，
// 配合 gradle 的 --no-daemon 让 fork 出的 JVM 落在同一棵树，杀得干净（无需按端口杀）。
package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// spawn 在指定工作目录起一个子进程，stdout/stderr 重定向到日志文件、stdin 不继承，
// 并按平台设置进程组（便于整树击杀）。返回已启动的 *exec.Cmd 与打开的日志文件句柄。
func spawn(workDir, name string, args []string, env []string, outLog, errLog string) (*exec.Cmd, *os.File, *os.File, error) {
	outFile, err := os.Create(outLog)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("创建 stdout 日志 %s 失败：%w", outLog, err)
	}
	errFile, err := os.Create(errLog)
	if err != nil {
		_ = outFile.Close()
		return nil, nil, nil, fmt.Errorf("创建 stderr 日志 %s 失败：%w", errLog, err)
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdin = nil // 不继承 stdin，避免子进程阻塞等待输入
	cmd.Stdout = outFile
	cmd.Stderr = errFile
	setPgid(cmd)

	if err := cmd.Start(); err != nil {
		_ = outFile.Close()
		_ = errFile.Close()
		return nil, nil, nil, fmt.Errorf("启动进程 %s 失败：%w", name, err)
	}
	return cmd, outFile, errFile, nil
}

// stopProc 只负责发出终止信号并有界等待唯一 waiter；不得直接调用 Cmd.Wait。
func stopProc(
	cmd *exec.Cmd,
	done <-chan struct{},
	shouldKill bool,
	timeout time.Duration,
	killTreeFn func(*exec.Cmd) error,
	killRootFn func(*os.Process) error,
) (treeErr, rootErr error, timedOut bool) {
	if shouldKill && cmd != nil {
		treeErr = killTreeFn(cmd)
		if treeErr != nil && cmd.Process != nil {
			rootErr = killRootFn(cmd.Process)
		}
	}
	if done == nil {
		return treeErr, rootErr, true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return treeErr, rootErr, false
	case <-timer.C:
		return treeErr, rootErr, true
	}
}

// RepoRoot 由本文件位置经 runtime.Caller 回溯到仓库根（apps/server/test/e2e/harness → 上六级）。
func RepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("无法经 runtime.Caller 定位 harness 源文件")
	}
	// file = <repo>/apps/server/test/e2e/harness/process.go，向上六级回到 <repo>。
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))))
	return root, nil
}

// BuildBeacon 把控制面构建到 .tmp/beacon-e2e[.exe]，返回二进制绝对路径。
func BuildBeacon(repoRoot string) (string, error) {
	tmpDir := filepath.Join(repoRoot, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("创建 .tmp 目录失败：%w", err)
	}
	binName := "beacon-e2e"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(tmpDir, binName)

	cmd := exec.Command("go", "build", "-o", binPath, "./apps/server/cmd/beacon")
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("构建控制面失败：%w\n%s", err, string(out))
	}
	return binPath, nil
}

// gradlewPath 返回平台对应的 gradle 包装器路径（apps/agent/gradlew 或 apps/agent/gradlew.bat）。
func gradlewPath(repoRoot string) string {
	name := "gradlew"
	if runtime.GOOS == "windows" {
		name = "gradlew.bat"
	}
	return filepath.Join(repoRoot, "apps", "agent", name)
}
