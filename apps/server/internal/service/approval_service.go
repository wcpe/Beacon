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
	NamespaceID   *uint
	GlobalOnly    bool
	Keyword       string
	CreatedFrom   *time.Time
	CreatedTo     *time.Time
	ExpiresFrom   *time.Time
	ExpiresTo     *time.Time
	Page          int
	PageSize      int
}

// ApprovalRequestPreparer 在冻结前补充服务端权威审批目标。
type ApprovalRequestPreparer interface {
	PrepareApprovalRequest(authz.Operation, map[string]any, auth.Principal, string) (authz.Operation, map[string]any, error)
}

// ApprovalRequestTransactionHook 在审批请求已落库后、同一事务内写入领域待应用状态。
// hook 捕获的临时值绝不写入通用审批载荷，适用于一次性凭据等不能重放的领域。
type ApprovalRequestTransactionHook func(tx *gorm.DB, req authz.ApprovalRequest) error

// ApprovalSensitiveAccessGrantStore 是审批详情查询一次性敏感内容授权的最小端口。
type ApprovalSensitiveAccessGrantStore interface {
	FindByApprovalRequestID(requestID string) (*model.SensitiveAccessGrant, error)
}

// SensitiveAccessGrantReference 是审批详情向原申请主体投影的一次性消费引用。
type SensitiveAccessGrantReference struct {
	GrantID string
}

// ApprovalService 编排统一审批请求。
type ApprovalService struct {
	db         *gorm.DB
	repo       *repository.ApprovalRequestRepository
	audit      *repository.AuditLogRepository
	receipts   ApprovalReceiptStore
	grants     ApprovalSensitiveAccessGrantStore
	registry   *authz.ApprovalRegistry
	preparer   ApprovalRequestPreparer
	workerWake chan struct{}
}

// NewApprovalService 构造审批服务。
func NewApprovalService(db *gorm.DB, repo *repository.ApprovalRequestRepository, audit *repository.AuditLogRepository, registry *authz.ApprovalRegistry) *ApprovalService {
	return &ApprovalService{
		db: db, repo: repo, audit: audit,
		receipts:   repository.NewApprovalExecutionReceiptRepository(db),
		registry:   registry,
		workerWake: make(chan struct{}, 1),
	}
}

// WorkerWake 返回审批 worker 的唤醒通道。
func (s *ApprovalService) WorkerWake() <-chan struct{} { return s.workerWake }

func (s *ApprovalService) wakeWorker() {
	select {
	case s.workerWake <- struct{}{}:
	default:
	}
}

// SetApprovalRequestPreparer 注入可选的审批请求冻结器。
func (s *ApprovalService) SetApprovalRequestPreparer(preparer ApprovalRequestPreparer) {
	s.preparer = preparer
}

// SetSensitiveAccessGrantStore 注入敏感内容授权查询端口，仅用于成功审批详情的安全投影。
func (s *ApprovalService) SetSensitiveAccessGrantStore(store ApprovalSensitiveAccessGrantStore) {
	s.grants = store
}

// Request 创建或复用幂等审批请求。
func (s *ApprovalService) Request(op authz.Operation, payload map[string]any, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	return s.request(op, payload, principal, clientIP, nil)
}

