//go:build e2e

package harness

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ControlPlaneConfig 描述控制面启动所需的参数（敏感项由调用方从 env 注入，不写死）。
type ControlPlaneConfig struct {
	BinPath        string // 控制面二进制路径（BuildBeacon 产出）
	RepoRoot       string // 仓库根（日志落 .tmp 用）
	BaseURL        string // 控制面地址，如 http://localhost:8848
	DBDriver       string // 数据库驱动：sqlite | mysql
	DBDSN          string // 数据库 DSN（sqlite 为文件路径，mysql 为连接串）
	AdminPassword  string // 管理员口令
	AuthSecret     string // 令牌签名密钥
	BootstrapToken string // agent 共享令牌（X-Beacon-Token）
	LogPrefix      string // 日志文件名前缀，如 beacon-override，区分多套运行目录
	// 额外环境变量（可选）：在固定注入项之上叠加，用于按 e2e 需要覆盖控制面行为，
	// 如 metrics 用例设 BEACON_METRIC_SAMPLE_INTERVAL_SEC 调小采样间隔。默认 nil 不影响既有调用。
	ExtraEnv map[string]string
}

type controlPlaneState uint8

const (
	controlPlaneRunning controlPlaneState = iota
	controlPlaneExited
	controlPlaneStopping
	controlPlaneStopped

	defaultControlPlaneStopTimeout = 10 * time.Second
)

// ControlPlane 持有控制面子进程的唯一 waiter、日志证据与并发幂等停止状态。
type ControlPlane struct {
	cmd             *exec.Cmd
	outFile         *os.File
	errFile         *os.File
	stdoutPath      string
	stderrPath      string
	done            chan struct{}
	stopDone        chan struct{}
	stopOnce        sync.Once
	mu              sync.Mutex
	state           controlPlaneState
	waitErr         error
	stopErr         error
	killTreeFn      func(*exec.Cmd) error
	killRootFn      func(*os.Process) error
	stopWaitTimeout time.Duration
	repoRoot        string
}

// StartControlPlane 设置环境变量起控制面，重定向日志到 .tmp/<prefix>.{out,err}.log，
// 并轮询 /admin/v1/auth/login 直至控制面可达（任何 HTTP 响应即视为就绪）。
func StartControlPlane(cfg ControlPlaneConfig) (*ControlPlane, error) {
	tmpDir := filepath.Join(cfg.RepoRoot, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 .tmp 目录失败：%w", err)
	}
	prefix := cfg.LogPrefix
	if prefix == "" {
		prefix = "beacon"
	}
	outLog := filepath.Join(tmpDir, prefix+".out.log")
	errLog := filepath.Join(tmpDir, prefix+".err.log")

	// 以当前环境为基底叠加控制面所需变量（保留 PATH 等）。
	// BEACON_HTTP_ADDR 由 BaseURL 推出，使控制面 bind 端口与测试连接地址一致，
	// 消除依赖默认 8848 与仓库根 .env 的干扰；置于 ExtraEnv 之前，仍允许 ExtraEnv 覆盖。
	env := append(os.Environ(),
		"BEACON_HTTP_ADDR="+HTTPAddrFromURL(cfg.BaseURL),
		"BEACON_DB_DRIVER="+cfg.DBDriver,
		"BEACON_DB_DSN="+cfg.DBDSN,
		"BEACON_ADMIN_PASSWORD="+cfg.AdminPassword,
		"BEACON_AUTH_SECRET="+cfg.AuthSecret,
		"BEACON_BOOTSTRAP_TOKEN="+cfg.BootstrapToken,
		"BEACON_LOG_LEVEL=INFO",
	)
	// 叠加可选额外环境变量（置于固定项之后，后写覆盖前写，使 e2e 能按需覆盖控制面行为）。
	for k, v := range cfg.ExtraEnv {
		env = append(env, k+"="+v)
	}

	cmd, outFile, errFile, err := spawn(cfg.RepoRoot, cfg.BinPath, nil, env, outLog, errLog)
	if err != nil {
		return nil, err
	}
	cp := newControlPlane(cmd, outFile, errFile, outLog, errLog, cfg.RepoRoot)

	// 轮询就绪：登录端点能给出任何 HTTP 响应即说明 HTTP 服务已起（不在意状态码）；
	// 启动期进程早退会立即取消请求并返回 stdout/stderr 证据，而不是继续等满 30 秒。
	if err := waitControlPlaneReady(cfg.BaseURL, cp); err != nil {
		if stopErr := cp.StopE(); stopErr != nil {
			return nil, fmt.Errorf("控制面启动失败：%v；且停止失败：%w", err, stopErr)
		}
		return nil, fmt.Errorf("控制面启动失败：%w", err)
	}
	return cp, nil
}

