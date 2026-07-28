package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ApprovalRequestRepository 提供统一审批请求的数据访问。
type ApprovalRequestRepository struct {
	db *gorm.DB
}

// NewApprovalRequestRepository 构造仓库。
func NewApprovalRequestRepository(db *gorm.DB) *ApprovalRequestRepository {
	return &ApprovalRequestRepository{db: db}
}

// WithTx 返回绑定事务的仓库副本。
func (r *ApprovalRequestRepository) WithTx(tx *gorm.DB) *ApprovalRequestRepository {
	return &ApprovalRequestRepository{db: tx}
}

// Create 创建审批请求。
func (r *ApprovalRequestRepository) Create(req *model.ApprovalRequest) error {
	return r.db.Create(req).Error
}

// FindByID 按主键查审批请求，不存在返回 nil。
func (r *ApprovalRequestRepository) FindByID(id uint) (*model.ApprovalRequest, error) {
	var req model.ApprovalRequest
	err := r.db.First(&req, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// FindByPublicID 按公开 request_id 查审批请求，不存在返回 nil。
func (r *ApprovalRequestRepository) FindByPublicID(requestID string) (*model.ApprovalRequest, error) {
	var req model.ApprovalRequest
	err := r.db.Where("request_id = ?", requestID).First(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// FindByPrincipalOperationIdempotency 按主体、操作和幂等键查请求。
func (r *ApprovalRequestRepository) FindByPrincipalOperationIdempotency(requesterType, requesterID, operationKey, key string) (*model.ApprovalRequest, error) {
	if key == "" {
		return nil, nil
	}
	var req model.ApprovalRequest
	err := r.db.Where("requester_type = ? AND requester_id = ? AND operation_key = ? AND idempotency_key = ?", requesterType, requesterID, operationKey, key).
		Order("id desc").First(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// List 查询审批请求列表。
func (r *ApprovalRequestRepository) List(status, operationKey, riskLevel, requesterType, requesterID string) ([]model.ApprovalRequest, error) {
	q := r.db.Order("created_at desc, id desc")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if operationKey != "" {
		q = q.Where("operation_key = ?", operationKey)
	}
	if riskLevel != "" {
		q = q.Where("risk_level = ?", riskLevel)
	}
	if requesterType != "" {
		q = q.Where("requester_type = ?", requesterType)
	}
	if requesterID != "" {
		q = q.Where("requester_id = ?", requesterID)
	}
	var reqs []model.ApprovalRequest
	if err := q.Limit(1000).Find(&reqs).Error; err != nil {
		return nil, err
	}
	return reqs, nil
}

// Update 保存审批请求状态变化。
func (r *ApprovalRequestRepository) Update(req *model.ApprovalRequest) error {
	return r.db.Save(req).Error
}
