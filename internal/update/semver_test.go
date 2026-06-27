package update

import "testing"

// TestCompareBase 穷举主/次/补丁高低与相等；预发布标识不影响基线比较（ADR-0055）。
func TestCompareBase(t *testing.T) {
	cases := []struct {
		a, b string
		want int // a 相对 b 的基线：-1 小于 / 0 相等 / 1 大于
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},                // 前导 v 容忍、不影响比较
		{"1.2.0", "1.1.9", 1},                 // 次版本高
		{"1.1.0", "1.2.0", -1},                // 次版本低
		{"2.0.0", "1.9.9", 1},                 // 主版本高
		{"1.0.1", "1.0.0", 1},                 // 补丁高
		{"1.0.0", "1.0.1", -1},                // 补丁低
		{"1.0.0-dev.abc", "1.0.0-dev.def", 0}, // 预发布标识不影响基线
		{"0.18.0-dev.abc", "0.18.0", 0},       // dev 与正式同基线
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
		if got := compareBase(a, b); got != c.want {
			t.Errorf("compareBase(%q, %q)=%d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

// TestParseSemverAcceptsPrerelease 接受 X.Y.Z 与 X.Y.Z-<prerelease>（滚动预发布 dev.<sha>，ADR-0055）。
func TestParseSemverAcceptsPrerelease(t *testing.T) {
	cases := []struct {
		s       string
		wantPre string
	}{
		{"1.2.3", ""},
		{"v1.2.3", ""},
		{"0.18.0-dev.715989a", "dev.715989a"},
		{"v0.18.0-dev.abc1234", "dev.abc1234"},
	}
	for _, c := range cases {
		v, err := parseSemver(c.s)
		if err != nil {
			t.Fatalf("解析 %q 应成功: %v", c.s, err)
		}
		if v.prerelease != c.wantPre {
			t.Errorf("parseSemver(%q).prerelease=%q，期望 %q", c.s, v.prerelease, c.wantPre)
		}
	}
}

// TestParseSemverRejectsInvalid 非法版本号一律拒绝，避免误判更新。
func TestParseSemverRejectsInvalid(t *testing.T) {
	bad := []string{
		"", "v", "1.2", "1.2.3.4", "1.2.x", "a.b.c",
		"-1.0.0",      // 负数
		"1.2.0-",      // 空预发布段
		"1.2.0+build", // build 元数据不支持
		"1.2.0-dev+x", // 含 build 元数据
	}
	for _, s := range bad {
		if _, err := parseSemver(s); err == nil {
			t.Errorf("非法版本 %q 应解析失败", s)
		}
	}
}

// TestIsNewer 覆盖基线高/低/同号、dev 预发布标识变化、dev 哨兵（ADR-0055：
// 基线比较 + 同基线预发布标识不同即更新，使滚动预发布每次 push 可反复触发）。
func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, remote string
		wantNewer       bool
		wantErr         bool
	}{
		{"1.0.0", "1.0.1", true, false},                    // 远端基线更高
		{"0.16.0", "0.17.0", true, false},                  // 跨次版本
		{"1.0.1", "1.0.0", false, false},                   // 远端更低
		{"1.0.0", "1.0.0", false, false},                   // 同基线同标识（都空）→ 不更新
		{"0.17.0", "0.18.0-dev.abc", true, false},          // 正式 → 预发布跨基线 → 更新
		{"0.18.0-dev.abc", "0.18.0-dev.def", true, false},  // 同基线不同 sha → 更新（反复测试触发核心）
		{"0.18.0-dev.abc", "0.18.0-dev.abc", false, false}, // 完全相同 → 不更新
		{"0.18.0-dev.abc", "0.18.0", true, false},          // 同基线 dev → 正式 → 更新（升正式）
		{"0.18.0-dev.abc", "0.17.0", false, false},         // 远端基线更低 → 不更新
		{"dev", "0.18.0-dev.abc", false, false},            // dev 哨兵：不提示、不报错
		{"1.0.0", "garbage", false, true},                  // 远端非法：不提示 + 报错
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
