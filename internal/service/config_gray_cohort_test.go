package service

import (
	"reflect"
	"testing"
)

// TestEncodeCohort 验证 cohort 名单序列化：去重 / 去空白 / 字典序确定性 + 空名单报错。
func TestEncodeCohort(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string // 期望 JSON 文本；空表示期望报错
	}{
		{"正常排序", []string{"s2", "s1"}, `["s1","s2"]`},
		{"去重", []string{"s1", "s1", "s2"}, `["s1","s2"]`},
		{"去空白与空串", []string{" s1 ", "", "  "}, `["s1"]`},
		{"全空报错", []string{"", "  "}, ""},
		{"nil 报错", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := encodeCohort(c.in)
			if c.want == "" {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if got != c.want {
				t.Fatalf("序列化错误：want %q got %q", c.want, got)
			}
		})
	}
}

// TestCohortContains 验证从落库文本反解析后的命中判定。
func TestCohortContains(t *testing.T) {
	encoded, err := encodeCohort([]string{"s1", "s3"})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	set, err := decodeCohort(encoded)
	if err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if !set["s1"] || !set["s3"] {
		t.Fatalf("应命中 s1/s3，实际 %v", set)
	}
	if set["s2"] {
		t.Fatalf("不应命中 s2")
	}
}

// TestDecodeCohortKeys 验证反序列化保留全部成员（供唤醒按名单逐 serverId）。
func TestDecodeCohortKeys(t *testing.T) {
	encoded, _ := encodeCohort([]string{"s3", "s1", "s2"})
	set, err := decodeCohort(encoded)
	if err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	got := cohortMembers(set)
	want := []string{"s1", "s2", "s3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("成员清单错误：want %v got %v", want, got)
	}
}
