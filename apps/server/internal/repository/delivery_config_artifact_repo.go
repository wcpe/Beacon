package repository

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// DeliveryConfigArtifactRepository 提供配置灰度冻结渲染工件的数据访问（FR-171，见 ADR-0071）。
// 工件在启动 payload 准备期由控制面渲染写入，供 manifest 下发、下载授权反查、清理护栏三处读取，不再重渲染。
type DeliveryConfigArtifactRepository struct {
	db *gorm.DB
}

// NewDeliveryConfigArtifactRepository 构造仓库。
func NewDeliveryConfigArtifactRepository(db *gorm.DB) *DeliveryConfigArtifactRepository {
	return &DeliveryConfigArtifactRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *DeliveryConfigArtifactRepository) WithTx(tx *gorm.DB) *DeliveryConfigArtifactRepository {
	return &DeliveryConfigArtifactRepository{db: tx}
}

// UpsertBatch 幂等批量落工件：按 (order_id, server_id, path) 冲突则覆盖 sha256 与 size_bytes
// （重跑 start 同键覆盖，不产生重复行）。OnConflict 三方言（MySQL/Postgres/sqlite）均可移植；入参空为 no-op。
func (r *DeliveryConfigArtifactRepository) UpsertBatch(artifacts []model.DeliveryConfigArtifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "order_id"}, {Name: "server_id"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{"sha256", "size_bytes"}),
	}).CreateInBatches(&artifacts, 500).Error
}

// ListByOrderServer 取某单为某目标冻结的全部工件（manifest 归一为文件项用；path 升序稳定）。
func (r *DeliveryConfigArtifactRepository) ListByOrderServer(orderID uint, serverID string) ([]model.DeliveryConfigArtifact, error) {
	var arts []model.DeliveryConfigArtifact
	if err := r.db.Where("order_id = ? AND server_id = ?", orderID, serverID).
		Order("path ASC").Find(&arts).Error; err != nil {
		return nil, err
	}
	return arts, nil
}

// ExistsAuthorizedSHA 判定下载授权：本 namespace 内、状态在 statuses 内的某单，是否为「本目标 serverID」冻结过
// 该 sha 的配置工件（config blob 下载授权反查，严格到「该单该目标该 blob」）。statuses 为空返回 false。
func (r *DeliveryConfigArtifactRepository) ExistsAuthorizedSHA(namespaceID uint, serverID, sha string, statuses []string) (bool, error) {
	if len(statuses) == 0 {
		return false, nil
	}
	var n int64
	err := r.db.Model(&model.DeliveryConfigArtifact{}).
		Where("server_id = ? AND sha256 = ?", serverID, sha).
		Where("order_id IN (?)", r.db.Model(&model.ChangeOrder{}).Select("id").
			Where("namespace_id = ? AND status IN ?", namespaceID, statuses)).
		Count(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListSHAsByOrder 取某单全部工件的去重 sha 集（清理保护刷新引用用）。
func (r *DeliveryConfigArtifactRepository) ListSHAsByOrder(orderID uint) ([]string, error) {
	var shas []string
	if err := r.db.Model(&model.DeliveryConfigArtifact{}).
		Distinct().Where("order_id = ?", orderID).
		Pluck("sha256", &shas).Error; err != nil {
		return nil, err
	}
	return shas, nil
}

// ListSHAsReferencedByStatusNotIn 取给定 sha 集合中仍被「状态不在 excluded 集合内的变更单」以配置工件引用的子集
// （config blob 清理阻断判定：命中即不可删；入参为空返回空集）。与文件项同口径护栏，防误删活动单 config blob。
func (r *DeliveryConfigArtifactRepository) ListSHAsReferencedByStatusNotIn(shas []string, excluded []string) (map[string]struct{}, error) {
	blocked := make(map[string]struct{}, len(shas))
	if len(shas) == 0 {
		return blocked, nil
	}
	var rows []string
	err := r.db.Model(&model.DeliveryConfigArtifact{}).
		Distinct().
		Joins("JOIN change_order AS o ON o.id = delivery_config_artifact.order_id").
		Where("delivery_config_artifact.sha256 IN ?", shas).
		Where("o.status NOT IN ?", excluded).
		Pluck("delivery_config_artifact.sha256", &rows).Error
	if err != nil {
		return nil, err
	}
	for _, sha := range rows {
		blocked[sha] = struct{}{}
	}
	return blocked, nil
}
