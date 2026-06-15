// Package service 是业务编排层：规则校验、事务、跨域协调。
package service

import (
	"log/slog"

	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
)

// NamespaceService 编排环境（namespace）相关的业务逻辑。
type NamespaceService struct {
	repo *repository.NamespaceRepository
}

// NewNamespaceService 构造服务。
func NewNamespaceService(repo *repository.NamespaceRepository) *NamespaceService {
	return &NamespaceService{repo: repo}
}

// List 返回全部环境。
func (s *NamespaceService) List() ([]model.Namespace, error) {
	return s.repo.List()
}

// Create 新建环境；编码为空返回参数错误，同名返回冲突。
func (s *NamespaceService) Create(code, name string) (*model.Namespace, error) {
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
	if err := s.repo.Create(ns); err != nil {
		return nil, err
	}
	slog.Info("新建环境", "code", code, "name", name)
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
