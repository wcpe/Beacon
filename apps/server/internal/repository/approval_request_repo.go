package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ApprovalRequestListFilter 描述审批请求的持久化安全筛选条件。
type ApprovalRequestListFilter struct {
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

// List 查询审批请求列表，保留旧接口并使用默认第一页。
func (r *ApprovalRequestRepository) List(status, operationKey, riskLevel, requesterType, requesterID string) ([]model.ApprovalRequest, error) {
	items, _, err := r.ListPage(ApprovalRequestListFilter{
		Status: status, OperationKey: operationKey, RiskLevel: riskLevel, RequesterType: requesterType, RequesterID: requesterID,
		Page: 1, PageSize: 1000,
	})
	return items, err
}

// ListPage 按页查询审批请求，并返回符合筛选条件的总数。
func (r *ApprovalRequestRepository) ListPage(filter ApprovalRequestListFilter) ([]model.ApprovalRequest, int64, error) {
	page, pageSize := normalizeApprovalPage(filter.Page, filter.PageSize)
	q := r.db.Model(&model.ApprovalRequest{})
	q = applyApprovalFilter(q, filter)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var reqs []model.ApprovalRequest
	if err := q.Order("CASE WHEN status = 'pending' THEN 0 ELSE 1 END, CASE WHEN status = 'pending' THEN expires_at END ASC, CASE WHEN status <> 'pending' THEN created_at END DESC, id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&reqs).Error; err != nil {
		return nil, 0, err
	}
	return reqs, total, nil
}

func applyApprovalFilter(q *gorm.DB, filter ApprovalRequestListFilter) *gorm.DB {
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.OperationKey != "" {
		q = q.Where("operation_key = ?", filter.OperationKey)
	}
	if filter.RiskLevel != "" {
		q = q.Where("risk_level = ?", filter.RiskLevel)
	}
	if filter.RequesterType != "" {
		q = q.Where("requester_type = ?", filter.RequesterType)
	}
	if filter.RequesterID != "" {
		q = q.Where("requester_id = ?", filter.RequesterID)
	}
	if filter.NamespaceID != nil {
		q = q.Where("namespace_id = ?", *filter.NamespaceID)
	} else if filter.GlobalOnly {
		q = q.Where("namespace_id IS NULL")
	}
	if filter.Keyword != "" {
		q = q.Where("safe_summary LIKE ? OR operation_key LIKE ? OR resource_id LIKE ? OR request_reason LIKE ?", "%"+filter.Keyword+"%", "%"+filter.Keyword+"%", "%"+filter.Keyword+"%", "%"+filter.Keyword+"%")
	}
	if filter.CreatedFrom != nil {
		q = q.Where("created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		q = q.Where("created_at <= ?", *filter.CreatedTo)
	}
	if filter.ExpiresFrom != nil {
		q = q.Where("expires_at >= ?", *filter.ExpiresFrom)
	}
	if filter.ExpiresTo != nil {
		q = q.Where("expires_at <= ?", *filter.ExpiresTo)
	}
	return q
}

func normalizeApprovalPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	return page, pageSize
}

// Update 保存审批请求状态变化。
func (r *ApprovalRequestRepository) Update(req *model.ApprovalRequest) error {
	return r.db.Save(req).Error
}
