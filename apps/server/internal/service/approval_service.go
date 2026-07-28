package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

const approvalTTL = 24 * time.Hour

// ApprovalListFilter 描述审批列表筛选条件。
type ApprovalListFilter struct {
	Status        string
	OperationKey  string
	RiskLevel     string
	RequesterType string
	RequesterID   string
}

// ApprovalService 编排统一审批请求。
type ApprovalService struct {
	db       *gorm.DB
	repo     *repository.ApprovalRequestRepository
	audit    *repository.AuditLogRepository
	registry *authz.ApprovalRegistry
}

// NewApprovalService 构造审批服务。
func NewApprovalService(db *gorm.DB, repo *repository.ApprovalRequestRepository, audit *repository.AuditLogRepository, registry *authz.ApprovalRegistry) *ApprovalService {
	return &ApprovalService{db: db, repo: repo, audit: audit, registry: registry}
}

// Request 创建或复用幂等审批请求。
func (s *ApprovalService) Request(op authz.Operation, payload map[string]any, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	reason := strings.TrimSpace(op.Reason)
	if reason == "" {
		return model.ApprovalRequest{}, apperr.ErrApprovalReasonRequired
	}
	if op.Kind == "" {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	if !validIdempotencyKey(op.IdempotencyKey) {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	if err := authz.Authorize(principal, op); err != nil {
		return model.ApprovalRequest{}, err
	}
	body, hash, err := freezePayload(payload)
	if err != nil {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	principal = authNormalize(principal)
	operationKey := normalizeOperationKey(op.Kind)
	var out model.ApprovalRequest
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		existing, e := repo.FindByPrincipalOperationIdempotency(principal.StableKind(), principal.StableID(), operationKey, op.IdempotencyKey)
		if e != nil {
			return e
		}
		if existing != nil {
			if existing.FrozenPayloadSHA256 != hash {
				return apperr.ErrIdempotencyKeyReused
			}
			out = *existing
			return nil
		}
		expiresAt := time.Now().UTC().Add(approvalTTL)
		req := model.ApprovalRequest{
			RequestID: newApprovalRequestID(), OperationKey: operationKey, OperationKind: op.Kind,
			RiskLevel: normalizeRiskLevel(op.RiskLevel), ResourceType: op.Resource, ResourceID: op.ResourceID,
			IdempotencyKey: op.IdempotencyKey, RequestReason: reason, SafeSummary: safeSummary(op),
			Payload: string(body), FrozenPayloadSHA256: hash, Status: model.ApprovalStatusPending,
			RequesterType: principal.StableKind(), RequesterID: principal.StableID(), RequestedBy: principal.AuditRef(),
			ExpiresAt: &expiresAt, Version: 1,
		}
		if e := repo.Create(&req); e != nil {
			return e
		}
		out = req
		return s.audit.WithTx(tx).Create(approvalAudit(&req, principal.AuditRef(), model.ActionApprovalRequest, model.ResultOK, clientIP, reason))
	})
	return out, err
}

// List 查询审批请求列表，机器主体只能看自己的申请。
func (s *ApprovalService) List(filter ApprovalListFilter, principal auth.Principal) ([]model.ApprovalRequest, error) {
	principal = authNormalize(principal)
	if !principal.HasCapability(auth.CapabilityApprovalRead) {
		return nil, apperr.ErrForbidden
	}
	if !principal.IsHuman() {
		filter.RequesterType = principal.StableKind()
		filter.RequesterID = principal.StableID()
	}
	return s.repo.List(filter.Status, filter.OperationKey, filter.RiskLevel, filter.RequesterType, filter.RequesterID)
}

// Detail 查询审批请求详情。
func (s *ApprovalService) Detail(ref string, principal auth.Principal) (model.ApprovalRequest, error) {
	principal = authNormalize(principal)
	if !principal.HasCapability(auth.CapabilityApprovalRead) {
		return model.ApprovalRequest{}, apperr.ErrForbidden
	}
	req, err := s.findByRef(ref)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	if !principal.IsHuman() && !sameRequester(req, principal) {
		return model.ApprovalRequest{}, apperr.ErrApprovalNotFound
	}
	return *req, nil
}

// Approve 批准并同步执行审批请求。
func (s *ApprovalService) Approve(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	approved, err := s.markExecuting(ref, principal, clientIP)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	execReq := toAuthzRequest(&approved)
	execReq.Actor = principal.AuditRef()
	if err := s.registry.Execute(execReq, approved.FrozenPayloadSHA256); err != nil {
		failed, markErr := s.markExecuted(&approved, model.ApprovalStatusFailed, err.Error())
		if markErr != nil {
			return failed, markErr
		}
		return failed, err
	}
	return s.markExecuted(&approved, model.ApprovalStatusSucceeded, "")
}

func (s *ApprovalService) markExecuting(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	principal = authNormalize(principal)
	var out model.ApprovalRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		req, err := findByRef(s.repo.WithTx(tx), ref)
		if err != nil {
			return err
		}
		if err := ensureDecidable(req, principal); err != nil {
			return err
		}
		now := time.Now().UTC()
		if req.ExpiresAt != nil && now.After(*req.ExpiresAt) {
			if err := s.expirePending(tx, req); err != nil {
				return err
			}
			return apperr.ErrApprovalExpired
		}
		decider := principal.AuditRef()
		oldVersion := req.Version
		updates := map[string]any{
			"status": model.ApprovalStatusExecuting, "decider_type": principal.StableKind(), "decider_id": principal.StableID(),
			"approved_by": decider, "approved_at": now, "decided_at": now, "version": oldVersion + 1,
		}
		res := tx.Model(&model.ApprovalRequest{}).
			Where("id = ? AND status = ? AND version = ?", req.ID, model.ApprovalStatusPending, oldVersion).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return apperr.ErrIllegalState
		}
		req.Status = model.ApprovalStatusExecuting
		req.DeciderType = principal.StableKind()
		req.DeciderID = principal.StableID()
		req.ApprovedBy = &decider
		req.ApprovedAt = &now
		req.DecidedAt = &now
		req.Version = oldVersion + 1
		out = *req
		return s.audit.WithTx(tx).Create(approvalAudit(req, principal.AuditRef(), model.ActionApprovalApprove, model.ResultOK, clientIP, ""))
	})
	return out, err
}

