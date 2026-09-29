package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestMCPConfigProductionModeDefaultsOffAndParsesYAML 验证生产模式开关（FR-237）：
// 默认关闭（零值，行为与既有完全一致），且 yaml 键 production-mode 可被正确解析。
func TestMCPConfigProductionModeDefaultsOffAndParsesYAML(t *testing.T) {
	if Default().MCP.ProductionMode {
		t.Fatal("MCP 生产模式必须默认关闭")
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte("mcp:\n  production-mode: true\n"), &cfg); err != nil {
		t.Fatalf("解析 mcp.production-mode 失败: %v", err)
	}
	if !cfg.MCP.ProductionMode {
		t.Fatal("mcp.production-mode: true 未被解析为开启")
	}
}

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
