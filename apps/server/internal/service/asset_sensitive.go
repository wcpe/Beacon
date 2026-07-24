package service

import (
	"regexp"
	"strings"
)

// defaultSensitivePatterns 是内置默认敏感路径规则（spec §4.6 默认清单，命中即禁止预览 / diff 内容）。
// 与 devmock DEFAULT_SENSITIVE_PATTERNS 逐条对齐；运维可经 PUT /admin/v2/assets/sensitive-rules 整体替换（含删改默认项）。
// 规则只作用于内容查看，不影响清单元数据（路径 / 哈希 / 大小 / mtime 仍可见）。
var defaultSensitivePatterns = []string{
	"**/*secret*",
	"**/*password*",
	"**/*credential*",
	"**/*.pem",
	"**/*.key",
	"**/*.jks",
	"**/*.p12",
	"**/.env",
	"**/.env.*",
	"**/token.*",
	"plugins/Beacon/**",
}

// sensitiveMatcher 是一组敏感 glob 规则编译成的匹配器（FR-164，spec §4.6）。
// 规则匹配在控制面执行（命令下发前拦截），agent 不感知敏感语义。
type sensitiveMatcher struct {
	res []*regexp.Regexp
}

// newSensitiveMatcher 把 glob 规则逐条编译为锚定正则；任一条非法即返回错误（供 PUT 校验坏规则）。
func newSensitiveMatcher(patterns []string) (*sensitiveMatcher, error) {
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(globToRegex(p))
		if err != nil {
			return nil, err
		}
		res = append(res, re)
	}
	return &sensitiveMatcher{res: res}, nil
}

// matches 判断相对路径是否命中任一敏感规则（大小写不敏感）。
func (m *sensitiveMatcher) matches(path string) bool {
	lower := strings.ToLower(path)
	for _, re := range m.res {
		if re.MatchString(lower) {
			return true
		}
	}
	return false
}

// globRegexEscape 是需在正则中转义的元字符集合（与 devmock globMatch 的转义集逐字对齐：. + ^ $ { } ( ) | [ ] \）。
// 不含 * —— * 有 glob 语义，单独处理；/ 与 ? 保持字面。
const globRegexEscape = `.+^${}()|[]\`

// globToRegex 把单条 glob 转为锚定的正则源串（与 devmock globMatch 同口径，spec §4.6）：
// `**/` 匹配零或多层前缀目录（(?:.*/)?）、`**` 跨层任意（.*）、`*` 单层非分隔（[^/]*）；其余正则元字符转义。
// glob 与待匹配路径均在调用侧小写化（不用 (?i)），与 devmock 大小写不敏感语义完全一致。
func globToRegex(glob string) string {
	lower := strings.ToLower(glob)
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(lower); {
		switch {
		case strings.HasPrefix(lower[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(lower[i:], "**"):
			b.WriteString(".*")
			i += 2
		case lower[i] == '*':
			b.WriteString("[^/]*")
			i++
		default:
			c := lower[i]
			if strings.IndexByte(globRegexEscape, c) >= 0 {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
			i++
		}
	}
	b.WriteByte('$')
	return b.String()
}
