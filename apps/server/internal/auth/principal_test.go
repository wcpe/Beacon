package auth

import (
	"context"
	"testing"
)

// TestPrincipalRoundTrip 验证认证主体写入 context 后可携带语义字段、旧字段与能力原样取回。
func TestPrincipalRoundTrip(t *testing.T) {
	p := HumanPrincipal("alice")
	ctx := WithPrincipal(context.Background(), p)

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("应能从 context 取回认证主体")
	}
	if got.ID != p.ID || got.Kind != PrincipalKindHuman || got.AuthMethod != AuthMethodLoginToken || got.Source != SourceLogin || got.Role != "full" {
		t.Fatalf("主体基础字段不符：%+v", got)
	}
	if !got.HasCapability(CapabilityApprovalRequest) || !got.HasCapability(CapabilityApprovalDecide) {
		t.Fatalf("人工主体能力判定不符：%+v", got.Capabilities)
	}
}

// TestLegacyOperatorRoleCompatibility 验证新主体注入后旧的 Operator/Role 读取仍保持兼容。
func TestLegacyOperatorRoleCompatibility(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "apikey:ci", Operator: "apikey:ci", Source: SourceAPIKey, Role: "readonly"})

	if got := Operator(ctx); got != "apikey:ci" {
		t.Fatalf("旧 Operator 应仍可读取 apikey:ci，实际 %q", got)
	}
	if got := Role(ctx); got != "readonly" {
		t.Fatalf("旧 Role 应仍可读取 readonly，实际 %q", got)
	}
}

// TestMCPPrincipal 验证 MCP 主体具备稳定类型和认证方式。
func TestMCPPrincipal(t *testing.T) {
	p := MCPPrincipal("client-1", "自动化客户端", "full")
	if p.Kind != PrincipalKindMCP || p.Source != SourceMCP || p.AuthMethod != AuthMethodMCPToken {
		t.Fatalf("MCP 主体字段不符：%+v", p)
	}
	if p.HasCapability(CapabilityApprovalDecide) {
		t.Fatalf("MCP 主体不应具备审批决定能力：%+v", p.Capabilities)
	}
}

// TestMachinePrincipalsNeverDecide 验证机器主体即使旧角色为 full 也不会得到审批决定能力。
func TestMachinePrincipalsNeverDecide(t *testing.T) {
	for _, kind := range []string{PrincipalKindAPIKey, PrincipalKindMCP, PrincipalKindSystem} {
		p := NormalizePrincipal(Principal{ID: kind + ":1", Kind: kind, Role: "full", Capabilities: []string{CapabilityApprovalDecide, CapabilityApprovalRequest}})
		if p.HasCapability(CapabilityApprovalDecide) {
			t.Fatalf("%s 主体不应具备审批决定能力：%+v", kind, p.Capabilities)
		}
	}
}

// TestSystemPrincipal 验证系统主体具备受限能力且不被误判为人工主体。
func TestSystemPrincipal(t *testing.T) {
	p := SystemPrincipal()
	if p.Kind != PrincipalKindSystem || p.Source != SourceSystem || p.Operator != "system" || p.Role != "system" || p.AuthMethod != AuthMethodSystem {
		t.Fatalf("系统主体字段不符：%+v", p)
	}
	if !p.HasCapability(CapabilityManagementRead) || p.HasCapability(CapabilityApprovalDecide) || p.HasCapability(CapabilityApprovalRequest) {
		t.Fatalf("系统主体能力边界不符：%+v", p.Capabilities)
	}
}
