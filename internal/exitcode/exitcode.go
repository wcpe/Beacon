// Package exitcode 集中定义 beacon 主进程与 beacon-launcher 监督进程共享的退出码协议（FR-96，见 ADR-0045）。
//
// 退出码是主进程与 launcher 之间唯一的进程间约定：主进程以约定码退出、launcher 据此决策（不重启 / 崩溃重启 /
// 换二进制后重启）。两端必须引用同一组常量，禁止把这些数字散落在各处（防魔法数字）。
package exitcode

// 退出码协议（FR-96，见 ADR-0045）。取值刻意收窄、含义固定，新增出口须在此登记并写明语义。
const (
	// OK 表示主进程正常退出（如优雅关停完成）。launcher 收到后随之退出、不重启。
	OK = 0

	// Crash 表示主进程崩溃或启动失败。launcher 收到后按固定间隔重启，并计连续失败数，超上限即停并打 ERROR。
	// 进程被信号杀死时操作系统会以 128+signum 作退出码（如 SIGINT=130、SIGKILL=137），launcher 同样视作崩溃。
	Crash = 1

	// RequestUpdateRestart 表示主进程已把待用新二进制落位为 pending，请求 launcher 做原子替换后重启（FR-97 触发，本期仅备好出口）。
	// 取项目内未占用值 70，作命名常量而非散落数字。注意：本值不沿用 sysexits.h 的语义，仅项目内自定义约定。
	RequestUpdateRestart = 70
)

// IsCrashExit 判定给定退出码是否应视为崩溃（需按崩溃策略重启）。
// 约定：0=正常、70=请求更新重启，二者各有专门处置；其余一切非零退出码（含 128+signum 的信号退出）一律视作崩溃。
func IsCrashExit(code int) bool {
	return code != OK && code != RequestUpdateRestart
}
