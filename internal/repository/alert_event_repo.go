package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// AlertEventFilter 是告警事件查询的过滤与分页条件（零值字段不过滤；时间零值不设界）。
type AlertEventFilter struct {
	Type      string
	Level     string
	Namespace string
	From      time.Time
	To        time.Time
	Page      int // 从 1 起
	Size      int
}

// AlertEventRepository 提供 alert_event 表的数据访问（append-only 留痕，FR-89）。
type AlertEventRepository struct {
	db *gorm.DB
}

// NewAlertEventRepository 构造仓库。
func NewAlertEventRepository(db *gorm.DB) *AlertEventRepository {
	return &AlertEventRepository{db: db}
}

// Create 追加一条告警事件。
func (r *AlertEventRepository) Create(e *model.AlertEvent) error {
	return r.db.Create(e).Error
}

// applyAlertEventFilter 把过滤条件叠加到查询上（仅占位符 + 标准 SQL，不依赖方言函数，保 Postgres 可移植）。
func applyAlertEventFilter(q *gorm.DB, f AlertEventFilter) *gorm.DB {
	if f.Type != "" {
		q = q.Where("type = ?", f.Type)
	}
	if f.Level != "" {
		q = q.Where("level = ?", f.Level)
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
