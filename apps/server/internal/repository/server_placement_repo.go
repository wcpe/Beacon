package repository

import (
	"errors"

	"gorm.io/gorm"
)

// ServerPlacement 是实例区服归属的解析结果（新真源 = server 表）。
// GroupCode 是大区 code（server.zone_id → zone.region_id → region.code），
// ZoneCode 是小区 code（server.zone_id → zone.code）。
// 两者与 config_item / file_object 的 (group_code, scope_target) 书写口径一致，
// 即 v1 时代 zone_assignment.group_code / zone_code 的同一个业务含义。
type ServerPlacement struct {
	GroupCode string
	ZoneCode  string
}

// ServerPlacementRepository 从新真源（server 表的 zone_id）解析实例归属。
// 取代已退役的 ZoneAssignmentRepository：旧 zone_assignment 表仅由 v1 写路径写入，
// 生产上恒 0 行，读它会把所有实例的归属解析成「未分配」（见 assignment_repo.go）。
type ServerPlacementRepository struct {
	db *gorm.DB
}

// NewServerPlacementRepository 构造仓库。
func NewServerPlacementRepository(db *gorm.DB) *ServerPlacementRepository {
	return &ServerPlacementRepository{db: db}
}

// FindByServer 解析某 (namespace, serverId) 的区服归属；无区服归属时返回 (nil, nil)。
//
// 归属判据是「已分配到小区」（server.zone_id 非空），大区 code 由 zone → region 解链取得。
// 据此，以下情形一律按「无归属」返回 (nil, nil)，由调用方回退 groupHint / 空 zone（与旧表语义一致）：
//   - server 行不存在（未注册资产）；
//   - 仅分配到 BC 集群的代理（bc_cluster_id，无 zone）——v1 时代代理本就不可被指派 zone；
//   - 仅加入大厅集群（lobby_cluster_id）、尚未落业务小区。
//
// 不按 lifecycle 过滤：归档 / 墓碑服的归属仍然是历史事实，运行资格由调用方各自的
// 环境与资产闸（ensureServerActiveForNamespace 等）把关，本查询只做纯投影。
//
// 单条 JOIN 查询取齐三张表，无 N+1（实例注册热路径每次注册都会调用）。
func (r *ServerPlacementRepository) FindByServer(ns, serverID string) (*ServerPlacement, error) {
	if ns == "" || serverID == "" {
		return nil, nil
	}
	var row struct {
		GroupCode string `gorm:"column:group_code"`
		ZoneCode  string `gorm:"column:zone_code"`
	}
	err := r.db.Table("server").
		Select("region.code AS group_code, zone.code AS zone_code").
		Joins("JOIN namespace ON namespace.id = server.namespace_id").
		Joins("JOIN zone ON zone.id = server.zone_id").
		Joins("JOIN region ON region.id = zone.region_id").
		Where("namespace.code = ? AND server.server_id = ?", ns, serverID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ServerPlacement{GroupCode: row.GroupCode, ZoneCode: row.ZoneCode}, nil
}

// FindByNamespace 一次性解析某 namespace 下全部实例的区服归属，返回 serverId → ServerPlacement 映射。
//
// 与 FindByServer 逐条查等价：同一组 JOIN、同一列口径、同一「已分配到小区」判据；
// 未分配 / 仅分到 BC 集群的代理 / 仅入大厅的 server 行不进入结果，调用方按「无归属」回退
// GroupHint 与空 zone（映射本身无序，调用方若需要稳定顺序请自行排序）。
//
// 供批量消费方使用（发布影响面预览、长轮询唤醒集合反查）：一次查询取齐整个环境，
// 避免按 serverId 逐个 FindByServer 的 N+1。
func (r *ServerPlacementRepository) FindByNamespace(ns string) (map[string]ServerPlacement, error) {
	if ns == "" {
		return map[string]ServerPlacement{}, nil
	}
	var rows []struct {
		ServerID  string `gorm:"column:server_id"`
		GroupCode string `gorm:"column:group_code"`
		ZoneCode  string `gorm:"column:zone_code"`
	}
	err := r.db.Table("server").
		Select("server.server_id AS server_id, region.code AS group_code, zone.code AS zone_code").
		Joins("JOIN namespace ON namespace.id = server.namespace_id").
		Joins("JOIN zone ON zone.id = server.zone_id").
		Joins("JOIN region ON region.id = zone.region_id").
		Where("namespace.code = ?", ns).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]ServerPlacement, len(rows))
	for _, row := range rows {
		out[row.ServerID] = ServerPlacement{GroupCode: row.GroupCode, ZoneCode: row.ZoneCode}
	}
	return out, nil
}
