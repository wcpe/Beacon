// 小区默认入口的 v2 真源解析（ADR-0067 取代 ADR-0031 的存储决策）：
// 默认入口唯一真源 = server.is_default_entry 列（管理台分配勾选 / 独立 toggle 写入），
// 发现 / 实例视图的 zoneDefaultEntry 标志与 v1 只读列表均从本文件解析，不再读已废弃的 zone_default_entry 表。
package service

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// DefaultEntryItem 是小区默认入口列表项（v1 GET /admin/v1/zones/default-entry 兼容形状，Legacy 只读消费）。
// Group / Zone 取 v2 大区名 / 小区名（与 v1 group_code/zone_code 语义对应）。
type DefaultEntryItem struct {
	Namespace       string
	Group           string
	Zone            string
	DefaultServerID string
	UpdatedAt       time.Time
}

// DefaultEntryServerIDs 解析某 namespace 下被指定为小区默认入口的 serverId 集合（FR-48 下发消费）。
// 供发现 / 实例视图渲染 zoneDefaultEntry 标志；namespace 不存在按空集返回（发现链路不因坏参数中断）。
func (s *V2ControlPlaneService) DefaultEntryServerIDs(ns string) (map[string]bool, error) {
	nsRow, err := s.namespaceByCode(ns)
	if err != nil {
		return nil, err
	}
	if nsRow == nil {
		return map[string]bool{}, nil
	}
	var ids []string
	if err := s.db.Model(&model.Server{}).
		Where("namespace_id = ? AND is_default_entry = ?", nsRow.ID, true).
		Pluck("server_id", &ids).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// ListDefaultEntries 列出某 namespace（可选按大区名过滤）的小区默认入口（v1 列表端点兼容）。
func (s *V2ControlPlaneService) ListDefaultEntries(ns, group string) ([]DefaultEntryItem, error) {
	nsRow, err := s.namespaceByCode(ns)
	if err != nil {
		return nil, err
	}
	if nsRow == nil {
		return []DefaultEntryItem{}, nil
	}
	var servers []model.Server
	if err := s.db.
		Where("namespace_id = ? AND is_default_entry = ? AND zone_id IS NOT NULL", nsRow.ID, true).
		Order("server_id ASC").Find(&servers).Error; err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return []DefaultEntryItem{}, nil
	}
	zoneIDs := make([]uint, 0, len(servers))
	for i := range servers {
		zoneIDs = append(zoneIDs, *servers[i].ZoneID)
	}
	zoneByID, regionNameByID, err := loadZoneRegionNames(s.db, zoneIDs)
	if err != nil {
		return nil, err
	}
	items := make([]DefaultEntryItem, 0, len(servers))
	for i := range servers {
		zone, ok := zoneByID[*servers[i].ZoneID]
		if !ok {
			continue // 小区行缺失（并发删除竞态）：跳过而非报错
		}
		regionName := regionNameByID[zone.RegionID]
		if group != "" && regionName != group {
			continue
		}
		items = append(items, DefaultEntryItem{
			Namespace: ns, Group: regionName, Zone: zone.Name,
			DefaultServerID: servers[i].ServerID, UpdatedAt: servers[i].UpdatedAt,
		})
	}
	return items, nil
}

// namespaceByCode 按环境编码取 namespace 行；不存在返回 (nil, nil)。
func (s *V2ControlPlaneService) namespaceByCode(code string) (*model.Namespace, error) {
	if code == "" {
		return nil, nil
	}
	var ns model.Namespace
	if err := s.db.Where("code = ?", code).First(&ns).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &ns, nil
}
