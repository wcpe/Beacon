package exitcode

import "testing"

// 校验 IsCrashExit 对退出码协议各取值的判定：0/70 非崩溃，其余（含信号退出 128+signum）均崩溃。
func TestIsCrashExit(t *testing.T) {
	cases := []struct {
		name string
		code int
		want bool
	}{
		{"正常退出 0", OK, false},
		{"请求更新重启 70", RequestUpdateRestart, false},
		{"崩溃码 1", Crash, true},
		{"SIGINT 130", 130, true},
		{"SIGKILL 137", 137, true},
		{"其它非零 2", 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsCrashExit(c.code); got != c.want {
				t.Fatalf("IsCrashExit(%d)=%v，期望 %v", c.code, got, c.want)
			}
		})
	}
}
