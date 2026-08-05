package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

const sensitiveAccessGrantTTL = 5 * time.Minute

// SensitiveAccessGrantService 封装审批领域对敏感内容授权的激活与消费边界。
type SensitiveAccessGrantService struct {
	repo *repository.SensitiveAccessGrantRepository
}

// NewSensitiveAccessGrantService 构造敏感内容授权服务。
func NewSensitiveAccessGrantService(repo *repository.SensitiveAccessGrantRepository) *SensitiveAccessGrantService {
	return &SensitiveAccessGrantService{repo: repo}
}

// WithTx 返回绑定审批领域事务的授权服务；适配器必须在该事务内激活授权并写执行回执。
func (s *SensitiveAccessGrantService) WithTx(tx *gorm.DB) *SensitiveAccessGrantService {
	if s == nil || s.repo == nil || tx == nil {
		return nil
	}
	return NewSensitiveAccessGrantService(s.repo.WithTx(tx))
}

// CreatePending 在审批申请创建事务中签发唯一的待激活授权。
func (s *SensitiveAccessGrantService) CreatePending(requestID, requesterType, requesterID, operation, targetRef, contentHash string) (*model.SensitiveAccessGrant, error) {
	return s.createPending(requestID, requesterType, requesterID, operation, targetRef, contentHash, "", "")
}

// CreatePendingPair 创建只能双侧原子消费的待激活授权。
func (s *SensitiveAccessGrantService) CreatePendingPair(requestID, requesterType, requesterID, operation, targetRef, contentHash, pairID, pairSide string) (*model.SensitiveAccessGrant, error) {
	if pairID == "" || (pairSide != "left" && pairSide != "right") {
		return nil, apperr.ErrInvalidParam
	}
	return s.createPending(requestID, requesterType, requesterID, operation, targetRef, contentHash, pairID, pairSide)
}

func (s *SensitiveAccessGrantService) createPending(requestID, requesterType, requesterID, operation, targetRef, contentHash, pairID, pairSide string) (*model.SensitiveAccessGrant, error) {
	if s == nil || s.repo == nil || requestID == "" || requesterType == "" || requesterID == "" || operation == "" || targetRef == "" || contentHash == "" {
		return nil, apperr.ErrInvalidParam
	}
	grant := &model.SensitiveAccessGrant{
		GrantID: newSensitiveAccessGrantID(), ApprovalRequestID: requestID,
		RequesterType: requesterType, RequesterID: requesterID,
		Operation: operation, PairID: pairID, PairSide: pairSide, TargetRef: targetRef, ContentVersionHash: contentHash,
		MaxUses: 1, ExpiresAt: time.Now().UTC(), Status: model.SensitiveAccessGrantStatusPending,
	}
	if err := s.repo.Create(grant); err != nil {
		return nil, err
	}
	return grant, nil
}

// Activate 按冻结审批和原申请主体激活一份待消费授权。
func (s *SensitiveAccessGrantService) Activate(requestID string, principal auth.Principal, operation, targetRef, contentHash string, now time.Time) error {
	if s == nil || s.repo == nil || requestID == "" || operation == "" || targetRef == "" || contentHash == "" {
		return apperr.ErrForbidden
	}
	grant, err := s.repo.FindByApprovalRequestID(requestID)
	if err != nil || grant == nil {
		return apperr.ErrForbidden
	}
	principal = auth.NormalizePrincipal(principal)
	if grant.RequesterType != principal.StableKind() || grant.RequesterID != principal.StableID() {
		return apperr.ErrSensitiveAccessWrongPrincipal
	}
	_, err = s.repo.Activate(requestID, operation, targetRef, contentHash, now.UTC().Add(sensitiveAccessGrantTTL))
	return err
}

// ActivatePendingCommandFromAgent 在 Agent 成功回传后激活绑定该命令的待授权。
func (s *SensitiveAccessGrantService) ActivatePendingCommandFromAgent(commandID uint, operation, contentHash string, now time.Time) error {
	if s == nil || s.repo == nil || commandID == 0 {
		return apperr.ErrForbidden
	}
	targetRef := fmt.Sprintf("agent-command/%d", commandID)
	grant, err := s.repo.FindPendingByTargetRef(targetRef)
	if err != nil || grant == nil {
		return apperr.ErrForbidden
	}
	if grant.Operation != operation || grant.ContentVersionHash != contentHash {
		return apperr.ErrForbidden
	}
	principal := auth.Principal{Kind: grant.RequesterType, ID: grant.RequesterID}
	return s.Activate(grant.ApprovalRequestID, principal, operation, targetRef, contentHash, now)
}

// ConsumePair 原子消费同一双侧读取组的授权。
func (s *SensitiveAccessGrantService) ConsumePair(grantID string, principal auth.Principal, now time.Time) (*model.SensitiveAccessGrant, *model.SensitiveAccessGrant, error) {
	if s == nil || s.repo == nil || grantID == "" {
		return nil, nil, apperr.ErrForbidden
	}
	return s.repo.ConsumePair(grantID, principal, now.UTC())
}

// RevokePair 使发生版本漂移的整组授权失效。
func (s *SensitiveAccessGrantService) RevokePair(pairID string) error {
	if s == nil || s.repo == nil {
		return apperr.ErrForbidden
	}
	return s.repo.RevokePair(pairID)
}

// BindAndActivatePendingCommandFromAgent 在内容只能由 Agent 回传后确定时，绑定回传哈希并激活授权。
func (s *SensitiveAccessGrantService) BindAndActivatePendingCommandFromAgent(commandID uint, operation, contentHash string, now time.Time) error {
	if s == nil || s.repo == nil || commandID == 0 || operation == "" || contentHash == "" {
		return apperr.ErrForbidden
	}
	_, err := s.repo.BindAndActivatePendingCommand(fmt.Sprintf("agent-command/%d", commandID), operation, contentHash, now.UTC().Add(sensitiveAccessGrantTTL))
	return err
}

// RevokePendingCommand 使 Agent 读取失败对应的待消费授权永久失效。
func (s *SensitiveAccessGrantService) RevokePendingCommand(commandID uint) error {
	if s == nil || s.repo == nil || commandID == 0 {
		return apperr.ErrForbidden
	}
	_, err := s.repo.RevokePendingByTargetRef(fmt.Sprintf("agent-command/%d", commandID))
	return err
}

// Consume 验证冻结目标和原申请主体后原子消费一次授权。
func (s *SensitiveAccessGrantService) Consume(grantID string, principal auth.Principal, operation, targetRef, contentHash string, now time.Time) error {
	if s == nil || s.repo == nil || operation == "" || targetRef == "" || contentHash == "" {
		return apperr.ErrForbidden
	}
	grant, err := s.repo.FindByID(grantID)
	if err != nil || grant == nil || grant.Operation != operation || grant.TargetRef != targetRef || grant.ContentVersionHash != contentHash {
		return apperr.ErrForbidden
	}
	return s.repo.Consume(grantID, principal, now.UTC())
}

func newSensitiveAccessGrantID() string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return "sag_" + hex.EncodeToString(raw[:])
}
