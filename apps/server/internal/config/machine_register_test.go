package config

import "testing"

// FR-222 机器注册通道的启动校验（见 docs/specs/internal-trust-channel.md §3.2）：
// 开关开启时 agent-token 不得为留空或已知弱默认值（默认值与开关同开必须 fail-fast）。
func TestAllowMachineRegisterRequiresStrongAgentToken(t *testing.T) {
	strong := validMCPConfigBase()
	strong.AgentToken = "8f3b1c0d5e7a9246b0c1d2e3f4a5b6c7"
	strong.MCP.AllowMachineRegister = true
	if err := strong.validate(); err != nil {
		t.Fatalf("开关开启 + 强随机 token 应通过校验，实际 %v", err)
	}

	for name, token := range map[string]string{
		"内置默认值":  DefaultAgentToken,
		"配置样例默认": ExampleAgentToken,
		"留空":     "",
		"仅空白":    "   ",
	} {
		cfg := validMCPConfigBase()
		cfg.AgentToken = token
		cfg.MCP.AllowMachineRegister = true
		if err := cfg.validate(); err == nil {
			t.Fatalf("开关开启但 agent-token 为%s时必须拒绝启动", name)
		}
	}
}

// TestMachineRegisterDefaultOffKeepsWeakTokenAccepted 默认关闭时不得因弱 token 影响既有部署（向后兼容）。
func TestMachineRegisterDefaultOffKeepsWeakTokenAccepted(t *testing.T) {
	for _, token := range []string{"", DefaultAgentToken, ExampleAgentToken, "any-token"} {
		cfg := validMCPConfigBase()
		cfg.AgentToken = token
		if cfg.MCP.AllowMachineRegister {
			t.Fatal("默认配置的 allow-machine-register 必须为 false")
		}
		if err := cfg.validate(); err != nil {
			t.Fatalf("开关关闭时不应约束 agent-token（%q），实际 %v", token, err)
		}
	}
}
