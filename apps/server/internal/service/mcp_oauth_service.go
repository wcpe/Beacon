package service

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

const (
	mcpAccessTokenTTL = 15 * time.Minute
	mcpClientIDPrefix = "mcp_"
	mcpSecretPrefix   = "mcs_"
	mcpTokenPrefix    = "mct_"
)

// MCPOAuthService 管理 MCP OAuth 的凭据和短期 bearer，不处理 HTTP 或 MCP 协议。
type MCPOAuthService struct {
	db        *gorm.DB
	repo      *repository.MCPOAuthRepository
	audit     *repository.AuditLogRepository
	now       func() time.Time
	approval  *ApprovalService
	requestMu sync.Mutex
}

// SetApprovalService 注入统一审批核心；客户端凭据的创建、轮换和启用均必须经它执行。
func (s *MCPOAuthService) SetApprovalService(approval *ApprovalService) { s.approval = approval }

// NewMCPOAuthService 构造 MCP OAuth 服务。
func NewMCPOAuthService(db *gorm.DB, repo *repository.MCPOAuthRepository, audit *repository.AuditLogRepository) *MCPOAuthService {
	return &MCPOAuthService{db: db, repo: repo, audit: audit, now: func() time.Time { return time.Now().UTC() }}
}

// MCPClientSecret 是创建或轮换响应中仅出现一次的明文 secret。
type MCPClientSecret struct {
	ClientID string
	Secret   string
	Prefix   string
}

// NewMCPClientSecret 生成一份尚未持久化的高熵客户端凭据。
func NewMCPClientSecret() (MCPClientSecret, string, error) {
	clientID, err := mcpRandom(mcpClientIDPrefix)
	if err != nil {
		return MCPClientSecret{}, "", err
	}
	secret, err := mcpRandom(mcpSecretPrefix)
	if err != nil {
		return MCPClientSecret{}, "", err
	}
	return MCPClientSecret{ClientID: clientID, Secret: secret, Prefix: secret[:12]}, mcpHash(secret), nil
}

// IssueAccessToken 按已认证 client secret 签发 audience-bound 短期 token。
func (s *MCPOAuthService) IssueAccessToken(clientID, clientSecret, audience, scope string) (string, time.Duration, auth.Principal, error) {
	return s.IssueAccessTokenFrom(clientID, clientSecret, audience, scope, "")
}

// IssueAccessTokenFrom 按已认证 client secret 签发 token，并以来源地址记专项审计。
func (s *MCPOAuthService) IssueAccessTokenFrom(clientID, clientSecret, audience, scope, clientIP string) (string, time.Duration, auth.Principal, error) {
	if clientID == "" || clientSecret == "" || audience == "" {
		s.auditTokenDenied(clientID, "invalid_request", clientIP)
		return "", 0, auth.Principal{}, apperr.ErrAdminUnauthorized
	}
	client, err := s.repo.FindClient(clientID)
	if err != nil || client == nil || client.Status != model.MCPClientStatusActive ||
		subtle.ConstantTimeCompare([]byte(client.SecretHash), []byte(mcpHash(clientSecret))) != 1 {
		s.auditTokenDenied(clientID, "invalid_client", clientIP)
		return "", 0, auth.Principal{}, apperr.ErrAdminUnauthorized
	}
	effectiveScope := scope
	if effectiveScope == "" {
		effectiveScope = client.Profile
	}
	if !mcpScopeAllowed(client.Profile, effectiveScope) {
		s.auditTokenDenied(client.ClientID, "invalid_scope", clientIP)
		return "", 0, auth.Principal{}, apperr.ErrForbidden
	}
	token, err := mcpRandom(mcpTokenPrefix)
	if err != nil {
		return "", 0, auth.Principal{}, err
	}
	now := s.now()
	expiresAt := now.Add(mcpAccessTokenTTL)
	stored := &model.MCPAccessToken{
		TokenHash: mcpHash(token), ClientID: client.ClientID, SecretVersion: client.SecretVersion,
		Audience: audience, Profile: client.Profile, Scope: effectiveScope, IssuedAt: now, ExpiresAt: expiresAt,
	}
	if err := s.repo.CreateAccessToken(stored); err != nil {
		return "", 0, auth.Principal{}, err
	}
	if err := s.audit.Create(mcpAudit(client.ClientID, "mcp.token.issued", "ok", clientIP)); err != nil {
		return "", 0, auth.Principal{}, err
	}
	return token, mcpAccessTokenTTL, auth.MCPPrincipal(client.ClientID, client.DisplayName, client.Profile), nil
}

