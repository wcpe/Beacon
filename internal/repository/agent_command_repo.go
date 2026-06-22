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

// UpdateImprintReady 把命令从 fetched CAS 迁移 ready 并转存拓印内容（FR-46）：仅当当前 status=fetched 才迁移。
// content 即目标文件磁盘原文（瞬态，待确认 / 失败 / 过期清空）。返回是否命中（前态不符 / 不存在则 false）。
func (r *AgentCommandRepository) UpdateImprintReady(id uint, content string) (bool, error) {
	res := r.db.Model(&model.AgentCommand{}).
		Where("id = ? AND status = ?", id, model.CommandStatusFetched).
		Updates(map[string]any{"status": model.CommandStatusReady, "imprint_content": content})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateStatusClearImprint 按期望前态做状态迁移并清空拓印瞬态内容（FR-46 确认落库后）：仅当 status=expect 才迁移。
// result 为结果摘要（无敏感内容），空则不动该列。返回是否命中。
func (r *AgentCommandRepository) UpdateStatusClearImprint(id uint, expect, next, result string) (bool, error) {
	updates := map[string]any{"status": next, "imprint_content": ""}
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

// ExpireStale 把创建早于 before、仍处 pending/fetched/ready 的命令标 expired（超时清理）；返回受影响条数。
// ready（FR-46 拓印已抓取未确认）一并过期并清空瞬态拓印内容，避免未确认的磁盘原文长期滞留。
func (r *AgentCommandRepository) ExpireStale(before time.Time) (int64, error) {
	res := r.db.Model(&model.AgentCommand{}).
		Where("status IN ? AND created_at < ?",
			[]string{model.CommandStatusPending, model.CommandStatusFetched, model.CommandStatusReady}, before).
		Updates(map[string]any{"status": model.CommandStatusExpired, "imprint_content": ""})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
