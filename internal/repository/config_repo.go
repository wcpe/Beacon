package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/secret"
)

// ConfigFilter 是配置项列表查询的可选过滤条件。
type ConfigFilter struct {
	Namespace  string
	Group      string
	DataID     string
	ScopeLevel string
}

// ConfigItemRepository 提供 config_item 表的数据访问。
// 持有 cipher 以在写入前加密、读出后解密敏感项 content（at-rest 边界，FR-20）；
// service 层始终只见明文，md5/merge/schema 校验零改。
type ConfigItemRepository struct {
	db     *gorm.DB
	cipher *secret.Cipher
}

// NewConfigItemRepository 构造仓库。cipher 负责敏感项的落库加密 / 读取解密。
func NewConfigItemRepository(db *gorm.DB, cipher *secret.Cipher) *ConfigItemRepository {
	return &ConfigItemRepository{db: db, cipher: cipher}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在事务内复用）。
func (r *ConfigItemRepository) WithTx(tx *gorm.DB) *ConfigItemRepository {
	return &ConfigItemRepository{db: tx, cipher: r.cipher}
}

// encryptForStore 把敏感项的明文 content 加密为落库密文；非敏感项原样返回。
func (r *ConfigItemRepository) encryptForStore(item *model.ConfigItem) (string, error) {
	if !item.Sensitive {
		return item.Content, nil
	}
	return r.cipher.Encrypt(item.Content)
}

// decryptOne 把单条读出的敏感项 content 从密文解密回明文（非敏感项不动）。
// 兼容历史明文：未带密文前缀的内容视为明文，原样保留，便于"先建项后转敏感"的演进。
func (r *ConfigItemRepository) decryptOne(item *model.ConfigItem) error {
	if !item.Sensitive || !secret.IsEncrypted(item.Content) {
		return nil
	}
	plain, err := r.cipher.Decrypt(item.Content)
	if err != nil {
		return err
	}
	item.Content = plain
	return nil
}

// decryptInPlace 批量解密读出的敏感项 content。
func (r *ConfigItemRepository) decryptInPlace(items []model.ConfigItem) error {
	for i := range items {
		if err := r.decryptOne(&items[i]); err != nil {
			return err
		}
	}
	return nil
}

// active 返回仅含未软删记录的查询（deleted_at = 哨兵）。
func (r *ConfigItemRepository) active() *gorm.DB {
	return r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
}

// Create 插入一个配置项。敏感项落库前把 content 临时替换为密文，落库后恢复明文，
// 使 GORM 钩子（如软删哨兵）正常写在 item 上、且调用方仍持明文。
func (r *ConfigItemRepository) Create(item *model.ConfigItem) error {
	stored, err := r.encryptForStore(item)
	if err != nil {
		return err
	}
	plain := item.Content
	item.Content = stored
	defer func() { item.Content = plain }()
	return r.db.Create(item).Error
}

// Save 全量保存配置项（发布更新 content/md5/current_revision/version 等）。
// 敏感项落库前临时替换为密文、落库后恢复明文，保持调用方 item 仍为明文。
func (r *ConfigItemRepository) Save(item *model.ConfigItem) error {
	stored, err := r.encryptForStore(item)
	if err != nil {
		return err
	}
	plain := item.Content
	item.Content = stored
	defer func() { item.Content = plain }()
	return r.db.Save(item).Error
}

// FindByID 按主键查找未软删项；不存在返回 (nil, nil)。
func (r *ConfigItemRepository) FindByID(id uint) (*model.ConfigItem, error) {
	var item model.ConfigItem
	err := r.active().Where("id = ?", id).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.decryptOne(&item); err != nil {
		return nil, err
	}
	return &item, nil
}

// FindByIDs 一次性按主键集合取未软删项（WHERE id IN (?)，占位符无方言、可移植）。
// 供批量端点替代逐项 FindByID 消除 N+1；返回数量可能少于入参（含不存在 id），由上层据此判 404。
func (r *ConfigItemRepository) FindByIDs(ids []uint) ([]model.ConfigItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var items []model.ConfigItem
	if err := r.active().Where("id IN ?", ids).Find(&items).Error; err != nil {
		return nil, err
	}
	if err := r.decryptInPlace(items); err != nil {
		return nil, err
	}
	return items, nil
}

// FindByIdentity 按标识五元组查找未软删项；不存在返回 (nil, nil)。
func (r *ConfigItemRepository) FindByIdentity(ns, group, dataID, scopeLevel, scopeTarget string) (*model.ConfigItem, error) {
	var item model.ConfigItem
	err := r.active().Where(
		"namespace_code = ? AND group_code = ? AND data_id = ? AND scope_level = ? AND scope_target = ?",
		ns, group, dataID, scopeLevel, scopeTarget,
	).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.decryptOne(&item); err != nil {
		return nil, err
	}
	return &item, nil
}

// FindFormatByDataID 取同一 (ns, dataId) 下任一未软删项的格式（跨层格式一致性校验）。
// 同一 dataId 在全网各层必须同格式（含 __GLOBAL__ 层），故按 ns+dataId 而非按 group 查。
// 不存在返回空串。
func (r *ConfigItemRepository) FindFormatByDataID(ns, dataID string) (string, error) {
	var item model.ConfigItem
	err := r.active().Where("namespace_code = ? AND data_id = ?", ns, dataID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return item.Format, nil
}

// List 按可选条件列出未软删配置项。
func (r *ConfigItemRepository) List(f ConfigFilter) ([]model.ConfigItem, error) {
	q := r.active()
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if f.Group != "" {
		q = q.Where("group_code = ?", f.Group)
	}
	if f.DataID != "" {
		q = q.Where("data_id = ?", f.DataID)
	}
	if f.ScopeLevel != "" {
		q = q.Where("scope_level = ?", f.ScopeLevel)
	}
	var items []model.ConfigItem
	if err := q.Order("namespace_code, group_code, data_id, scope_level, scope_target").Find(&items).Error; err != nil {
		return nil, err
	}
	if err := r.decryptInPlace(items); err != nil {
		return nil, err
	}
	return items, nil
}

// CountSensitive 统计库中未软删的敏感配置项数量（供启动 fail-fast 探测）。
func (r *ConfigItemRepository) CountSensitive() (int64, error) {
	var n int64
	err := r.active().Model(&model.ConfigItem{}).Where("sensitive = ?", true).Count(&n).Error
	return n, err
}

// CountByNamespace 统计某环境下未软删的配置项数（供环境删除守卫，FR-53）。
func (r *ConfigItemRepository) CountByNamespace(ns string) (int64, error) {
	var n int64
	err := r.active().Model(&model.ConfigItem{}).Where("namespace_code = ?", ns).Count(&n).Error
	return n, err
}

// SoftDelete 软删配置项：填真实删除时间并置 enabled=false。
// 校验 RowsAffected：0 命中（项已被并发软删）即返回 not-found，由批量调用方据此回滚，
// 杜绝预取通过、事务内目标已消失却照常写「幽灵审计」（TOCTOU，FR-74）。
func (r *ConfigItemRepository) SoftDelete(id uint, deletedAt time.Time) error {
	res := r.db.Model(&model.ConfigItem{}).
		Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).
		Updates(map[string]any{"deleted_at": deletedAt, "enabled": false})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apperr.ErrConfigNotFound
	}
	return nil
}

