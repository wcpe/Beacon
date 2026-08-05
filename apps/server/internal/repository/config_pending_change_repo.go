package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ConfigPendingChangeRepository 管理审批前冻结的加密配置变更。
type ConfigPendingChangeRepository struct{ db *gorm.DB }

// NewConfigPendingChangeRepository 构造待审批配置变更仓库。
func NewConfigPendingChangeRepository(db *gorm.DB) *ConfigPendingChangeRepository {
	return &ConfigPendingChangeRepository{db: db}
}

// WithTx 返回绑定事务的仓库副本。
func (r *ConfigPendingChangeRepository) WithTx(tx *gorm.DB) *ConfigPendingChangeRepository {
	return &ConfigPendingChangeRepository{db: tx}
}

// Create 写入一条尚未应用的冻结变更。
func (r *ConfigPendingChangeRepository) Create(change *model.ConfigPendingChange) error {
	return r.db.Create(change).Error
}

// FindByApprovalRequest 查询一条审批绑定的冻结变更。
func (r *ConfigPendingChangeRepository) FindByApprovalRequest(requestID string) (*model.ConfigPendingChange, error) {
	var change model.ConfigPendingChange
	err := r.db.Where("approval_request_id = ?", requestID).First(&change).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &change, nil
}

// ApplyCAS 仅把仍待执行的冻结变更标记为已应用。
func (r *ConfigPendingChangeRepository) ApplyCAS(changeID string) (bool, error) {
	result := r.db.Model(&model.ConfigPendingChange{}).Where("change_id = ? AND status = ?", changeID, model.ConfigPendingChangePending).
		Update("status", model.ConfigPendingChangeApplied)
	return result.RowsAffected == 1, result.Error
}

// InvalidateByApprovalRequest 使未应用变更失效；重复终态回调保持幂等。
func (r *ConfigPendingChangeRepository) InvalidateByApprovalRequest(requestID string) error {
	return r.db.Model(&model.ConfigPendingChange{}).Where("approval_request_id = ? AND status = ?", requestID, model.ConfigPendingChangePending).
		Update("status", model.ConfigPendingChangeInvalidated).Error
}
