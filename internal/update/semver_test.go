package update

import "testing"

// TestCompareSemver 穷举主/次/补丁高低与相等（ADR-0052：按 X.Y.Z 三段数字判，不再有 rc 预发布段）。
func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int // a 相对 b：-1 小于 / 0 相等 / 1 大于
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0}, // 前导 v 容忍、不影响比较
		{"1.2.0", "1.1.9", 1},  // 次版本高
		{"1.1.0", "1.2.0", -1}, // 次版本低
		{"2.0.0", "1.9.9", 1},  // 主版本高
		{"1.0.1", "1.0.0", 1},  // 补丁高
		{"1.0.0", "1.0.1", -1}, // 补丁低
	}
	for _, c := range cases {
		a, err := parseSemver(c.a)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", c.a, err)
		}
		b, err := parseSemver(c.b)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", c.b, err)
		}
		if got := compareSemver(a, b); got != c.want {
			t.Errorf("compareSemver(%q, %q)=%d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

// TestParseSemverRejectsInvalid 非法版本号一律拒绝，避免误判更新。
// ADR-0052：去 rc 预发布段——任何带后缀（含 -rc.N）的版本号均非法。
func TestParseSemverRejectsInvalid(t *testing.T) {
	bad := []string{
		"", "v", "1.2", "1.2.3.4", "1.2.x", "a.b.c",
		"1.2.0-beta.1", // 不再支持任何预发布段
		"1.2.0-rc.1",   // rc 段已退场
		"1.2.0-rc",     // 任何 - 后缀均拒
		"-1.0.0",       // 负数
	}
	for _, s := range bad {
		if _, err := parseSemver(s); err == nil {
			t.Errorf("非法版本 %q 应解析失败", s)
		}
	}
}

// TestIsNewer 覆盖跨号高 / 低 / 同号不判 / dev 哨兵（ADR-0052 决策 4/5：同 X.Y.Z 不提示、跨号才提示）。
func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, remote string
		wantNewer       bool
		wantErr         bool
	}{
		{"1.0.0", "1.0.1", true, false},   // 远端跨号更高
		{"0.16.0", "0.17.0", true, false}, // 跨次版本提示（ADR-0052 决策 5 示例）
		{"1.0.1", "1.0.0", false, false},  // 远端更低
		{"1.0.0", "1.0.0", false, false},  // 同号不算更新（滚动覆盖同 X.Y.Z 不提示）
		{"0.17.0", "0.17.0", false, false},
		{"dev", "1.0.0", false, false},    // dev 哨兵：不提示、不报错
		{"1.0.0", "garbage", false, true}, // 远端非法：不提示 + 报错
	}
	for _, c := range cases {
		newer, err := IsNewer(c.current, c.remote)
		if (err != nil) != c.wantErr {
			t.Errorf("IsNewer(%q,%q) err=%v，期望 wantErr=%v", c.current, c.remote, err, c.wantErr)
		}
		if newer != c.wantNewer {
			t.Errorf("IsNewer(%q,%q)=%v，期望 %v", c.current, c.remote, newer, c.wantNewer)
		}
	}
}
