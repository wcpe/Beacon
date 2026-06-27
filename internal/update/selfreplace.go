package update

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// 控制面单进程二进制自替换 + 自动回滚（FR-119，见 ADR-0053，取代 launcher 双进程模型）。
//
// 核心：Windows 允许 rename 运行中的 exe（仅移动目录项、已打开句柄仍指原 inode），故主进程可在
// 单进程内完成「自己让位 → 新版就位 → spawn 新进程 → 旧进程退出」，无需第三方监督进程换文件。
// 下载落位的 pending 与运行二进制同目录同卷，全程 rename 原子，无需跨卷复制。

const (
	// maxStartAttempts 换版后新版连续启动尝试上限：达此值即自动回退旧版（容忍 maxStartAttempts-1 次崩溃）。
	maxStartAttempts = 3
	// sentinelName 换版待验证标记文件名（运行二进制同目录）。
	sentinelName = "beacon.update-pending"
	// oldSuffix 旧二进制让位备份后缀（仅留 1 份，供自动回退）。
	oldSuffix = ".old"
	// failedSuffix 自动回退时坏新版的归档后缀（便于事后排查）。
	failedSuffix = ".failed"
)

// 进程级钩子（默认真实实现，单测可替换以断言 spawn / exit 行为而不真起进程 / 真退出）。
var (
	// verifyDuration 新版稳定运行确认期：启动后存活过此时长即判更新成功、清理 sentinel 与 .old 备份。
	// 用 var 便于单测调短；不外部化为配置（YAGNI）。
	verifyDuration = 10 * time.Second
	// osExit 进程退出钩子。
	osExit = os.Exit
	// spawnProcess 拉起新进程钩子；默认以原参数 / 环境 / 工作目录 / 标准流启动 exe。
	spawnProcess = defaultSpawn
)

// sentinelState 是换版待验证标记内容：记新版启动尝试计数与目标版本（计数用于「崩 N 次自动回退」判定）。
type sentinelState struct {
	Attempt int    `json:"attempt"`
	Version string `json:"version"`
}

// SwapAndRespawn 在主进程优雅关停（端口已释放）后执行自替换并重启（FR-119，见 ADR-0053）。
// 让位三步换二进制：runPath→.old、pending→runPath；成功则写 sentinel 标记待验证、spawn 新版；
// 换二进制失败则就地回退（已在 landBinary 内还原旧版）、spawn 旧版继续服务（回退兜底）。
// spawn 后由调用方（main）正常退出本进程，新进程接管同端口。
func SwapAndRespawn(runPath, pendingPath, version string) error {
	if err := landBinary(runPath, pendingPath); err != nil {
		// 换二进制失败：landBinary 已就地还原旧版；端口已被调用方关停释放，须 spawn 旧版恢复服务。
		slog.Error("自替换换二进制失败，保留并重启旧版", "错误", err)
		if spErr := spawnProcess(runPath); spErr != nil {
			return fmt.Errorf("换二进制失败且重启旧版失败: 换=%v 重启=%w", err, spErr)
		}
		return nil
	}
	// 换成功：写 sentinel（attempt=0），新版启动早期自检消费（CheckAndAutoRollback）。
	if err := writeSentinel(runPath, sentinelState{Attempt: 0, Version: version}); err != nil {
		slog.Warn("写换版待验证标记失败，新版将无自动回滚保护", "错误", err)
	}
	slog.Info("已自替换为新版二进制，重启以应用更新", "目标版本", version)
	if err := spawnProcess(runPath); err != nil {
		return fmt.Errorf("自替换后重启新版失败: %w", err)
	}
	return nil
}

// CheckAndAutoRollback 在新进程启动早期（HTTP 起之前）做换版自检（FR-119，见 ADR-0053）。
// 无 sentinel → 非换版后首启，直接返回。有则累加启动计数：达上限则自动回退上一版本（内部 spawn 旧版后退出本进程）；
// 未达上限则启动验证定时器——存活过验证期即确认更新成功、清理 sentinel 与 .old。
func CheckAndAutoRollback(runPath string) {
	st, ok := readSentinel(runPath)
	if !ok {
		return
	}
	st.Attempt++
	if st.Attempt >= maxStartAttempts {
		slog.Error("新版反复启动失败，自动回退到上一版本",
			"启动尝试次数", st.Attempt, "上限", maxStartAttempts, "目标版本", st.Version)
		autoRollback(runPath)
		return // autoRollback 内 osExit，正常不会执行到此
	}
	if err := writeSentinel(runPath, st); err != nil {
		slog.Warn("换版待验证标记累加写入失败", "错误", err)
	}
	slog.Info("换版后首启自检：进入验证期，稳定运行后确认更新成功",
		"启动尝试次数", st.Attempt, "验证期秒", int(verifyDuration.Seconds()), "目标版本", st.Version)
	go func() {
		time.Sleep(verifyDuration)
		ConfirmUpdateSuccess(runPath)
		slog.Info("新版已稳定运行，更新确认成功，已清理备份", "目标版本", st.Version)
	}()
}

