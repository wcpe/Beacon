package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// hookRecorder 记录 spawn / exit 钩子调用，供断言「拉起了哪个二进制 / 以什么码退出」而不真起进程 / 真退出。
type hookRecorder struct {
	spawnedExe string
	spawnCalls int
	exitCode   int
	exitCalls  int
}

// stubHooks 替换包级 spawn / exit 钩子为记录器，测试结束还原。
func stubHooks(t *testing.T) *hookRecorder {
	t.Helper()
	rec := &hookRecorder{exitCode: -1}
	origSpawn, origExit := spawnProcess, osExit
	spawnProcess = func(exe string) error { rec.spawnedExe = exe; rec.spawnCalls++; return nil }
	osExit = func(code int) { rec.exitCode = code; rec.exitCalls++ }
	t.Cleanup(func() { spawnProcess = origSpawn; osExit = origExit })
	return rec
}

// writeFile 写一个文件并断言成功（测试辅助）。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件 %s 失败: %v", path, err)
	}
}

// readFile 读文件内容并断言成功（测试辅助）。
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件 %s 失败: %v", path, err)
	}
	return string(data)
}

// TestLandBinarySwapsAndKeepsOld 让位三步：新版就位运行路径、旧版保留为 .old、pending 消失。
func TestLandBinarySwapsAndKeepsOld(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	pending := filepath.Join(dir, "beacon.new")
	writeFile(t, run, "旧版")
	writeFile(t, pending, "新版")

	if err := landBinary(run, pending); err != nil {
		t.Fatalf("landBinary 应成功: %v", err)
	}
	if got := readFile(t, run); got != "新版" {
		t.Fatalf("运行路径应为新版，实际 %q", got)
	}
	if got := readFile(t, run+oldSuffix); got != "旧版" {
		t.Fatalf(".old 应保留旧版，实际 %q", got)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal("pending 应已 rename 消失")
	}
}

// TestLandBinaryRejectsMissingPending pending 缺失即拒绝，绝不破坏运行路径、不产生 .old。
func TestLandBinaryRejectsMissingPending(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	writeFile(t, run, "旧版")

	if err := landBinary(run, filepath.Join(dir, "不存在.new")); err == nil {
		t.Fatal("pending 缺失应返回错误")
	}
	if got := readFile(t, run); got != "旧版" {
		t.Fatalf("运行路径不应被破坏，实际 %q", got)
	}
	if _, err := os.Stat(run + oldSuffix); !os.IsNotExist(err) {
		t.Fatal("不应产生 .old")
	}
}

// TestSentinelRoundTrip 待验证标记写读往返一致。
func TestSentinelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	if err := writeSentinel(run, sentinelState{Attempt: 2, Version: "v1.2.3"}); err != nil {
		t.Fatalf("写 sentinel 失败: %v", err)
	}
	st, ok := readSentinel(run)
	if !ok {
		t.Fatal("应读到 sentinel")
	}
	if st.Attempt != 2 || st.Version != "v1.2.3" {
		t.Fatalf("sentinel 内容不符: %+v", st)
	}
}

// TestCheckAndAutoRollbackNoSentinel 无标记（非换版后首启）：直接返回，不 spawn / 不 exit。
func TestCheckAndAutoRollbackNoSentinel(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	writeFile(t, run, "当前版")
	rec := stubHooks(t)

	CheckAndAutoRollback(run)
	if rec.spawnCalls != 0 || rec.exitCalls != 0 {
		t.Fatalf("无标记不应 spawn/exit，实际 spawn=%d exit=%d", rec.spawnCalls, rec.exitCalls)
	}
}

// TestCheckAndAutoRollbackUnderThreshold 未达上限：累加计数、不回退；验证期后确认成功删 sentinel 与 .old。
func TestCheckAndAutoRollbackUnderThreshold(t *testing.T) {
	origVerify := verifyDuration
	verifyDuration = 20 * time.Millisecond
	t.Cleanup(func() { verifyDuration = origVerify })

	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	writeFile(t, run, "新版")
	writeFile(t, run+oldSuffix, "旧版")
	if err := writeSentinel(run, sentinelState{Attempt: 0, Version: "v2"}); err != nil {
		t.Fatalf("写 sentinel 失败: %v", err)
	}
	rec := stubHooks(t)

	CheckAndAutoRollback(run)
	// 立即：计数累加到 1、不回退。
	if rec.spawnCalls != 0 || rec.exitCalls != 0 {
		t.Fatalf("未达上限不应回退，实际 spawn=%d exit=%d", rec.spawnCalls, rec.exitCalls)
	}
	st, ok := readSentinel(run)
	if !ok || st.Attempt != 1 {
		t.Fatalf("计数应累加到 1，实际 %+v ok=%v", st, ok)
	}
	// 验证期后：确认成功，sentinel 与 .old 均被清理。
	time.Sleep(80 * time.Millisecond)
	if _, ok := readSentinel(run); ok {
		t.Fatal("验证期后 sentinel 应被清理")
	}
	if _, err := os.Stat(run + oldSuffix); !os.IsNotExist(err) {
		t.Fatal("验证期后 .old 应被清理")
	}
}

