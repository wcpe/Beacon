package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientIP 验证来源 IP 提取优先级与边界：X-Forwarded-For 首跳 > X-Real-IP > RemoteAddr host。
func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		xff        string
		xRealIP    string
		remoteAddr string
		want       string
	}{
		{"XFF 单跳", "203.0.113.7", "", "10.0.0.1:5000", "203.0.113.7"},
		{"XFF 多跳取首跳并去空格", "203.0.113.7 , 70.41.3.18, 150.172.238.178", "", "10.0.0.1:5000", "203.0.113.7"},
		{"无 XFF 退 X-Real-IP", "", "198.51.100.9", "10.0.0.1:5000", "198.51.100.9"},
		{"X-Real-IP 去空格", "", "  198.51.100.9  ", "10.0.0.1:5000", "198.51.100.9"},
		{"XFF 优先于 X-Real-IP", "203.0.113.7", "198.51.100.9", "10.0.0.1:5000", "203.0.113.7"},
		{"无代理头退 RemoteAddr host", "", "", "192.0.2.5:443", "192.0.2.5"},
		{"IPv6 RemoteAddr 去端口", "", "", "[2001:db8::1]:8080", "2001:db8::1"},
		{"RemoteAddr 无端口原样返回", "", "", "192.0.2.5", "192.0.2.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if c.xRealIP != "" {
				r.Header.Set("X-Real-IP", c.xRealIP)
			}
			if got := clientIP(r); got != c.want {
				t.Fatalf("clientIP() = %q，期望 %q", got, c.want)
			}
		})
	}
}
