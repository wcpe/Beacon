package config

import "testing"

func TestMCPConfigEnabledRequiresHTTPSBaseAndTrustedProxy(t *testing.T) {
	valid := validMCPConfigBase()
	valid.MCP = MCPConfig{Enabled: true, PublicBaseURL: "https://beacon.example", TrustedProxyCIDRs: []string{"192.0.2.0/24"}}
	if err := valid.validate(); err != nil {
		t.Fatalf("合法 MCP 配置不应失败: %v", err)
	}
	for _, candidate := range []MCPConfig{{Enabled: true, PublicBaseURL: "http://beacon.example", TrustedProxyCIDRs: []string{"192.0.2.0/24"}}, {Enabled: true, PublicBaseURL: "https://beacon.example/admin", TrustedProxyCIDRs: []string{"192.0.2.0/24"}}, {Enabled: true, PublicBaseURL: "https://beacon.example"}} {
		cfg := validMCPConfigBase()
		cfg.MCP = candidate
		if err := cfg.validate(); err == nil {
			t.Fatalf("不安全 MCP 配置必须失败关闭: %+v", candidate)
		}
	}
}

func validMCPConfigBase() Config {
	cfg := Default()
	cfg.Auth.Password = "test-password"
	cfg.Auth.Secret = "test-secret"
	return cfg
}
