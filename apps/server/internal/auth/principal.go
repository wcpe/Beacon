package auth

import "context"

// 主体来源，保留给旧调用方兼容。
const (
	SourceLogin  = "login"
	SourceAPIKey = "api_key"
	SourceMCP    = "mcp"
	SourceSystem = "system"
)

// 主体类型。
const (
	PrincipalKindHuman  = "human"
	PrincipalKindAPIKey = "api_key"
	PrincipalKindMCP    = "mcp"
	PrincipalKindSystem = "system"
)

// 认证方式。
const (
	AuthMethodLoginToken = "login_token"
	AuthMethodAPIKey     = "api_key"
	AuthMethodMCPToken   = "mcp"
	AuthMethodSystem     = "system"
)

// 能力标识。
const (
	CapabilityManagementRead      = "management.read"
	CapabilityManagementDirect    = "management.direct"
	CapabilityApprovalRequest     = "approval.request"
	CapabilityApprovalRead        = "approval.read"
	CapabilityApprovalWithdrawOwn = "approval.withdraw.own"
	CapabilityApprovalDecide      = "approval.decide"
	CapabilityDeliveryApprove     = CapabilityApprovalRequest
	CapabilityEmergencyRollback   = CapabilityManagementDirect
)

// Principal 表示经过认证后的调用主体。
type Principal struct {
	ID            string
	Kind          string
	DisplayName   string
	CredentialRef string
	AuthMethod    string
	Operator      string
	Source        string
	Role          string
	Capabilities  []string
}

// HumanPrincipal 构造登录令牌主体。
func HumanPrincipal(username string) Principal {
	return normalize(Principal{
		ID: username, Kind: PrincipalKindHuman, DisplayName: username, Operator: username,
		Source: SourceLogin, Role: "full", AuthMethod: AuthMethodLoginToken,
		Capabilities: CapabilitiesForRole(PrincipalKindHuman, "full"),
	})
}

// APIKeyPrincipal 构造 API key 主体。
func APIKeyPrincipal(id, name, role, credentialRef string) Principal {
	return normalize(Principal{
		ID: id, Kind: PrincipalKindAPIKey, DisplayName: name, CredentialRef: credentialRef, Operator: id,
		Source: SourceAPIKey, Role: role, AuthMethod: AuthMethodAPIKey,
		Capabilities: CapabilitiesForRole(PrincipalKindAPIKey, role),
	})
}

// MCPPrincipal 构造 MCP 主体。
func MCPPrincipal(id, name, role string) Principal {
	return normalize(Principal{
		ID: id, Kind: PrincipalKindMCP, DisplayName: name, Operator: id,
		Source: SourceMCP, Role: role, AuthMethod: AuthMethodMCPToken,
		Capabilities: CapabilitiesForRole(PrincipalKindMCP, role),
	})
}

// CapabilitiesForRole 把旧角色映射为语义能力集合。
func CapabilitiesForRole(kind, role string) []string {
	if kind == PrincipalKindMCP {
		return mcpCapabilities(role)
	}
	caps := []string{CapabilityManagementRead}
	if role != "full" {
		return append(caps, CapabilityApprovalRead)
	}
	caps = append(caps, CapabilityManagementDirect, CapabilityApprovalRequest, CapabilityApprovalRead, CapabilityApprovalWithdrawOwn)
	if kind == PrincipalKindHuman {
		caps = append(caps, CapabilityApprovalDecide)
	}
	return caps
}

func mcpCapabilities(profile string) []string {
	caps := []string{CapabilityManagementRead, CapabilityApprovalRead}
	if profile != "automation" {
		return caps
	}
	return append(caps, CapabilityManagementDirect, CapabilityApprovalRequest, CapabilityApprovalWithdrawOwn)
}

