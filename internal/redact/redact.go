// Package redact 把错误 / 日志文本中的凭据类敏感片段打码后再对外展示（FR-122，见 ADR-0057）。
// 用途：控制面把内部错误脱敏后展示到前端，让运维看到真实原因又不泄露凭据。
// 叶子包（无内部依赖），供 render（HTTP 层）与 update（core）等共用，避免反向依赖成环。
//
// 边界（ADR-0057）：只打码**凭据**（口令 / token / secret / api-key / URL 账密 / Bearer·Basic 令牌）；
// 内网地址 / 主机名 / 文件路径 / 业务标识属运维定位上下文、非凭据，**不打码**。
package redact

import "regexp"

// 打码占位符
const masked = "***"

var (
	// URL 里的 user:pass@host → user:***@host：打码密码段，保留 scheme 与用户名便于辨识。
	urlCredRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:/@\s]+):[^@/\s]+@`)
	// 键值形式的凭据：token=xxx / password=xxx / secret=xxx / pwd=xxx / api-key=xxx 等（大小写不敏感）。
	// 不含 authorization——其常见形态「Authorization: Bearer xxx」由 schemeTokenRe 处理，避免部分匹配漏码。
	kvSecretRe = regexp.MustCompile(`(?i)\b(token|password|passwd|pwd|secret|api[_-]?key|access[_-]?key)([=:]\s*)("?)[^"&\s]+`)
	// Bearer / Basic 令牌：Bearer <token> → Bearer ***（保留 scheme 关键字大小写）。
	schemeTokenRe = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._\-+/=]+`)
)

// Desensitize 把文本中的常见凭据片段打码（best-effort），用于把错误安全地展示到前端。
// 非凭据（内网地址 / 主机名 / 路径 / 业务标识）原样保留。新增凭据形态时扩规则并补单测。
func Desensitize(s string) string {
	s = urlCredRe.ReplaceAllString(s, `$1:`+masked+`@`)
	s = kvSecretRe.ReplaceAllString(s, `${1}${2}${3}`+masked)
	s = schemeTokenRe.ReplaceAllString(s, `$1 `+masked)
	return s
}

// DesensitizeErr 对 error 取消息并脱敏；nil 返回空串。便于调用方一行把错误转为可安全展示的文案。
func DesensitizeErr(err error) string {
	if err == nil {
		return ""
	}
	return Desensitize(err.Error())
}
