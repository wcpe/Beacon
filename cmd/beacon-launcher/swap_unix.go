//go:build !windows

package main

import (
	"fmt"
	"os"
)

// swapBinaryFiles 在类 Unix 平台用 pending 新二进制原子覆盖运行路径（FR-96，见 ADR-0045）。
//
// 此函数仅在 beacon 子进程已退出后调用：旧 inode 进程已释放，rename 直接覆盖运行路径即可——
// rename 在同一文件系统上是原子的，旧文件 inode 自动 unlink，无需手动让位（区别于 Windows 的 exe 文件锁）。
// pending 缺失/不可读则返回 error，由调用方保留旧二进制回退。
func swapBinaryFiles(runPath, pendingPath string) error {
	// 校验 pending 存在且为常规文件，缺失即拒绝（回退兜底，绝不在缺 pending 时破坏 runPath）。
	info, err := os.Stat(pendingPath)
	if err != nil {
		return fmt.Errorf("pending 新二进制不可用: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("pending 新二进制不是常规文件: %s", pendingPath)
	}

	// 原子覆盖：rename 同时完成"换内容"与"删旧 inode"。
	if err := os.Rename(pendingPath, runPath); err != nil {
		return fmt.Errorf("用 pending 覆盖运行二进制失败: %w", err)
	}
	return nil
}
