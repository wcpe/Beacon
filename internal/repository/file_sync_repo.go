package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// FileSyncRepository 提供文件同步任务相关表的数据访问（FR-129/FR-131）。
type FileSyncRepository struct {
	db *gorm.DB
}

// NewFileSyncRepository 构造仓库。
func NewFileSyncRepository(db *gorm.DB) *FileSyncRepository {
	return &FileSyncRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *FileSyncRepository) WithTx(tx *gorm.DB) *FileSyncRepository {
	return &FileSyncRepository{db: tx}
}

// CreateTask 新建任务。
func (r *FileSyncRepository) CreateTask(task *model.FileSyncTask) error {
	return r.db.Create(task).Error
}

// GetTask 按 id 查任务；不存在返回 (nil, nil)。
func (r *FileSyncRepository) GetTask(id uint) (*model.FileSyncTask, error) {
	var task model.FileSyncTask
	err := r.db.Where("id = ?", id).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// ListTasks 按可选 namespace/status 查询任务，最新在前。
func (r *FileSyncRepository) ListTasks(namespace, status string) ([]model.FileSyncTask, error) {
	q := r.db.Model(&model.FileSyncTask{})
	if namespace != "" {
		q = q.Where("namespace_code = ?", namespace)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var tasks []model.FileSyncTask
	if err := q.Order("id desc").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// SetSourceCommandID 记录源扫描命令 id。
func (r *FileSyncRepository) SetSourceCommandID(taskID, commandID uint) error {
	return r.db.Model(&model.FileSyncTask{}).Where("id = ?", taskID).
		Update("source_command_id", commandID).Error
}

// MarkSourceReady 写入源清单统计并标记源缓存已就绪。
func (r *FileSyncRepository) MarkSourceReady(taskID uint, fileCount int, totalBytes int64) error {
	updates := map[string]any{
		"source_ready":       true,
		"source_file_count":  fileCount,
		"source_total_bytes": totalBytes,
	}
	if err := r.db.Model(&model.FileSyncTask{}).Where("id = ? AND status = ?", taskID, model.FileSyncTaskStatusScanning).
		Updates(withTaskStatus(updates, model.FileSyncTaskStatusCached)).Error; err != nil {
		return err
	}
	return r.db.Model(&model.FileSyncTask{}).Where("id = ? AND status <> ?", taskID, model.FileSyncTaskStatusScanning).
		Updates(updates).Error
}

// UpdateTaskPlan 写入规划统计并把任务置为 planned。
func (r *FileSyncRepository) UpdateTaskPlan(taskID uint, targetCount, batchCount int) (bool, error) {
	res := r.db.Model(&model.FileSyncTask{}).
		Where("id = ? AND status IN ?", taskID, []string{
			model.FileSyncTaskStatusDraft, model.FileSyncTaskStatusScanning,
			model.FileSyncTaskStatusCached, model.FileSyncTaskStatusPlanned,
		}).
		Updates(map[string]any{
			"status":       model.FileSyncTaskStatusPlanned,
			"target_count": targetCount,
			"batch_count":  batchCount,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func withTaskStatus(updates map[string]any, status string) map[string]any {
	copyMap := make(map[string]any, len(updates)+1)
	for k, v := range updates {
		copyMap[k] = v
	}
	copyMap["status"] = status
	return copyMap
}

// UpdateTaskStatusCAS 按允许前态迁移任务状态。
func (r *FileSyncRepository) UpdateTaskStatusCAS(taskID uint, from []string, to string, startedAt, finishedAt *time.Time) (bool, error) {
	updates := map[string]any{"status": to}
	if startedAt != nil {
		updates["started_at"] = startedAt
	}
	if finishedAt != nil {
		updates["finished_at"] = finishedAt
	}
	res := r.db.Model(&model.FileSyncTask{}).
		Where("id = ? AND status IN ?", taskID, from).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ClearPlan 删除任务的旧批次和目标，用于重新规划 draft/planned 任务。
func (r *FileSyncRepository) ClearPlan(taskID uint) error {
	if err := r.db.Where("task_id = ?", taskID).Delete(&model.FileSyncTarget{}).Error; err != nil {
		return err
	}
	return r.db.Where("task_id = ?", taskID).Delete(&model.FileSyncBatch{}).Error
}

// ReplaceFiles 原子替换某任务的源文件清单。
func (r *FileSyncRepository) ReplaceFiles(taskID uint, files []model.FileSyncFile) error {
	if err := r.db.Where("task_id = ?", taskID).Delete(&model.FileSyncFile{}).Error; err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	return r.db.Create(&files).Error
}

// CountFiles 统计任务源清单文件数。
func (r *FileSyncRepository) CountFiles(taskID uint) (int64, error) {
	var n int64
	err := r.db.Model(&model.FileSyncFile{}).Where("task_id = ?", taskID).Count(&n).Error
	return n, err
}

// ListFiles 查询源清单，按 path 升序。
func (r *FileSyncRepository) ListFiles(taskID uint) ([]model.FileSyncFile, error) {
	var files []model.FileSyncFile
	if err := r.db.Where("task_id = ?", taskID).Order("path asc").Find(&files).Error; err != nil {
		return nil, err
	}
	return files, nil
}

// CreateBatch 新建批次。
func (r *FileSyncRepository) CreateBatch(batch *model.FileSyncBatch) error {
	return r.db.Create(batch).Error
}

// CreateTargets 批量新建目标。
func (r *FileSyncRepository) CreateTargets(targets []model.FileSyncTarget) error {
	if len(targets) == 0 {
		return nil
	}
	return r.db.Create(&targets).Error
}

// ListBatches 查询任务批次，按 batch_no 升序。
func (r *FileSyncRepository) ListBatches(taskID uint) ([]model.FileSyncBatch, error) {
	var batches []model.FileSyncBatch
	if err := r.db.Where("task_id = ?", taskID).Order("batch_no asc").Find(&batches).Error; err != nil {
		return nil, err
	}
	return batches, nil
}

// UpdateBatchStatus 更新批次状态与可选时间。
func (r *FileSyncRepository) UpdateBatchStatus(batchID uint, status string, startedAt, finishedAt *time.Time) error {
	updates := map[string]any{"status": status}
	if startedAt != nil {
		updates["started_at"] = startedAt
	}
	if finishedAt != nil {
		updates["finished_at"] = finishedAt
	}
	return r.db.Model(&model.FileSyncBatch{}).Where("id = ?", batchID).Updates(updates).Error
}

// UpdateBatchCounts 写回批次成功 / 失败统计。
func (r *FileSyncRepository) UpdateBatchCounts(batchID uint, success, failed int) error {
	return r.db.Model(&model.FileSyncBatch{}).Where("id = ?", batchID).
		Updates(map[string]any{"success_count": success, "failed_count": failed}).Error
}

// ListTargets 查询任务目标，按批次与 id 升序。
func (r *FileSyncRepository) ListTargets(taskID uint) ([]model.FileSyncTarget, error) {
	var targets []model.FileSyncTarget
	if err := r.db.Where("task_id = ?", taskID).Order("batch_no asc, id asc").Find(&targets).Error; err != nil {
		return nil, err
	}
	return targets, nil
}

// ListTargetsByBatch 查询批次目标。
func (r *FileSyncRepository) ListTargetsByBatch(batchID uint) ([]model.FileSyncTarget, error) {
	var targets []model.FileSyncTarget
	if err := r.db.Where("batch_id = ?", batchID).Order("id asc").Find(&targets).Error; err != nil {
		return nil, err
	}
	return targets, nil
}

// GetTarget 按 id 查询目标。
func (r *FileSyncRepository) GetTarget(id uint) (*model.FileSyncTarget, error) {
	var target model.FileSyncTarget
	err := r.db.Where("id = ?", id).First(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &target, nil
}

// MarkTargetDispatched 把 pending 目标迁移为已下发状态。
func (r *FileSyncRepository) MarkTargetDispatched(id, commandID uint, now time.Time) (bool, error) {
	res := r.db.Model(&model.FileSyncTarget{}).
		Where("id = ? AND status = ?", id, model.FileSyncTargetStatusPending).
		Updates(map[string]any{
			"status":     model.FileSyncTargetStatusManifesting,
			"command_id": commandID,
			"started_at": now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// FinishTarget 写入目标终态。
func (r *FileSyncRepository) FinishTarget(id uint, status, backupPath string, current, changed, skipped int,
	bytesTotal, bytesDone int64, lastError string, finishedAt time.Time) (bool, error) {
	res := r.db.Model(&model.FileSyncTarget{}).
		Where("id = ? AND status NOT IN ?", id, []string{
			model.FileSyncTargetStatusSucceeded, model.FileSyncTargetStatusFailed, model.FileSyncTargetStatusSkipped,
		}).
		Updates(map[string]any{
			"status":             status,
			"backup_path":        backupPath,
			"current_file_count": current,
			"changed_file_count": changed,
			"skipped_file_count": skipped,
			"bytes_total":        bytesTotal,
			"bytes_done":         bytesDone,
			"last_error":         lastError,
			"finished_at":        finishedAt,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SkipPendingTargets 把尚未下发的目标标记为 skipped。
func (r *FileSyncRepository) SkipPendingTargets(taskID uint) error {
	return r.db.Model(&model.FileSyncTarget{}).
		Where("task_id = ? AND status = ?", taskID, model.FileSyncTargetStatusPending).
		Update("status", model.FileSyncTargetStatusSkipped).Error
}

// CreateLog 追加任务日志。
func (r *FileSyncRepository) CreateLog(log *model.FileSyncLog) error {
	return r.db.Create(log).Error
}

// ListLogsAfter 查询 task 下 id 大于 afterID 的日志，按 id 升序。
// afterID 为 0 且设置 limit 时返回最新尾部，并保持升序供页面回放。
func (r *FileSyncRepository) ListLogsAfter(taskID, afterID uint, limit int) ([]model.FileSyncLog, error) {
	if afterID == 0 && limit > 0 {
		return r.listLatestLogs(taskID, limit)
	}
	q := r.db.Where("task_id = ? AND id > ?", taskID, afterID).Order("id asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var logs []model.FileSyncLog
	if err := q.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

func (r *FileSyncRepository) listLatestLogs(taskID uint, limit int) ([]model.FileSyncLog, error) {
	var logs []model.FileSyncLog
	if err := r.db.Where("task_id = ?", taskID).Order("id desc").Limit(limit).Find(&logs).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}
	return logs, nil
}
