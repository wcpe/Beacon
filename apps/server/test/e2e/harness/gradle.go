//go:build e2e

package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

type gradleProcState uint8

const (
	gradleProcRunning gradleProcState = iota
	gradleProcExited
	gradleProcStopping
	gradleProcStopped

	defaultGradleStopTimeout = 10 * time.Second
)

// GradleProc 独占一个持久 Gradle 任务的等待、早退诊断与整树收尾。
type GradleProc struct {
	cmd             *exec.Cmd
	outFile         *os.File
	errFile         *os.File
	task            string
	stdoutPath      string
	stderrPath      string
	done            chan struct{}
	stopDone        chan struct{}
	stopOnce        sync.Once
	mu              sync.Mutex
	state           gradleProcState
	waitErr         error
	stopErr         error
	killTreeFn      func(*exec.Cmd) error
	killRootFn      func(*os.Process) error
	stopWaitTimeout time.Duration
}

// StartGradleTask 在 apps/agent 下启动持久 Gradle 任务，并把额外环境仅注入子进程。
// args 只放非敏感任务参数；动态凭据必须通过 env 传递，禁止拼入命令行。
func StartGradleTask(repoRoot, task string, args []string, env map[string]string, logPrefix string) (*GradleProc, error) {
	tmpDir := filepath.Join(repoRoot, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 .tmp 目录失败：%w", err)
	}
	outLog, err := filepath.Abs(filepath.Join(tmpDir, logPrefix+".out.log"))
	if err != nil {
		return nil, fmt.Errorf("解析 stdout 日志路径失败：%w", err)
	}
	errLog, err := filepath.Abs(filepath.Join(tmpDir, logPrefix+".err.log"))
	if err != nil {
		return nil, fmt.Errorf("解析 stderr 日志路径失败：%w", err)
	}

	gradleArgs := append([]string{task, "--no-daemon", "--console=plain"}, args...)
	workDir := filepath.Join(repoRoot, "apps", "agent")
	cmd, outFile, errFile, err := spawn(
		workDir, gradlewPath(repoRoot), gradleArgs, processEnv(env), outLog, errLog,
	)
	if err != nil {
		return nil, err
	}
	return newGradleProc(task, cmd, outFile, errFile, outLog, errLog), nil
}

func newGradleProc(task string, cmd *exec.Cmd, outFile, errFile *os.File, outLog, errLog string) *GradleProc {
	proc := &GradleProc{
		cmd: cmd, outFile: outFile, errFile: errFile,
		task: task, stdoutPath: outLog, stderrPath: errLog,
		done: make(chan struct{}), stopDone: make(chan struct{}), state: gradleProcRunning,
		killTreeFn: killTree, killRootFn: killRootProcess, stopWaitTimeout: defaultGradleStopTimeout,
	}
	go proc.wait()
	return proc
}

// Done 返回唯一 waiter 完成后关闭的只读信号。
func (g *GradleProc) Done() <-chan struct{} {
	if g == nil {
		return nil
	}
	return g.done
}

// LogPaths 返回该任务的 stdout/stderr 证据路径。
func (g *GradleProc) LogPaths() (string, string) {
	if g == nil {
		return "", ""
	}
	return g.stdoutPath, g.stderrPath
}

// CleanupGradle 注册不会因 defer 丢失错误的测试清理；停止失败会标记测试失败。
func CleanupGradle(t testing.TB, proc *GradleProc) {
	t.Helper()
	t.Cleanup(func() {
		StopGradle(t, proc)
	})
}

// StopGradle 停止 Gradle 任务并把清理错误报告给测试。
func StopGradle(t testing.TB, proc *GradleProc) {
	t.Helper()
	if err := proc.Stop(); err != nil {
		t.Errorf("停止 Gradle 任务失败：%v", err)
	}
}

// CheckEarlyExit 在任务未进入主动收尾却已退出时返回带证据路径的诊断错误。
func (g *GradleProc) CheckEarlyExit() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != gradleProcExited {
		return nil
	}
	return fmt.Errorf(
		"Gradle 任务 %s 意外早退：退出结果=%s；stdout=%s；stderr=%s",
		g.task, exitResult(g.waitErr), g.stdoutPath, g.stderrPath,
	)
}

// Stop 标记主动收尾、整树终止仍存活的进程，并有界等待唯一 waiter 完成；可重复并发调用。
func (g *GradleProc) Stop() error {
	if g == nil {
		return nil
	}
	g.stopOnce.Do(func() {
		err := g.stop()
		g.mu.Lock()
		g.stopErr = err
		g.mu.Unlock()
		close(g.stopDone)
	})
	<-g.stopDone
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopErr
}

func (g *GradleProc) stop() error {
	shouldKill := g.beginStop()
	var treeErr, rootErr error
	if shouldKill {
		treeErr = g.killTreeFn(g.cmd)
		if treeErr != nil && g.cmd != nil {
			rootErr = g.killRootFn(g.cmd.Process)
		}
	}
	select {
	case <-g.done:
		if treeErr != nil {
			return g.stopDiagnostic("整树终止失败，已尝试终止根进程", treeErr, rootErr)
		}
		return nil
	case <-time.After(g.stopWaitTimeout):
		return g.stopDiagnostic("等待进程退出超时", treeErr, rootErr)
	}
}

func (g *GradleProc) beginStop() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	shouldKill := g.state == gradleProcRunning
	if g.state != gradleProcStopped {
		g.state = gradleProcStopping
	}
	return shouldKill
}

func (g *GradleProc) stopDiagnostic(reason string, treeErr, rootErr error) error {
	return fmt.Errorf(
		"停止 Gradle 任务 %s 失败：%s；整树终止=%s；根进程回退=%s；等待上限=%s；stdout=%s；stderr=%s",
		g.task, reason, errorResult(treeErr), errorResult(rootErr), g.stopWaitTimeout, g.stdoutPath, g.stderrPath,
	)
}

func killRootProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}

func errorResult(err error) string {
	if err == nil {
		return "无错误"
	}
	return err.Error()
}

func (g *GradleProc) wait() {
	err := g.cmd.Wait()
	_ = g.outFile.Close()
	_ = g.errFile.Close()

	g.mu.Lock()
	g.waitErr = err
	if g.state == gradleProcStopping {
		g.state = gradleProcStopped
	} else {
		g.state = gradleProcExited
	}
	g.mu.Unlock()
	close(g.done)
}

func exitResult(err error) string {
	if err == nil {
		return "退出码 0"
	}
	return err.Error()
}

func processEnv(extra map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+extra[key])
	}
	return env
}
