package update

import "testing"

// TestCompareBase 穷举主/次/补丁高低与相等；预发布段不影响基线比较（ADR-0056）。
func TestCompareBase(t *testing.T) {
	cases := []struct {
		a, b string
		want int // a 相对 b 的基线：-1 小于 / 0 相等 / 1 大于
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},                              // 前导 v 容忍、不影响比较
		{"1.2.0", "1.1.9", 1},                               // 次版本高
		{"1.1.0", "1.2.0", -1},                              // 次版本低
		{"2.0.0", "1.9.9", 1},                               // 主版本高
		{"1.0.1", "1.0.0", 1},                               // 补丁高
		{"1.0.0", "1.0.1", -1},                              // 补丁低
		{"1.0.0-dev.1.gaaaaaaa", "1.0.0-dev.9.gbbbbbbb", 0}, // 预发布段不影响基线
		{"0.17.0-dev.3.g6b6dd71", "0.17.0", 0},              // dev 与正式同基线
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

// TestParseSemverAcceptsPrerelease 接受 X.Y.Z 与 X.Y.Z-<prerelease>，并从滚动预发布段
// dev.<提交距离>.g<sha> 提取提交距离序号（ADR-0056）；非 dev 预发布段 isPre 为真但序号 -1。
func TestParseSemverAcceptsPrerelease(t *testing.T) {
	cases := []struct {
		s       string
		wantPre bool
		wantSeq int
	}{
		{"1.2.3", false, -1},
		{"v1.2.3", false, -1},
		{"0.17.0-dev.3.g6b6dd71", true, 3},
		{"v0.18.0-dev.12.gabc1234", true, 12},
		{"1.0.0-rc.1", true, -1}, // 非 dev 预发布段：isPre 为真、序号未知 -1
	}
	for _, c := range cases {
		v, err := parseSemver(c.s)
		if err != nil {
			t.Fatalf("解析 %q 应成功: %v", c.s, err)
		}
		if v.isPre != c.wantPre {
			t.Errorf("parseSemver(%q).isPre=%v，期望 %v", c.s, v.isPre, c.wantPre)
		}
		if v.devSeq != c.wantSeq {
			t.Errorf("parseSemver(%q).devSeq=%d，期望 %d", c.s, v.devSeq, c.wantSeq)
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

// TestIsNewer 覆盖基线高/低/同号、dev 提交距离序号比较、正式↔dev 渠道切换、dev 哨兵（ADR-0056：
// 基线比较为先；同基线时正式↔dev 视为更新、dev 间比提交距离序号——序号大才更新，使滚动预发布
// 每次 push（提交距离+1）可反复触发、无新提交不误报）。
func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, remote string
		wantNewer       bool
		wantErr         bool
	}{
		{"1.0.0", "1.0.1", true, false},                                  // 远端基线更高
		{"0.16.0", "0.17.0", true, false},                                // 跨次版本
		{"1.0.1", "1.0.0", false, false},                                 // 远端更低
		{"1.0.0", "1.0.0", false, false},                                 // 都正式、同基线 → 不更新
		{"0.17.0", "0.17.0-dev.3.gaaaaaaa", true, false},                 // 正式 → dev（同基线，dev 是基线后更新提交）→ 更新
		{"0.17.0-dev.3.gaaaaaaa", "0.17.0", false, false},                // dev → 正式（同基线，dev 在基线之后=更新，正式是更旧基线）→ 不更新（fix-a）
		{"0.18.0-dev.8.g0fa2bca", "0.18.0", false, false},                // 真机复现：dev 构建不被提示更新回同基线正式版（fix-a）
		{"0.17.0-dev.3.gaaaaaaa", "0.17.0-dev.5.gbbbbbbb", true, false},  // dev 序号 5>3 → 更新（反复测试核心）
		{"0.17.0-dev.5.gaaaaaaa", "0.17.0-dev.3.gbbbbbbb", false, false}, // dev 序号 3<5 → 不更新
		{"0.17.0-dev.3.gaaaaaaa", "0.17.0-dev.3.gbbbbbbb", false, false}, // dev 序号相等（无新提交）→ 不更新
		{"0.17.0-dev.3.gaaaaaaa", "0.18.0-dev.1.gbbbbbbb", true, false},  // 远端基线更高（即便序号小）→ 更新
		{"0.18.0-dev.3.gaaaaaaa", "0.17.0-dev.9.gbbbbbbb", false, false}, // 远端基线更低（即便序号大）→ 不更新
		{"dev", "0.17.0-dev.3.gaaaaaaa", false, false},                   // dev 哨兵：不提示、不报错
		{"1.0.0", "garbage", false, true},                                // 远端非法：不提示 + 报错
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
