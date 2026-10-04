package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ZoneAssignmentRepository 提供 zone_assignment 表的数据访问。
//
// 【已退役 · 仅历史数据与 Legacy 兼容读，新写入请勿再用】
//
// 实例区服归属的唯一真源是 server 表的 zone_id / bc_cluster_id / lobby_cluster_id（见 model.Server，
// 经 v2 分配路径 applyAssignment 写入，POST /admin/v2/server-assignments）。
//
// 本仓库访问的 zone_assignment 表仅由 v1 旧写路径（service/zone_service.go 的 applyAssignInTx，
// 其对外入口 ZoneService.Assign 已恒返回 FORBIDDEN）写入，生产上恒 0 行。任何读取方读本表都会把
// 全部实例解析成「未分配」，故读方已在本次迁移全部改读 ServerPlacementRepository；新代码要读归属
// 请一律使用 ServerPlacementRepository.FindByServer。
//
// 保留本仓库的目的仅为：历史数据留存、Legacy 兼容读（zone_service.go 的只读汇总 / 审批回显）
// 与测试构造。禁止新增调用方，禁止在此新增写方法。
type ZoneAssignmentRepository struct {
	db *gorm.DB
}

// NewZoneAssignmentRepository 构造仓库。
func NewZoneAssignmentRepository(db *gorm.DB) *ZoneAssignmentRepository {
	return &ZoneAssignmentRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ZoneAssignmentRepository) WithTx(tx *gorm.DB) *ZoneAssignmentRepository {
	return &ZoneAssignmentRepository{db: tx}
}

// FindByServer 解析某 serverId 在某环境的未软删归属；未指派返回 (nil, nil)。
//
// 【已退役】读方请改用 ServerPlacementRepository.FindByServer（读 server 表真源）。
// 本方法在生产上恒返回 (nil, nil)（表恒 0 行），仅保留供 Legacy 兼容读与测试。
func (r *ZoneAssignmentRepository) FindByServer(ns, serverID string) (*model.ZoneAssignment, error) {
	var a model.ZoneAssignment
	err := r.db.Where("namespace_code = ? AND server_id = ? AND deleted_at = ?",
		ns, serverID, model.SoftDeleteSentinel).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Upsert 新增或改派某 serverId 的归属（按 (ns, serverId) 唯一）。
//
// 【已退役 · 新写入请勿再用】改派归属请走 v2 路径（V2ControlPlaneService.applyAssignment 写
// server.zone_id / bc_cluster_id）。本方法只被 v1 旧路径调用，生产上无调用方。
func (r *ZoneAssignmentRepository) Upsert(ns, serverID, group, zone, note string) (*model.ZoneAssignment, error) {
	existing, err := r.FindByServer(ns, serverID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		existing.GroupCode, existing.ZoneCode, existing.Note = group, zone, note
		if err := r.db.Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}
	a := &model.ZoneAssignment{NamespaceCode: ns, ServerID: serverID, GroupCode: group, ZoneCode: zone, Note: note}
	if err := r.db.Create(a).Error; err != nil {
		return nil, err
	}
	return a, nil
}

// List 按可选条件列出未软删的指派。
func (r *ZoneAssignmentRepository) List(ns, group, zone string) ([]model.ZoneAssignment, error) {
	q := r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
	if ns != "" {
		q = q.Where("namespace_code = ?", ns)
	}
	if group != "" {
		q = q.Where("group_code = ?", group)
	}
	if zone != "" {
		q = q.Where("zone_code = ?", zone)
	}
	var list []model.ZoneAssignment
	if err := q.Order("namespace_code, group_code, zone_code, server_id").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// CountByNamespace 统计某环境下未软删的 zone 指派数（供环境删除守卫，FR-53）。
func (r *ZoneAssignmentRepository) CountByNamespace(ns string) (int64, error) {
	var n int64
	err := r.db.Model(&model.ZoneAssignment{}).
		Where("namespace_code = ? AND deleted_at = ?", ns, model.SoftDeleteSentinel).
		Count(&n).Error
	return n, err
}

// SoftDelete 软删某 serverId 的归属；返回是否命中。
func (r *ZoneAssignmentRepository) SoftDelete(ns, serverID string, deletedAt time.Time) (bool, error) {
	res := r.db.Model(&model.ZoneAssignment{}).
		Where("namespace_code = ? AND server_id = ? AND deleted_at = ?", ns, serverID, model.SoftDeleteSentinel).
		Update("deleted_at", deletedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
