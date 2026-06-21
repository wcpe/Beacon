package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// AgentCommandRepository 提供 agent_command 表的数据访问（FR-39，见 ADR-0027）。
// 命令真源在库：建为 pending，agent 拉取后 CAS 迁移状态，超时由清理标 expired。
type AgentCommandRepository struct {
	db *gorm.DB
}

// NewAgentCommandRepository 构造仓库。
func NewAgentCommandRepository(db *gorm.DB) *AgentCommandRepository {
	return &AgentCommandRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *AgentCommandRepository) WithTx(tx *gorm.DB) *AgentCommandRepository {
	return &AgentCommandRepository{db: tx}
}

// Create 追加一条命令（状态由调用方置 pending）。
func (r *AgentCommandRepository) Create(cmd *model.AgentCommand) error {
	return r.db.Create(cmd).Error
}

// FindByID 按主键查命令；不存在返回 (nil, nil)。
func (r *AgentCommandRepository) FindByID(id uint) (*model.AgentCommand, error) {
	var c model.AgentCommand
	err := r.db.Where("id = ?", id).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// FindOldestPending 取某目标实例最早一条 pending 命令（供 agent 拉取）；无则 (nil, nil)。
func (r *AgentCommandRepository) FindOldestPending(ns, serverID string) (*model.AgentCommand, error) {
	var c model.AgentCommand
	err := r.db.Where("namespace = ? AND server_id = ? AND status = ?", ns, serverID, model.CommandStatusPending).
		Order("id asc").First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateStatus 按期望前态做状态迁移（CAS，幂等）：仅当当前 status=expect 才改为 next。
// 返回是否命中（前态不符 / 不存在则 false）。result 为结果摘要（无敏感内容），空则不动该列。
func (r *AgentCommandRepository) UpdateStatus(id uint, expect, next, result string) (bool, error) {
	updates := map[string]any{"status": next}
	if result != "" {
		updates["result_detail"] = result
	}
	res := r.db.Model(&model.AgentCommand{}).
		Where("id = ? AND status = ?", id, expect).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ExpireStale 把创建早于 before、仍处 pending/fetched 的命令标 expired（超时清理）；返回受影响条数。
func (r *AgentCommandRepository) ExpireStale(before time.Time) (int64, error) {
	res := r.db.Model(&model.AgentCommand{}).
		Where("status IN ? AND created_at < ?",
			[]string{model.CommandStatusPending, model.CommandStatusFetched}, before).
		Update("status", model.CommandStatusExpired)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
