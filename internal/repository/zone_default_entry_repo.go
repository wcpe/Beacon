package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// ZoneDefaultEntryRepository 提供 zone_default_entry 表的数据访问（FR-48）。
type ZoneDefaultEntryRepository struct {
	db *gorm.DB
}

// NewZoneDefaultEntryRepository 构造仓库。
func NewZoneDefaultEntryRepository(db *gorm.DB) *ZoneDefaultEntryRepository {
	return &ZoneDefaultEntryRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ZoneDefaultEntryRepository) WithTx(tx *gorm.DB) *ZoneDefaultEntryRepository {
	return &ZoneDefaultEntryRepository{db: tx}
}

// FindByZone 查某 (ns, group, zone) 的默认入口；未设返回 (nil, nil)。
func (r *ZoneDefaultEntryRepository) FindByZone(ns, group, zone string) (*model.ZoneDefaultEntry, error) {
	var e model.ZoneDefaultEntry
	err := r.db.Where("namespace_code = ? AND group_code = ? AND zone_code = ?", ns, group, zone).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Upsert 设置或覆盖某 (ns, group, zone) 的默认入口 serverId（按 (ns, group, zone) 唯一）。
func (r *ZoneDefaultEntryRepository) Upsert(ns, group, zone, serverID string) (*model.ZoneDefaultEntry, error) {
	existing, err := r.FindByZone(ns, group, zone)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		existing.DefaultServerID = serverID
		if err := r.db.Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}
	e := &model.ZoneDefaultEntry{NamespaceCode: ns, GroupCode: group, ZoneCode: zone, DefaultServerID: serverID}
	if err := r.db.Create(e).Error; err != nil {
		return nil, err
	}
	return e, nil
}

// Delete 清除某 (ns, group, zone) 的默认入口（硬删）；返回是否命中。
func (r *ZoneDefaultEntryRepository) Delete(ns, group, zone string) (bool, error) {
	res := r.db.Where("namespace_code = ? AND group_code = ? AND zone_code = ?", ns, group, zone).
		Delete(&model.ZoneDefaultEntry{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// List 按可选条件（ns / group 过滤）列出默认入口（按 group, zone 升序，稳定输出）。
func (r *ZoneDefaultEntryRepository) List(ns, group string) ([]model.ZoneDefaultEntry, error) {
	q := r.db.Model(&model.ZoneDefaultEntry{})
	if ns != "" {
		q = q.Where("namespace_code = ?", ns)
	}
	if group != "" {
		q = q.Where("group_code = ?", group)
	}
	var list []model.ZoneDefaultEntry
	if err := q.Order("namespace_code, group_code, zone_code").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
