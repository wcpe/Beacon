package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// SensitiveAccessGrantRepository 提供敏感内容访问授权的持久化操作。
type SensitiveAccessGrantRepository struct{ db *gorm.DB }

// NewSensitiveAccessGrantRepository 构造授权仓库。
func NewSensitiveAccessGrantRepository(db *gorm.DB) *SensitiveAccessGrantRepository {
	return &SensitiveAccessGrantRepository{db: db}
}

// WithTx 返回绑定事务的授权仓库。
func (r *SensitiveAccessGrantRepository) WithTx(tx *gorm.DB) *SensitiveAccessGrantRepository {
	return &SensitiveAccessGrantRepository{db: tx}
}

// Create 持久化由审批事务签发的授权。
func (r *SensitiveAccessGrantRepository) Create(grant *model.SensitiveAccessGrant) error {
	return r.db.Create(grant).Error
}

// FindByID 按公开授权 ID 查询授权；不存在返回 nil。
func (r *SensitiveAccessGrantRepository) FindByID(grantID string) (*model.SensitiveAccessGrant, error) {
	var grant model.SensitiveAccessGrant
	err := r.db.Where("grant_id = ?", grantID).First(&grant).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

// FindByApprovalRequestID 按审批申请查询其唯一授权；不存在返回 nil。
func (r *SensitiveAccessGrantRepository) FindByApprovalRequestID(requestID string) (*model.SensitiveAccessGrant, error) {
	var grant model.SensitiveAccessGrant
	err := r.db.Where("approval_request_id = ?", requestID).First(&grant).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

// FindPendingByTargetRef 查找绑定指定命令且仍等待回传的授权。
func (r *SensitiveAccessGrantRepository) FindPendingByTargetRef(targetRef string) (*model.SensitiveAccessGrant, error) {
	var grant model.SensitiveAccessGrant
	err := r.db.Where("target_ref = ? AND status = ?", targetRef, model.SensitiveAccessGrantStatusPending).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

// FindByTargetRef 查询绑定既有命令的唯一正文授权；不存在返回 nil。
func (r *SensitiveAccessGrantRepository) FindByTargetRef(targetRef string) (*model.SensitiveAccessGrant, error) {
	var grant model.SensitiveAccessGrant
	err := r.db.Where("target_ref = ?", targetRef).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

// Activate 仅在冻结审批、操作、目标与版本哈希全部一致时把 pending 授权原子激活。
func (r *SensitiveAccessGrantRepository) Activate(requestID, operation, targetRef, contentHash string, expiresAt time.Time) (bool, error) {
	result := r.db.Model(&model.SensitiveAccessGrant{}).
		Where("approval_request_id = ? AND operation = ? AND target_ref = ? AND content_version_hash = ? AND status = ?", requestID, operation, targetRef, contentHash, model.SensitiveAccessGrantStatusPending).
		Updates(map[string]any{"status": model.SensitiveAccessGrantStatusActive, "expires_at": expiresAt})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, apperr.ErrForbidden
	}
	return true, nil
}

// FindPairByGrantID 返回同一双侧读取组的两份授权；组不完整时失败关闭。
func (r *SensitiveAccessGrantRepository) FindPairByGrantID(grantID string) (*model.SensitiveAccessGrant, *model.SensitiveAccessGrant, error) {
	var grants []model.SensitiveAccessGrant
	if err := r.db.Where("pair_id = (SELECT pair_id FROM sensitive_access_grant WHERE grant_id = ?) AND pair_id <> ''", grantID).Order("pair_side ASC").Find(&grants).Error; err != nil {
		return nil, nil, err
	}
	if len(grants) != 2 || grants[0].GrantID == grants[1].GrantID || grants[0].PairSide == grants[1].PairSide {
		return nil, nil, apperr.ErrForbidden
	}
	return &grants[0], &grants[1], nil
}

// BindAndActivatePendingCommand 把 Agent 回传的内容哈希绑定到已批准命令，并原子激活一次性授权。
func (r *SensitiveAccessGrantRepository) BindAndActivatePendingCommand(targetRef, operation, contentHash string, expiresAt time.Time) (bool, error) {
	result := r.db.Model(&model.SensitiveAccessGrant{}).
		Where("target_ref = ? AND operation = ? AND status = ?", targetRef, operation, model.SensitiveAccessGrantStatusPending).
		Updates(map[string]any{"content_version_hash": contentHash, "status": model.SensitiveAccessGrantStatusActive, "expires_at": expiresAt})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, apperr.ErrForbidden
	}
	return true, nil
}

// RevokePendingByTargetRef 将尚未成功回传内容的待授权失效，避免命令失败后遗留可激活记录。
func (r *SensitiveAccessGrantRepository) RevokePendingByTargetRef(targetRef string) (bool, error) {
	result := r.db.Model(&model.SensitiveAccessGrant{}).
		Where("target_ref = ? AND status = ?", targetRef, model.SensitiveAccessGrantStatusPending).
		Update("status", model.SensitiveAccessGrantStatusRevoked)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// Consume 仅允许原申请主体在未过期且未使用时原子消费一次。
func (r *SensitiveAccessGrantRepository) Consume(grantID string, principal auth.Principal, now time.Time) error {
	grant, err := r.FindByID(grantID)
	if err != nil || grant == nil {
		return apperr.ErrSensitiveAccessExpired
	}
	principal = auth.NormalizePrincipal(principal)
	if grant.RequesterType != principal.StableKind() || grant.RequesterID != principal.StableID() {
		return apperr.ErrSensitiveAccessWrongPrincipal
	}
	if grant.Status == model.SensitiveAccessGrantStatusConsumed {
		return apperr.ErrSensitiveAccessConsumed
	}
	if grant.Status != model.SensitiveAccessGrantStatusActive || !now.Before(grant.ExpiresAt) {
		return apperr.ErrSensitiveAccessExpired
	}
	result := r.db.Model(&model.SensitiveAccessGrant{}).Where("id = ? AND status = ? AND used_at IS NULL AND expires_at > ?", grant.ID, model.SensitiveAccessGrantStatusActive, now).
		Updates(map[string]any{"status": model.SensitiveAccessGrantStatusConsumed, "used_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperr.ErrSensitiveAccessConsumed
	}
	return nil
}

// ConsumePair 仅在同一原申请主体的两份授权均可用时原子消费，避免出现单侧已消费而另一侧失败。
func (r *SensitiveAccessGrantRepository) ConsumePair(grantID string, principal auth.Principal, now time.Time) (*model.SensitiveAccessGrant, *model.SensitiveAccessGrant, error) {
	var left, right model.SensitiveAccessGrant
	err := r.db.Transaction(func(tx *gorm.DB) error {
		pairLeft, pairRight, pairErr := r.WithTx(tx).FindPairByGrantID(grantID)
		if pairErr != nil {
			return pairErr
		}
		left, right = *pairLeft, *pairRight
		principal = auth.NormalizePrincipal(principal)
		if left.RequesterType != principal.StableKind() || left.RequesterID != principal.StableID() ||
			right.RequesterType != principal.StableKind() || right.RequesterID != principal.StableID() ||
			left.RequesterType != right.RequesterType || left.RequesterID != right.RequesterID {
			return apperr.ErrSensitiveAccessWrongPrincipal
		}
		if left.Status == model.SensitiveAccessGrantStatusConsumed || right.Status == model.SensitiveAccessGrantStatusConsumed {
			return apperr.ErrSensitiveAccessConsumed
		}
		if left.Status != model.SensitiveAccessGrantStatusActive || right.Status != model.SensitiveAccessGrantStatusActive ||
			!now.Before(left.ExpiresAt) || !now.Before(right.ExpiresAt) {
			return apperr.ErrSensitiveAccessExpired
		}
		for _, grant := range []*model.SensitiveAccessGrant{&left, &right} {
			result := tx.Model(&model.SensitiveAccessGrant{}).Where("id = ? AND status = ? AND used_at IS NULL AND expires_at > ?", grant.ID, model.SensitiveAccessGrantStatusActive, now).
				Updates(map[string]any{"status": model.SensitiveAccessGrantStatusConsumed, "used_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return apperr.ErrSensitiveAccessConsumed
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &left, &right, nil
}

// RevokePair 在任一侧版本漂移时撤销整组未消费授权。
func (r *SensitiveAccessGrantRepository) RevokePair(pairID string) error {
	if pairID == "" {
		return apperr.ErrForbidden
	}
	return r.db.Model(&model.SensitiveAccessGrant{}).Where("pair_id = ? AND status IN ?", pairID, []string{model.SensitiveAccessGrantStatusPending, model.SensitiveAccessGrantStatusActive}).
		Update("status", model.SensitiveAccessGrantStatusRevoked).Error
}
