package service

import (
	"encoding/json"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// MCPClientApprovalTicket 是一次 MCP 客户端危险生命周期申请的最小响应。
// ClientSecret 只会在首次创建申请的同步响应中出现。
type MCPClientApprovalTicket struct {
	ApprovalRequestID string `json:"approvalRequestId"`
	ClientID          string `json:"clientId"`
	ClientSecret      string `json:"clientSecret,omitempty"`
	Status            string `json:"status"`
}

type mcpClientChangePayload struct {
	SchemaVersion int    `json:"schemaVersion"`
	ChangeID      string `json:"changeId"`
	ClientID      string `json:"clientId"`
	DisplayName   string `json:"displayName"`
	Profile       string `json:"profile"`
	TargetVersion uint   `json:"targetVersion"`
	Operator      string `json:"operator"`
	ClientIP      string `json:"clientIP"`
}

// RegisterMCPOAuthApprovalAdapters 注册 MCP 凭据的唯一审批执行入口。
func RegisterMCPOAuthApprovalAdapters(registry *authz.ApprovalRegistry, svc *MCPOAuthService) {
	if registry == nil || svc == nil {
		return
	}
	adapter := mcpOAuthApprovalAdapter{svc: svc}
	for _, kind := range []string{authz.OperationMCPOAuthClientCreate, authz.OperationMCPOAuthClientRotate, authz.OperationMCPOAuthClientEnable} {
		registry.RegisterDescriptor(authz.OperationDescriptor{
			Key: kind, SchemaVersion: approvalSchemaVersion, Capability: auth.CapabilityApprovalRequest,
			RiskLevel: "high", RequiresTerminalCallback: true,
		}, authz.RequireExecutionReceipt(adapter))
	}
}

type mcpOAuthApprovalAdapter struct{ svc *MCPOAuthService }

func (mcpOAuthApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a mcpOAuthApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if a.svc == nil || tx == nil {
		return nil, apperr.ErrForbidden
	}
	p, change, err := a.loadPendingChange(tx, req, permit)
	if err != nil {
		return nil, err
	}
	if err := a.applyChange(tx, req, p, change); err != nil {
		return nil, err
	}
	if ok, err := a.svc.repo.WithTx(tx).ApplyChangeCAS(change.ChangeID); err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, apperr.ErrApprovalTargetChanged
	}
	receipt := &model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: p.ClientID}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	return nil, a.svc.audit.WithTx(tx).Create(mcpAudit(p.ClientID, "mcp.client."+change.ChangeType+".applied", "ok", p.ClientIP))
}

