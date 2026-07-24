package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ArchiveJobFilter 是任务列表查询过滤（FR-151，spec §5）：状态 / 模式 / 触发方式 + 分页。
type ArchiveJobFilter struct {
	Status   string
	Mode     string
	Trigger  string
	Page     int
	PageSize int
}

// ArchiveJobRepository 提供归档任务表（archive_job / archive_job_item，落热库）的数据访问（FR-151）。
type ArchiveJobRepository struct {
	db *gorm.DB
}

// NewArchiveJobRepository 构造仓库。
func NewArchiveJobRepository(db *gorm.DB) *ArchiveJobRepository {
	return &ArchiveJobRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在外层事务内复用，避免嵌套开事务死锁）。
func (r *ArchiveJobRepository) WithTx(tx *gorm.DB) *ArchiveJobRepository {
	return &ArchiveJobRepository{db: tx}
}

// CreateJob 插入一条任务（含审计的事务由 service 控制边界）。
func (r *ArchiveJobRepository) CreateJob(job *model.ArchiveJob) error {
	return r.db.Create(job).Error
}

// CreateItems 批量插入工作项。
func (r *ArchiveJobRepository) CreateItems(items []model.ArchiveJobItem) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.Create(&items).Error
}

// SaveJob 全量保存任务（worker 持内存任务对象、状态推进后落库）。
func (r *ArchiveJobRepository) SaveJob(job *model.ArchiveJob) error {
	return r.db.Save(job).Error
}

// SaveItem 全量保存工作项（worker 持内存工作项对象、阶段 / 游标 / 校验结果推进后落库）。
func (r *ArchiveJobRepository) SaveItem(item *model.ArchiveJobItem) error {
	return r.db.Save(item).Error
}

// CompareAndSwapStatus 条件状态迁移：仅当当前状态为 from 时置为 to（可附带其它列），返回是否命中。
// 用于与后台工作器的并发安全迁移（pending→running / pending→cancelled / running→cancelling），避免全量 Save 相互覆盖。
func (r *ArchiveJobRepository) CompareAndSwapStatus(id uint, from, to string, fields map[string]any) (bool, error) {
	upd := map[string]any{"status": to}
	for k, v := range fields {
		upd[k] = v
	}
	res := r.db.Model(&model.ArchiveJob{}).Where("id = ? AND status = ?", id, from).Updates(upd)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// GetJob 按 id 取任务；不存在返回 (nil, nil)。
func (r *ArchiveJobRepository) GetJob(id uint) (*model.ArchiveJob, error) {
	var job model.ArchiveJob
	err := r.db.First(&job, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// GetJobStatus 只取任务当前状态（worker 批次边界轮询是否被请求取消，避免全量重载）。
// 不存在返回空串。
func (r *ArchiveJobRepository) GetJobStatus(id uint) (string, error) {
	var job model.ArchiveJob
	err := r.db.Select("status").First(&job, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return job.Status, nil
}

// Items 取某任务的全部工作项（按 id 升序，稳定处理顺序）。
func (r *ArchiveJobRepository) Items(jobID uint) ([]model.ArchiveJobItem, error) {
	var items []model.ArchiveJobItem
	if err := r.db.Where("job_id = ?", jobID).Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ActiveJob 取当前活跃（pending / running / cancelling）任务中最早创建的一个；无则 (nil, nil)。
// 单飞约束的判据 + worker 拾取入口（crash 后 running 任务续跑亦经此）。
func (r *ArchiveJobRepository) ActiveJob() (*model.ArchiveJob, error) {
	var job model.ArchiveJob
	err := r.db.Where("status IN ?", []string{
		model.ArchiveJobPending, model.ArchiveJobRunning, model.ArchiveJobCancelling,
	}).Order("created_at ASC").First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// HasActiveJob 判断是否已有活跃任务（CreateJob 单飞 409 守卫）。
func (r *ArchiveJobRepository) HasActiveJob() (bool, error) {
	var count int64
	err := r.db.Model(&model.ArchiveJob{}).
		Where("status IN ?", []string{
			model.ArchiveJobPending, model.ArchiveJobRunning, model.ArchiveJobCancelling,
		}).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RecentJobs 取最近若干任务（created_at 降序），供 overview 逐域推导 lastJob。
func (r *ArchiveJobRepository) RecentJobs(limit int) ([]model.ArchiveJob, error) {
	var jobs []model.ArchiveJob
	if err := r.db.Order("created_at DESC").Limit(limit).Find(&jobs).Error; err != nil {
		return nil, err
	}
	return jobs, nil
}

// List 按过滤 + 分页查任务（created_at 降序），返回本页与总数。
func (r *ArchiveJobRepository) List(f ArchiveJobFilter) ([]model.ArchiveJob, int64, error) {
	q := r.db.Model(&model.ArchiveJob{})
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Mode != "" {
		q = q.Where("mode = ?", f.Mode)
	}
	if f.Trigger != "" {
		q = q.Where("trigger = ?", f.Trigger)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	size := f.PageSize
	if size < 1 {
		size = 20
	}
	var jobs []model.ArchiveJob
	if err := q.Order("created_at DESC").
		Offset((page - 1) * size).Limit(size).Find(&jobs).Error; err != nil {
		return nil, 0, err
	}
	return jobs, total, nil
}
