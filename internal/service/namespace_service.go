// Package service 是业务编排层：规则校验、事务、跨域协调。
package service

import (
	"encoding/json"
	"log/slog"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// NamespaceService 编排环境（namespace）相关的业务逻辑。
type NamespaceService struct {
	db        *gorm.DB
	repo      *repository.NamespaceRepository
	auditRepo *repository.AuditLogRepository
}

// NewNamespaceService 构造服务。
func NewNamespaceService(db *gorm.DB, repo *repository.NamespaceRepository, auditRepo *repository.AuditLogRepository) *NamespaceService {
	return &NamespaceService{db: db, repo: repo, auditRepo: auditRepo}
}

// List 返回全部环境。
func (s *NamespaceService) List() ([]model.Namespace, error) {
	return s.repo.List()
}

// Create 新建环境；编码为空返回参数错误，同名返回冲突。
// 环境写入与审计在同一事务内原子完成（FR-7/FR-30）。
func (s *NamespaceService) Create(code, name, operator, clientIP string) (*model.Namespace, error) {
	if code == "" {
		return nil, apperr.ErrInvalidParam
	}
	exist, err := s.repo.FindByCode(code)
	if err != nil {
		return nil, err
	}
	if exist != nil {
		return nil, apperr.ErrNamespaceConflict
	}
	ns := &model.Namespace{Code: code, Name: name}
	detail, _ := json.Marshal(map[string]string{"name": name})
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.repo.WithTx(tx).Create(ns); err != nil {
			return err
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns.Code,
			Operator:      operator,
			Action:        model.ActionNamespaceCreate,
			TargetType:    model.TargetTypeNamespace,
			TargetRef:     ns.Code,
			Detail:        string(detail),
			Result:        model.ResultOK,
			ClientIP:      clientIP,
		})
	}); err != nil {
		return nil, err
	}
	slog.Info("新建环境", "code", code, "name", name, "operator", operator)
	return ns, nil
}

// SeedDefaults 在环境表为空时预置 prod / test 两个环境（幂等）。
func (s *NamespaceService) SeedDefaults() error {
	n, err := s.repo.Count()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	defaults := []model.Namespace{
		{Code: "prod", Name: "生产环境"},
		{Code: "test", Name: "测试环境"},
	}
	for i := range defaults {
		if err := s.repo.Create(&defaults[i]); err != nil {
			return err
		}
		slog.Info("预置环境", "code", defaults[i].Code, "name", defaults[i].Name)
	}
	return nil
}