func (s *ApprovalService) request(op authz.Operation, payload map[string]any, principal auth.Principal, clientIP string, hook ApprovalRequestTransactionHook) (model.ApprovalRequest, error) {
	if op.Kind == "" {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	descriptor, err := s.validateOperation(op.Kind)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	reason := strings.TrimSpace(op.Reason)
	if reason == "" {
		return model.ApprovalRequest{}, apperr.ErrApprovalReasonRequired
	}
	if !validIdempotencyKey(op.IdempotencyKey) {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	if err := authz.AuthorizeDescriptor(principal, op, descriptor); err != nil {
		return model.ApprovalRequest{}, err
	}
	if s.preparer != nil {
		op, payload, err = s.preparer.PrepareApprovalRequest(op, payload, principal, clientIP)
		if err != nil {
			return model.ApprovalRequest{}, err
		}
		descriptor, err = s.validateOperation(op.Kind)
		if err != nil {
			return model.ApprovalRequest{}, err
		}
		if err := authz.AuthorizeDescriptor(principal, op, descriptor); err != nil {
			return model.ApprovalRequest{}, err
		}
	}
	body, hash, err := freezePayload(payload)
	if err != nil {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	evidenceSnapshot, err := freezeEvidenceSnapshot(op.EvidenceSnapshot)
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
		riskLevel := descriptor.RiskLevel
		if riskLevel == "" {
			riskLevel = normalizeRiskLevel(op.RiskLevel)
		}
		req := model.ApprovalRequest{
			RequestID: newApprovalRequestID(), OperationKey: operationKey, OperationKind: op.Kind,
			SchemaVersion: descriptor.SchemaVersion, RequiredCapability: descriptor.Capability,
			RiskLevel: riskLevel, NamespaceID: op.NamespaceID, ResourceType: op.Resource, ResourceID: op.ResourceID,
			IdempotencyKey: op.IdempotencyKey, RequestReason: reason, SafeSummary: safeSummary(op),
			PreconditionSummary: preconditionSummary(op), ImpactSummary: approvalImpactSummary(op), EvidenceSnapshot: evidenceSnapshot,
			Payload: string(body), FrozenPayloadSHA256: hash, Status: model.ApprovalStatusPending,
			RequesterType: principal.StableKind(), RequesterID: principal.StableID(), RequestedBy: principal.AuditRef(),
			ExpiresAt: &expiresAt, Version: 1,
		}
		if e := repo.Create(&req); e != nil {
			return e
		}
		if e := s.registry.PrepareApprovalRequestInTx(tx, toAuthzRequest(&req)); e != nil {
			return e
		}
		if hook != nil {
			if e := hook(tx, toAuthzRequest(&req)); e != nil {
				return e
			}
		}
		out = req
		return s.audit.WithTx(tx).Create(approvalAudit(&req, principal.AuditRef(), model.ActionApprovalRequest, model.ResultOK, clientIP, reason))
	})
	return out, err
}

// List 查询审批请求列表，保留旧接口并返回默认第一页。
func (s *ApprovalService) List(filter ApprovalListFilter, principal auth.Principal) ([]model.ApprovalRequest, error) {
	filter.Page, filter.PageSize = 1, 1000
	items, _, err := s.ListPage(filter, principal)
	return items, err
}

// ListPage 查询审批请求当前页与 total，机器主体只能看自己的申请。
func (s *ApprovalService) ListPage(filter ApprovalListFilter, principal auth.Principal) ([]model.ApprovalRequest, int64, error) {
	principal = authNormalize(principal)
	if !principal.HasCapability(auth.CapabilityApprovalRead) {
		return nil, 0, apperr.ErrForbidden
	}
	if !principal.IsHuman() {
		filter.RequesterType = principal.StableKind()
		filter.RequesterID = principal.StableID()
	}
	return s.repo.ListPage(repository.ApprovalRequestListFilter{
		Status: filter.Status, OperationKey: filter.OperationKey, RiskLevel: filter.RiskLevel,
		RequesterType: filter.RequesterType, RequesterID: filter.RequesterID,
		NamespaceID: filter.NamespaceID, GlobalOnly: filter.GlobalOnly, Keyword: filter.Keyword,
		CreatedFrom: filter.CreatedFrom, CreatedTo: filter.CreatedTo,
		ExpiresFrom: filter.ExpiresFrom, ExpiresTo: filter.ExpiresTo,
		Page: filter.Page, PageSize: filter.PageSize,
	})
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

// DetailSensitiveAccessGrant 返回原申请主体可消费的成功授权引用，不返回正文或冻结载荷。
func (s *ApprovalService) DetailSensitiveAccessGrant(ref string, principal auth.Principal) (*SensitiveAccessGrantReference, error) {
	req, err := s.Detail(ref, principal)
	if err != nil || s.grants == nil || req.Status != model.ApprovalStatusSucceeded || !sameRequester(&req, authNormalize(principal)) {
		return nil, err
	}
	grant, err := s.grants.FindByApprovalRequestID(req.RequestID)
	if err != nil || grant == nil || grant.Status != model.SensitiveAccessGrantStatusActive || grant.Operation != req.OperationKind {
		return nil, err
	}
	return &SensitiveAccessGrantReference{GrantID: grant.GrantID}, nil
}

// DetailEvidence 返回详情及领域适配器生成的脱敏实时证据。
func (s *ApprovalService) DetailEvidence(ref string, principal auth.Principal) (model.ApprovalRequest, authz.ApprovalEvidence, error) {
	req, err := s.Detail(ref, principal)
	if err != nil {
		return model.ApprovalRequest{}, authz.ApprovalEvidence{}, err
	}
	if s.registry == nil {
		return req, authz.ApprovalEvidence{EvidenceStatus: "unavailable", DriftStatus: "none"}, nil
	}
	return req, s.registry.ReadEvidence(toAuthzRequest(&req)), nil
}

// Approve 仅 CAS 标记 executing、写审计并唤醒异步执行器。
func (s *ApprovalService) Approve(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	approved, err := s.markExecuting(ref, principal, clientIP)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	s.wakeWorker()
	return approved, nil
}

func (s *ApprovalService) markExecuting(ref string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	principal = authNormalize(principal)
	var out model.ApprovalRequest
	expired := false
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
			if err := s.expirePending(tx, req, principal.AuditRef()); err != nil {
				return err
			}
			expired = true
			return nil
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
	if err == nil && expired {
		return model.ApprovalRequest{}, apperr.ErrApprovalExpired
	}
	return out, err
}

func (s *ApprovalService) expirePending(tx *gorm.DB, req *model.ApprovalRequest, actor string) error {
	oldVersion := req.Version
	now := time.Now().UTC()
	res := tx.Model(&model.ApprovalRequest{}).
		Where("id = ? AND status = ? AND version = ?", req.ID, model.ApprovalStatusPending, oldVersion).
		Updates(map[string]any{"status": model.ApprovalStatusExpired, "finished_at": now, "version": oldVersion + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return apperr.ErrIllegalState
	}
	req.Status, req.FinishedAt, req.Version = model.ApprovalStatusExpired, &now, oldVersion+1
	return s.completeTerminalInTx(tx, req, model.ApprovalStatusExpired, actor)
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
		decider := principal.AuditRef()
		req.DecisionReason = reason
		if err := updatePendingApproval(tx, req, map[string]any{
			"status": model.ApprovalStatusRejected, "decider_type": principal.StableKind(), "decider_id": principal.StableID(),
			"approved_by": decider, "reject_reason": reason, "decision_reason": reason,
			"decided_at": now, "finished_at": now,
		}); err != nil {
			return err
		}
		req.Status = model.ApprovalStatusRejected
		req.DeciderType = principal.StableKind()
		req.DeciderID = principal.StableID()
		req.ApprovedBy = &decider
		req.RejectReason = reason
		req.DecisionReason = reason
		req.DecidedAt = &now
		req.FinishedAt = &now
		req.Version++
		if err := s.completeTerminalInTx(tx, req, model.ApprovalStatusRejected, principal.AuditRef()); err != nil {
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
		if err := updatePendingApproval(tx, req, map[string]any{
			"status": model.ApprovalStatusWithdrawn, "finished_at": now,
		}); err != nil {
			return err
		}
		req.Status = model.ApprovalStatusWithdrawn
		req.FinishedAt = &now
		req.Version++
		if err := s.completeTerminalInTx(tx, req, model.ApprovalStatusWithdrawn, principal.AuditRef()); err != nil {
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

// updatePendingApproval 仅在读取时的 pending 版本仍有效时写入终态，防止并发决定覆盖。
func updatePendingApproval(tx *gorm.DB, req *model.ApprovalRequest, updates map[string]any) error {
	updates["version"] = req.Version + 1
	result := tx.Model(&model.ApprovalRequest{}).
		Where("id = ? AND status = ? AND version = ?", req.ID, model.ApprovalStatusPending, req.Version).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperr.ErrIllegalState
	}
	return nil
}

func (s *ApprovalService) validateOperation(kind string) (authz.OperationDescriptor, error) {
	if s.registry == nil || kind == "" {
		return authz.OperationDescriptor{}, apperr.ErrForbidden
	}
	return s.registry.ValidateOperation(kind)
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
	// 默认仅人类可决（分权）；显式开启 mcp.allow-approval-decide 的内网部署
	// 允许持有 approval.decide 的机器主体闭环审批。
	if !principal.IsHuman() && !auth.MCPApprovalDecideEnabled() {
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
		ID: req.ID, RequestID: req.RequestID, OperationKey: req.OperationKey,
		Operation:     authz.Operation{Kind: req.OperationKind, Resource: req.ResourceType, ResourceID: req.ResourceID, IdempotencyKey: req.IdempotencyKey, RiskLevel: req.RiskLevel, Reason: req.RequestReason},
		SchemaVersion: req.SchemaVersion, RequiredCapability: req.RequiredCapability,
		Payload: []byte(req.Payload), PayloadHash: req.FrozenPayloadSHA256, Status: req.Status,
		LeaseOwner: req.LeaseOwner, LeaseUntil: req.LeaseUntil, DeciderType: req.DeciderType,
		DeciderID: req.DeciderID, ApprovedAt: req.ApprovedAt, DecisionReason: req.DecisionReason,
		RequesterType: req.RequesterType, RequesterID: req.RequesterID, Version: req.Version,
	}
}

// completeTerminalInTx 让声明终态回调的领域与审批状态在同一事务内收敛。
func (s *ApprovalService) completeTerminalInTx(tx *gorm.DB, req *model.ApprovalRequest, status, actor string) error {
	if s.registry == nil {
		return apperr.ErrForbidden
	}
	return s.registry.ExecuteTerminalInTx(tx, req.RequestID, req.Version, status, actor)
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
	if err := validateFrozenValue(payload); err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func freezeEvidenceSnapshot(lines []authz.ApprovalEvidenceLine) (string, error) {
	if len(lines) == 0 {
		return "", nil
	}
	for _, line := range lines {
		if strings.TrimSpace(line.Label) == "" || strings.TrimSpace(line.Value) == "" || len(line.Label) > 128 || len(line.Value) > 512 {
			return "", apperr.ErrInvalidParam
		}
	}
	body, err := json.Marshal(lines)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func validateFrozenValue(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if forbiddenFrozenKey(key) {
				return apperr.ErrInvalidParam
			}
			if err := validateFrozenValue(nested); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range typed {
			if err := validateFrozenValue(nested); err != nil {
				return err
			}
		}
	}
	return nil
}

func forbiddenFrozenKey(key string) bool {
	key = strings.ToLower(key)
	for _, term := range []string{"token", "secret", "password", "body", "content"} {
		if strings.Contains(key, term) {
			return true
		}
	}
	return false
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

func preconditionSummary(op authz.Operation) string {
	if strings.TrimSpace(op.PreconditionSummary) != "" {
		return strings.TrimSpace(op.PreconditionSummary)
	}
	return "执行前校验冻结目标状态"
}

func approvalImpactSummary(op authz.Operation) string {
	if strings.TrimSpace(op.ImpactSummary) != "" {
		return strings.TrimSpace(op.ImpactSummary)
	}
	return safeSummary(op)
}

func newApprovalRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "apr_" + hex.EncodeToString(b[:])
}

func authNormalize(principal auth.Principal) auth.Principal {
	return auth.NormalizePrincipal(principal)
}
