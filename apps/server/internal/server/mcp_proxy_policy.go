package server

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

var errInvalidMCPProxyConfig = errors.New("MCP 反向代理配置无效")

// MCPProxyPolicy 只信任部署配置列出的反向代理，不在业务进程中假设 TLS 已终止。
type MCPProxyPolicy struct {
	enabled  bool
	audience string
	host     string
	proxies  []netip.Prefix
}

// NewMCPProxyPolicy 校验固定公网基址与可信代理网段。
func NewMCPProxyPolicy(enabled bool, publicBaseURL string, trustedProxyCIDRs []string) (*MCPProxyPolicy, error) {
	if !enabled {
		return &MCPProxyPolicy{}, nil
	}
	base, err := url.Parse(publicBaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, errInvalidMCPProxyConfig
	}
	prefixes := make([]netip.Prefix, 0, len(trustedProxyCIDRs))
	for _, raw := range trustedProxyCIDRs {
		prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(raw))
		if parseErr != nil {
			return nil, errInvalidMCPProxyConfig
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, errInvalidMCPProxyConfig
	}
	return &MCPProxyPolicy{enabled: true, audience: strings.TrimRight(publicBaseURL, "/") + "/admin/v2/mcp", host: base.Host, proxies: prefixes}, nil
}

func (p *MCPProxyPolicy) Audience() string {
	if p == nil {
		return ""
	}
	return p.audience
}
func (p *MCPProxyPolicy) Enabled() bool { return p != nil && p.enabled }

// Accepts 仅在连接来自可信代理、且代理传入的固定 scheme/host 与部署配置相符时放行。
func (p *MCPProxyPolicy) Accepts(r *http.Request) bool {
	if p == nil || !p.enabled || r == nil || !p.trustedRemote(r.RemoteAddr) {
		return false
	}
	if r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-Host") != p.host {
		return false
	}
	if r.Host != p.host {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != strings.TrimSuffix(p.audience, "/admin/v2/mcp") {
		return false
	}
	return true
}

func (p *MCPProxyPolicy) trustedRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, prefix := range p.proxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
