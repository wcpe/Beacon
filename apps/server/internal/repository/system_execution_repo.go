package repository

import (
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"gorm.io/gorm"
)

// SystemExecutionRepository 提供系统执行记录的最小持久化入口。
type SystemExecutionRepository struct{ db *gorm.DB }

func NewSystemExecutionRepository(db *gorm.DB) *SystemExecutionRepository {
	return &SystemExecutionRepository{db: db}
}

func (r *SystemExecutionRepository) WithTx(tx *gorm.DB) *SystemExecutionRepository {
	return &SystemExecutionRepository{db: tx}
}

func (r *SystemExecutionRepository) Create(item *model.SystemExecution) error {
	return r.db.Create(item).Error
}

func (r *SystemExecutionRepository) FindByRequestID(requestID string) (*model.SystemExecution, error) {
	var item model.SystemExecution
	if err := r.db.Where("request_id = ?", requestID).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