// HasCapability 判断主体是否具备指定能力。
func (p Principal) HasCapability(capability string) bool {
	for _, c := range p.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// AddCapability 返回增加指定能力后的主体副本。
func (p Principal) AddCapability(capability string) Principal {
	if capability == "" || p.HasCapability(capability) {
		return p
	}
	p.Capabilities = append(p.Capabilities, capability)
	return normalize(p)
}

// RemoveCapability 返回移除指定能力后的主体副本。
func (p Principal) RemoveCapability(capability string) Principal {
	p.Capabilities = removeCapability(p.Capabilities, capability)
	return p
}

// StableKind 返回规范主体类型。
func (p Principal) StableKind() string {
	return normalize(p).Kind
}

// StableID 返回规范主体 ID。
func (p Principal) StableID() string {
	return normalize(p).ID
}

// AuditRef 返回规范审计引用。
func (p Principal) AuditRef() string {
	n := normalize(p)
	return n.Kind + ":" + n.ID
}

// IsHuman 判断主体是否为人工登录主体。
func (p Principal) IsHuman() bool {
	return p.StableKind() == PrincipalKindHuman
}

// SystemPrincipal 返回系统执行主体。
func SystemPrincipal() Principal {
	return normalize(Principal{ID: "system", Kind: PrincipalKindSystem, DisplayName: "system", Operator: "system", Source: SourceSystem, Role: SourceSystem, AuthMethod: AuthMethodSystem, Capabilities: []string{CapabilityManagementRead}})
}

// NormalizePrincipal 补齐旧字段构造的主体语义字段。
func NormalizePrincipal(principal Principal) Principal {
	return normalize(principal)
}

// WithPrincipal 把认证主体放入 context，并兼容旧的 operator / role 读取。
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	principal = normalize(principal)
	ctx = WithOperator(ctx, principal.Operator)
	ctx = WithRole(ctx, principal.Role)
	return context.WithValue(ctx, principalKey, principal)
}

// FromContext 从 context 读取主体。
func FromContext(ctx context.Context) (Principal, bool) {
	if v, ok := ctx.Value(principalKey).(Principal); ok {
		return normalize(v), true
	}
	return Principal{}, false
}

func normalize(p Principal) Principal {
	if sourceKind := kindFromSource(p.Source); sourceKind != "" {
		p.Kind = sourceKind
		p.AuthMethod = authMethodFromSource(p.Source)
	}
	if p.Kind == "" && p.Operator != "" {
		p.Kind = PrincipalKindHuman
	}
	if p.Source == "" {
		p.Source = sourceFromKind(p.Kind)
	}
	if p.AuthMethod == "" {
		p.AuthMethod = authMethodFromSource(p.Source)
	}
	if p.AuthMethod == "" && p.Kind == PrincipalKindHuman {
		p.AuthMethod = AuthMethodLoginToken
	}
	if p.ID == "" {
		p.ID = p.Operator
	}
	if p.DisplayName == "" {
		p.DisplayName = p.Operator
	}
	if p.Operator == "" && p.Kind != "" && p.ID != "" {
		p.Operator = p.Kind + ":" + p.ID
	}
	if len(p.Capabilities) == 0 && p.Role != "" {
		p.Capabilities = CapabilitiesForRole(p.Kind, p.Role)
	}
	if p.Kind == PrincipalKindAPIKey || p.Kind == PrincipalKindMCP || p.Kind == PrincipalKindSystem {
		p.Capabilities = removeCapability(p.Capabilities, CapabilityApprovalDecide)
	}
	return p
}

func kindFromSource(source string) string {
	switch source {
	case SourceLogin:
		return PrincipalKindHuman
	case SourceAPIKey:
		return PrincipalKindAPIKey
	case SourceMCP:
		return PrincipalKindMCP
	case SourceSystem:
		return PrincipalKindSystem
	default:
		return ""
	}
}

func removeCapability(caps []string, denied string) []string {
	out := make([]string, 0, len(caps))
	for _, cap := range caps {
		if cap != denied {
			out = append(out, cap)
		}
	}
	return out
}

func sourceFromKind(kind string) string {
	switch kind {
	case PrincipalKindHuman:
		return SourceLogin
	case PrincipalKindAPIKey:
		return SourceAPIKey
	case PrincipalKindMCP:
		return SourceMCP
	case PrincipalKindSystem:
		return SourceSystem
	default:
		return ""
	}
}

func authMethodFromSource(source string) string {
	switch source {
	case SourceLogin:
		return AuthMethodLoginToken
	case SourceAPIKey:
		return AuthMethodAPIKey
	case SourceMCP:
		return AuthMethodMCPToken
	case SourceSystem:
		return AuthMethodSystem
	default:
		return ""
	}
}
