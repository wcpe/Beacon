package httpx

import (
	"net/url"
	"strings"
)

// redactedMask 是脱敏后替换 userinfo 单段的占位符。
const redactedMask = "***"

// RedactURLCredentials 精确掩去 URL 的 userinfo 凭据段，仅用于展示 / 审计 / 日志，绝不用于出站连接。
// 规则（宁严勿松、只掩敏感位）：
//   - 无 userinfo（如 http://h:port）→ 原样返回，不动 host/port/path。
//   - 空串 → 返回空串（代表直连，不可误成 ***）。
//   - http://user:pass@h → http://***:***@h；http://user@h → http://***@h。
//   - URL 解析失败 → 整体返回 "***"（绝不回传可能含凭据的原串）。
//
// proxy-url 是设置 store 首个含凭据项（FR-98，见 ADR-0047），扩展 ADR-0038「value 可明文记」前提。
func RedactURLCredentials(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// 解析失败无法定位 userinfo 段，宁严勿松整体掩去（原串可能恰好含凭据）。
		return redactedMask
	}
	if parsed.User == nil {
		// 无 userinfo：不含凭据，原样返回（不误掩 host/port/path）。
		return raw
	}
	// 拼掩码 userinfo 段：有口令掩两段、仅用户名掩一段。
	// 不走 url.UserPassword 再 String()——它会把 "***" 的 "*" 百分号编码成 %2A，掩码可读性丢失；
	// 故清掉 User 后由 String() 得无凭据 URL，再把掩码 userinfo 插回 scheme:// 之后。
	mask := redactedMask
	if _, hasPassword := parsed.User.Password(); hasPassword {
		mask = redactedMask + ":" + redactedMask
	}
	parsed.User = nil
	bare := parsed.String() // scheme://host[:port][/path]，已无 userinfo
	const sep = "://"
	idx := strings.Index(bare, sep)
	if idx < 0 {
		// 理论不可达（已通过 url.Parse 且有 scheme）；兜底整体掩，绝不回传原串。
		return redactedMask
	}
	return bare[:idx+len(sep)] + mask + "@" + bare[idx+len(sep):]
}
