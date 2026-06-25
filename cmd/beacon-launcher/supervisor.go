package main

import (
	"log/slog"

	"github.com/wcpe/Beacon/internal/exitcode"
)

// supervisor 是 launcher 的监督循环：反复启动 beacon 子进程，依退出码协议（见 internal/exitcode、ADR-0045）决策。
//
// 设计上把"怎么跑子进程""怎么换二进制""怎么退避等待"都抽成可注入函数，使决策逻辑可纯单测，
// 真实实现（os/exec 启子进程、跨平台换文件、time.Sleep 退避）在 main.go / swap_*.go 里装配。
type supervisor struct {
	// runChild 启动一次 beacon 子进程并阻塞到其退出，返回退出码（或启动失败 error）。
	runChild func() (int, error)
	// swapBinary 用主进程落位的 pending 新二进制原子替换运行路径；失败返回 error 由调用方回退。
	swapBinary func() error
	// maxRestarts 连续崩溃重启上限：超过即停并以非 0 退出，避免疯狂重启。
	maxRestarts int
	// sleep 在崩溃重启前的固定间隔等待（注入便于测试不真睡）。
	sleep func()
}

// run 执行监督循环，返回 launcher 自身的退出码。
// 协议：子进程正常退出(0) → 跟随退出 0；崩溃/信号退出 → 固定间隔重启 + 连续失败计数，超上限停；
// 请求更新重启(70) → 换二进制后重启（换失败回退按旧二进制重启）。
func (s *supervisor) run() int {
	consecutiveFailures := 0
	for {
		code, err := s.runChild()
		if err != nil {
			// 无法启动子进程（exec 失败）按崩溃处理。
			slog.Error("启动 beacon 子进程失败", "错误", err)
			code = exitcode.Crash
		}

		switch {
		case code == exitcode.OK:
			slog.Info("beacon 子进程正常退出，launcher 随之退出")
			return exitcode.OK

		case code == exitcode.RequestUpdateRestart:
			// 主进程已落位 pending 新二进制，请求换二进制后重启（FR-97 触发，本期仅备好这条出口）。
			consecutiveFailures = 0
			if err := s.swapBinary(); err != nil {
				// 换失败：保留旧二进制、按旧版重启（回滚兜底）。
				slog.Warn("换二进制失败，保留旧二进制并按旧版重启", "错误", err)
			} else {
				slog.Info("已用 pending 新二进制原子替换运行路径，重启以应用更新")
			}
			continue

		case exitcode.IsCrashExit(code):
			// 崩溃或信号退出：固定间隔重启 + 连续失败计数。
			consecutiveFailures++
			slog.Warn("beacon 子进程异常退出", "退出码", code, "连续失败次数", consecutiveFailures)
			if consecutiveFailures > s.maxRestarts {
				slog.Error("连续崩溃超过重启上限，launcher 停止重启并退出", "上限", s.maxRestarts)
				return code
			}
			s.sleep()
			slog.Info("按固定间隔后重启 beacon 子进程", "第几次重启", consecutiveFailures)
		}
	}
}
