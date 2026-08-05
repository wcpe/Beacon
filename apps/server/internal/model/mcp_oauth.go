package model

import "time"

const (
	// MCPClientProfileObserver 仅允许读取 MCP 工具。
	MCPClientProfileObserver = "observer"
	// MCPClientProfileAutomation 允许低风险动作和提交自己的审批申请。
	MCPClientProfileAutomation = "automation"

	// MCPClientStatusActive 表示客户端可换取 MCP 令牌。
	MCPClientStatusActive = "active"
	// MCPClientStatusRevoked 表示客户端已被止损吊销。
	MCPClientStatusRevoked = "revoked"

	// MCPClientChangePending 表示待审批应用的客户端变更。
	MCPClientChangePending = "pending"
	// MCPClientChangeApplied 表示已由审批执行器应用的客户端变更。
	MCPClientChangeApplied = "applied"
	// MCPClientChangeInvalidated 表示终态审批使变更永久失效。
	MCPClientChangeInvalidated = "invalidated"

	// MCPClientChangeCreate 是待审批的新客户端凭据变更。
	MCPClientChangeCreate = "create"
	// MCPClientChangeRotate 是待审批的 secret 轮换变更。
	MCPClientChangeRotate = "rotate"
	// MCPClientChangeEnable 是待审批的已吊销客户端重新启用变更。
	MCPClientChangeEnable = "enable"
)

// MCPOAuthClient 是一个外部 MCP 集成的可撤销认证主体；明文 secret 永不入库。
type MCPOAuthClient struct {
	ID uint `gorm:"primaryKey;autoIncrement"`
	// ClientID 是公开且不可变的高熵标识。
	ClientID string `gorm:"column:client_id;size:96;uniqueIndex;not null"`
	// DisplayName 只供人类辨识，不参与认证或寻址。
	DisplayName string `gorm:"column:display_name;size:128;not null"`
	// SecretHash 只保存当前版本 secret 的 SHA-256 摘要。
	SecretHash string `gorm:"column:secret_hash;size:64;not null"`
	// SecretPrefix 是不能反推完整 secret 的展示前缀。
	SecretPrefix string `gorm:"column:secret_prefix;size:16;not null"`
	// Profile 固定为 observer 或 automation。
	Profile string `gorm:"column:profile;size:32;not null"`
	Status  string `gorm:"column:status;size:32;not null;index"`
	// SecretVersion 每次轮换单调递增，使旧 access token 立即失效。
	SecretVersion uint   `gorm:"column:secret_version;not null"`
	CreatedBy     string `gorm:"column:created_by;size:160;not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
}

// TableName 固定 MCP OAuth 客户端表名。
func (MCPOAuthClient) TableName() string { return "mcp_oauth_client" }

// MCPOAuthClientChange 是审批前生成的不可变凭据变更，不参与认证。
type MCPOAuthClientChange struct {
	ID                uint   `gorm:"primaryKey;autoIncrement"`
	ChangeID          string `gorm:"column:change_id;size:96;uniqueIndex;not null"`
	ApprovalRequestID string `gorm:"column:approval_request_id;size:96;uniqueIndex;not null"`
	ClientID          string `gorm:"column:client_id;size:96;index;not null"`
	ChangeType        string `gorm:"column:change_type;size:32;not null"`
	DisplayName       string `gorm:"column:display_name;size:128"`
	SecretHash        string `gorm:"column:secret_hash;size:64;not null"`
	SecretPrefix      string `gorm:"column:secret_prefix;size:16;not null"`
	Profile           string `gorm:"column:profile;size:32;not null"`
	TargetVersion     uint   `gorm:"column:target_version;not null"`
	Status            string `gorm:"column:status;size:32;not null;index"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TableName 固定 MCP OAuth 客户端待应用变更表名。
func (MCPOAuthClientChange) TableName() string { return "mcp_oauth_client_change" }

// MCPAccessToken 是短期、不透明且仅保存哈希的 MCP bearer。
type MCPAccessToken struct {
	ID            uint       `gorm:"primaryKey;autoIncrement"`
	TokenHash     string     `gorm:"column:token_hash;size:64;uniqueIndex;not null"`
	ClientID      string     `gorm:"column:client_id;size:96;index;not null"`
	SecretVersion uint       `gorm:"column:secret_version;not null"`
	Audience      string     `gorm:"column:audience;size:512;not null"`
	Profile       string     `gorm:"column:profile;size:32;not null"`
	Scope         string     `gorm:"column:scope;size:512;not null"`
	IssuedAt      time.Time  `gorm:"column:issued_at;index"`
	ExpiresAt     time.Time  `gorm:"column:expires_at;index"`
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
}

// TableName 固定 MCP access token 表名。
func (MCPAccessToken) TableName() string { return "mcp_access_token" }

// IsValidMCPClientProfile 校验固定 MCP profile。
func IsValidMCPClientProfile(profile string) bool {
	return profile == MCPClientProfileObserver || profile == MCPClientProfileAutomation
}
