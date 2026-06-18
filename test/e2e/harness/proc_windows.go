//go:build e2e && windows

package harness

import (
	"os/exec"
	"strconv"
)

// setPgid 在 Windows 上无进程组概念，留空；整树击杀由 taskkill /T 完成。
func setPgid(cmd *exec.Cmd) {}

// killTree 用 taskkill /F /T 按 PID 杀掉整棵进程树（连带 gradle 与 fork 的 JVM）。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	// /T 杀进程树、/F 强制；忽略「进程已退出」类错误由调用方决定是否容忍。
	return exec.Command("taskkill", "/F", "/T", "/PID", pid).Run()
}
