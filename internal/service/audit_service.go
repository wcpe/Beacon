package service

import (
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// 审计分页默认与上限。
const (
	defaultAuditPageSize = 20
	maxAuditPageSize     = 200
)

// AuditService 提供审计查询。
type AuditService struct {
	repo *repository.AuditLogRepository
}

// NewAuditService 构造服务。
func NewAuditService(repo *repository.AuditLogRepository) *AuditService {
	return &AuditService{repo: repo}
}

// List 分页查询审计；规整 page/size 后委托仓库。
func (s *AuditService) List(f repository.AuditFilter) ([]model.AuditLog, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 {
		f.Size = defaultAuditPageSize
	}
	if f.Size > maxAuditPageSize {
		f.Size = maxAuditPageSize
	}
	return s.repo.List(f)
}
