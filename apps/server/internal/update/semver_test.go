package update

import "testing"

// TestParseSemverAcceptsGA 验证内部比较只接受三段正式版本，可容忍运行时版本带或不带 v。
func TestParseSemverAcceptsGA(t *testing.T) {
	for _, value := range []string{"0.0.0", "v0.0.0", "1.2.3", "v10.20.30"} {
		if _, err := parseSemver(value); err != nil {
			t.Errorf("正式版本 %q 应解析成功，实际 %v", value, err)
		}
	}
}

// TestParseSemverRejectsNonGA 验证预发布、构建元数据、缺段、前导零和开发版本不进入正式版本比较。
func TestParseSemverRejectsNonGA(t *testing.T) {
	bad := []string{
		"", "v", "1.2", "1.2.3.4", "1.2.x", "a.b.c",
		"01.2.3", "1.02.3", "1.2.03",
		"-1.0.0", "1.2.3-rc.1", "1.2.3-dev.1.gabcdef0",
		"1.2.3+build.1", "dev", " v1.2.3", "v1.2.3 ",
	}
	for _, value := range bad {
		if _, err := parseSemver(value); err == nil {
			t.Errorf("非正式版本 %q 应解析失败", value)
		}
	}
}

// TestCompareSemver 覆盖正式版本主、次、补丁三段的高低与相等比较。
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
	}
	for _, tc := range cases {
		a, err := parseSemver(tc.a)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", tc.a, err)
		}
		b, err := parseSemver(tc.b)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", tc.b, err)
		}
		if got := compareSemver(a, b); got != tc.want {
			t.Errorf("compareSemver(%q, %q)=%d，期望 %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestIsNewerComparesOnlyGA 验证在线更新只比较正式版本；开发哨兵保持不提示更新。
func TestIsNewerComparesOnlyGA(t *testing.T) {
	cases := []struct {
		current, remote string
		wantNewer       bool
		wantErr         bool
	}{
		{"1.0.0", "v1.0.1", true, false},
		{"v1.0.0", "v2.0.0", true, false},
		{"1.0.1", "v1.0.0", false, false},
		{"1.0.0", "v1.0.0", false, false},
		{"dev", "v1.0.0", false, false},
		{"1.0.0-dev.1.gabcdef0", "v1.0.0", false, true},
		{"1.0.0", "v1.0.1-rc.1", false, true},
		{"1.0.0", "v1.0.1+build.1", false, true},
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
