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

// EnterConflictReview 把任务从 fetching CAS 迁移 conflict-review 并暂存 submit 回传内容（FR-59）：
// 仅当当前 status=fetching 才迁移。submitContent 为暂存内容信封 JSON（瞬态，resolve / 取消 / 过期后清空）。
// note 记冲突摘要（无文件内容）。返回是否命中（前态不符 / 不存在则 false）。
func (r *ReverseFetchTaskRepository) EnterConflictReview(id uint, submitContent, note string) (bool, error) {
	res := r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ? AND status = ?", id, model.ReverseFetchTaskFetching).
		Updates(map[string]any{
			"status":         model.ReverseFetchTaskConflictReview,
			"submit_content": submitContent,
			"note":           note,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ClaimConflictReview 把任务从 conflict-review CAS 迁移 ingesting（resolve 认领，防并发双 resolve）：
// 仅当当前 status=conflict-review 才迁移。返回是否命中（被并发认领 / 前态不符则 false）。
func (r *ReverseFetchTaskRepository) ClaimConflictReview(id uint) (bool, error) {
	return r.UpdateStatus(id, model.ReverseFetchTaskConflictReview, model.ReverseFetchTaskIngesting)
}

// SetScanCommandID 回填 scan 命令 id（建任务事务内，命令 Create 后 id 已回填）。
func (r *ReverseFetchTaskRepository) SetScanCommandID(id, scanCommandID uint) error {
	return r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ?", id).Update("scan_command_id", scanCommandID).Error
}

// MarkTerminal 把任务从期望前态 CAS 迁移到终态并置 active 哨兵为真实时间（解除互斥占位）。
// note 为结果 / 失败原因摘要（无敏感内容），空则不动该列。clearTransient=true 时一并清空清单与冲突暂存瞬态
// （过期 / 取消 / 失败），避免大树清单 TEXT 与冲突内容长期滞留。
// 返回是否命中（前态不符 / 已被并发终结则 false）。
func (r *ReverseFetchTaskRepository) MarkTerminal(id uint, expect, terminal, note string, clearTransient bool, now time.Time) (bool, error) {
	updates := map[string]any{"status": terminal, "active_at": now}
	if note != "" {
		updates["note"] = note
	}
	if clearTransient {
		updates["manifest"] = ""
		updates["submit_content"] = ""
	}
	res := r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ? AND status = ?", id, expect).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// MarkFailedWithError 把任务从期望前态 CAS 迁移到 failed、写失败明细 last_error、清空瞬态并解除互斥占位（FR-87）。
// 与 MarkTerminal 平行：MarkTerminal 写 note（结果 / 取消摘要），本方法专写 last_error（失败错因）。
// 返回是否命中（前态不符 / 已被并发终结则 false）。
func (r *ReverseFetchTaskRepository) MarkFailedWithError(id uint, expect, lastError string, now time.Time) (bool, error) {
	res := r.db.Model(&model.ReverseFetchTask{}).
		Where("id = ? AND status = ?", id, expect).
		Updates(map[string]any{
			"status":         model.ReverseFetchTaskFailed,
			"active_at":      now,
			"last_error":     lastError,
			"manifest":       "",
			"submit_content": "",
		})
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
			model.ReverseFetchTaskFetching, model.ReverseFetchTaskConflictReview,
			model.ReverseFetchTaskIngesting,
		}, before).
		Updates(map[string]any{
			"status":         model.ReverseFetchTaskExpired,
			"manifest":       "",
			"submit_content": "",
			"active_at":      now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
