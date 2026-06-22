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

// instanceCounter 是环境删除守卫所需的注册表实例计数能力（仅依赖最小接口，
// 由 *runtime.Registry 实现；解耦具体类型便于单测注入替身）。
type instanceCounter interface {
	// CountByNamespace 返回某环境下当前在线实例条目数。
	CountByNamespace(ns string) int
}

// NamespaceService 编排环境（namespace）相关的业务逻辑。
type NamespaceService struct {
	db         *gorm.DB
	repo       *repository.NamespaceRepository
	assignRepo *repository.ZoneAssignmentRepository
	configRepo *repository.ConfigItemRepository
	instances  instanceCounter
	auditRepo  *repository.AuditLogRepository
}

// NewNamespaceService 构造服务。assignRepo/configRepo/instances 供删除守卫查在用数据。
func NewNamespaceService(
	db *gorm.DB,
	repo *repository.NamespaceRepository,
	assignRepo *repository.ZoneAssignmentRepository,
	configRepo *repository.ConfigItemRepository,
	instances instanceCounter,
	auditRepo *repository.AuditLogRepository,
) *NamespaceService {
	return &NamespaceService{
		db:         db,
		repo:       repo,
		assignRepo: assignRepo,
		configRepo: configRepo,
		instances:  instances,
		auditRepo:  auditRepo,
	}
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

// Update 改环境显示名（code 为不可变身份键，不改）；环境不存在返回 NOT_FOUND。
// 改名与审计在同一事务内原子完成（FR-53）。
func (s *NamespaceService) Update(code, name, operator, clientIP string) (*model.Namespace, error) {
	exist, err := s.repo.FindByCode(code)
	if err != nil {
		return nil, err
	}
	if exist == nil {
		return nil, apperr.ErrNamespaceNotFound
	}
	detail, _ := json.Marshal(map[string]string{"name": name})
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.repo.WithTx(tx).UpdateName(code, name); err != nil {
			return err
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: code,
			Operator:      operator,
			Action:        model.ActionNamespaceUpdate,
			TargetType:    model.TargetTypeNamespace,
			TargetRef:     code,
			Detail:        string(detail),
			Result:        model.ResultOK,
			ClientIP:      clientIP,
		})
	}); err != nil {
		return nil, err
	}
	slog.Info("更新环境显示名", "code", code, "name", name, "operator", operator)
	exist.Name = name
	return exist, nil
}

// Delete 删除环境（硬删）；环境不存在返回 NOT_FOUND。
// 删除守卫：该环境下有已注册实例 / 已指派 zone / 已有配置则禁删，分别返回对应业务错误且不删不审计（FR-53）。
// 守卫全过则硬删与审计在同一事务内原子完成。
func (s *NamespaceService) Delete(code, operator, clientIP string) error {
	exist, err := s.repo.FindByCode(code)
	if err != nil {
		return err
	}
	if exist == nil {
		return apperr.ErrNamespaceNotFound
	}
	if err := s.guardDeletable(code); err != nil {
		return err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.repo.WithTx(tx).DeleteByCode(code); err != nil {
			return err
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: code,
			Operator:      operator,
			Action:        model.ActionNamespaceDelete,
			TargetType:    model.TargetTypeNamespace,
			TargetRef:     code,
			Result:        model.ResultOK,
			ClientIP:      clientIP,
		})
	}); err != nil {
		return err
	}
	slog.Info("删除环境", "code", code, "operator", operator)
	return nil
}

// guardDeletable 检查环境是否仍有在用数据：依次查实例 / zone 指派 / 配置，命中即返对应业务错误。
func (s *NamespaceService) guardDeletable(code string) error {
	if s.instances.CountByNamespace(code) > 0 {
		return apperr.ErrNamespaceHasInstances
	}
	assignments, err := s.assignRepo.CountByNamespace(code)
	if err != nil {
		return err
	}
	if assignments > 0 {
		return apperr.ErrNamespaceHasAssignments
	}
	configs, err := s.configRepo.CountByNamespace(code)
	if err != nil {
		return err
	}
	if configs > 0 {
		return apperr.ErrNamespaceHasConfigs
	}
	return nil
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
