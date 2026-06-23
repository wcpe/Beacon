package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeLongpollSettings 是 longpollSettings 测试替身：以固定 max-hold-ms 驱动挂起上限解析（FR-61）。
type fakeLongpollSettings struct {
	maxHoldMs int
}

func (f fakeLongpollSettings) GetInt(key string) int {
	if key == longpollSettingMaxHoldMs {
		return f.maxHoldMs
	}
	return 0
}

// TestResolveHoldTimeout 长轮询挂起时长 = min(客户端 timeoutMs, 设置 store 的服务端上限)（FR-61）。
func TestResolveHoldTimeout(t *testing.T) {
	settings := fakeLongpollSettings{maxHoldMs: 30000}
	cases := []struct {
		name      string
		clientStr string
		want      time.Duration
	}{
		{"客户端为空取服务端上限", "", 30000 * time.Millisecond},
		{"客户端更小取客户端", "5000", 5000 * time.Millisecond},
		{"客户端更大取服务端上限", "60000", 30000 * time.Millisecond},
		{"客户端非法忽略取上限", "abc", 30000 * time.Millisecond},
		{"客户端非正忽略取上限", "0", 30000 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveHoldTimeout(settings, c.clientStr); got != c.want {
				t.Fatalf("resolveHoldTimeout(%q) = %v，期望 %v", c.clientStr, got, c.want)
			}
		})
	}
}

// TestResolveHoldTimeoutHotChange 服务端上限从设置 store 读、热生效：上限改了挂起时长随之变（FR-61）。
func TestResolveHoldTimeoutHotChange(t *testing.T) {
	if got := resolveHoldTimeout(fakeLongpollSettings{maxHoldMs: 10000}, ""); got != 10000*time.Millisecond {
		t.Fatalf("上限 10s 应得 10s，实际 %v", got)
	}
	if got := resolveHoldTimeout(fakeLongpollSettings{maxHoldMs: 45000}, ""); got != 45000*time.Millisecond {
		t.Fatalf("上限热改为 45s 应得 45s，实际 %v", got)
	}
}

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
