package service

import "testing"

// TestGlobToRegexAndMatch 穷举默认敏感清单与各路径的命中 / 不命中（spec §4.6），与 devmock globMatch 同口径。
func TestSensitiveMatcherDefaultPatterns(t *testing.T) {
	m, err := newSensitiveMatcher(defaultSensitivePatterns)
	if err != nil {
		t.Fatalf("默认规则应可编译: %v", err)
	}
	cases := []struct {
		path string
		want bool
	}{
		// **/*secret* —— 任意层含 secret 的文件名
		{"plugins/Auth/client-secret.yml", true},
		{"secret.txt", true},
		{"a/b/c/mysecret.json", true},
		// **/*password* —— 大小写不敏感
		{"plugins/Economy/database-password.yml", true},
		{"plugins/Economy/DATABASE-PASSWORD.YML", true},
		// **/*.pem / .key / .jks / .p12
		{"certs/server.pem", true},
		{"a/private.key", true},
		{"keystore.jks", true},
		{"plugins/x/store.p12", true},
		// **/.env 与 **/.env.*
		{".env", true},
		{"plugins/Foo/.env", true},
		{"plugins/Foo/.env.prod", true},
		// **/token.*
		{"token.json", true},
		{"config/token.yml", true},
		// plugins/Beacon/** —— agent 身份目录整目录
		{"plugins/Beacon/config.yml", true},
		{"plugins/Beacon/data/identity.json", true},
		// 不命中：普通业务配置
		{"plugins/Essentials/config.yml", false},
		{"server.properties", false},
		// 注意：Beacon.jar 不在 plugins/Beacon/ 目录内，不命中
		{"plugins/Beacon.jar", false},
		// *password* 会命中 passwords-doc.md（模式偏宽，靠单次原因放行兜底，spec §8.4）
		{"plugins/Foo/passwords-doc.md", true},
		// 不含 .env. 前缀分隔，不误伤 environment.yml
		{"plugins/Foo/environment.yml", false},
	}
	for _, c := range cases {
		if got := m.matches(c.path); got != c.want {
			t.Errorf("matches(%q)=%v，期望 %v（正则集 %v）", c.path, got, c.want, patternRegexes(defaultSensitivePatterns))
		}
	}
}

// TestGlobToRegexShape 校验 glob→regex 的关键转换形态（**/ 零或多层、** 跨层、* 单层、元字符转义）。
func TestGlobToRegexShape(t *testing.T) {
	cases := map[string]string{
		"**/*.pem":          `^(?:.*/)?[^/]*\.pem$`,
		"plugins/Beacon/**": `^plugins/beacon/.*$`,
		"**/.env":           `^(?:.*/)?\.env$`,
		"**/token.*":        `^(?:.*/)?token\.[^/]*$`,
		"*.key":             `^[^/]*\.key$`,
	}
	for glob, want := range cases {
		if got := globToRegex(glob); got != want {
			t.Errorf("globToRegex(%q)=%q，期望 %q", glob, got, want)
		}
	}
}

// TestNewSensitiveMatcherBadGlob 非法 glob（嵌套重复算子 ???）编译失败——供 PUT 校验拒绝坏规则。
// 注意方括号会被转义为字面量（与 devmock 同口径），故 `plugins/[bad` 反而合法；能触发编译失败的是裸 ? 连缀。
func TestNewSensitiveMatcherBadGlob(t *testing.T) {
	if _, err := newSensitiveMatcher([]string{"a???b"}); err == nil {
		t.Fatal("嵌套重复算子的 glob 应编译失败")
	}
}

// TestNormalizeSensitivePatterns 去空白 / 丢空串，允许空数组。
func TestNormalizeSensitivePatterns(t *testing.T) {
	got := normalizeSensitivePatterns([]string{" **/*.pem ", "", "  ", "plugins/Beacon/**"})
	if len(got) != 2 || got[0] != "**/*.pem" || got[1] != "plugins/Beacon/**" {
		t.Fatalf("归一结果异常: %#v", got)
	}
	if len(normalizeSensitivePatterns([]string{"", "  "})) != 0 {
		t.Fatal("全空应归一为空数组（关闭保护）")
	}
}

// patternRegexes 仅测试断言失败时打印，便于定位。
func patternRegexes(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, globToRegex(p))
	}
	return out
}
