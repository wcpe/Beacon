package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 在临时目录里建一个"运行二进制"与一个"pending 新二进制"，校验换二进制成功后运行路径变为新内容。
// 本测试不区分平台：调用统一入口 swapBinaryFiles，分别由 swap_windows.go / swap_unix.go 提供实现，
// 二者均应满足"成功后 runPath 内容 == pending 内容、pending 已消费"的契约。
func TestSwapBinaryFiles_Success(t *testing.T) {
	dir := t.TempDir()
	runPath := filepath.Join(dir, "beacon.exe")
	pendingPath := filepath.Join(dir, "beacon.new.exe")

	writeFile(t, runPath, "旧二进制")
	writeFile(t, pendingPath, "新二进制")

	if err := swapBinaryFiles(runPath, pendingPath); err != nil {
		t.Fatalf("换二进制应成功，实际错误：%v", err)
	}

	if got := readFile(t, runPath); got != "新二进制" {
		t.Fatalf("换后运行路径应为新内容，实际 %q", got)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("换后 pending 文件应已被消费/移走，stat err=%v", err)
	}
}

// pending 缺失：换二进制应失败并保留原运行二进制（回滚兜底，由 supervisor 据此回退按旧版重启）。
func TestSwapBinaryFiles_PendingMissingFails(t *testing.T) {
	dir := t.TempDir()
	runPath := filepath.Join(dir, "beacon.exe")
	pendingPath := filepath.Join(dir, "beacon.new.exe")

	writeFile(t, runPath, "旧二进制")
	// 故意不创建 pending

	if err := swapBinaryFiles(runPath, pendingPath); err == nil {
		t.Fatalf("pending 缺失时换二进制应失败")
	}
	// 原运行二进制必须原样保留。
	if got := readFile(t, runPath); got != "旧二进制" {
		t.Fatalf("换失败后运行路径应保留旧内容，实际 %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("写文件 %s 失败：%v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件 %s 失败：%v", path, err)
	}
	return string(b)
}
