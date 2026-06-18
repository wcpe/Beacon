//go:build e2e

package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GradleProc 持有一个 gradle 服务端子进程（runServer / runBungee）与其日志句柄。
type GradleProc struct {
	cmd     *exec.Cmd
	outFile *os.File
	errFile *os.File
}

// StartGradleTask 在 agent/ 目录起一个 gradle 任务（task 如 :agent-e2e:runServer），
// 强制带 --no-daemon --console=plain：让 runServer fork 的 JVM 落在同一棵进程树，
// 配合整树击杀杀得干净（无需按端口杀）。props 为额外 -P 属性（如 -Pe2eMcPort=25566）。
// stdout/stderr 重定向到 .tmp/<logPrefix>.{out,err}.log，stdin 不继承。
func StartGradleTask(repoRoot, task string, props []string, logPrefix string) (*GradleProc, error) {
	tmpDir := filepath.Join(repoRoot, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 .tmp 目录失败：%w", err)
	}
	outLog := filepath.Join(tmpDir, logPrefix+".out.log")
	errLog := filepath.Join(tmpDir, logPrefix+".err.log")

	// 关键：--no-daemon 让 gradle 与 fork 的 JVM 同树；--console=plain 关掉富终端控制字符。
	args := append([]string{task, "--no-daemon", "--console=plain"}, props...)
	workDir := filepath.Join(repoRoot, "agent")

	cmd, outFile, errFile, err := spawn(workDir, gradlewPath(repoRoot), args, os.Environ(), outLog, errLog)
	if err != nil {
		return nil, err
	}
	return &GradleProc{cmd: cmd, outFile: outFile, errFile: errFile}, nil
}

// Stop 整树击杀 gradle 服务端及其 fork 的 JVM，并回收日志句柄。
func (g *GradleProc) Stop() {
	if g == nil {
		return
	}
	stopProc(g.cmd, g.outFile, g.errFile)
}
