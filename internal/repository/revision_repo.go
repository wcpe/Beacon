package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/secret"
)

// ConfigRevisionRepository 提供 config_revision 表的数据访问（append-only）。
// 与 ConfigItemRepository 一致地对敏感版本快照做落库加密 / 读取解密（FR-20）。
type ConfigRevisionRepository struct {
	db     *gorm.DB
	cipher *secret.Cipher
}

// NewConfigRevisionRepository 构造仓库。cipher 负责敏感版本快照的加解密。
func NewConfigRevisionRepository(db *gorm.DB, cipher *secret.Cipher) *ConfigRevisionRepository {
	return &ConfigRevisionRepository{db: db, cipher: cipher}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ConfigRevisionRepository) WithTx(tx *gorm.DB) *ConfigRevisionRepository {
	return &ConfigRevisionRepository{db: tx, cipher: r.cipher}
}

// decryptOne 把单条敏感版本快照的 content 解密回明文（非敏感 / 历史明文不动）。
func (r *ConfigRevisionRepository) decryptOne(rev *model.ConfigRevision) error {
	if !rev.Sensitive || !secret.IsEncrypted(rev.Content) {
		return nil
	}
	plain, err := r.cipher.Decrypt(rev.Content)
	if err != nil {
		return err
	}
	rev.Content = plain
	return nil
}

// Create 追加一条版本快照。敏感快照落库前把 content 临时替换为密文、落库后恢复明文，
// 使自增主键等钩子字段正常写在 rev 上、且调用方仍持明文。
func (r *ConfigRevisionRepository) Create(rev *model.ConfigRevision) error {
	if !rev.Sensitive {
		return r.db.Create(rev).Error
	}
	enc, err := r.cipher.Encrypt(rev.Content)
	if err != nil {
		return err
	}
	plain := rev.Content
	rev.Content = enc
	defer func() { rev.Content = plain }()
	return r.db.Create(rev).Error
}

// ListByItem 按版本升序列出某配置项的全部历史版本。
func (r *ConfigRevisionRepository) ListByItem(itemID uint) ([]model.ConfigRevision, error) {
	var revs []model.ConfigRevision
	if err := r.db.Where("config_item_id = ?", itemID).Order("version asc").Find(&revs).Error; err != nil {
		return nil, err
	}
	for i := range revs {
		if err := r.decryptOne(&revs[i]); err != nil {
			return nil, err
		}
	}
	return revs, nil
}

// FindByItemAndVersion 取某配置项的指定版本；不存在返回 (nil, nil)。
func (r *ConfigRevisionRepository) FindByItemAndVersion(itemID uint, version int64) (*model.ConfigRevision, error) {
	var rev model.ConfigRevision
	err := r.db.Where("config_item_id = ? AND version = ?", itemID, version).First(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.decryptOne(&rev); err != nil {
		return nil, err
	}
	return &rev, nil
}
