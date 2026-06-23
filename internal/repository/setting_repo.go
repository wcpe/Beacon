package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// SettingRepository 提供 setting 表的数据访问（FR-61，见 ADR-0038）。
// 真源在库（热改项）：GetAll 启动载入内存缓存；Get 取单项；Upsert 以乐观锁 CAS 写值（首启 seed 与运行期 Update 共用）。
type SettingRepository struct {
	db *gorm.DB
}

// NewSettingRepository 构造仓库。
func NewSettingRepository(db *gorm.DB) *SettingRepository {
	return &SettingRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在外层事务内复用，避免嵌套开事务死锁）。
func (r *SettingRepository) WithTx(tx *gorm.DB) *SettingRepository {
	return &SettingRepository{db: tx}
}

// GetAll 列出全部设置项（供启动载入内存缓存）。
func (r *SettingRepository) GetAll() ([]model.Setting, error) {
	var list []model.Setting
	if err := r.db.Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// Get 按 key 取单项；不存在返回 (nil, nil)。
func (r *SettingRepository) Get(key string) (*model.Setting, error) {
	var s model.Setting
	err := r.db.Where("setting_key = ?", key).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Upsert 写入或更新某 key 的值（version+1 乐观锁）。
// 不存在则插入（version=1）；已存在则更新 value/value_type 并 version+1。返回落库后的记录（含新 version）。
//
// 直接在 r.db 上执行（r.db 可为外层事务 tx，经 WithTx 注入）——**不自开嵌套事务**：
// 嵌套 db.Transaction 在单连接池（MaxOpenConns=1，如测试内存库）下会因抢同一连接而死锁。
// 调用方（Update 在事务内、SeedFromConfig 单语句）控制事务边界。
func (r *SettingRepository) Upsert(key, value, valueType string) (*model.Setting, error) {
	var existing model.Setting
	e := r.db.Where("setting_key = ?", key).First(&existing).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		saved := model.Setting{Key: key, Value: value, ValueType: valueType, Version: 1}
		if err := r.db.Create(&saved).Error; err != nil {
			return nil, err
		}
		return &saved, nil
	}
	if e != nil {
		return nil, e
	}
	// CAS：仅当 version 未被并发改动时更新，version+1。
	res := r.db.Model(&model.Setting{}).
		Where("setting_key = ? AND version = ?", key, existing.Version).
		Updates(map[string]any{"value": value, "value_type": valueType, "version": existing.Version + 1})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound // 并发改动，由调用方重读后重试（控制面单节点，极少见）
	}
	var saved model.Setting
	if err := r.db.Where("setting_key = ?", key).First(&saved).Error; err != nil {
		return nil, err
	}
	return &saved, nil
}