// TestCheckAndAutoRollbackTriggersRollback 达上限：自动回退——旧版还原、坏新版归档 .failed、清 sentinel、spawn 旧版、exit 0。
func TestCheckAndAutoRollbackTriggersRollback(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	writeFile(t, run, "坏新版")
	writeFile(t, run+oldSuffix, "好旧版")
	// attempt 已达 maxStartAttempts-1，本次自检 ++ 即触发回退。
	if err := writeSentinel(run, sentinelState{Attempt: maxStartAttempts - 1, Version: "v2"}); err != nil {
		t.Fatalf("写 sentinel 失败: %v", err)
	}
	rec := stubHooks(t)

	CheckAndAutoRollback(run)

	if got := readFile(t, run); got != "好旧版" {
		t.Fatalf("回退后运行路径应为旧版，实际 %q", got)
	}
	if got := readFile(t, run+failedSuffix); got != "坏新版" {
		t.Fatalf("坏新版应归档为 .failed，实际 %q", got)
	}
	if _, ok := readSentinel(run); ok {
		t.Fatal("回退后 sentinel 应清除")
	}
	if rec.spawnCalls != 1 || rec.spawnedExe != run {
		t.Fatalf("应 spawn 旧版 1 次，实际 calls=%d exe=%q", rec.spawnCalls, rec.spawnedExe)
	}
	if rec.exitCalls != 1 || rec.exitCode != 0 {
		t.Fatalf("应以 0 退出，实际 calls=%d code=%d", rec.exitCalls, rec.exitCode)
	}
}

// TestAutoRollbackNoBackup 无 .old 可退：清 sentinel 后返回（继续当前版），不 spawn / 不 exit。
func TestAutoRollbackNoBackup(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	writeFile(t, run, "坏新版")
	if err := writeSentinel(run, sentinelState{Attempt: maxStartAttempts - 1, Version: "v2"}); err != nil {
		t.Fatalf("写 sentinel 失败: %v", err)
	}
	rec := stubHooks(t)

	CheckAndAutoRollback(run)
	if rec.spawnCalls != 0 || rec.exitCalls != 0 {
		t.Fatalf("无备份不应 spawn/exit，实际 spawn=%d exit=%d", rec.spawnCalls, rec.exitCalls)
	}
	if _, ok := readSentinel(run); ok {
		t.Fatal("无备份回退应清除 sentinel 避免无限尝试")
	}
}

// TestConfirmUpdateSuccessRemovesSentinelAndOld 确认成功：清 sentinel 与 .old（幂等）。
func TestConfirmUpdateSuccessRemovesSentinelAndOld(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	writeFile(t, run, "新版")
	writeFile(t, run+oldSuffix, "旧版")
	if err := writeSentinel(run, sentinelState{Attempt: 1, Version: "v2"}); err != nil {
		t.Fatalf("写 sentinel 失败: %v", err)
	}

	ConfirmUpdateSuccess(run)
	if _, ok := readSentinel(run); ok {
		t.Fatal("sentinel 应被清理")
	}
	if _, err := os.Stat(run + oldSuffix); !os.IsNotExist(err) {
		t.Fatal(".old 应被清理")
	}
	// 幂等：再次调用不报错。
	ConfirmUpdateSuccess(run)
}

// TestSwapAndRespawnSuccess 换二进制成功：新版就位、旧版存 .old、写 sentinel(attempt=0)、spawn 新版。
func TestSwapAndRespawnSuccess(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	pending := filepath.Join(dir, "beacon.new")
	writeFile(t, run, "旧版")
	writeFile(t, pending, "新版")
	rec := stubHooks(t)

	if err := SwapAndRespawn(run, pending, "v9.9.9"); err != nil {
		t.Fatalf("SwapAndRespawn 应成功: %v", err)
	}
	if got := readFile(t, run); got != "新版" {
		t.Fatalf("运行路径应为新版，实际 %q", got)
	}
	if got := readFile(t, run+oldSuffix); got != "旧版" {
		t.Fatalf(".old 应为旧版，实际 %q", got)
	}
	st, ok := readSentinel(run)
	if !ok || st.Attempt != 0 || st.Version != "v9.9.9" {
		t.Fatalf("应写 sentinel{0,v9.9.9}，实际 %+v ok=%v", st, ok)
	}
	if rec.spawnCalls != 1 || rec.spawnedExe != run {
		t.Fatalf("应 spawn 新版 1 次，实际 calls=%d exe=%q", rec.spawnCalls, rec.spawnedExe)
	}
}

// TestSwapAndRespawnLandFailFallback 换二进制失败（pending 缺失）：不破坏旧版、不写 sentinel、spawn 旧版兜底。
func TestSwapAndRespawnLandFailFallback(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	writeFile(t, run, "旧版")
	rec := stubHooks(t)

	if err := SwapAndRespawn(run, filepath.Join(dir, "不存在.new"), "v9.9.9"); err != nil {
		t.Fatalf("换失败应回退兜底、不返回错误: %v", err)
	}
	if got := readFile(t, run); got != "旧版" {
		t.Fatalf("换失败运行路径应保持旧版，实际 %q", got)
	}
	if _, ok := readSentinel(run); ok {
		t.Fatal("换失败不应写 sentinel")
	}
	if rec.spawnCalls != 1 || rec.spawnedExe != run {
		t.Fatalf("换失败应 spawn 旧版兜底，实际 calls=%d exe=%q", rec.spawnCalls, rec.spawnedExe)
	}
}
