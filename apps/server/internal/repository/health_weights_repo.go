package repository

import (
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// HealthWeightsRepository 提供健康权重版本表 health_weights_rev 的数据访问（FR-147，见 §3.3）。
// 版本只增不改不删：Insert 在调用方事务内以 max(rev)+1 指派新号，主键唯一约束兜底并发重复。
type HealthWeightsRepository struct {
	db *gorm.DB
}

// NewHealthWeightsRepository 构造仓库。
func NewHealthWeightsRepository(db *gorm.DB) *HealthWeightsRepository {
	return &HealthWeightsRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在外层事务内复用，避免嵌套开事务死锁）。
func (r *HealthWeightsRepository) WithTx(tx *gorm.DB) *HealthWeightsRepository {
	return &HealthWeightsRepository{db: tx}
}

// Latest 取最新（rev 最大）一行；表空返回 (nil, nil)。
func (r *HealthWeightsRepository) Latest() (*model.HealthWeightsRev, error) {
	var rows []model.HealthWeightsRev
	if err := r.db.Order("rev DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// ListAll 按 rev 升序列出全部历史版本（供 GET 历史列表；版本数 = 人工修改次数，量级小无需分页）。
func (r *HealthWeightsRepository) ListAll() ([]model.HealthWeightsRev, error) {
	var rows []model.HealthWeightsRev
	if err := r.db.Order("rev ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// InsertNext 以当前 max(rev)+1 指派新版本号插入一行，返回落库行（含新 rev）。
// 须在调用方事务内执行（经 WithTx）：取号与插入同事务，主键约束防并发重号。
func (r *HealthWeightsRepository) InsertNext(config, operator string) (*model.HealthWeightsRev, error) {
	latest, err := r.Latest()
	if err != nil {
		return nil, err
	}
	next := 1
	if latest != nil {
		next = latest.Rev + 1
	}
	row := model.HealthWeightsRev{Rev: next, Config: config, Operator: operator}
	if err := r.db.Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}