// ConfirmUpdateSuccess 确认更新成功：清理 sentinel 与 .old 备份（幂等，不存在即忽略）。
// 由验证定时器与正常关停（管理员 / docker stop 介入=新版已被接受）双路径调用。
func ConfirmUpdateSuccess(runPath string) {
	removeSentinel(runPath)
	if err := os.Remove(runPath + oldSuffix); err != nil && !os.IsNotExist(err) {
		slog.Warn("清理上一版本备份失败", "错误", err)
	}
}

// landBinary 让位三步换二进制：清残留 .old → runPath 让位为 .old → pending 就位为 runPath（FR-119）。
// 两平台统一：Windows / Unix 均允许 rename 运行中的 exe；保留 .old 供自动回退。就位失败就地还原旧版。
// 非 Windows 补可执行位（下载落位的临时文件为 0600，不补则新版不可执行）。
func landBinary(runPath, pendingPath string) error {
	info, err := os.Stat(pendingPath)
	if err != nil {
		return fmt.Errorf("pending 新二进制不可用: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("pending 新二进制不是常规文件: %s", pendingPath)
	}

	oldPath := runPath + oldSuffix
	_ = os.Remove(oldPath) // 清理上次残留，避免让位撞名
	if err := os.Rename(runPath, oldPath); err != nil {
		return fmt.Errorf("旧二进制让位（rename 为 %s）失败: %w", oldSuffix, err)
	}
	if err := os.Rename(pendingPath, runPath); err != nil {
		// 就位失败：把让位的旧版改回原名，保证仍能按旧版启动。
		if rbErr := os.Rename(oldPath, runPath); rbErr != nil {
			return fmt.Errorf("新二进制就位失败且还原旧版失败: 就位=%v 还原=%w", err, rbErr)
		}
		return fmt.Errorf("新二进制就位失败，已还原旧版: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(runPath, 0o755); err != nil {
			slog.Warn("新二进制补可执行位失败", "错误", err)
		}
	}
	return nil
}

// autoRollback 自动回退到上一版本：坏新版归档为 .failed → .old 还原为 runPath → 清 sentinel → spawn 旧版 → 退出本进程。
// 无 .old 可退则清 sentinel 后返回（继续以当前版启动，无更好选择）；任一 rename 失败均尽力还原、不致卡死。
func autoRollback(runPath string) {
	oldPath := runPath + oldSuffix
	if _, err := os.Stat(oldPath); err != nil {
		slog.Error("自动回退失败：无上一版本备份，清除待验证标记后继续以当前版本启动", "错误", err)
		removeSentinel(runPath)
		return
	}
	failedPath := runPath + failedSuffix
	_ = os.Remove(failedPath)
	if err := os.Rename(runPath, failedPath); err != nil {
		slog.Error("自动回退失败：归档坏新版失败，继续以当前版本启动", "错误", err)
		removeSentinel(runPath)
		return
	}
	if err := os.Rename(oldPath, runPath); err != nil {
		_ = os.Rename(failedPath, runPath) // 还原失败：把坏新版改回，保当前版可启动
		slog.Error("自动回退失败：还原旧版失败，已复原当前版本继续启动", "错误", err)
		removeSentinel(runPath)
		return
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(runPath, 0o755)
	}
	removeSentinel(runPath)
	slog.Info("已自动回退到上一版本，重启旧版")
	if err := spawnProcess(runPath); err != nil {
		slog.Error("回退后重启旧版失败", "错误", err)
		osExit(1)
		return
	}
	osExit(0)
}

// defaultSpawn 以原参数 / 环境 / 工作目录 / 标准流启动 exe（自替换 / 回退后拉起新进程）。
func defaultSpawn(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...) // #nosec G204 -- exe 由自身路径推导、非外部输入
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	return cmd.Start()
}

// sentinelPath 返回换版待验证标记的路径（运行二进制同目录）。
func sentinelPath(runPath string) string {
	return filepath.Join(filepath.Dir(runPath), sentinelName)
}

// readSentinel 读换版待验证标记；不存在 / 解析失败均按「无标记」处理（false）。
func readSentinel(runPath string) (sentinelState, bool) {
	data, err := os.ReadFile(sentinelPath(runPath))
	if err != nil {
		return sentinelState{}, false
	}
	var st sentinelState
	if err := json.Unmarshal(data, &st); err != nil {
		slog.Warn("换版待验证标记解析失败，按无标记处理", "错误", err)
		return sentinelState{}, false
	}
	return st, true
}

// writeSentinel 写换版待验证标记（0600）。
func writeSentinel(runPath string, st sentinelState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(sentinelPath(runPath), data, 0o600)
}

// removeSentinel 删换版待验证标记（幂等，不存在即忽略）。
func removeSentinel(runPath string) {
	if err := os.Remove(sentinelPath(runPath)); err != nil && !os.IsNotExist(err) {
		slog.Warn("清理换版待验证标记失败", "错误", err)
	}
}
