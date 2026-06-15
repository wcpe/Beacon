package repository

import (
	"gorm.io/gorm"

	"beacon/internal/model"
)

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
