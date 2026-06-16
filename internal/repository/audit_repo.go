package repository

import (
	"time"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// AuditFilter 是审计查询的过滤与分页条件（零值字段不过滤；时间零值不设界）。
type AuditFilter struct {
	Namespace  string
	Action     string
	TargetType string
	TargetRef  string
	From       time.Time
	To         time.Time
	Page       int // 从 1 起
	Size       int
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
