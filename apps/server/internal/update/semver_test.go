package update

import "testing"

// TestParseRuntimeVersionAcceptsVersionAndCandidate 验证运行时版本解析同时接受正式版与候选版（ADR-0092）。
// 候选版必须能解析：候选构建注入的就是 "1.5.0-rc.2"，拒绝会让更新判新静默失效。
func TestParseRuntimeVersionAcceptsVersionAndCandidate(t *testing.T) {
	good := []string{
		"0.0.0", "v0.0.0", "1.2.3", "v10.20.30",
		"1.5.0-rc.1", "v1.5.0-rc.2", "1.5.0-rc.10",
	}
	for _, value := range good {
		if _, err := parseRuntimeVersion(value); err != nil {
			t.Errorf("运行时版本 %q 应解析成功，实际 %v", value, err)
		}
	}
}

// TestParseRuntimeVersionRejectsMalformed 验证构建元数据、缺段、前导零、空后缀与开发哨兵不被接受。
func TestParseRuntimeVersionRejectsMalformed(t *testing.T) {
	bad := []string{
		"", "v", "1.2", "1.2.3.4", "1.2.x", "a.b.c",
		"01.2.3", "1.02.3", "1.2.03",
		"-1.0.0", "1.2.3-", "1.2.3+build.1", "dev", " v1.2.3", "v1.2.3 ",
	}
	for _, value := range bad {
		if _, err := parseRuntimeVersion(value); err == nil {
			t.Errorf("非法版本 %q 应解析失败", value)
		}
	}
}

// TestParseGAVersionRejectsCandidate 验证远端 GA 口径严格拒绝候选与开发后缀。
// 这是「在线更新只消费 GA」的解析层防线：放宽即会让候选被装进生产实例。
func TestParseGAVersionRejectsCandidate(t *testing.T) {
	for _, value := range []string{"1.2.3-rc.1", "v1.2.3-rc.1", "1.2.3-dev.1.gabcdef0"} {
		if _, err := parseGAVersion(value); err == nil {
			t.Errorf("远端 %q 带候选或开发后缀，应解析失败", value)
		}
	}
	for _, value := range []string{"1.2.3", "v1.2.3", "v10.20.30"} {
		if _, err := parseGAVersion(value); err != nil {
			t.Errorf("正式版本 %q 应解析成功，实际 %v", value, err)
		}
	}
}

// TestCompareSemver 覆盖三段数字高低、相等，以及候选后缀的 SemVer 第 11 条排序。
func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.2.0", "1.1.9", 1},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		// 数字相同时：正式版高于候选版。
		{"1.5.0", "1.5.0-rc.2", 1},
		{"1.5.0-rc.2", "1.5.0", -1},
		// 候选序号递增；两位数按数值而非字典序比较（rc.10 > rc.9）。
		{"1.5.0-rc.2", "1.5.0-rc.1", 1},
		{"1.5.0-rc.10", "1.5.0-rc.9", 1},
		{"1.5.0-rc.1", "1.5.0-rc.2", -1},
		{"1.5.0-rc.2", "1.5.0-rc.2", 0},
		// 三段数字优先于候选：补丁号更高的候选仍高于补丁号低的正式版。
		{"1.5.1-rc.1", "1.5.0", 1},
	}
	for _, tc := range cases {
		a, err := parseRuntimeVersion(tc.a)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", tc.a, err)
		}
		b, err := parseRuntimeVersion(tc.b)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", tc.b, err)
		}
		if got := compareSemver(a, b); got != tc.want {
			t.Errorf("compareSemver(%q, %q)=%d，期望 %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestIsNewerUsesAsymmetricParsing 锁定两侧强度不对称：
// 当前版本允许候选后缀，远端严格只接受 GA。候选实例因此能升级到同号 GA 与更高候选。
func TestIsNewerUsesAsymmetricParsing(t *testing.T) {
	cases := []struct {
		current, remote string
		wantNewer       bool
		wantErr         bool
	}{
		// 正式版本之间的常规比较。
		{"1.0.0", "v1.0.1", true, false},
		{"v1.0.0", "v2.0.0", true, false},
		{"1.0.1", "v1.0.0", false, false},
		{"1.0.0", "v1.0.0", false, false},
		// 开发哨兵不提示更新（版本未知）。
		{"dev", "v1.0.0", false, false},
		// 候选当前版本：能解析，GA 与更高候选都判为有更新。
		{"1.5.0-rc.2", "v1.5.0", true, false},
		{"1.5.0-rc.2", "v1.5.0-rc.3", false, true},
		{"1.5.0-rc.2", "v1.5.1", true, false},
		{"1.5.0", "v1.5.0-rc.2", false, true},
		// 远端带候选 / 开发 / 构建元数据：一律解析失败，绝不判为可更新。
		{"1.0.0", "v1.0.1-rc.1", false, true},
		{"1.0.0", "v1.0.1+build.1", false, true},
		{"1.0.0", "v1.0.1-dev.1.gabcdef0", false, true},
	}
	for _, tc := range cases {
		newer, err := IsNewer(tc.current, tc.remote)
		if (err != nil) != tc.wantErr {
			t.Errorf("IsNewer(%q, %q) err=%v，期望 wantErr=%v", tc.current, tc.remote, err, tc.wantErr)
		}
		if newer != tc.wantNewer {
			t.Errorf("IsNewer(%q, %q)=%v，期望 %v", tc.current, tc.remote, newer, tc.wantNewer)
		}
	}
}
