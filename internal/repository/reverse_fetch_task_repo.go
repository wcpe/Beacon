package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// ReverseFetchTaskRepository 提供 reverse_fetch_task 表的数据访问（FR-58，见 ADR-0037）。
// 任务真源在库：建为 scanning，按状态机 CAS 迁移；非终态受单实例互斥（active 哨兵唯一键）约束。
type ReverseFetchTaskRepository struct {
	db *gorm.DB
}

// NewReverseFetchTaskRepository 构造仓库。
func NewReverseFetchTaskRepository(db *gorm.DB) *ReverseFetchTaskRepository {
	return &ReverseFetchTaskRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ReverseFetchTaskRepository) WithTx(tx *gorm.DB) *ReverseFetchTaskRepository {
	return &ReverseFetchTaskRepository{db: tx}
}

// Create 追加一条任务（状态由调用方置 scanning）。
func (r *ReverseFetchTaskRepository) Create(t *model.ReverseFetchTask) error {
	return r.db.Create(t).Error
}

// GetByID 按主键查任务；不存在返回 (nil, nil)。
func (r *ReverseFetchTaskRepository) GetByID(id uint) (*model.ReverseFetchTask, error) {
	var t model.ReverseFetchTask
	err := r.db.Where("id = ?", id).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// FindActiveByServer 查某 (ns, serverId) 当前的非终态任务（互斥用）；无则 (nil, nil)。
// 以 active 哨兵命中：未终结任务 active_at = 哨兵值，至多一条。
func (r *ReverseFetchTaskRepository) FindActiveByServer(ns, serverID string) (*model.ReverseFetchTask, error) {
	var t model.ReverseFetchTask
	err := r.db.Where("namespace_code = ? AND server_id = ? AND active_at = ?",
		ns, serverID, model.SoftDeleteSentinel).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// List 列出任务（ns / serverId / status 任一为空则不过滤），按 id 倒序（最新在前，供任务台）。
func (r *ReverseFetchTaskRepository) List(ns, serverID, status string) ([]model.ReverseFetchTask, error) {
	q := r.db.Model(&model.ReverseFetchTask{})
	if ns != "" {
		q = q.Where("namespace_code = ?", ns)
	}
	if serverID != "" {
		q = q.Where("server_id = ?", serverID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var list []model.ReverseFetchTask
	if err := q.Order("id desc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// UpdateStatus 按期望前态做非终态间的状态迁移（CAS）：仅当当前 status=expect 才改为 next。
// 仅用于非终态 → 非终态（如 scanning→pending-review、fetching→ingesting），不动 active 哨兵。
// 返回是否命中（前态不符 / 不存在则 false）。
func (r *ReverseFetchTaskRepository) UpdateStatus(id uint, expect, next string) (bool, error) {
	res := r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ? AND status = ?", id, expect).
		Update("status", next)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SaveManifest 存扫描清单 + 计数并把任务从 scanning CAS 迁移 pending-review（scan 回传落库）。
// 返回是否命中（前态不符 / 不存在则 false）。
func (r *ReverseFetchTaskRepository) SaveManifest(id uint, manifest string, totalFiles, overThreshold, skipped int) (bool, error) {
	res := r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ? AND status = ?", id, model.ReverseFetchTaskScanning).
		Updates(map[string]any{
			"status":               model.ReverseFetchTaskPendingReview,
			"manifest":             manifest,
			"total_files":          totalFiles,
			"over_threshold_count": overThreshold,
			"skipped_count":        skipped,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SaveSelected 存选定 path 集 + 计数 + submit 命令 id 并把任务从 pending-review CAS 迁移 fetching（提交）。
// 返回是否命中。
func (r *ReverseFetchTaskRepository) SaveSelected(id uint, selectedPaths string, selectedCount int, submitCommandID uint) (bool, error) {
	res := r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ? AND status = ?", id, model.ReverseFetchTaskPendingReview).
		Updates(map[string]any{
			"status":            model.ReverseFetchTaskFetching,
			"selected_paths":    selectedPaths,
			"selected_count":    selectedCount,
			"submit_command_id": submitCommandID,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SetScanCommandID 回填 scan 命令 id（建任务事务内，命令 Create 后 id 已回填）。
func (r *ReverseFetchTaskRepository) SetScanCommandID(id, scanCommandID uint) error {
	return r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ?", id).Update("scan_command_id", scanCommandID).Error
}

// MarkTerminal 把任务从期望前态 CAS 迁移到终态并置 active 哨兵为真实时间（解除互斥占位）。
// note 为结果 / 失败原因摘要（无敏感内容），空则不动该列。clearManifest=true 时一并清空清单瞬态（过期 / 取消）。
// 返回是否命中（前态不符 / 已被并发终结则 false）。
func (r *ReverseFetchTaskRepository) MarkTerminal(id uint, expect, terminal, note string, clearManifest bool, now time.Time) (bool, error) {
	updates := map[string]any{"status": terminal, "active_at": now}
	if note != "" {
		updates["note"] = note
	}
	if clearManifest {
		updates["manifest"] = ""
	}
	res := r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ? AND status = ?", id, expect).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ExpireStale 把创建早于 before、仍处非终态的任务标 expired、清空清单瞬态并置 active 哨兵为 now（解除互斥）。
// 返回受影响条数。GORM 不支持跨行用列值赋 active_at，故统一用 now（陈旧任务批量终结，时间足够区分历史并存）。
func (r *ReverseFetchTaskRepository) ExpireStale(before, now time.Time) (int64, error) {
	res := r.db.Model(&model.ReverseFetchTask{}).
		Where("status IN ? AND created_at < ?", []string{
			model.ReverseFetchTaskScanning, model.ReverseFetchTaskPendingReview,
			model.ReverseFetchTaskFetching, model.ReverseFetchTaskIngesting,
		}, before).
		Updates(map[string]any{
			"status":    model.ReverseFetchTaskExpired,
			"manifest":  "",
			"active_at": now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
