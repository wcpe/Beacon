package service

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// SetApprovalService 注入 FR-207 统一审批核心。
func (s *APIKeyService) SetApprovalService(approval *ApprovalService) {
	s.approval = approval
}

// RegisterAPIKeyApprovalAdapters 注册 API key 危险操作执行适配器。
func RegisterAPIKeyApprovalAdapters(registry *authz.ApprovalRegistry, svc *APIKeyService) {
	if registry == nil || svc == nil {
		return
	}
	registry.Register(authz.OperationCredentialCreate, authz.AdapterFunc(svc.executeApprovedAPIKeyOperation))
	registry.Register(authz.OperationCredentialRotate, authz.AdapterFunc(svc.executeApprovedAPIKeyOperation))
}

func (s *APIKeyService) executeApprovedAPIKeyOperation(req authz.ApprovalRequest, permit authz.Permit) error {
	switch permit.Operation() {
	case authz.OperationCredentialCreate:
		var p apiKeyCreatePayload
		if err := json.Unmarshal(req.Payload, &p); err != nil {
			return apperr.ErrInvalidParam
		}
		_, _, err := s.CreateApproved(p.Name, p.Role, p.expiresAt(), p.Operator, p.ClientIP, permit)
		return err
	case authz.OperationCredentialRotate:
		var p apiKeyRotatePayload
		if err := json.Unmarshal(req.Payload, &p); err != nil {
			return apperr.ErrInvalidParam
		}
		_, _, err := s.ResetApproved(p.ID, p.Operator, p.ClientIP, permit)
		return err
	default:
		return apperr.ErrInvalidParam
	}
}

// RequestCreate 创建 API key 新增审批；明文与哈希均不进入审批 payload。
func (s *APIKeyService) RequestCreate(name, role string, expiresAt *time.Time, reason, operator, clientIP, idempotencyKey string, principal auth.Principal) (ApprovalTicketView, error) {
	payload := apiKeyCreatePayload{Name: name, Role: role, Reason: reason, Operator: operator, ClientIP: clientIP}
	if expiresAt != nil {
		payload.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	return s.requestAPIKeyApproval(authz.OperationCredentialCreate, "new", idempotencyKey, reason, clientIP, payload.toMap(), principal)
}

// RequestReset 创建 API key 轮换审批；批准后生成新明文但不再通过兼容入口返回。
func (s *APIKeyService) RequestReset(id uint, reason, operator, clientIP, idempotencyKey string, principal auth.Principal) (ApprovalTicketView, error) {
	payload := map[string]any{"schemaVersion": approvalSchemaVersion, "id": id, "operator": operator, "clientIP": clientIP}
	return s.requestAPIKeyApproval(authz.OperationCredentialRotate, strconv.FormatUint(uint64(id), 10), idempotencyKey, reason, clientIP, payload, principal)
}

func (s *APIKeyService) requestAPIKeyApproval(kind, resourceID, explicitKey, reason, clientIP string, payload map[string]any, principal auth.Principal) (ApprovalTicketView, error) {
	if s.approval == nil {
		return ApprovalTicketView{}, apperr.ErrInternal
	}
	if reason == "" {
		return ApprovalTicketView{}, apperr.ErrApprovalReasonRequired
	}
	key := explicitKey
	if key == "" {
		key = approvalIdempotencyKey(kind, resourceID, payload)
	}
	created, err := s.approval.Request(authz.Operation{
		Kind: kind, Resource: model.TargetTypeAPIKey, ResourceID: resourceID,
		IdempotencyKey: key, RiskLevel: "high", Reason: reason,
	}, payload, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: created.RequestID, Status: created.Status, OperationKey: created.OperationKey, SecretReturned: false}, nil
}

type apiKeyCreatePayload struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	Reason    string `json:"reason"`
	Operator  string `json:"operator"`
	ClientIP  string `json:"clientIP"`
}

func (p apiKeyCreatePayload) expiresAt() *time.Time {
	if p.ExpiresAt == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, p.ExpiresAt)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

func (p apiKeyCreatePayload) toMap() map[string]any {
	out := map[string]any{"schemaVersion": approvalSchemaVersion, "name": p.Name, "role": p.Role, "operator": p.Operator, "clientIP": p.ClientIP}
	if p.ExpiresAt != "" {
		out["expiresAt"] = p.ExpiresAt
	}
	return out
}

type apiKeyRotatePayload struct {
	ID       uint   `json:"id"`
	Operator string `json:"operator"`
	ClientIP string `json:"clientIP"`
}

// CreateApproved 只能由审批适配器持 permit 调用，返回明文仅供丢弃或专项测试。
func (s *APIKeyService) CreateApproved(name, role string, expiresAt *time.Time, operator, clientIP string, permit authz.Permit) (string, *model.APIKey, error) {
	if err := ensurePermit(permit, authz.OperationCredentialCreate); err != nil {
		return "", nil, err
	}
	return s.Create(name, role, expiresAt, operator, clientIP)
}

// ResetApproved 只能由审批适配器持 permit 调用，返回明文仅供丢弃或专项测试。
func (s *APIKeyService) ResetApproved(id uint, operator, clientIP string, permit authz.Permit) (string, *model.APIKey, error) {
	if err := ensurePermit(permit, authz.OperationCredentialRotate); err != nil {
		return "", nil, err
	}
	return s.Reset(id, operator, clientIP)
}
