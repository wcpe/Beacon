package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// APIKeyRepository 提供 api_key 表的数据访问（FR-42，见 ADR-0026）。
// 真源在库：认证按 key_hash 查未软删行；吊销 = 软删（哨兵）；重置 = 轮换 hash。
type APIKeyRepository struct {
	db *gorm.DB
}

// NewAPIKeyRepository 构造仓库。
func NewAPIKeyRepository(db *gorm.DB) *APIKeyRepository {
	return &APIKeyRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *APIKeyRepository) WithTx(tx *gorm.DB) *APIKeyRepository {
	return &APIKeyRepository{db: tx}
}

// Create 追加一把新密钥。
func (r *APIKeyRepository) Create(key *model.APIKey) error {
	return r.db.Create(key).Error
}

// FindActiveByHash 按摘要查未软删（未吊销）密钥；不存在返回 (nil, nil)。
// 过期与否由 service 校验（repo 只剔除已吊销），以便区分语义。
func (r *APIKeyRepository) FindActiveByHash(hash string) (*model.APIKey, error) {
	var k model.APIKey
	err := r.db.Where("key_hash = ? AND deleted_at = ?", hash, model.SoftDeleteSentinel).First(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// FindActiveByID 按主键查未软删密钥；不存在 / 已吊销返回 (nil, nil)。
func (r *APIKeyRepository) FindActiveByID(id uint) (*model.APIKey, error) {
	var k model.APIKey
	err := r.db.Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).First(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// List 列出全部密钥（含已吊销，供列表展示状态），按创建时间倒序。
func (r *APIKeyRepository) List() ([]model.APIKey, error) {
	var list []model.APIKey
	if err := r.db.Order("created_at desc, id desc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// Revoke 吊销某密钥（软删）；返回是否命中（已吊销 / 不存在则 false）。
func (r *APIKeyRepository) Revoke(id uint, deletedAt time.Time) (bool, error) {
	res := r.db.Model(&model.APIKey{}).
		Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).
		Update("deleted_at", deletedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// RotateSecret 重置某密钥的明文（换新 hash 与展示片段、清空最近使用）；返回是否命中。
// 旧明文随 hash 变更立即失效；已吊销密钥不可重置（返回 false）。
func (r *APIKeyRepository) RotateSecret(id uint, newHash, newPrefix string) (bool, error) {
	res := r.db.Model(&model.APIKey{}).
		Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).
		Updates(map[string]any{"key_hash": newHash, "key_prefix": newPrefix, "last_used_at": nil})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// TouchLastUsed 更新最近使用时刻（认证成功时节流调用，best-effort）。
func (r *APIKeyRepository) TouchLastUsed(id uint, at time.Time) error {
	return r.db.Model(&model.APIKey{}).Where("id = ?", id).Update("last_used_at", at).Error
}
