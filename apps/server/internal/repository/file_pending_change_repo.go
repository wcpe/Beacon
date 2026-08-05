package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// FilePendingChangeRepository 管理审批前冻结的加密文件与覆盖集变更。
type FilePendingChangeRepository struct{ db *gorm.DB }

// NewFilePendingChangeRepository 构造待审批文件变更仓库。
func NewFilePendingChangeRepository(db *gorm.DB) *FilePendingChangeRepository {
	return &FilePendingChangeRepository{db: db}
}

// WithTx 返回绑定事务的仓库副本。
func (r *FilePendingChangeRepository) WithTx(tx *gorm.DB) *FilePendingChangeRepository {
	return &FilePendingChangeRepository{db: tx}
}

// Create 写入一条尚未应用的冻结变更。
func (r *FilePendingChangeRepository) Create(change *model.FilePendingChange) error {
	return r.db.Create(change).Error
}

// FindByApprovalRequest 查询一条审批绑定的冻结变更。
func (r *FilePendingChangeRepository) FindByApprovalRequest(requestID string) (*model.FilePendingChange, error) {
	var change model.FilePendingChange
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
func (r *FilePendingChangeRepository) ApplyCAS(changeID string) (bool, error) {
	result := r.db.Model(&model.FilePendingChange{}).Where("change_id = ? AND status = ?", changeID, model.FilePendingChangePending).
		Update("status", model.FilePendingChangeApplied)
	return result.RowsAffected == 1, result.Error
}

// InvalidateByApprovalRequest 使未应用变更失效；重复终态回调保持幂等。
func (r *FilePendingChangeRepository) InvalidateByApprovalRequest(requestID string) error {
	return r.db.Model(&model.FilePendingChange{}).Where("approval_request_id = ? AND status = ?", requestID, model.FilePendingChangePending).
		Update("status", model.FilePendingChangeInvalidated).Error
}
