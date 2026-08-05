package service

import (
	"encoding/json"
	"strconv"
	"time"

	"gorm.io/gorm"

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
	adapter := apiKeyApprovalAdapter{svc: svc}
	registry.Register(authz.OperationCredentialCreate, authz.RequireExecutionReceipt(adapter))
	registry.Register(authz.OperationCredentialRotate, authz.RequireExecutionReceipt(adapter))
}

type apiKeyApprovalAdapter struct{ svc *APIKeyService }

func (apiKeyApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a apiKeyApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	return a.svc.executeApprovedAPIKeyOperationInTx(tx, req, permit)
}

// ReadApprovalEvidence 按持久化 API 密钥目标读取当前安全事实，不读取明文或哈希。
func (a apiKeyApprovalAdapter) ReadApprovalEvidence(req authz.ApprovalRequest) (authz.ApprovalEvidence, error) {
	if a.svc == nil || a.svc.repo == nil {
		return authz.ApprovalEvidence{}, apperr.ErrInternal
	}
	if req.Operation.Kind == authz.OperationCredentialCreate {
		var count int64
		if err := a.svc.db.Model(&model.APIKey{}).Where("name = ? AND deleted_at = ?", req.Operation.ResourceID, model.SoftDeleteSentinel).Count(&count).Error; err != nil {
			return authz.ApprovalEvidence{}, err
		}
		return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: []authz.ApprovalEvidenceLine{
			{Label: "密钥名称", Value: req.Operation.ResourceID},
			{Label: "同名有效密钥数", Value: strconv.FormatInt(count, 10)},
		}}, nil
	}
	id, err := strconv.ParseUint(req.Operation.ResourceID, 10, 64)
	if err != nil || id == 0 {
		return authz.ApprovalEvidence{}, apperr.ErrApprovalTargetChanged
	}
	key, err := a.svc.repo.FindActiveByID(uint(id))
	if err != nil || key == nil {
		return authz.ApprovalEvidence{}, apperr.ErrApprovalTargetChanged
	}
	return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: []authz.ApprovalEvidenceLine{
		{Label: "密钥名称", Value: key.Name}, {Label: "角色", Value: key.Role},
	}}, nil
}

func (s *APIKeyService) executeApprovedAPIKeyOperationInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if err := ensureRequestPermit(req, permit); err != nil {
		return nil, err
	}
	var err error
	resultRef := req.Operation.ResourceID
	switch permit.Operation() {
	case authz.OperationCredentialCreate:
		var p apiKeyCreatePayload
		if err := json.Unmarshal(req.Payload, &p); err != nil {
			return nil, apperr.ErrInvalidParam
		}
		if err = ensurePermit(permit, authz.OperationCredentialCreate); err == nil {
			var key *model.APIKey
			var plaintext string
			plaintext, key, err = s.applyCreateInTx(tx, p.Name, p.Role, p.expiresAt(), p.Operator, p.ClientIP)
			if err == nil {
				err = s.storeCredentialSecretInTx(tx, req.RequestID, plaintext)
			}
			if key != nil {
				resultRef = strconv.FormatUint(uint64(key.ID), 10)
			}
		}
	case authz.OperationCredentialRotate:
		var p apiKeyRotatePayload
		if err := json.Unmarshal(req.Payload, &p); err != nil {
			return nil, apperr.ErrInvalidParam
		}
		if err = ensurePermit(permit, authz.OperationCredentialRotate); err == nil {
			plaintext, _, resetErr := s.applyResetInTx(tx, p.ID, p.Operator, p.ClientIP)
			if resetErr != nil {
				err = resetErr
			} else {
				err = s.storeCredentialSecretInTx(tx, req.RequestID, plaintext)
			}
		}
	default:
		return nil, apperr.ErrInvalidParam
	}
	if err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: resultRef}
	return nil, tx.Create(receipt).Error
}

// RequestCreate 创建 API key 新增审批；明文与哈希均不进入审批 payload。
func (s *APIKeyService) RequestCreate(name, role string, expiresAt *time.Time, reason, operator, clientIP, idempotencyKey string, principal auth.Principal) (ApprovalTicketView, error) {
	payload := apiKeyCreatePayload{Name: name, Role: role, Reason: reason, Operator: operator, ClientIP: clientIP}
	if expiresAt != nil {
		payload.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	return s.requestAPIKeyApproval(authz.OperationCredentialCreate, name, idempotencyKey, reason, clientIP, payload.toMap(), principal)
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
	evidence, err := (apiKeyApprovalAdapter{svc: s}).ReadApprovalEvidence(authz.ApprovalRequest{Operation: authz.Operation{Kind: kind, Resource: model.TargetTypeAPIKey, ResourceID: resourceID}})
	if err != nil || evidence.EvidenceStatus != "available" {
		return ApprovalTicketView{}, apperr.ErrApprovalTargetChanged
	}
	created, err := s.approval.Request(authz.Operation{
		Kind: kind, Resource: model.TargetTypeAPIKey, ResourceID: resourceID,
		IdempotencyKey: key, RiskLevel: "high", Reason: reason, EvidenceSnapshot: evidence.CurrentFactsSummary,
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
