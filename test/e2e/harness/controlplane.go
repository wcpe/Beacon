//go:build e2e

package harness

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// ControlPlane 持有控制面子进程与日志句柄，提供启动就绪等待与整树停止。
type ControlPlane struct {
	cmd     *exec.Cmd
	outFile *os.File
	errFile *os.File
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
	env := append(os.Environ(),
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
	cp := &ControlPlane{cmd: cmd, outFile: outFile, errFile: errFile}

	// 轮询就绪：登录端点能给出任何 HTTP 响应即说明 HTTP 服务已起（不在意状态码）。
	loginURL := strings.TrimRight(cfg.BaseURL, "/") + "/admin/v1/auth/login"
	if !waitReady(30*time.Second, func() bool {
		resp, err := http.Post(loginURL, "application/json", strings.NewReader("{}"))
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}) {
		cp.Stop()
		return nil, fmt.Errorf("控制面未在预期时间内就绪（见 %s）", errLog)
	}
	return cp, nil
}

// Stop 整树击杀控制面并回收日志句柄。
func (c *ControlPlane) Stop() {
	if c == nil {
		return
	}
	stopProc(c.cmd, c.outFile, c.errFile)
}

// waitReady 在超时内每秒重试 cond，命中即返回 true。
func waitReady(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return cond()
}