func (s *MCPOAuthService) auditTokenDenied(clientID, reason, clientIP string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Create(&model.AuditLog{Operator: "mcp:" + clientID, Action: "mcp.token.denied", TargetType: model.TargetTypeMCPClient, TargetRef: clientID, Detail: reason, Result: "fail", ClientIP: clientIP})
}

// VerifyAccessToken 校验 token、client 状态、secret version 与固定 audience。
func (s *MCPOAuthService) VerifyAccessToken(raw, audience string) (auth.Principal, error) {
	if raw == "" || audience == "" {
		return auth.Principal{}, apperr.ErrAdminUnauthorized
	}
	token, err := s.repo.FindAccessToken(mcpHash(raw))
	if err != nil || token == nil || token.RevokedAt != nil || !s.now().Before(token.ExpiresAt) || token.Audience != audience {
		return auth.Principal{}, apperr.ErrAdminUnauthorized
	}
	client, err := s.repo.FindClient(token.ClientID)
	if err != nil || client == nil || client.Status != model.MCPClientStatusActive || client.SecretVersion != token.SecretVersion || client.Profile != token.Profile {
		return auth.Principal{}, apperr.ErrAdminUnauthorized
	}
	return auth.MCPPrincipal(client.ClientID, client.DisplayName, client.Profile), nil
}

// ListClients 返回脱敏客户端元数据；调用方不得把 secretHash 输出到边界外。
func (s *MCPOAuthService) ListClients(principal auth.Principal) ([]model.MCPOAuthClient, error) {
	if !auth.NormalizePrincipal(principal).HasCapability(auth.CapabilityManagementRead) {
		return nil, apperr.ErrForbidden
	}
	return s.repo.ListClients()
}

// GetClient 返回单个脱敏客户端元数据。
func (s *MCPOAuthService) GetClient(clientID string, principal auth.Principal) (*model.MCPOAuthClient, error) {
	if !auth.NormalizePrincipal(principal).HasCapability(auth.CapabilityManagementRead) {
		return nil, apperr.ErrForbidden
	}
	client, err := s.repo.FindClient(clientID)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, apperr.ErrMCPClientNotFound
	}
	return client, nil
}

// RevokeClient 直接吊销 active 客户端，立即使其所有 bearer 失效并记录专项审计。
func (s *MCPOAuthService) RevokeClient(clientID string, principal auth.Principal, clientIP string) error {
	principal = auth.NormalizePrincipal(principal)
	if !principal.IsHuman() || !principal.HasCapability(auth.CapabilityManagementDirect) {
		return apperr.ErrForbidden
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		ok, err := s.repo.WithTx(tx).RevokeClientCAS(clientID, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return apperr.ErrMCPClientNotFound
		}
		return s.audit.WithTx(tx).Create(&model.AuditLog{Operator: principal.AuditRef(), Action: "mcp.client.revoked", TargetType: model.TargetTypeMCPClient, TargetRef: clientID, Result: "ok", ClientIP: clientIP})
	})
}

func mcpRandom(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 MCP 凭据随机值失败: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func mcpHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mcpScopeAllowed(profile, scope string) bool {
	if scope == "" {
		return true
	}
	for _, item := range strings.Fields(scope) {
		if item != "observer" && item != profile {
			return false
		}
	}
	return profile == model.MCPClientProfileAutomation || scope == "observer"
}

func mcpAudit(clientID, action, result, clientIP string) *model.AuditLog {
	return &model.AuditLog{Operator: "mcp:" + clientID, Action: action, TargetType: "mcp_client", TargetRef: clientID, Result: result, ClientIP: clientIP}
}
