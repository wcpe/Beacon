package httpx

import "testing"

// TestRedactURLCredentials 穷举 URL 凭据脱敏：无凭据不误掩 / user:pass 全掩 / 仅用户名 / 空串 / 非法 URL 不 panic 且整体掩 / https。
func TestRedactURLCredentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// 无 userinfo：原样返回，不误掩 host/port/path。
		{"无凭据 http", "http://proxy.example.com:8080", "http://proxy.example.com:8080"},
		{"无凭据带路径", "http://proxy.example.com:8080/path?q=1", "http://proxy.example.com:8080/path?q=1"},
		{"无凭据 https", "https://proxy.example.com", "https://proxy.example.com"},
		// 空串：返回空串（=直连，不可误成 ***）。
		{"空串", "", ""},
		// 完整 user:pass：用户名与口令两段都掩。
		{"user pass http", "http://user:pass@proxy:8080", "http://***:***@proxy:8080"},
		{"user pass https", "https://alice:s3cr3t@proxy.example.com:3128/p", "https://***:***@proxy.example.com:3128/p"},
		// 仅用户名（无口令）：只掩用户名段。
		{"仅用户名", "http://user@proxy:8080", "http://***@proxy:8080"},
		// 口令含特殊字符（@ 编码）仍掩。
		{"特殊字符口令", "http://user:p%40ss@proxy:8080", "http://***:***@proxy:8080"},
		// 非法 URL：不 panic，宁严勿松整体掩为 ***（绝不回传可能含凭据的原串）。
		{"非法 URL 控制字符", "http://user:pass@\x7f host", "***"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("RedactURLCredentials(%q) 不应 panic，实际 %v", c.in, r)
				}
			}()
			got := RedactURLCredentials(c.in)
			if got != c.want {
				t.Fatalf("RedactURLCredentials(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

// TestRedactNeverLeaksCredentials 任何含 @ 凭据的输入，脱敏结果都不得含原口令子串（兜底防泄露）。
func TestRedactNeverLeaksCredentials(t *testing.T) {
	for _, in := range []string{
		"http://user:topsecret@proxy:8080",
		"https://admin:hunter2@10.0.0.1:3128",
	} {
		got := RedactURLCredentials(in)
		for _, leak := range []string{"topsecret", "hunter2"} {
			if contains(got, leak) {
				t.Fatalf("脱敏结果 %q 泄露了口令 %q", got, leak)
			}
		}
	}
}

// contains 是不引 strings 的极简子串判断（仅测试内用）。
func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
