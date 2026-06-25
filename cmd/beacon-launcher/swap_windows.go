//go:build windows

package main

import (
	"fmt"
	"os"
)

// swapBinaryFiles 在 Windows 用 pending 新二进制替换运行路径（FR-96，见 ADR-0045）。
//
// Windows 运行中的 .exe 文件被锁、无法直接覆盖，故走"让位"三步（此函数仅在 beacon 子进程已退出后调用）：
//  1. 旧 beacon.exe rename 为 beacon.old.exe（让出运行路径名）；
//  2. pending（如 beacon.new.exe）rename 为 beacon.exe（新二进制就位）；
//  3. 删除 beacon.old.exe（旧二进制清理，删失败仅 WARN、不影响已就位的新二进制）。
//
// 任一关键步失败均尽力回滚到旧二进制并返回 error，由调用方保留旧版重启（回滚兜底）。
// pending 缺失则在动旧文件前就拒绝，绝不破坏运行路径。
func swapBinaryFiles(runPath, pendingPath string) error {
	// pending 必须存在且为常规文件，缺失即拒绝（不碰 runPath，直接回退）。
	info, err := os.Stat(pendingPath)
	if err != nil {
		return fmt.Errorf("pending 新二进制不可用: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("pending 新二进制不是常规文件: %s", pendingPath)
	}

	oldPath := runPath + ".old"
	// 清理可能残留的上次未删净的 .old，避免 rename 让位时撞名失败。
	_ = os.Remove(oldPath)

	// 步骤 1：旧 exe 让位。
	if err := os.Rename(runPath, oldPath); err != nil {
		return fmt.Errorf("旧 beacon 让位（rename 为 .old）失败: %w", err)
	}

	// 步骤 2：pending 就位为运行路径。
	if err := os.Rename(pendingPath, runPath); err != nil {
		// 就位失败：把让位的旧 exe 改回原名回滚，保证仍能按旧版重启。
		if rbErr := os.Rename(oldPath, runPath); rbErr != nil {
			return fmt.Errorf("pending 就位失败且回滚旧二进制失败: 就位错误=%v 回滚错误=%w", err, rbErr)
		}
		return fmt.Errorf("pending 就位失败，已回滚至旧二进制: %w", err)
	}

	// 步骤 3：删旧二进制（best-effort，删不掉不影响新二进制已就位）。
	if err := os.Remove(oldPath); err != nil {
		// 仅返回 nil + 让上层照常重启；此处不引 slog 以保持本文件纯文件操作，残留 .old 无害、下次让位前会清理。
		return nil
	}
	return nil
}