func (s *ApprovalService) expirePending(tx *gorm.DB, req *model.ApprovalRequest) error {
	oldVersion := req.Version
	res := tx.Model(&model.ApprovalRequest{}).
		Where("id = ? AND status = ? AND version = ?", req.ID, model.ApprovalStatusPending, oldVersion).
		Updates(map[string]any{"status": model.ApprovalStatusExpired, "version": oldVersion + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return apperr.ErrIllegalState
	}
	return nil
}

func (s *ApprovalService) markExecuted(req *model.ApprovalRequest, status, failureReason string) (model.ApprovalRequest, error) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		fresh, err := s.repo.WithTx(tx).FindByID(req.ID)
		if err != nil {
			return err
		}
		if fresh == nil {
			return apperr.ErrApprovalNotFound
		}
		now := time.Now().UTC()
		fresh.Status = status
		fresh.FailureReason = failureReason
		fresh.Version++
		fresh.FinishedAt = &now
		if status == model.ApprovalStatusSucceeded {
			fresh.ExecutedAt = &now
		}
		*req = *fresh
		return s.repo.WithTx(tx).Update(fresh)
	})
	return *req, err
}

// Reject 驳回审批请求。
func (s *ApprovalService) Reject(ref string, principal auth.Principal, clientIP, reason string) (model.ApprovalRequest, error) {
	principal = authNormalize(principal)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return model.ApprovalRequest{}, apperr.ErrApprovalReasonRequired
	}
	var out model.ApprovalRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		req, err := findByRef(s.repo.WithTx(tx), ref)
		if err != nil {
			return err
		}
		if err := ensureDecidable(req, principal); err != nil {
			return err
		}
		now := time.Now().UTC()
		req.Status = model.ApprovalStatusRejected
		req.DeciderType = principal.StableKind()
		req.DeciderID = principal.StableID()
		decider := principal.AuditRef()
		req.ApprovedBy = &decider
		req.RejectReason = reason
		req.DecisionReason = reason
		req.DecidedAt = &now
		req.FinishedAt = &now
		req.Version++
		if err := s.repo.WithTx(tx).Update(req); err != nil {
			return err
		}
		out = *req
		return s.audit.WithTx(tx).Create(approvalAudit(req, principal.AuditRef(), model.ActionApprovalReject, model.ResultOK, clientIP, reason))
	})
	return out, err
}