func newControlPlane(cmd *exec.Cmd, outFile, errFile *os.File, outLog, errLog, repoRoot string) *ControlPlane {
	cp := &ControlPlane{
		cmd: cmd, outFile: outFile, errFile: errFile,
		stdoutPath: outLog, stderrPath: errLog, repoRoot: repoRoot,
		done: make(chan struct{}), stopDone: make(chan struct{}), state: controlPlaneRunning,
		killTreeFn: killTree, killRootFn: killRootProcess, stopWaitTimeout: defaultControlPlaneStopTimeout,
	}
	go cp.wait()
	return cp
}

// Done 返回唯一 waiter 完成后关闭的只读信号。
func (c *ControlPlane) Done() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.done
}

// LogPaths 返回控制面的 stdout/stderr 证据路径。
func (c *ControlPlane) LogPaths() (string, string) {
	if c == nil {
		return "", ""
	}
	return c.stdoutPath, c.stderrPath
}

// CheckEarlyExit 在控制面未进入主动收尾却已退出时返回带日志路径的诊断。
func (c *ControlPlane) CheckEarlyExit() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != controlPlaneExited {
		return nil
	}
	return fmt.Errorf(
		"控制面进程意外早退：退出结果=%s；stdout=%s；stderr=%s",
		exitResult(c.waitErr), c.stdoutPath, c.stderrPath,
	)
}

func waitControlPlaneReady(baseURL string, controlPlane *ControlPlane) error {
	loginURL := strings.TrimRight(baseURL, "/") + "/admin/v1/auth/login"
	return WaitForCondition(30*time.Second, time.Second, controlPlane, func(ctx context.Context) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader("{}"))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := DoRequestWithGuard(req, time.Second, controlPlane)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	})
}

// Stop 保留既有无返回值调用兼容性；需要检查阻断错误的主路径应调用 StopE。
func (c *ControlPlane) Stop() {
	_ = c.StopE()
}

// CleanupControlPlane 注册可报告停止错误的测试清理。
func CleanupControlPlane(t testing.TB, controlPlane *ControlPlane) {
	t.Helper()
	t.Cleanup(func() {
		if err := finalizeControlPlane(controlPlane); err != nil {
			t.Errorf("停止控制面或扫描 E2E 归档敏感内容失败：%v", err)
		}
	})
}

func finalizeControlPlane(controlPlane *ControlPlane) error {
	stopErr := controlPlane.StopE()
	scanErr := scanRegisteredArtifactSecrets(controlPlane.repoRoot)
	if stopErr == nil {
		return scanErr
	}
	if scanErr == nil {
		return stopErr
	}
	return fmt.Errorf("停止控制面失败：%v；归档敏感内容扫描失败：%w", stopErr, scanErr)
}

// StopE 并发幂等地整树终止控制面，并有界等待唯一 waiter 回收进程与日志句柄。
func (c *ControlPlane) StopE() error {
	if c == nil {
		return nil
	}
	c.stopOnce.Do(func() {
		shouldKill := c.beginStop()
		treeErr, rootErr, timedOut := stopProc(
			c.cmd, c.done, shouldKill, c.stopWaitTimeout, c.killTreeFn, c.killRootFn,
		)
		var err error
		switch {
		case timedOut:
			err = c.stopDiagnostic("等待进程退出超时", treeErr, rootErr)
		case treeErr != nil:
			err = c.stopDiagnostic("整树终止失败，已尝试终止根进程", treeErr, rootErr)
		}
		c.mu.Lock()
		c.stopErr = err
		c.mu.Unlock()
		close(c.stopDone)
	})
	<-c.stopDone
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopErr
}

func (c *ControlPlane) beginStop() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	shouldKill := c.state == controlPlaneRunning
	if c.state != controlPlaneStopped {
		c.state = controlPlaneStopping
	}
	return shouldKill
}

func (c *ControlPlane) stopDiagnostic(reason string, treeErr, rootErr error) error {
	return fmt.Errorf(
		"停止控制面失败：%s；整树终止=%s；根进程回退=%s；等待上限=%s；stdout=%s；stderr=%s",
		reason, errorResult(treeErr), errorResult(rootErr), c.stopWaitTimeout, c.stdoutPath, c.stderrPath,
	)
}

func (c *ControlPlane) wait() {
	err := c.cmd.Wait()
	_ = c.outFile.Close()
	_ = c.errFile.Close()

	c.mu.Lock()
	c.waitErr = err
	if c.state == controlPlaneStopping {
		c.state = controlPlaneStopped
	} else {
		c.state = controlPlaneExited
	}
	c.mu.Unlock()
	close(c.done)
}
