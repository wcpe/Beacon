package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// AuditFilter 是审计查询的过滤与分页条件（零值字段不过滤；时间零值不设界）。
type AuditFilter struct {
	Namespace  string
	Operator   string
	Action     string
	TargetType string
	TargetRef  string
	From       time.Time
	To         time.Time
	Page       int // 从 1 起
	Size       int
}

// AuditAnalyticsRow 是审计聚合用的窗口内投影行（仅取聚合所需三列，FR-73）。
type AuditAnalyticsRow struct {
	CreatedAt time.Time
	Result    string
	Action    string
}

// AuditLogRepository 提供 audit_log 表的数据访问（append-only）。
type AuditLogRepository struct {
	db *gorm.DB
}

// NewAuditLogRepository 构造仓库。
func NewAuditLogRepository(db *gorm.DB) *AuditLogRepository {
	return &AuditLogRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *AuditLogRepository) WithTx(tx *gorm.DB) *AuditLogRepository {
	return &AuditLogRepository{db: tx}
}

// Create 追加一条审计记录。
func (r *AuditLogRepository) Create(entry *model.AuditLog) error {
	return r.db.Create(entry).Error
}

// List 按过滤条件分页查询审计（时间倒序），返回当页记录与总数。
func (r *AuditLogRepository) List(f AuditFilter) ([]model.AuditLog, int64, error) {
	q := r.db.Model(&model.AuditLog{})
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if f.Operator != "" {
		q = q.Where("operator = ?", f.Operator)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.TargetType != "" {
		q = q.Where("target_type = ?", f.TargetType)
	}
	if f.TargetRef != "" {
		q = q.Where("target_ref = ?", f.TargetRef)
	}
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("created_at <= ?", f.To)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.AuditLog
	if err := q.Order("created_at desc, id desc").
		Limit(f.Size).Offset((f.Page - 1) * f.Size).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ScanForAnalytics 取窗口内审计的聚合投影行（仅 created_at/result/action 三列、按时间升序）。
// 只复用 Namespace/From/To 过滤，日分桶与计数交由 service 在 Go 侧做（禁方言日期函数，保可移植）。
func (r *AuditLogRepository) ScanForAnalytics(f AuditFilter) ([]AuditAnalyticsRow, error) {
	q := r.db.Model(&model.AuditLog{})
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("created_at <= ?", f.To)
	}
	var rows []AuditAnalyticsRow
	if err := q.Select("created_at", "result", "action").
		Order("created_at asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
