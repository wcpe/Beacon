package httpx

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestNewClientDirectWhenEmptyProxy 空代理 → 直连：Transport.Proxy 为 nil、超时按入参。
func TestNewClientDirectWhenEmptyProxy(t *testing.T) {
	c, err := NewClient("", 7*time.Second)
	if err != nil {
		t.Fatalf("空代理不应报错，实际 %v", err)
	}
	if c.Timeout != 7*time.Second {
		t.Fatalf("超时应为 7s，实际 %v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 应为 *http.Transport，实际 %T", c.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("空代理时 Transport.Proxy 应为 nil（直连）")
	}
}

// TestNewClientUsesProxyWhenSet 显式代理 → Transport.Proxy 返回该代理地址。
func TestNewClientUsesProxyWhenSet(t *testing.T) {
	c, err := NewClient("http://proxy.example.com:8080", 5*time.Second)
	if err != nil {
		t.Fatalf("合法代理不应报错，实际 %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 应为 *http.Transport，实际 %T", c.Transport)
	}
	if tr.Proxy == nil {
		t.Fatal("设了代理时 Transport.Proxy 不应为 nil")
	}
	// Proxy 函数对任意出站请求都应返回配置的代理地址。
	req, _ := http.NewRequest(http.MethodGet, "https://github.com/wcpe/Beacon", nil)
	proxyURL, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy 函数不应报错，实际 %v", err)
	}
	if proxyURL == nil {
		t.Fatal("Proxy 函数应返回代理地址，实际 nil")
	}
	want, _ := url.Parse("http://proxy.example.com:8080")
	if proxyURL.String() != want.String() {
		t.Fatalf("代理地址应为 %s，实际 %s", want, proxyURL)
	}
}

// TestNewClientRejectsInvalidProxy 非法代理（空 host / 非 http(s) scheme / 解析失败）→ 报错、不返回客户端。
func TestNewClientRejectsInvalidProxy(t *testing.T) {
	bad := []string{
		"://nohost",           // 缺 scheme
		"ftp://proxy:21",      // 非 http(s)
		"socks5://proxy:1080", // 不支持 socks5
		"http://",             // 缺 host
		"not a url",           // 无 scheme/host
		"http://proxy",        // 缺端口（host:port 须含端口）
	}
	for _, p := range bad {
		if _, err := NewClient(p, time.Second); err == nil {
			t.Fatalf("非法代理 %q 应报错", p)
		}
	}
}