func (a mcpOAuthApprovalAdapter) loadPendingChange(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (mcpClientChangePayload, *model.MCPOAuthClientChange, error) {
	var p mcpClientChangePayload
	if err := json.Unmarshal(req.Payload, &p); err != nil || p.SchemaVersion != approvalSchemaVersion || p.ChangeID == "" || p.ClientID == "" || !model.IsValidMCPClientProfile(p.Profile) {
		return p, nil, apperr.ErrInvalidParam
	}
	if err := ensurePermit(permit, req.Operation.Kind); err != nil {
		return p, nil, err
	}
	change, err := a.svc.repo.WithTx(tx).FindChangeByApprovalRequest(req.RequestID)
	if err != nil || change == nil {
		if err != nil {
			return p, nil, err
		}
		return p, nil, apperr.ErrApprovalTargetChanged
	}
	if change.Status != model.MCPClientChangePending || change.ChangeID != p.ChangeID || change.ClientID != p.ClientID || change.Profile != p.Profile || change.TargetVersion != p.TargetVersion || change.DisplayName != p.DisplayName {
		return p, nil, apperr.ErrApprovalTargetChanged
	}
	return p, change, nil
}

func (a mcpOAuthApprovalAdapter) applyChange(tx *gorm.DB, req authz.ApprovalRequest, p mcpClientChangePayload, change *model.MCPOAuthClientChange) error {
	repo := a.svc.repo.WithTx(tx)
	switch req.Operation.Kind {
	case authz.OperationMCPOAuthClientCreate:
		if change.ChangeType != model.MCPClientChangeCreate || p.TargetVersion != 1 {
			return apperr.ErrApprovalTargetChanged
		}
		return repo.CreateClient(&model.MCPOAuthClient{ClientID: p.ClientID, DisplayName: p.DisplayName, SecretHash: change.SecretHash, SecretPrefix: change.SecretPrefix, Profile: p.Profile, Status: model.MCPClientStatusActive, SecretVersion: 1, CreatedBy: p.Operator})
	case authz.OperationMCPOAuthClientRotate:
		if change.ChangeType != model.MCPClientChangeRotate || p.TargetVersion < 2 {
			return apperr.ErrApprovalTargetChanged
		}
		result := tx.Model(&model.MCPOAuthClient{}).Where("client_id = ? AND status = ? AND secret_version = ?", p.ClientID, model.MCPClientStatusActive, p.TargetVersion-1).Updates(map[string]any{"secret_hash": change.SecretHash, "secret_prefix": change.SecretPrefix, "secret_version": p.TargetVersion})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.ErrApprovalTargetChanged
		}
		return nil
	case authz.OperationMCPOAuthClientEnable:
		if change.ChangeType != model.MCPClientChangeEnable {
			return apperr.ErrApprovalTargetChanged
		}
		result := tx.Model(&model.MCPOAuthClient{}).Where("client_id = ? AND status = ? AND secret_version = ?", p.ClientID, model.MCPClientStatusRevoked, p.TargetVersion).Updates(map[string]any{"status": model.MCPClientStatusActive, "revoked_at": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.ErrApprovalTargetChanged
		}
		return nil
	default:
		return apperr.ErrInvalidParam
	}
}

func (a mcpOAuthApprovalAdapter) CompleteTerminalInTx(tx *gorm.DB, req authz.ApprovalRequest, _ string) error {
	if a.svc == nil || tx == nil {
		return apperr.ErrForbidden
	}
	ok, err := a.svc.repo.WithTx(tx).InvalidateChangeCAS(req.RequestID)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrApprovalTargetChanged
	}
	return nil
}

// RequestCreate 创建 MCP 客户端审批，明文 secret 只保留在首次同步响应中。
func (s *MCPOAuthService) RequestCreate(displayName, profile, reason, idempotencyKey string, principal auth.Principal, clientIP string) (MCPClientApprovalTicket, error) {
	if !model.IsValidMCPClientProfile(profile) || strings.TrimSpace(displayName) == "" {
		return MCPClientApprovalTicket{}, apperr.ErrInvalidParam
	}
	return s.requestCredentialChange(authz.OperationMCPOAuthClientCreate, displayName, profile, reason, idempotencyKey, principal, clientIP, nil)
}

// RequestRotate 创建 MCP 客户端 secret 轮换审批。
func (s *MCPOAuthService) RequestRotate(clientID, reason, idempotencyKey string, principal auth.Principal, clientIP string) (MCPClientApprovalTicket, error) {
	client, err := s.repo.FindClient(clientID)
	if err != nil {
		return MCPClientApprovalTicket{}, err
	}
	if client == nil || client.Status != model.MCPClientStatusActive {
		return MCPClientApprovalTicket{}, apperr.ErrMCPClientNotFound
	}
	return s.requestCredentialChange(authz.OperationMCPOAuthClientRotate, client.DisplayName, client.Profile, reason, idempotencyKey, principal, clientIP, client)
}

// RequestEnable 创建已吊销 MCP 客户端重新启用审批。
func (s *MCPOAuthService) RequestEnable(clientID, reason, idempotencyKey string, principal auth.Principal, clientIP string) (MCPClientApprovalTicket, error) {
	client, err := s.repo.FindClient(clientID)
	if err != nil {
		return MCPClientApprovalTicket{}, err
	}
	if client == nil || client.Status != model.MCPClientStatusRevoked {
		return MCPClientApprovalTicket{}, apperr.ErrMCPClientNotFound
	}
	return s.requestCredentialChange(authz.OperationMCPOAuthClientEnable, client.DisplayName, client.Profile, reason, idempotencyKey, principal, clientIP, client)
}