// Withdraw 撤回自己的待处理审批请求。
func (s *ApprovalService) Withdraw(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	principal = authNormalize(principal)
	if !principal.HasCapability(auth.CapabilityApprovalWithdrawOwn) {
		return model.ApprovalRequest{}, apperr.ErrForbidden
	}
	var out model.ApprovalRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		req, err := findByRef(s.repo.WithTx(tx), ref)
		if err != nil {
			return err
		}
		if !sameRequester(req, principal) {
			return apperr.ErrApprovalNotOwner
		}
		if model.IsTerminalApprovalStatus(req.Status) {
			return apperr.ErrApprovalTerminal
		}
		if req.Status != model.ApprovalStatusPending {
			return apperr.ErrIllegalState
		}
		now := time.Now().UTC()
		req.Status = model.ApprovalStatusWithdrawn
		req.FinishedAt = &now
		req.Version++
		if err := s.repo.WithTx(tx).Update(req); err != nil {
			return err
		}
		out = *req
		return s.audit.WithTx(tx).Create(approvalAudit(req, principal.AuditRef(), model.ActionApprovalRequest, model.ResultOK, clientIP, "withdraw"))
	})
	return out, err
}

func (s *ApprovalService) findByRef(ref string) (*model.ApprovalRequest, error) {
	return findByRef(s.repo, ref)
}

func findByRef(repo *repository.ApprovalRequestRepository, ref string) (*model.ApprovalRequest, error) {
	if strings.HasPrefix(ref, "apr_") {
		req, err := repo.FindByPublicID(ref)
		return ensureFound(req, err)
	}
	id, err := strconv.ParseUint(ref, 10, 64)
	if err != nil || id == 0 {
		return nil, apperr.ErrApprovalNotFound
	}
	req, err := repo.FindByID(uint(id))
	return ensureFound(req, err)
}

func ensureFound(req *model.ApprovalRequest, err error) (*model.ApprovalRequest, error) {
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, apperr.ErrApprovalNotFound
	}
	return req, nil
}

func ensureDecidable(req *model.ApprovalRequest, principal auth.Principal) error {
	if !principal.IsHuman() {
		return apperr.ErrMachinePrincipalCannotDecide
	}
	if !principal.HasCapability(auth.CapabilityApprovalDecide) {
		return apperr.ErrForbidden
	}
	if req.Status != model.ApprovalStatusPending {
		return apperr.ErrIllegalState
	}
	return nil
}

func sameRequester(req *model.ApprovalRequest, principal auth.Principal) bool {
	return req.RequesterType == principal.StableKind() && req.RequesterID == principal.StableID()
}

func toAuthzRequest(req *model.ApprovalRequest) authz.ApprovalRequest {
	return authz.ApprovalRequest{
		ID: req.ID, RequestID: req.RequestID,
		Operation: authz.Operation{Kind: req.OperationKind, Resource: req.ResourceType, ResourceID: req.ResourceID, IdempotencyKey: req.IdempotencyKey, RiskLevel: req.RiskLevel},
		Payload:   []byte(req.Payload), Status: req.Status,
	}
}

func approvalAudit(req *model.ApprovalRequest, operator, action, result, clientIP, reason string) *model.AuditLog {
	detail := map[string]any{
		"requestId": req.RequestID, "id": req.ID, "operationKey": req.OperationKey,
		"resourceType": req.ResourceType, "resourceId": req.ResourceID,
	}
	if reason != "" {
		detail["reason"] = reason
	}
	raw, _ := json.Marshal(detail)
	return &model.AuditLog{
		Operator: operator, Action: action, TargetType: model.TargetTypeApprovalRequest,
		TargetRef: req.RequestID, Detail: string(raw), Result: result, ClientIP: clientIP,
	}
}

func freezePayload(payload map[string]any) ([]byte, string, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func validIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func normalizeOperationKey(kind string) string {
	return kind
}

func normalizeRiskLevel(risk string) string {
	if risk == "" {
		return "high"
	}
	return risk
}

func safeSummary(op authz.Operation) string {
	if op.Resource == "" && op.ResourceID == "" {
		return op.Kind
	}
	return fmt.Sprintf("%s %s/%s", op.Kind, op.Resource, op.ResourceID)
}

func newApprovalRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "apr_" + hex.EncodeToString(b[:])
}

func authNormalize(principal auth.Principal) auth.Principal {
	return auth.NormalizePrincipal(principal)
}
