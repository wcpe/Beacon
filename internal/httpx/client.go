// Package httpx 收口控制面「更新相关出站」的 HTTP 客户端构造（FR-98，见 ADR-0047）。
// 单一小工厂应对「要不要走代理」这一真实变化点：非空代理→带代理 Transport，空→直连。
// 只支持 http/https 正向代理（标准库原生），不引 socks5、不引新依赖、不读 *_PROXY 环境变量（YAGNI）。
// 明确不照搬 ADR-0005 的 agent transport 接口抽象（那是 agent 侧防库冲突的约束，控制面用标准库即足）。
// 作用域仅更新出站：不替代、不改 internal/runtime/alert/webhook.go 的既有裸连行为（向后兼容）。
package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// NewClient 构造带代理与超时的更新出站客户端。
// proxyURL 非空 → 解析校验后用 http.Transport{Proxy: http.ProxyURL(parsed)}；空 → 直连（Proxy 为 nil）。
// proxyURL 非法（scheme 非 http/https、缺 host、缺端口、解析失败）返回错误，不返回客户端。
func NewClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{}
	if proxyURL != "" {
		parsed, err := ParseProxyURL(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// ParseProxyURL 解析并校验代理地址：scheme∈{http,https}、host 非空且形如 host:port（须含端口）。
// 合法返回解析后的 *url.URL（保留 userinfo 凭据供出站连接代理用）；非法返回错误。
// 供 NewClient 与设置层 update.proxy-url 校验共用，确保「能存进 store 的代理一定能构造客户端」。
func ParseProxyURL(proxyURL string) (*url.URL, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("代理地址解析失败: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("代理地址 scheme 仅支持 http/https，实际 %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("代理地址缺少主机")
	}
	// 要求显式 host:port——代理地址须明确端口，避免歧义默认端口。
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host == "" || port == "" {
		return nil, fmt.Errorf("代理地址须为 host:port 形式（含端口）")
	}
	return parsed, nil
}
