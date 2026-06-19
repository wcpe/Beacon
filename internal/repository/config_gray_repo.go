package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"beacon/internal/model"
	"beacon/internal/secret"
)

// ConfigGrayRepository 提供 config_gray 表的数据访问（FR-9，见 ADR-0021）。
// 与 ConfigItemRepository 一致地对敏感灰度 content 做落库加密 / 读取解密（FR-20），
// service 层始终只见明文。
type ConfigGrayRepository struct {
	db     *gorm.DB
	cipher *secret.Cipher
}

// NewConfigGrayRepository 构造仓库。cipher 负责敏感灰度内容的加解密。
func NewConfigGrayRepository(db *gorm.DB, cipher *secret.Cipher) *ConfigGrayRepository {
	return &ConfigGrayRepository{db: db, cipher: cipher}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ConfigGrayRepository) WithTx(tx *gorm.DB) *ConfigGrayRepository {
	return &ConfigGrayRepository{db: tx, cipher: r.cipher}
}

// encryptForStore 把敏感灰度的明文 content 加密为落库密文；非敏感原样返回。
func (r *ConfigGrayRepository) encryptForStore(g *model.ConfigGray) (string, error) {
	if !g.Sensitive {
		return g.Content, nil
	}
	return r.cipher.Encrypt(g.Content)
}

// decryptOne 把单条读出的敏感灰度 content 解密回明文（非敏感 / 历史明文不动）。
func (r *ConfigGrayRepository) decryptOne(g *model.ConfigGray) error {
	if !g.Sensitive || !secret.IsEncrypted(g.Content) {
		return nil
	}
	plain, err := r.cipher.Decrypt(g.Content)
	if err != nil {
		return err
	}
	g.Content = plain
	return nil
}

// decryptInPlace 批量解密读出的敏感灰度 content。
func (r *ConfigGrayRepository) decryptInPlace(grays []model.ConfigGray) error {
	for i := range grays {
		if err := r.decryptOne(&grays[i]); err != nil {
			return err
		}
	}
	return nil
}

// Create 插入一条灰度。敏感灰度落库前临时替换为密文、落库后恢复明文，
// 使软删哨兵等钩子正常写在 g 上、且调用方仍持明文。
func (r *ConfigGrayRepository) Create(g *model.ConfigGray) error {
	stored, err := r.encryptForStore(g)
	if err != nil {
		return err
	}
	plain := g.Content
	g.Content = stored
	defer func() { g.Content = plain }()
	return r.db.Create(g).Error
}

// FindActiveByItem 取某 config_item 的活跃灰度（未软删）；不存在返回 (nil, nil)。
func (r *ConfigGrayRepository) FindActiveByItem(itemID uint) (*model.ConfigGray, error) {
	var g model.ConfigGray
	err := r.db.Where("config_item_id = ? AND deleted_at = ?", itemID, model.SoftDeleteSentinel).First(&g).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.decryptOne(&g); err != nil {
		return nil, err
	}
	return &g, nil
}

// ListActiveByItemIDs 一次性取一批 config_item 的活跃灰度（按 ns + item 集合查，避免 N+1）。
// 返回 itemID→灰度的映射，供有效配置解析按版本选择层叠加。
func (r *ConfigGrayRepository) ListActiveByItemIDs(ns string, itemIDs []uint) (map[uint]model.ConfigGray, error) {
	out := make(map[uint]model.ConfigGray, len(itemIDs))
	if len(itemIDs) == 0 {
		return out, nil
	}
	var grays []model.ConfigGray
	err := r.db.Where("namespace_code = ? AND deleted_at = ? AND config_item_id IN ?",
		ns, model.SoftDeleteSentinel, itemIDs).Find(&grays).Error
	if err != nil {
		return nil, err
	}
	if err := r.decryptInPlace(grays); err != nil {
		return nil, err
	}
	for _, g := range grays {
		out[g.ConfigItemID] = g
	}
	return out, nil
}

// ListActive 列出某环境内全部活跃灰度（ns 为空则全量）。
func (r *ConfigGrayRepository) ListActive(ns string) ([]model.ConfigGray, error) {
	q := r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
	if ns != "" {
		q = q.Where("namespace_code = ?", ns)
	}
	var grays []model.ConfigGray
	if err := q.Order("namespace_code, config_item_id").Find(&grays).Error; err != nil {
		return nil, err
	}
	if err := r.decryptInPlace(grays); err != nil {
		return nil, err
	}
	return grays, nil
}

// SoftDelete 软删某 config_item 的活跃灰度（promote/abort 收口）；返回是否命中。
func (r *ConfigGrayRepository) SoftDelete(itemID uint, deletedAt time.Time) (bool, error) {
	res := r.db.Model(&model.ConfigGray{}).
		Where("config_item_id = ? AND deleted_at = ?", itemID, model.SoftDeleteSentinel).
		Update("deleted_at", deletedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
