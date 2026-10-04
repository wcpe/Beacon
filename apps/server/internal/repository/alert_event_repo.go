package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// AlertEventFilter 是告警事件查询的过滤与分页条件（零值字段不过滤；时间零值不设界）。
type AlertEventFilter struct {
	Type      string
	Level     string
	Namespace string
	// Status 非空时按处理状态过滤（open / acknowledged / resolved）。
	// 真机验收发现此前缺此字段，导致 `?status=open` 被静默忽略、列表把已处理条目一并返回。
	Status string
	// ServerID 非空时按涉及实例过滤（FR-230 详情时间线）。
	ServerID       string
	NamespaceCodes []string
	Scoped         bool
	From           time.Time
	To             time.Time
	Page           int // 从 1 起
	Size           int
}

// AlertEventRepository 提供 alert_event 表的数据访问（append-only 留痕，FR-89）。
type AlertEventRepository struct {
	db *gorm.DB
}

// NewAlertEventRepository 构造仓库。
func NewAlertEventRepository(db *gorm.DB) *AlertEventRepository {
	return &AlertEventRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供处理工作流在事务内与审计原子落库，FR-157）。
func (r *AlertEventRepository) WithTx(tx *gorm.DB) *AlertEventRepository {
	return &AlertEventRepository{db: tx}
}

// Create 追加一条告警事件。
func (r *AlertEventRepository) Create(e *model.AlertEvent) error {
	return r.db.Create(e).Error
}

// Get 按主键取单条告警事件；不存在返回 gorm.ErrRecordNotFound，由 service 转领域错误。
func (r *AlertEventRepository) Get(id uint) (*model.AlertEvent, error) {
	var e model.AlertEvent
	if err := r.db.First(&e, id).Error; err != nil {
		return nil, err
	}
	return &e, nil
}

// Save 覆盖保存一条告警事件（处理工作流更新 status / handled_* 用）。
func (r *AlertEventRepository) Save(e *model.AlertEvent) error {
	return r.db.Save(e).Error
}

// AlertActiveCount 是按实例聚合的活跃（open）告警计数行（FR-157，健康 activeAlerts 因子输入）。
type AlertActiveCount struct {
	Namespace string
	ServerID  string
	Count     int
}

// ActiveCounts 一次性批量统计各实例当前 open 告警数（namespace + serverId → 计数）。
// 单条分组查询（标准 GROUP BY，无方言函数），供健康计算轮每轮取一次——严禁逐实例循环查库。
func (r *AlertEventRepository) ActiveCounts() ([]AlertActiveCount, error) {
	var rows []AlertActiveCount
	if err := r.db.Model(&model.AlertEvent{}).
		Select("namespace, server_id, COUNT(*) AS count").
		Where("status = ?", model.AlertEventStatusOpen).
		Group("namespace, server_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// applyAlertEventFilter 把过滤条件叠加到查询上（仅占位符 + 标准 SQL，不依赖方言函数，保 Postgres 可移植）。
// Scoped（观测范围）与 Namespace（精确 namespace）**可叠加**：Scoped 施加 `namespace IN codes`，
// Namespace 非空再 AND 上 `namespace = ?`——供 FR-230 详情在受限范围内进一步锁定该告警所属 namespace。
func applyAlertEventFilter(q *gorm.DB, f AlertEventFilter) *gorm.DB {
	if f.Type != "" {
		q = q.Where("type = ?", f.Type)
	}
	if f.Level != "" {
		// 级别筛选取「生效级别」（FR-231）：人工改级（severity_override 非空）优先于自动分级列，
		// 否则改级后按级别筛选 / 排序不会跟随，与验收要求「人工改级后排序 / 筛选随之变化」不符。
		// COALESCE/NULLIF 为标准 SQL，MySQL / SQLite / Postgres 均支持，保持方言无关。
		q = q.Where("COALESCE(NULLIF(severity_override, ''), level) = ?", f.Level)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.ServerID != "" {
		q = q.Where("server_id = ?", f.ServerID)
	}
	if f.Scoped {
		if len(f.NamespaceCodes) == 0 {
			return q.Where("1 = 0")
		}
		q = q.Where("namespace IN ?", f.NamespaceCodes)
	}
	if f.Namespace != "" {
		q = q.Where("namespace = ?", f.Namespace)
	}
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("created_at <= ?", f.To)
	}
	return q
}

// List 按过滤条件分页查询告警事件（时间倒序），返回当页记录与总数。
func (r *AlertEventRepository) List(f AlertEventFilter) ([]model.AlertEvent, int64, error) {
	q := applyAlertEventFilter(r.db.Model(&model.AlertEvent{}), f)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.AlertEvent
	if err := q.Order("created_at desc, id desc").
		Limit(f.Size).Offset((f.Page - 1) * f.Size).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// HandleBatch 按过滤条件批量处理「未处理（open）」告警（FR-229）：一条 UPDATE，仅影响 status='open' 的行，
// 返回受影响行数。已非 open 的行不变，故重复执行幂等。仅标准 SQL + 占位符，保 Postgres 可移植。
func (r *AlertEventRepository) HandleBatch(f AlertEventFilter, status, handledBy, note string, now time.Time) (int64, error) {
	q := applyAlertEventFilter(r.db.Model(&model.AlertEvent{}), f)
	res := q.Where("status = ?", model.AlertEventStatusOpen).Updates(map[string]any{
		"status":      status,
		"handled_by":  handledBy,
		"handled_at":  now,
		"handle_note": note,
	})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// FindUnresolvedByDedupKey 按收敛键 (namespace, server_id, type) 查最近一条「未恢复」（status != resolved）告警（FR-232）。
// 命中则走合并计数；未命中（含已 resolved 的旧行）→ 视为该键当前无未恢复行，由调用方插新行。
// **收敛键刻意不含 to_status**：同一实例的一次健康恶化链（degraded → lost → offline）只留 1 行，
// 中间阶段不再各开一行；行内 to_status 由 service.mergeOccurrence 按「只升不降」刷新为最严重态。
// 该查询由复合索引 idx_alert_event_dedup_v2 = (server_id, namespace, type) 覆盖。
func (r *AlertEventRepository) FindUnresolvedByDedupKey(namespace, serverID, typ string) (*model.AlertEvent, error) {
	var e model.AlertEvent
	err := r.db.
		Where("namespace = ? AND server_id = ? AND type = ? AND status <> ?",
			namespace, serverID, typ, model.AlertEventStatusResolved).
		Order("id DESC").First(&e).Error
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// AutoResolveByServer 把某实例的**全部**未恢复告警批量置为 resolved（FR-232 实例恢复 / 主动下线 / 归档自动消解）：
// 一条 UPDATE，仅影响 status != resolved 的行；handled_by 记 system、note 标明自动消解，使 UI 可区分人机处理。返回受影响行数。
//
// 「含 acknowledged 行」是这些触发点的**有意口径**：它们都以「该实例的告警已无出路」为前提（实例恢复 online
// 意味着触发条件已消失；主动下线 / 归档 / 永久删除意味着实例已由运维处置），此时把人工确认过的行一并关闭
// 是期望行为。与此相对，外部消失这类**没有「事件已结束」事实**的触发点走 AutoResolveOpenByServer（只消解 open）。
func (r *AlertEventRepository) AutoResolveByServer(namespace, serverID string, now time.Time, note string) (int64, error) {
	return r.autoResolveByServerWhere("namespace = ? AND server_id = ? AND status <> ?",
		[]any{namespace, serverID, model.AlertEventStatusResolved}, now, note)
}

// AutoResolveOpenByServer 把某实例**未处理（open）**的告警批量置为 resolved：
// 一条 UPDATE，仅影响 status = open 的行；handled_by 记 system、note 标明自动消解。返回受影响行数。
//
// 与 AutoResolveByServer 的唯一差别是**不碰 acknowledged 行**：那些行带人工写下的 handled_by / handle_note
// （“谁在跟、跟到哪”），而自动消解会把两列覆盖成 system + 固定文案，且本表无历史表、自动消解不逐条写审计，
// 覆盖后无法再从行上区分「人工已处理」与「系统自动消解」——正是人机留痕设计想区分的事。
// 孤儿清理器（实例**外部消失**，没有「事件已结束」的事实，只有「实例不见了」）因此取本方法：
// 宁可让运维确认过的行继续留在待办里等人处理，也不悄悄抹掉他的处置痕迹（与调用方清理器判据 3
// 「在册实例的告警绝不能被自动关闭、运维必须看到」同一取舍方向）。
func (r *AlertEventRepository) AutoResolveOpenByServer(namespace, serverID string, now time.Time, note string) (int64, error) {
	return r.autoResolveByServerWhere("namespace = ? AND server_id = ? AND status = ?",
		[]any{namespace, serverID, model.AlertEventStatusOpen}, now, note)
}

// autoResolveByServerWhere 是实例级自动消解的**唯一写点**：把满足 cond 的行置 resolved + handled_by=system
// + 固定 note（一条 UPDATE、幂等），返回受影响行数。只接收代码内常量条件与占位符，不接受外部拼串。
func (r *AlertEventRepository) autoResolveByServerWhere(cond string, args []any, now time.Time, note string) (int64, error) {
	res := r.db.Model(&model.AlertEvent{}).
		Where(cond, args...).
		Updates(map[string]any{
			"status":      model.AlertEventStatusResolved,
			"handled_by":  model.AutoResolveOperator,
			"handled_at":  now,
			"handle_note": note,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// AutoResolveByNamespace 把某环境（namespace code）下**全部**实例的未恢复告警批量置为 resolved（FR-232 环境归档 / 永久删除自动消解）：
// 一条 UPDATE，仅影响 status != resolved 的行；handled_by 记 system、note 标明自动消解，使 UI 可区分人机处理。返回受影响行数。
// namespace 入参是 namespace **code**（与 alert_event.namespace 列同源），不是主键 id。
func (r *AlertEventRepository) AutoResolveByNamespace(namespace string, now time.Time, note string) (int64, error) {
	res := r.db.Model(&model.AlertEvent{}).
		Where("namespace = ? AND status <> ?", namespace, model.AlertEventStatusResolved).
		Updates(map[string]any{
			"status":      model.AlertEventStatusResolved,
			"handled_by":  model.AutoResolveOperator,
			"handled_at":  now,
			"handle_note": note,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
