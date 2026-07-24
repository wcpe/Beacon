package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// EnvRepository 提供 env 展示维度与 env→namespace 映射表的数据访问（FR-178）。
// 纯 GORM CRUD，不含业务规则；映射的整体替换语义、冲突判定与审计由 service 编排。
type EnvRepository struct {
	db *gorm.DB
}

// NewEnvRepository 构造仓库。
func NewEnvRepository(db *gorm.DB) *EnvRepository {
	return &EnvRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *EnvRepository) WithTx(tx *gorm.DB) *EnvRepository {
	return &EnvRepository{db: tx}
}

// List 返回全部 env（按 id 升序，展示稳定）。
func (r *EnvRepository) List() ([]model.Env, error) {
	var items []model.Env
	if err := r.db.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// FindByID 按 id 查 env；不存在返回 (nil, nil)。
func (r *EnvRepository) FindByID(id uint) (*model.Env, error) {
	var env model.Env
	err := r.db.First(&env, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &env, nil
}

// FindByName 按名查 env（撞名判定用）；不存在返回 (nil, nil)。
func (r *EnvRepository) FindByName(name string) (*model.Env, error) {
	var env model.Env
	err := r.db.Where("name = ?", name).First(&env).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &env, nil
}

// Create 插入一个 env。
func (r *EnvRepository) Create(env *model.Env) error {
	return r.db.Create(env).Error
}

// Save 保存 env（改名 / 改描述）。
func (r *EnvRepository) Save(env *model.Env) error {
	return r.db.Save(env).Error
}

// DeleteByID 按 id 硬删 env（映射行的级联删除由 service 在事务内先行完成）。
func (r *EnvRepository) DeleteByID(id uint) error {
	return r.db.Delete(&model.Env{}, id).Error
}

// ListMappings 返回全部 env→namespace 映射行（列表富化用，批量取避免 N+1）。
func (r *EnvRepository) ListMappings() ([]model.EnvNamespace, error) {
	var items []model.EnvNamespace
	if err := r.db.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListMappingsByEnv 返回某 env 的映射行。
func (r *EnvRepository) ListMappingsByEnv(envID uint) ([]model.EnvNamespace, error) {
	var items []model.EnvNamespace
	if err := r.db.Where("env_id = ?", envID).Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// FindMappingsByNamespaceIDs 查给定 namespace 的现有映射行（冲突判定用）。
func (r *EnvRepository) FindMappingsByNamespaceIDs(namespaceIDs []uint) ([]model.EnvNamespace, error) {
	if len(namespaceIDs) == 0 {
		return nil, nil
	}
	var items []model.EnvNamespace
	if err := r.db.Where("namespace_id IN ?", namespaceIDs).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// DeleteMappingsByEnv 删除某 env 的全部映射行（整体替换的先删步骤）。
func (r *EnvRepository) DeleteMappingsByEnv(envID uint) error {
	return r.db.Where("env_id = ?", envID).Delete(&model.EnvNamespace{}).Error
}

// CreateMappings 批量插入映射行（整体替换的后插步骤）；空切片无操作。
func (r *EnvRepository) CreateMappings(rows []model.EnvNamespace) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.Create(&rows).Error
}
