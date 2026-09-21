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

// MCPProxyPolicy 只信任部署配置列出的反向代理；直连模式下改用 Host 白名单。
type MCPProxyPolicy struct {
	enabled  bool
	// direct 为 true 表示内网明文直连模式：跳过 X-Forwarded-* 与来源网段校验，
	// 改为按 allowedHosts 校验 Host 头（防 DNS rebinding）。
	direct       bool
	audience     string
	host         string
	allowedHosts []string
	proxies      []netip.Prefix
}

// NewMCPProxyPolicy 校验入口基址与信任边界。
// trustedProxyCIDRs 非空 → 反向代理模式（要求 https + 受信来源 + X-Forwarded-* 一致）。
// s.direct 模式（allow-insecure-internal）→ 允许 http，改用 allowedHosts 白名单校验 Host。
func NewMCPProxyPolicy(enabled bool, publicBaseURL string, trustedProxyCIDRs []string, extra ...MCPProxyOptions) (*MCPProxyPolicy, error) {
	if !enabled {
		return &MCPProxyPolicy{}, nil
	}
	var opt MCPProxyOptions
	if len(extra) > 0 {
		opt = extra[0]
	}
	base, err := url.Parse(publicBaseURL)
	if err != nil || base.Host == "" || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, errInvalidMCPProxyConfig
	}
	if opt.AllowInsecureInternal {
		if base.Scheme != "http" && base.Scheme != "https" {
			return nil, errInvalidMCPProxyConfig
		}
	} else if base.Scheme != "https" {
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
	if len(prefixes) == 0 && !opt.AllowInsecureInternal {
		return nil, errInvalidMCPProxyConfig
	}
	// 直连模式：无 CIDR 时按本机回环放行（内网直连部署的典型来源）。
	if len(prefixes) == 0 && opt.AllowInsecureInternal {
		if p, e := netip.ParsePrefix("127.0.0.0/8"); e == nil {
			prefixes = append(prefixes, p)
		}
		if p, e := netip.ParsePrefix("::1/128"); e == nil {
			prefixes = append(prefixes, p)
		}
	}
	hosts := make([]string, 0, len(opt.AllowedHosts))
	for _, h := range opt.AllowedHosts {
		if t := strings.TrimSpace(h); t != "" {
			hosts = append(hosts, t)
		}
	}
	direct := opt.AllowInsecureInternal && len(trustedProxyCIDRs) == 0
	return &MCPProxyPolicy{
		enabled: true, direct: direct,
		audience:     strings.TrimRight(publicBaseURL, "/") + "/admin/v2/mcp",
		host:         base.Host,
		allowedHosts: hosts,
		proxies:      prefixes,
	}, nil
}

// MCPProxyOptions 可选信任边界扩展（保持既有调用点兼容）。
type MCPProxyOptions struct {
	// AllowInsecureInternal 允许内网明文 http 直连入口。
	AllowInsecureInternal bool
	// AllowedHosts 直连模式下放行的 Host 白名单；为空时仅校验与基址 host 一致。
	AllowedHosts []string
}

func (p *MCPProxyPolicy) Audience() string {
	if p == nil {
		return ""
	}
	return p.audience
}
func (p *MCPProxyPolicy) Enabled() bool { return p != nil && p.enabled }

// Direct 报告当前是否处于内网明文直连模式（无反向代理终止 TLS）。
func (p *MCPProxyPolicy) Direct() bool { return p != nil && p.direct }

// Accepts 校验请求是否来自受信边界。
// 反向代理模式：来源须在可信网段内，且 X-Forwarded-Proto/Host 与 Host 三者与部署配置一致。
// 直连模式：跳过转发头校验，仅要求 Host 命中白名单（或等于基址 host）。
func (p *MCPProxyPolicy) Accepts(r *http.Request) bool {
	if p == nil || !p.enabled || r == nil || !p.trustedRemote(r.RemoteAddr) {
		return false
	}
	if p.direct {
		if !p.hostAllowed(r.Host) {
			return false
		}
	} else {
		if r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-Host") != p.host {
			return false
		}
		if r.Host != p.host {
			return false
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != strings.TrimSuffix(p.audience, "/admin/v2/mcp") {
		return false
	}
	return true
}

// hostAllowed 直连模式下判定 Host 是否可放行。
func (p *MCPProxyPolicy) hostAllowed(host string) bool {
	if host == p.host {
		return true
	}
	for _, h := range p.allowedHosts {
		if h == host {
			return true
		}
	}
	return false
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
