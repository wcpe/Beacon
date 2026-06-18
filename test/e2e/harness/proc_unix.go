//go:build e2e && !windows

package harness

import (
	"os/exec"
	"syscall"
)

// setPgid 让子进程自成一个进程组（Setpgid），便于按进程组整树击杀，
// 从而连带 gradle --no-daemon fork 出来的 JVM 一并收掉。
func setPgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree 对子进程所在进程组发 SIGKILL（pid 取负 = 整组），杀掉整棵进程树。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// 负 pid 表示「杀掉该进程组内所有进程」。
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
