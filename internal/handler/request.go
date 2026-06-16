package handler

import (
	"net"
	"net/http"
	"strings"
)

// clientIP 提取请求来源 IP：优先 X-Forwarded-For 首跳、其次 X-Real-IP，再退回 RemoteAddr 的 host。
// 供审计 client_ip 落库（内网控制面，信任前置代理透传的来源头；无代理时即 TCP 对端 IP）。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// 多级代理时取第一跳（最初客户端）
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
