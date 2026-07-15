package repository

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// escapeLikeHash 用 '#' 转义 LIKE 元字符（# % _），配合 `ESCAPE '#'` 使子串条件按字面匹配。
// 刻意不用反斜杠做转义符：MySQL 字符串字面量本身消费反斜杠，`ESCAPE '\'` 会成为语法错误
// （真 MySQL 集成测试逮出），'#' 在 MySQL / Postgres / sqlite 三方言下行为一致。
func escapeLikeHash(s string) string {
	s = strings.ReplaceAll(s, "#", "##")
	s = strings.ReplaceAll(s, "%", "#%")
	s = strings.ReplaceAll(s, "_", "#_")
	return s
}

// ChangeOrderListQuery 是变更单列表过滤条件（spec §5.1）：零值字段不参与过滤。
type ChangeOrderListQuery struct {
	// 归属 namespace；0 = 不过滤
	NamespaceID uint
	// 单状态；空串 = 不过滤
	Status string
	// 创建人精确匹配；空串 = 不过滤
	CreatedBy string
	// 标题子串（大小写按库排序规则）；空串 = 不过滤
	Keyword string
	// 页码（1 起）
	Page int
	// 每页条数
	Size int
}

// ChangeTargetQuery 是目标分页过滤条件（spec §5.1 targets 端点）：零值字段不参与过滤。
type ChangeTargetQuery struct {
	// 批次序号；0 = 不过滤
	BatchNo int
	// 目标状态；空串 = 不过滤
	Status string
	// serverId 子串；空串 = 不过滤
	ServerID string
	// 页码（1 起）
	Page int
	// 每页条数
	Size int
}

// ChangeOrderRepository 提供交付编排变更单（单 / 项 / 批 / 目标）的数据访问
// （FR-162，见 v2-delivery-orchestration.md §3.1~§3.4）。状态机语义在 service 层，本仓库只做数据往返。
type ChangeOrderRepository struct {
	db *gorm.DB
}

// NewChangeOrderRepository 构造仓库。
func NewChangeOrderRepository(db *gorm.DB) *ChangeOrderRepository {
	return &ChangeOrderRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ChangeOrderRepository) WithTx(tx *gorm.DB) *ChangeOrderRepository {
	return &ChangeOrderRepository{db: tx}
}

// Create 新建变更单。
func (r *ChangeOrderRepository) Create(order *model.ChangeOrder) error {
	return r.db.Create(order).Error
}