// SetEnabled 置未软删配置项的启用态（批量禁用 / 启用复用，FR-74）。
// 同 SoftDelete：0 命中返回 not-found，挡并发软删后的幽灵审计。
func (r *ConfigItemRepository) SetEnabled(id uint, enabled bool) error {
	res := r.db.Model(&model.ConfigItem{}).
		Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).
		Update("enabled", enabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apperr.ErrConfigNotFound
	}
	return nil
}

// BumpGrayVersion 以乐观锁方式自增 gray_version：仅当当前值等于 expected 且项未软删时 +1。
// 返回是否命中（false=版本已被并发灰度发布改动，调用方应重读重试）。
// 作为并发灰度发布的 CAS 串行点，从源头消除「先软删后建」在 uk_gray_item 上的死锁（FR-9）。
func (r *ConfigItemRepository) BumpGrayVersion(id uint, expected int64) (bool, error) {
	res := r.db.Model(&model.ConfigItem{}).
		Where("id = ? AND gray_version = ? AND deleted_at = ?", id, expected, model.SoftDeleteSentinel).
		Update("gray_version", expected+1)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// FindEffectiveCandidates 拉取某 agent 身份的四层候选配置（已 enabled 且未软删）。
// 一条查询拉全 global/group/zone/server 四层，由上层按 dataId 分桶合并。
func (r *ConfigItemRepository) FindEffectiveCandidates(ns, group, zone, serverID string) ([]model.ConfigItem, error) {
	levelCond := r.db.
		Where("scope_level = ? AND group_code = ?", model.ScopeGlobal, model.GlobalGroupCode).
		Or("scope_level = ? AND group_code = ?", model.ScopeGroup, group).
		Or("scope_level = ? AND group_code = ? AND scope_target = ?", model.ScopeZone, group, zone).
		Or("scope_level = ? AND group_code = ? AND scope_target = ?", model.ScopeServer, group, serverID)

	var items []model.ConfigItem
	err := r.db.
		Where("deleted_at = ?", model.SoftDeleteSentinel).
		Where("enabled = ?", true).
		Where("namespace_code = ?", ns).
		Where(levelCond).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	// 解密敏感项，保证有效配置合并与下发链路拿到明文
	if err := r.decryptInPlace(items); err != nil {
		return nil, err
	}
	return items, nil
}
