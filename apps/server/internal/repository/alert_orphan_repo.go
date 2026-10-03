package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// AlertOrphanCandidate 是一台「当前尚有未处理告警」的实例候选（按 namespace + serverId 聚合一行）。
// Namespace 是 namespace **code**（与 alert_event.namespace 列同源），LastAt 是该实例未处理告警的最近触发时刻
// （历史行的 last_at 为空时用 created_at 兜底）。
type AlertOrphanCandidate struct {
	Namespace string
	ServerID  string
	LastAt    time.Time
}

// AlertServerKey 是「在册实例」的唯一键（namespace code + serverId），与 AlertOrphanCandidate 同口径。
type AlertServerKey struct {
	Namespace string
	ServerID  string
}

// alertUnresolvedRow 是一条未处理告警的归属与时间（行级读取用；时间列取真实列，驱动可原生扫描为 time.Time）。
type alertUnresolvedRow struct {
	Namespace string
	ServerID  string
	LastAt    *time.Time
	CreatedAt time.Time
}

// AlertOrphanRepository 提供「失联且不在受管目录」告警清理所需的查询。
// 单独成文件（而非并入 alert_event_repo.go）以免与并行的告警收敛改动抢同一文件；
// 查询全部是集合 / 聚合形态，严禁逐实例循环查库（N+1）。
type AlertOrphanRepository struct {
	db *gorm.DB
}

// NewAlertOrphanRepository 构造仓库。
func NewAlertOrphanRepository(db *gorm.DB) *AlertOrphanRepository {
	return &AlertOrphanRepository{db: db}
}

// ListUnresolvedByServer 列出所有当前存在未处理（status <> resolved）告警的 (namespace, server_id) 及其最近触发时间。
// 单次 DB 往返取全部未处理行、再在应用层按实例聚合取 MAX(COALESCE(last_at, created_at))：**无 N+1**，
// 且未处理行数被 FR-232 收敛（同键只留一行）压住，每次扫描的传输量可控。
//
// 为什么不是 SQL 的 `MAX(COALESCE(...)) GROUP BY`：聚合**表达式列**没有声明类型，SQLite 驱动会把时间聚合结果
// 当字符串返回（实测 `unsupported Scan, storing driver.Value type string into type *time.Time`），而
// MySQL / Postgres 返回时间类型——为绕开这层方言差异、同时保住 GROUP BY 形式取不回的「最近触发时刻」，
// 改用行级查询：last_at / created_at 都是声明了类型的真实列，各驱动都能原生扫描成 time.Time（守 DB 可移植）。
//
// server_id 为空的行（非实例维度事件，如集群级告警）不返回——它们不代表任何实例失联，不属本清理器职责。
func (r *AlertOrphanRepository) ListUnresolvedByServer() ([]AlertOrphanCandidate, error) {
	var rows []alertUnresolvedRow
	if err := r.db.Model(&model.AlertEvent{}).
		Select("namespace, server_id, last_at, created_at").
		Where("status <> ?", model.AlertEventStatusResolved).
		Where("server_id <> ''").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	// 应用层按实例聚合 MAX(COALESCE(last_at, created_at))：与 SQL 聚合同语义，但时间值来源是真实列。
	latest := make(map[AlertServerKey]time.Time, len(rows))
	for _, row := range rows {
		at := row.CreatedAt
		if row.LastAt != nil {
			at = *row.LastAt
		}
		key := AlertServerKey{Namespace: row.Namespace, ServerID: row.ServerID}
		if prev, ok := latest[key]; !ok || at.After(prev) {
			latest[key] = at
		}
	}
	out := make([]AlertOrphanCandidate, 0, len(latest))
	for key, at := range latest {
		out = append(out, AlertOrphanCandidate{Namespace: key.Namespace, ServerID: key.ServerID, LastAt: at})
	}
	return out, nil
}

// ActiveServerKeys 返回 server 表中仍处 active 的**在册**实例键集合（一次集合查询，无 N+1）。
// 这是本清理器安全红线的数据源：在册（active）实例即使离线很久也绝不能被自动关闭告警——运维必须看到；
// archived / tombstoned 行不算在册，其告警已由既有的生命周期自动消解覆盖（见 server_lifecycle.go）。
// namespace 经 join 取 code，与 alert_event.namespace 列同口径。
func (r *AlertOrphanRepository) ActiveServerKeys() (map[AlertServerKey]struct{}, error) {
	var rows []AlertServerKey
	if err := r.db.Model(&model.Server{}).
		Select("namespace.code AS namespace, server.server_id AS server_id").
		Joins("JOIN namespace ON namespace.id = server.namespace_id").
		Where("server.lifecycle = ?", model.ServerLifecycleActive).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	keys := make(map[AlertServerKey]struct{}, len(rows))
	for _, row := range rows {
		keys[row] = struct{}{}
	}
	return keys, nil
}
