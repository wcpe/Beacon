// 命令 beacon-launcher 是 beacon 控制面的常驻监督进程（FR-96，见 ADR-0045）。
//
// 职责严格收窄、极薄（仅标准库 os/exec + os，不连 DB、不碰业务逻辑、不引第三方依赖）：以子进程方式启动同目录的
// beacon 主进程（透传命令行参数与环境变量、继承 stdout/stderr），等其退出后按退出码协议（internal/exitcode）决策——
// 正常退出则跟随退出、崩溃/信号退出按固定间隔重启并设连续失败上限、请求更新重启则用主进程落位的 pending 新二进制
// 原子换二进制后重启。使控制面无需外部 systemd/docker 即自动重启。本期不自更新（实际更新逻辑在 FR-97）。
package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/wcpe/Beacon/internal/exitcode"
	"github.com/wcpe/Beacon/internal/version"
)

const (
	// restartIntervalSec 崩溃后固定重启间隔（秒）。简单优先：固定间隔，不做指数退避。
	restartIntervalSec = 3
	// maxConsecutiveRestarts 连续崩溃重启上限，超过即停并打 ERROR，避免疯狂重启。
	maxConsecutiveRestarts = 5
)

func main() {
	slog.Info("beacon-launcher 监督进程启动", "版本", version.Version)

	paths, err := resolvePaths()
	if err != nil {
		slog.Error("解析二进制路径失败，launcher 退出", "错误", err)
		os.Exit(exitcode.Crash)
	}
	slog.Info("将监督 beacon 主进程", "运行路径", paths.run, "pending 路径", paths.pending)

	sup := &supervisor{
		runChild:    func() (int, error) { return runChild(paths.run) },
		swapBinary:  func() error { return swapBinaryFiles(paths.run, paths.pending) },
		maxRestarts: maxConsecutiveRestarts,
		sleep:       func() { time.Sleep(restartIntervalSec * time.Second) },
	}
	os.Exit(sup.run())
}

// binaryPaths 持有运行二进制与 pending 新二进制的绝对路径。
type binaryPaths struct {
	run     string
	pending string
}

// resolvePaths 据 launcher 自身所在目录推导 beacon 运行路径与 pending 新二进制路径。
// 约定：beacon 主二进制与 launcher 同目录，名为 beacon[.exe]；pending 为同目录 beacon.new[.exe]。
func resolvePaths() (binaryPaths, error) {
	self, err := os.Executable()
	if err != nil {
		return binaryPaths{}, err
	}
	dir := filepath.Dir(self)
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return binaryPaths{
		run:     filepath.Join(dir, "beacon"+suffix),
		pending: filepath.Join(dir, "beacon.new"+suffix),
	}, nil
}

// runChild 以子进程方式启动 beacon 主进程并阻塞到其退出，返回退出码。
// 透传 launcher 自身命令行参数（如 -config）与全部环境变量，继承 stdout/stderr/stdin。
func runChild(runPath string) (int, error) {
	cmd := exec.Command(runPath, os.Args[1:]...) // #nosec G204 -- 运行路径由 launcher 自身目录推导、非外部输入
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	err := cmd.Wait()
	if err == nil {
		return exitcode.OK, nil
	}
	// 子进程以非 0 退出（含被信号杀死的 128+signum）：从 ExitError 取退出码交监督决策。
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	// 既非正常退出又拿不到退出码（极少见）：当作崩溃。
	return exitcode.Crash, nil
}