// FindByID 按主键查变更单；不存在返回 (nil, nil)。
func (r *ChangeOrderRepository) FindByID(id uint) (*model.ChangeOrder, error) {
	var order model.ChangeOrder
	err := r.db.Where("id = ?", id).First(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// List 按过滤条件分页查询变更单（创建时间倒序），返回当页记录与总数。
func (r *ChangeOrderRepository) List(q ChangeOrderListQuery) ([]model.ChangeOrder, int64, error) {
	query := r.db.Model(&model.ChangeOrder{})
	if q.NamespaceID != 0 {
		query = query.Where("namespace_id = ?", q.NamespaceID)
	}
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.CreatedBy != "" {
		query = query.Where("created_by = ?", q.CreatedBy)
	}
	if q.Keyword != "" {
		query = query.Where("title LIKE ? ESCAPE '#'", "%"+escapeLikeHash(q.Keyword)+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var orders []model.ChangeOrder
	if err := query.Order("created_at desc, id desc").
		Limit(q.Size).Offset((q.Page - 1) * q.Size).
		Find(&orders).Error; err != nil {
		return nil, 0, err
	}
	return orders, total, nil
}

// Save 全量更新一条变更单（按主键）；供 draft 编辑等整行写回。
func (r *ChangeOrderRepository) Save(order *model.ChangeOrder) error {
	return r.db.Save(order).Error
}

// UpdateStatusCAS 按前置状态集合 CAS 更新单状态与随迁字段：命中返回 true，前态不符返回 false。
// updates 内须含 "status" 新值；并发迁移靠 WHERE status IN 前态兜底（不加行锁）。
func (r *ChangeOrderRepository) UpdateStatusCAS(id uint, from []string, updates map[string]any) (bool, error) {
	res := r.db.Model(&model.ChangeOrder{}).
		Where("id = ? AND status IN ?", id, from).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteOrder 物理删除变更单主行（draft 单删除用；项由 DeleteItemsByOrder 同事务连带删）。
func (r *ChangeOrderRepository) DeleteOrder(id uint) error {
	return r.db.Where("id = ?", id).Delete(&model.ChangeOrder{}).Error
}

// —— 变更项 ——

// CreateItems 批量插入变更项（分批控制单条 SQL 参数量）。
func (r *ChangeOrderRepository) CreateItems(items []model.ChangeOrderItem) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.CreateInBatches(&items, 500).Error
}

// ListItems 取某单全部变更项（file_diff 按 path、config_change 按 id 稳定排序）。
func (r *ChangeOrderRepository) ListItems(orderID uint) ([]model.ChangeOrderItem, error) {
	var items []model.ChangeOrderItem
	if err := r.db.Where("order_id = ?", orderID).
		Order("kind ASC, path ASC, id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// FindItem 按 (orderID, itemID) 查单个变更项；不存在返回 (nil, nil)。
func (r *ChangeOrderRepository) FindItem(orderID, itemID uint) (*model.ChangeOrderItem, error) {
	var item model.ChangeOrderItem
	err := r.db.Where("order_id = ? AND id = ?", orderID, itemID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// DeleteItemsByKind 删除某单某载荷类型的全部变更项（diff-scan 重算 / 配置项整组替换用）。
func (r *ChangeOrderRepository) DeleteItemsByKind(orderID uint, kind string) error {
	return r.db.Where("order_id = ? AND kind = ?", orderID, kind).Delete(&model.ChangeOrderItem{}).Error
}

// DeleteItemsByOrder 删除某单全部变更项（draft 单物理删除连带）。
func (r *ChangeOrderRepository) DeleteItemsByOrder(orderID uint) error {
	return r.db.Where("order_id = ?", orderID).Delete(&model.ChangeOrderItem{}).Error
}

// —— 批次 / 目标（M1 只读消费：启动前恒为空集）——

// ListBatches 取某单全部批次（batch_no 升序）。
func (r *ChangeOrderRepository) ListBatches(orderID uint) ([]model.ChangeBatch, error) {
	var batches []model.ChangeBatch
	if err := r.db.Where("order_id = ?", orderID).Order("batch_no ASC").Find(&batches).Error; err != nil {
		return nil, err
	}
	return batches, nil
}

// —— M3 编排推进写（FR-166，spec §4.1/§4.4；启动固化 + 批次 / 目标状态机 CAS 迁移）——

// ListActiveOrders 取状态在给定集合内的全部变更单（编排推进器 drainActive 装载 rolling / paused 用；id 升序稳定）。
func (r *ChangeOrderRepository) ListActiveOrders(statuses []string) ([]model.ChangeOrder, error) {
	var orders []model.ChangeOrder
	if err := r.db.Where("status IN ?", statuses).Order("id asc").Find(&orders).Error; err != nil {
		return nil, err
	}
	return orders, nil
}

// CreateBatches 批量插入批次行（启动时一次性生成、执行中不重划，spec §4.4.1）。
func (r *ChangeOrderRepository) CreateBatches(batches []model.ChangeBatch) error {
	if len(batches) == 0 {
		return nil
	}
	return r.db.CreateInBatches(&batches, 200).Error
}

// CreateTargets 批量插入目标行（启动时按 selector 解析固化快照，spec §4.3.1）。
func (r *ChangeOrderRepository) CreateTargets(targets []model.ChangeTarget) error {
	if len(targets) == 0 {
		return nil
	}
	return r.db.CreateInBatches(&targets, 500).Error
}

// FindBatchByNo 按 (orderID, batchNo) 查批次；不存在返回 (nil, nil)。
func (r *ChangeOrderRepository) FindBatchByNo(orderID uint, batchNo int) (*model.ChangeBatch, error) {
	var batch model.ChangeBatch
	err := r.db.Where("order_id = ? AND batch_no = ?", orderID, batchNo).First(&batch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

// ListTargetsByOrder 取某单全部目标行（server_id 升序，编排推进逐服判定用；未启动恒为空集，无分页）。
func (r *ChangeOrderRepository) ListTargetsByOrder(orderID uint) ([]model.ChangeTarget, error) {
	var targets []model.ChangeTarget
	if err := r.db.Where("order_id = ?", orderID).Order("server_id ASC").Find(&targets).Error; err != nil {
		return nil, err
	}
	return targets, nil
}

// UpdateTargetCAS 按前置状态集合 CAS 迁移目标主状态与随迁字段：命中返回 true，前态不符返回 false。
// updates 内须含 "status" 新值；并发推进靠 WHERE status IN 前态兜底（单一驱动源仍是推进器，此为幂等护栏）。
func (r *ChangeOrderRepository) UpdateTargetCAS(id uint, from []string, updates map[string]any) (bool, error) {
	res := r.db.Model(&model.ChangeTarget{}).
		Where("id = ? AND status IN ?", id, from).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateBatchCAS 按前置状态集合 CAS 迁移批状态与随迁字段：命中返回 true，前态不符返回 false。
func (r *ChangeOrderRepository) UpdateBatchCAS(id uint, from []string, updates map[string]any) (bool, error) {
	res := r.db.Model(&model.ChangeBatch{}).
		Where("id = ? AND status IN ?", id, from).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateBatchColumns 无条件更新批次列（计数刷新，按目标终态重算后幂等写回）。
func (r *ChangeOrderRepository) UpdateBatchColumns(id uint, updates map[string]any) error {
	return r.db.Model(&model.ChangeBatch{}).Where("id = ?", id).Updates(updates).Error
}

// BulkUpdateTargetStatusByBatch 把某批内主状态在 from 集合内的目标批量改为 updates（熔断 / resume 重置用）；返回受影响数。
func (r *ChangeOrderRepository) BulkUpdateTargetStatusByBatch(batchID uint, from []string, updates map[string]any) (int64, error) {
	res := r.db.Model(&model.ChangeTarget{}).
		Where("batch_id = ? AND status IN ?", batchID, from).
		Updates(updates)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// BulkUpdateTargetStatusByOrder 把某单内主状态在 from 集合内的目标批量改为 updates（紧急终止把未开始目标置 skipped 用）；返回受影响数。
func (r *ChangeOrderRepository) BulkUpdateTargetStatusByOrder(orderID uint, from []string, updates map[string]any) (int64, error) {
	res := r.db.Model(&model.ChangeTarget{}).
		Where("order_id = ? AND status IN ?", orderID, from).
		Updates(updates)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// BulkUpdateBatchStatusByOrder 把某单内状态在 from 集合内的批次批量改为 updates（紧急终止把未开始批置 skipped 用）；返回受影响数。
func (r *ChangeOrderRepository) BulkUpdateBatchStatusByOrder(orderID uint, from []string, updates map[string]any) (int64, error) {
	res := r.db.Model(&model.ChangeBatch{}).
		Where("order_id = ? AND status IN ?", orderID, from).
		Updates(updates)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// ListActiveTargetServerIDs 取本 namespace 内「其他活动单（状态在 statuses 内、id ≠ excludeOrderID）」的全部目标 serverId
// （启动冲突守卫：同一目标服同时只允许被一个活动单覆盖，ADR-0071 §4.1）。子查询 IN 一次取齐、禁循环查库。
func (r *ChangeOrderRepository) ListActiveTargetServerIDs(namespaceID, excludeOrderID uint, statuses []string) ([]string, error) {
	var ids []string
	err := r.db.Model(&model.ChangeTarget{}).
		Distinct().
		Where("order_id IN (?)", r.db.Model(&model.ChangeOrder{}).Select("id").
			Where("namespace_id = ? AND id <> ? AND status IN ?", namespaceID, excludeOrderID, statuses)).
		Order("server_id asc").
		Pluck("server_id", &ids).Error
	return ids, err
}

// ListTargets 按批次 / 状态 / serverId 子串过滤分页取目标（server_id 升序），返回当页与总数。
// 批次过滤按业务批次号经 change_batch 子查询映射到 batch_id（目标行不冗余 batch_no）。
func (r *ChangeOrderRepository) ListTargets(orderID uint, q ChangeTargetQuery) ([]model.ChangeTarget, int64, error) {
	query := r.db.Model(&model.ChangeTarget{}).Where("order_id = ?", orderID)
	if q.BatchNo != 0 {
		query = query.Where("batch_id IN (?)", r.db.Model(&model.ChangeBatch{}).
			Select("id").Where("order_id = ? AND batch_no = ?", orderID, q.BatchNo))
	}
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.ServerID != "" {
		query = query.Where("server_id LIKE ? ESCAPE '#'", "%"+escapeLikeHash(q.ServerID)+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var targets []model.ChangeTarget
	if err := query.Order("server_id ASC").
		Limit(q.Size).Offset((q.Page - 1) * q.Size).
		Find(&targets).Error; err != nil {
		return nil, 0, err
	}
	return targets, total, nil
}

// ListTargetServerIDs 取某单全部目标 serverId（字典序）；未启动的单恒为空集。
func (r *ChangeOrderRepository) ListTargetServerIDs(orderID uint) ([]string, error) {
	var ids []string
	if err := r.db.Model(&model.ChangeTarget{}).Where("order_id = ?", orderID).
		Order("server_id ASC").Pluck("server_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// statusCountRow 是按状态分组计数的扫描载体。
type statusCountRow struct {
	Status string
	N      int64
}

// CountTargetsByStatus 按目标主状态分组计数（详情 targetCounts 用）。
func (r *ChangeOrderRepository) CountTargetsByStatus(orderID uint) (map[string]int64, error) {
	return r.groupTargetCounts(orderID, "status", "")
}

// CountTargetsByRollbackStatus 按目标回滚状态分组计数（未进入回滚的目标不计入）。
func (r *ChangeOrderRepository) CountTargetsByRollbackStatus(orderID uint) (map[string]int64, error) {
	return r.groupTargetCounts(orderID, "rollback_status", "rollback_status <> ''")
}

// groupTargetCounts 对某单目标按指定列分组计数；extraWhere 追加过滤（空串跳过）。
func (r *ChangeOrderRepository) groupTargetCounts(orderID uint, column, extraWhere string) (map[string]int64, error) {
	query := r.db.Model(&model.ChangeTarget{}).
		Select(column+" AS status, COUNT(*) AS n").Where("order_id = ?", orderID)
	if extraWhere != "" {
		query = query.Where(extraWhere)
	}
	var rows []statusCountRow
	if err := query.Group(column).Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.Status] = row.N
	}
	return counts, nil
}

// —— 交付数据面归属 / 清理反查（FR-165，spec §5.3/§4.5.4）——

// ListOrdersReferencingSHA 取某 namespace 内文件项引用了指定 sha256、且单状态在给定集合内的变更单
// （blob 归属校验用：模板源可上传 / 目标可下载的判定基础）。子查询 IN 一次取齐（免 JOIN 去重），禁循环查库。
func (r *ChangeOrderRepository) ListOrdersReferencingSHA(namespaceID uint, sha string, statuses []string) ([]model.ChangeOrder, error) {
	var orders []model.ChangeOrder
	err := r.db.Where("namespace_id = ? AND status IN ?", namespaceID, statuses).
		Where("id IN (?)", r.db.Model(&model.ChangeOrderItem{}).Select("order_id").
			Where("kind = ? AND sha256 = ?", model.ChangeItemKindFileDiff, sha)).
		Order("id asc").
		Find(&orders).Error
	return orders, err
}

// ListSHAsReferencedByStatusNotIn 取给定 sha 集合中仍被「状态不在 excluded 集合内的变更单」引用的子集
// （blob 清理阻断判定：命中即不可删；入参为空返回空集）。
func (r *ChangeOrderRepository) ListSHAsReferencedByStatusNotIn(shas []string, excluded []string) (map[string]struct{}, error) {
	blocked := make(map[string]struct{}, len(shas))
	if len(shas) == 0 {
		return blocked, nil
	}
	var rows []string
	err := r.db.Model(&model.ChangeOrderItem{}).
		Distinct().
		Joins("JOIN change_order AS o ON o.id = change_order_item.order_id").
		Where("change_order_item.kind = ? AND change_order_item.sha256 IN ?", model.ChangeItemKindFileDiff, shas).
		Where("o.status NOT IN ?", excluded).
		Pluck("change_order_item.sha256", &rows).Error
	if err != nil {
		return nil, err
	}
	for _, sha := range rows {
		blocked[sha] = struct{}{}
	}
	return blocked, nil
}

// —— from 锚点反查（ADR-0071）——

// FindLatestDeliveredToVersionID 查某 (config_file, scope) 最近一次已 completed 变更单交付的目标版本 id：
// 经 items→config_layer_version 反查同文件同作用域，按单结束时间倒序取首条；从无交付返回 (nil, nil)。
func (r *ChangeOrderRepository) FindLatestDeliveredToVersionID(configFileID uint, scopeKind string, scopeID uint) (*uint, error) {
	var versionIDs []uint
	err := r.db.Table("change_order_item AS i").
		Joins("JOIN change_order AS o ON o.id = i.order_id").
		Joins("JOIN config_layer_version AS v ON v.id = i.config_to_version_id").
		Where("o.status = ?", model.ChangeOrderStatusCompleted).
		Where("i.kind = ? AND i.config_scope_kind = ? AND i.config_scope_id = ?",
			model.ChangeItemKindConfigChange, scopeKind, scopeID).
		Where("v.config_file_id = ?", configFileID).
		Order("o.finished_at DESC, o.id DESC").
		Limit(1).
		Pluck("i.config_to_version_id", &versionIDs).Error
	if err != nil {
		return nil, err
	}
	if len(versionIDs) == 0 {
		return nil, nil
	}
	return &versionIDs[0], nil
}

// —— 配置灰度 (文件, 作用域) 冲突守卫（ADR-0071 决策5）——

// ConfigScopeKey 是一个 config_change 项的「文件 × 作用域」定位键：
// config_file_id 经 config_to_version_id 关联版本表解出（防两单并发灰度同一配置作用域经 head 互泄）。
type ConfigScopeKey struct {
	ConfigFileID uint   `gorm:"column:config_file_id"`
	ScopeKind    string `gorm:"column:scope_kind"`
	ScopeID      uint   `gorm:"column:scope_id"`
}

// configScopeKeyProjection 是 (文件, 作用域) 键投影列（JOIN 版本表把 config_to_version_id 解成 config_file_id）。
const configScopeKeyProjection = "clv.config_file_id AS config_file_id, coi.config_scope_kind AS scope_kind, coi.config_scope_id AS scope_id"

// ListConfigScopeKeysForOrder 取某单全部 config_change 项的 (文件, 作用域) 键集（冲突守卫求本单键）。
func (r *ChangeOrderRepository) ListConfigScopeKeysForOrder(orderID uint) ([]ConfigScopeKey, error) {
	var keys []ConfigScopeKey
	err := r.db.Table("change_order_item AS coi").
		Joins("JOIN config_layer_version AS clv ON clv.id = coi.config_to_version_id").
		Where("coi.order_id = ? AND coi.kind = ?", orderID, model.ChangeItemKindConfigChange).
		Select(configScopeKeyProjection).Scan(&keys).Error
	return keys, err
}

// ListActiveConfigScopeKeys 取同 namespace 内、除 excludeOrderID 外、状态在 statuses 内的活动单的
// config_change (文件, 作用域) 键集（冲突守卫求他单键；statuses 为空返回空集）。
func (r *ChangeOrderRepository) ListActiveConfigScopeKeys(namespaceID, excludeOrderID uint, statuses []string) ([]ConfigScopeKey, error) {
	var keys []ConfigScopeKey
	if len(statuses) == 0 {
		return keys, nil
	}
	err := r.db.Table("change_order_item AS coi").
		Joins("JOIN change_order AS o ON o.id = coi.order_id").
		Joins("JOIN config_layer_version AS clv ON clv.id = coi.config_to_version_id").
		Where("coi.kind = ?", model.ChangeItemKindConfigChange).
		Where("o.namespace_id = ? AND o.id <> ? AND o.status IN ?", namespaceID, excludeOrderID, statuses).
		Select(configScopeKeyProjection).Scan(&keys).Error
	return keys, err
}
