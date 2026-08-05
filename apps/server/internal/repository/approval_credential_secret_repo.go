package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ApprovalCredentialSecretRepository 提供审批临时凭据密文的数据访问。
type ApprovalCredentialSecretRepository struct {
	db *gorm.DB
}

// NewApprovalCredentialSecretRepository 构造审批临时凭据密文仓库。
func NewApprovalCredentialSecretRepository(db *gorm.DB) *ApprovalCredentialSecretRepository {
	return &ApprovalCredentialSecretRepository{db: db}
}

// WithTx 返回绑定事务的仓库副本。
func (r *ApprovalCredentialSecretRepository) WithTx(tx *gorm.DB) *ApprovalCredentialSecretRepository {
	return &ApprovalCredentialSecretRepository{db: tx}
}

// Create 写入一份待兑换密文。
func (r *ApprovalCredentialSecretRepository) Create(secret *model.ApprovalCredentialSecret) error {
	return r.db.Create(secret).Error
}

// FindByApprovalRequestID 查找审批对应的待兑换密文，不存在返回 nil。
func (r *ApprovalCredentialSecretRepository) FindByApprovalRequestID(requestID string) (*model.ApprovalCredentialSecret, error) {
	var secret model.ApprovalCredentialSecret
	err := r.db.Where("approval_request_id = ?", requestID).First(&secret).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &secret, nil
}

// Consume 原子标记已兑换；仅未消费记录成功并返回 true。
func (r *ApprovalCredentialSecretRepository) Consume(requestID string, at time.Time) (bool, error) {
	result := r.db.Model(&model.ApprovalCredentialSecret{}).
		Where("approval_request_id = ? AND consumed_at IS NULL", requestID).
		Update("consumed_at", at)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}