func (s *MCPOAuthService) requestCredentialChange(kind, displayName, profile, reason, idempotencyKey string, principal auth.Principal, clientIP string, current *model.MCPOAuthClient) (MCPClientApprovalTicket, error) {
	if s.approval == nil || !principal.IsHuman() || strings.TrimSpace(reason) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return MCPClientApprovalTicket{}, apperr.ErrForbidden
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if existing, err := s.findExistingChange(kind, idempotencyKey, principal, displayName, profile, current); err != nil || existing != nil {
		if err != nil {
			return MCPClientApprovalTicket{}, err
		}
		return *existing, nil
	}
	secret, secretHash, err := NewMCPClientSecret()
	if err != nil {
		return MCPClientApprovalTicket{}, err
	}
	if current != nil {
		secret.ClientID = current.ClientID
	}
	targetVersion := uint(1)
	changeType := model.MCPClientChangeCreate
	if current != nil {
		targetVersion = current.SecretVersion
		changeType = model.MCPClientChangeEnable
		if kind == authz.OperationMCPOAuthClientRotate {
			targetVersion++
			changeType = model.MCPClientChangeRotate
		}
	}
	changeID, err := mcpRandom("mcpc_")
	if err != nil {
		return MCPClientApprovalTicket{}, err
	}
	payload := mcpClientChangePayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, ClientID: secret.ClientID, DisplayName: displayName, Profile: profile, TargetVersion: targetVersion, Operator: principal.AuditRef(), ClientIP: clientIP}
	body := map[string]any{"schemaVersion": payload.SchemaVersion, "changeId": payload.ChangeID, "clientId": payload.ClientID, "displayName": payload.DisplayName, "profile": payload.Profile, "targetVersion": payload.TargetVersion, "operator": payload.Operator, "clientIP": payload.ClientIP}
	req, err := s.approval.request(authz.Operation{Kind: kind, Resource: model.TargetTypeMCPClient, ResourceID: secret.ClientID, IdempotencyKey: idempotencyKey, RiskLevel: "high", Reason: reason, EvidenceSnapshot: []authz.ApprovalEvidenceLine{{Label: "客户端", Value: secret.ClientID}, {Label: "配置", Value: profile}}}, body, principal, clientIP, func(tx *gorm.DB, request authz.ApprovalRequest) error {
		return s.repo.WithTx(tx).CreateChange(&model.MCPOAuthClientChange{ChangeID: changeID, ApprovalRequestID: request.RequestID, ClientID: secret.ClientID, ChangeType: changeType, DisplayName: displayName, SecretHash: secretHash, SecretPrefix: secret.Prefix, Profile: profile, TargetVersion: targetVersion, Status: model.MCPClientChangePending})
	})
	if err != nil {
		return MCPClientApprovalTicket{}, err
	}
	return MCPClientApprovalTicket{ApprovalRequestID: req.RequestID, ClientID: secret.ClientID, ClientSecret: secret.Secret, Status: req.Status}, nil
}

func (s *MCPOAuthService) findExistingChange(kind, key string, principal auth.Principal, displayName, profile string, current *model.MCPOAuthClient) (*MCPClientApprovalTicket, error) {
	p := auth.NormalizePrincipal(principal)
	req, err := s.approval.repo.FindByPrincipalOperationIdempotency(p.StableKind(), p.StableID(), kind, key)
	if err != nil || req == nil {
		return nil, err
	}
	change, err := s.repo.FindChangeByApprovalRequest(req.RequestID)
	if err != nil {
		return nil, err
	}
	if change == nil || change.DisplayName != displayName || change.Profile != profile || (current != nil && change.ClientID != current.ClientID) {
		return nil, apperr.ErrIdempotencyKeyReused
	}
	return &MCPClientApprovalTicket{ApprovalRequestID: req.RequestID, ClientID: change.ClientID, Status: req.Status}, nil
}
