package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ApprovalExecutionReceiptRepository 提供审批执行回执的数据访问。
type ApprovalExecutionReceiptRepository struct {
	db *gorm.DB
}

// NewApprovalExecutionReceiptRepository 构造执行回执仓库。
func NewApprovalExecutionReceiptRepository(db *gorm.DB) *ApprovalExecutionReceiptRepository {
	return &ApprovalExecutionReceiptRepository{db: db}
}

// FindByRequestID 按审批请求 ID 查找执行回执，不存在返回 nil。
func (r *ApprovalExecutionReceiptRepository) FindByRequestID(requestID string) (*model.ApprovalExecutionReceipt, error) {
	var receipt model.ApprovalExecutionReceipt
	err := r.db.Where("request_id = ?", requestID).First(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}

// Create 创建执行回执。
func (r *ApprovalExecutionReceiptRepository) Create(receipt *model.ApprovalExecutionReceipt) error {
	return r.db.Create(receipt).Error
}
