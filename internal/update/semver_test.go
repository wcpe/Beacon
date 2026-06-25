package update

import "testing"

// TestCompareSemver 穷举主/次/补丁高低、相等、rc 预发布序、rc 与正式版边界。
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
		// rc 预发布序：rc.N 间按数字
		{"1.2.0-rc.1", "1.2.0-rc.2", -1},
		{"1.2.0-rc.2", "1.2.0-rc.1", 1},
		{"1.2.0-rc.1", "1.2.0-rc.1", 0},
		// 同主次补丁：预发布 < 正式
		{"1.2.0-rc.1", "1.2.0", -1},
		{"1.2.0", "1.2.0-rc.9", 1},
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
func TestParseSemverRejectsInvalid(t *testing.T) {
	bad := []string{
		"", "v", "1.2", "1.2.3.4", "1.2.x", "a.b.c",
		"1.2.0-beta.1", // 仅支持 rc.N 预发布
		"1.2.0-rc",     // 缺序号
		"1.2.0-rc.x",   // 序号非数字
		"-1.0.0",       // 负数
	}
	for _, s := range bad {
		if _, err := parseSemver(s); err == nil {
			t.Errorf("非法版本 %q 应解析失败", s)
		}
	}
}

// TestIsNewer 覆盖高 / 低 / 相等 / rc 序 / dev 哨兵。
func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, remote string
		wantNewer       bool
		wantErr         bool
	}{
		{"1.0.0", "1.0.1", true, false},       // 远端更高
		{"1.0.1", "1.0.0", false, false},      // 远端更低
		{"1.0.0", "1.0.0", false, false},      // 相等不算更新
		{"1.2.0-rc.1", "1.2.0", true, false},  // rc → 正式算更新
		{"1.2.0", "1.2.0-rc.1", false, false}, // 正式 → rc 不算更新
		{"1.2.0-rc.1", "1.2.0-rc.2", true, false},
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
