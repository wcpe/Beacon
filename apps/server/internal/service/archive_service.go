package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/redact"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// archiveScheduleTick 是工作器调度检查周期：每分钟检查是否到达 schedule-hour-utc 自动触发点。
const archiveScheduleTick = time.Minute

// archiveRecentJobsForOverview 是 overview 逐域推导 lastJob 时回看的最近任务条数。
const archiveRecentJobsForOverview = 100

// ArchiveService 是热冷归档后台工作器（FR-151，见 ADR-0066）：进程内单例 goroutine + ticker。
//
// 每日 schedule-hour-utc 自动创建 execute 任务（auto-enabled 时）；单飞（同时至多一个活跃任务）。
// 单 item 流水线 copying→verifying→deleting→done：热连接读、归档连接写、幂等补偿、校验通过才删热库。
// 任务表落热库（控制面事实）。暴露 CreateJob / RetryJob / CancelJob / ListJobs / GetJob / Overview 供 P6b handler 调用。
type ArchiveService struct {
	hotDB     *gorm.DB
	archiveDB *gorm.DB // 归档连接；不可达降级时为 nil（工作器不启、拒绝创建、overview 标不可用）
	info      store.ArchiveInfo
	repo      *repository.ArchiveJobRepository
	settings  *SettingsService
	auditRepo *repository.AuditLogRepository
	now       func() time.Time
	wakeCh    chan struct{}
	// mu 串行化 CreateJob / RetryJob / CancelJob（单飞判据 + 写），与后台 worker 的状态迁移用 CAS 协同。
	mu sync.Mutex
	// lastAutoDay 记本日已尝试的自动触发 UTC 日（仅 worker goroutine 读写，无需锁）。
	lastAutoDay string
}

// NewArchiveService 构造归档工作器。archiveDB 为 nil 表示归档库不可达（启动连通性检查失败），能力降级。
func NewArchiveService(hotDB, archiveDB *gorm.DB, info store.ArchiveInfo,
	repo *repository.ArchiveJobRepository, settings *SettingsService, auditRepo *repository.AuditLogRepository) *ArchiveService {
	return &ArchiveService{
		hotDB: hotDB, archiveDB: archiveDB, info: info, repo: repo, settings: settings, auditRepo: auditRepo,
		now:    func() time.Time { return time.Now().UTC() },
		wakeCh: make(chan struct{}, 1),
	}
}

// Run 启动后台工作器循环，直到 ctx 取消（随关停信号优雅退出）。
// 归档库不可达（archiveDB=nil）时不启动搬运循环——overview 仍标不可用、创建任务被拒（fail-static，不阻断控制面）。
func (s *ArchiveService) Run(ctx context.Context) {
	if s.archiveDB == nil {
		slog.Warn("归档库不可用，归档后台工作器不启动（overview 标不可用、拒绝创建归档任务）")
		return
	}
	slog.Info("归档工作器已启动", "目标模式", s.info.Mode, "库", s.info.Database, "调度整点UTC", s.scheduleHour())
	// 启动先续跑可能残留的活跃任务（crash 恢复：running / cancelling 从 cursor / phase 续起）。
	s.drainActive(ctx)
	ticker := time.NewTicker(archiveScheduleTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("归档工作器已停止")
			return
		case <-s.wakeCh:
			s.drainActive(ctx)
		case <-ticker.C:
			s.maybeAutoCreate()
			s.drainActive(ctx)
		}
	}
}

// CreateJob 创建归档任务（页面手动触发，FR-151，spec §5）：dry_run 预览 / execute 执行。
// 单飞——已有活跃任务返回 409；归档库不可达返回 503。空 domains = 全部域。
func (s *ArchiveService) CreateJob(mode string, domains []string, operator string) (*ArchiveJobDetailView, error) {
	return s.createJobInternal(mode, domains, operator, model.ArchiveTriggerManual)
}

// createJobInternal 创建任务的统一入口（手动 / 自动共用）。
func (s *ArchiveService) createJobInternal(mode string, domains []string, operator, trigger string) (*ArchiveJobDetailView, error) {
	if !model.IsValidArchiveMode(mode) {
		return nil, apperr.ErrInvalidParam
	}
	for _, d := range domains {
		if !isValidArchiveDomain(d) {
			return nil, apperr.ErrArchiveDomainInvalid
		}
	}
	if !s.reachable() {
		return nil, apperr.ErrArchiveUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	active, err := s.repo.HasActiveJob()
	if err != nil {
		return nil, err
	}
	if active {
		return nil, apperr.ErrArchiveJobRunning
	}

	now := s.now()
	domainsJSON, _ := json.Marshal(normalizeDomainList(domains))
	cutoffsJSON, _ := json.Marshal(s.snapshotCutoffs(now))
	job := &model.ArchiveJob{
		Mode: mode, Trigger: trigger, Status: model.ArchiveJobPending,
		Domains: string(domainsJSON), Cutoffs: string(cutoffsJSON),
		Operator: operator, CreatedAt: now,
	}
	err = s.hotDB.Transaction(func(tx *gorm.DB) error {
		if e := s.repo.WithTx(tx).CreateJob(job); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(archiveAudit(model.ActionArchiveJobCreate, job, operator))
	})
	if err != nil {
		return nil, err
	}
	slog.Info("已创建归档任务", "id", job.ID, "模式", mode, "触发", trigger, "操作人", operator)
	s.wake()
	return s.jobDetailView(job.ID)
}

// RetryJob 对 failed 任务发起断点续跑（FR-151，spec §4.3）：任务回 running，done/skipped item 跳过、其余从 cursor/phase 续。
func (s *ArchiveService) RetryJob(id uint, operator string) (*ArchiveJobDetailView, error) {
	if !s.reachable() {
		return nil, apperr.ErrArchiveUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.repo.GetJob(id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, apperr.ErrArchiveJobNotFound
	}
	if job.Status != model.ArchiveJobFailed {
		return nil, apperr.ErrArchiveJobState
	}
	active, err := s.repo.HasActiveJob()
	if err != nil {
		return nil, err
	}
	if active {
		return nil, apperr.ErrArchiveJobRunning
	}
	job.Status = model.ArchiveJobRunning
	job.Error = ""
	job.FinishedAt = nil
	err = s.hotDB.Transaction(func(tx *gorm.DB) error {
		if e := s.repo.WithTx(tx).SaveJob(job); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(archiveAudit(model.ActionArchiveJobRetry, job, operator))
	})
	if err != nil {
		return nil, err
	}
	slog.Info("已重试归档任务", "id", id, "操作人", operator)
	s.wake()
	return s.jobDetailView(id)
}

// CancelJob 取消任务（FR-151，spec §4.3）：pending 直接 cancelled；running → cancelling（worker 批次边界收尾 cancelled）。
func (s *ArchiveService) CancelJob(id uint, operator string) (*ArchiveJobDetailView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.repo.GetJob(id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, apperr.ErrArchiveJobNotFound
	}
	if job.Status != model.ArchiveJobPending && job.Status != model.ArchiveJobRunning {
		return nil, apperr.ErrArchiveJobState // cancelling / 终态不可再取消
	}
	now := s.now()
	err = s.hotDB.Transaction(func(tx *gorm.DB) error {
		r := s.repo.WithTx(tx)
		if e := applyCancelTransition(r, id, job.Status, now); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(archiveAudit(model.ActionArchiveJobCancel, job, operator))
	})
	if err != nil {
		return nil, err
	}
	slog.Info("已请求取消归档任务", "id", id, "操作人", operator)
	return s.jobDetailView(id)
}

// applyCancelTransition 执行取消的条件状态迁移（并发安全）：pending→cancelled；若已被 worker 抢转 running，
// 或本就 running，则 running→cancelling（worker 批次边界收尾 cancelled）。
func applyCancelTransition(r *repository.ArchiveJobRepository, id uint, currentStatus string, now time.Time) error {
	if currentStatus == model.ArchiveJobPending {
		swapped, err := r.CompareAndSwapStatus(id, model.ArchiveJobPending, model.ArchiveJobCancelled,
			map[string]any{"finished_at": now})
		if err != nil {
			return err
		}
		if swapped {
			return nil
		}
		// 未命中：worker 已把 pending 抢转 running，落到下面走 cancelling。
	}
	_, err := r.CompareAndSwapStatus(id, model.ArchiveJobRunning, model.ArchiveJobCancelling, nil)
	return err
}

// ListJobs 分页查任务（状态 / 模式 / 触发方式过滤，created_at 降序）。
func (s *ArchiveService) ListJobs(filter repository.ArchiveJobFilter) (*ArchiveJobListView, error) {
	jobs, total, err := s.repo.List(filter)
	if err != nil {
		return nil, err
	}
	views := make([]ArchiveJobView, 0, len(jobs))
	for i := range jobs {
		views = append(views, toArchiveJobView(jobs[i]))
	}
	return &ArchiveJobListView{Items: views, Total: total}, nil
}

// GetJob 取任务详情（含 items）；不存在返回 ErrArchiveJobNotFound。
func (s *ArchiveService) GetJob(id uint) (*ArchiveJobDetailView, error) {
	return s.jobDetailView(id)
}

// Overview 归档总览：目标库形态 / 可达性 + 各域保留期 / 热库体量 / 归档体量 / 到期待归档量 / 最近一次任务。
func (s *ArchiveService) Overview() (*ArchiveOverviewView, error) {
	reachable := s.reachable()
	target := ArchiveTargetView{
		Mode: s.info.Mode, Database: s.info.Database, DSNMasked: s.info.DSNMasked, Reachable: reachable,
	}
	now := s.now()
	recent, err := s.repo.RecentJobs(archiveRecentJobsForOverview)
	if err != nil {
		return nil, err
	}
	domains := make([]ArchiveDomainOverviewView, 0, len(archiveDomains))
	for _, d := range archiveDomains {
		days := s.settings.GetInt(d.retentionKey)
		cutoff := cutoffFor(now, days)
		hotRows := s.safeDomainCount(s.hotDB, d, nil)
		expiredRows := s.safeDomainCount(s.hotDB, d, &cutoff)
		var archiveRows int64
		if reachable {
			archiveRows = s.safeDomainCount(s.archiveDB, d, nil)
		}
		domains = append(domains, ArchiveDomainOverviewView{
			Domain: d.name, RetentionDays: days,
			HotRows: hotRows, ArchiveRows: archiveRows, ExpiredRows: expiredRows,
			LastJob: lastJobForDomain(recent, d.name),
		})
	}
	return &ArchiveOverviewView{Target: target, Domains: domains}, nil
}

// ---- 后台工作器内部 ----

// reachable 归档库当前是否可达（archiveDB 非 nil 且 Ping 通）；overview / 创建 / 重试据此判可用。
func (s *ArchiveService) reachable() bool {
	if s.archiveDB == nil {
		return false
	}
	sqlDB, err := s.archiveDB.DB()
	if err != nil {
		return false
	}
	return sqlDB.Ping() == nil
}

// wake 非阻塞唤醒工作器（channel 满即已有待处理信号，丢弃本次不阻塞）。
func (s *ArchiveService) wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// drainActive 拾取并处理活跃任务直到无活跃任务（单飞下至多一个；防御性循环）。
func (s *ArchiveService) drainActive(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := s.repo.ActiveJob()
		if err != nil {
			slog.Error("归档工作器读取活跃任务失败", "错误", err)
			return
		}
		if job == nil {
			return
		}
		s.runJob(ctx, job)
		if ctx.Err() != nil {
			return
		}
	}
}

// maybeAutoCreate 到达 schedule-hour-utc 且开启自动归档时创建 execute 任务（每日至多一次，撞 running 跳过记 WARN）。
func (s *ArchiveService) maybeAutoCreate() {
	now := s.now()
	if !s.autoEnabled() {
		return
	}
	if now.Hour() != s.scheduleHour() {
		return
	}
	day := now.Format("20060102")
	if s.lastAutoDay == day {
		return
	}
	s.lastAutoDay = day // 本日已尝试，无论成功 / 跳过均不再触发（不排队，spec §8-8）
	if _, err := s.createJobInternal(model.ArchiveModeExecute, nil, "system", model.ArchiveTriggerScheduled); err != nil {
		if errors.Is(err, apperr.ErrArchiveJobRunning) {
			slog.Warn("已有归档任务在执行中，本轮每日自动归档跳过")
			return
		}
		slog.Error("自动创建归档任务失败", "错误", redact.DesensitizeErr(err))
	}
}

// runJob 执行 / 续跑一个任务：pending→running 条件迁移、展开 items、逐 item 流水线、收尾终态。
func (s *ArchiveService) runJob(ctx context.Context, job *model.ArchiveJob) {
	if job.Status == model.ArchiveJobPending {
		now := s.now()
		swapped, err := s.repo.CompareAndSwapStatus(job.ID, model.ArchiveJobPending, model.ArchiveJobRunning,
			map[string]any{"started_at": now})
		if err != nil {
			slog.Error("归档任务转 running 失败", "id", job.ID, "错误", err)
			return
		}
		if !swapped {
			return // 已被并发取消（pending→cancelled），跳过
		}
		job.Status = model.ArchiveJobRunning
		job.StartedAt = &now
	}

	items, err := s.repo.Items(job.ID)
	if err != nil {
		slog.Error("归档任务读取 items 失败", "id", job.ID, "错误", err)
		return
	}
	if len(items) == 0 {
		items, err = s.expandItems(job)
		if err != nil {
			s.finalizeJob(job, false, true, err)
			return
		}
	}

	runner := &archiveItemRunner{
		hot: s.hotDB, archive: s.archiveDB, mode: job.Mode,
		batchRows: s.batchRows(), batchInterval: s.batchInterval(), sampleSize: s.sampleSize(),
		saveItem:  s.repo.SaveItem,
		cancelled: func() bool { return s.isCancelRequested(job.ID) },
	}

	failed, cancelled := false, false
	var firstErr error
	for i := range items {
		item := &items[i]
		if model.IsArchiveItemDone(item.Phase) {
			continue
		}
		if ctx.Err() != nil {
			return // 关停：任务保持 running，下次启动 drainActive 续跑
		}
		if s.isCancelRequested(job.ID) {
			cancelled = true
			break
		}
		dom, ok := archiveDomainByName(item.Domain)
		if !ok {
			firstErr = fmt.Errorf("未知归档域 %s", item.Domain)
			s.failItem(item, firstErr)
			failed = true
			break
		}
		runner.dom = dom
		runErr := runner.run(item)
		if errors.Is(runErr, errArchiveCancelled) {
			cancelled = true
			break
		}
		if runErr != nil {
			firstErr = runErr
			s.failItem(item, runErr)
			failed = true
			break
		}
	}
	s.finalizeJob(job, failed, cancelled, firstErr)
}

// expandItems 按 cutoffs 快照展开工作项（日表逐张 / 单表按区间；无到期数据的域生成 skipped item）。
func (s *ArchiveService) expandItems(job *model.ArchiveJob) ([]model.ArchiveJobItem, error) {
	cutoffs := parseCutoffs(job.Cutoffs)
	selected := parseDomainList(job.Domains)
	selectedSet := make(map[string]struct{}, len(selected))
	for _, d := range selected {
		selectedSet[d] = struct{}{}
	}
	var items []model.ArchiveJobItem
	for _, d := range archiveDomains {
		if len(selected) > 0 {
			if _, ok := selectedSet[d.name]; !ok {
				continue
			}
		}
		cutoff := cutoffs[d.name]
		if d.form == archiveFormDaily {
			refs, err := expiredDailyTables(s.hotDB, d.baseTable, cutoff)
			if err != nil {
				return nil, err
			}
			if len(refs) == 0 {
				items = append(items, newArchiveItem(job.ID, d, d.baseTable, nil, model.ArchiveItemSkipped))
				continue
			}
			for _, ref := range refs {
				items = append(items, newArchiveItem(job.ID, d, ref.name, nil, model.ArchiveItemPending))
			}
			continue
		}
		// 单表：按 发生时间 < cutoff 的区间；无到期行 → skipped。
		rangeTo := cutoff
		var cnt int64
		if err := s.hotDB.Table(d.baseTable).Where(d.timeColumn+" < ?", rangeTo).Count(&cnt).Error; err != nil {
			return nil, err
		}
		phase := model.ArchiveItemPending
		if cnt == 0 {
			phase = model.ArchiveItemSkipped
		}
		items = append(items, newArchiveItem(job.ID, d, d.baseTable, &rangeTo, phase))
	}
	if err := s.repo.CreateItems(items); err != nil {
		return nil, err
	}
	return s.repo.Items(job.ID) // 重载取回自增 id
}

// finalizeJob 收尾任务终态：cancelled / failed / succeeded，写 finished_at + 完成 / 失败审计。
func (s *ArchiveService) finalizeJob(job *model.ArchiveJob, failed, cancelled bool, firstErr error) {
	now := s.now()
	job.FinishedAt = &now
	var action string
	switch {
	case cancelled:
		job.Status = model.ArchiveJobCancelled
		action = "" // 取消审计已在 CancelJob 请求时记录；自动展开失败的 cancelled 亦不重复
	case failed:
		job.Status = model.ArchiveJobFailed
		job.Error = redact.DesensitizeErr(firstErr)
		action = model.ActionArchiveJobFailed
	default:
		job.Status = model.ArchiveJobSucceeded
		action = model.ActionArchiveJobComplete
	}
	err := s.hotDB.Transaction(func(tx *gorm.DB) error {
		if e := s.repo.WithTx(tx).SaveJob(job); e != nil {
			return e
		}
		if action == "" {
			return nil
		}
		return s.auditRepo.WithTx(tx).Create(archiveAudit(action, job, job.Operator))
	})
	if err != nil {
		slog.Error("归档任务收尾落库失败", "id", job.ID, "错误", err)
		return
	}
	slog.Info("归档任务已收尾", "id", job.ID, "终态", job.Status)
}

// failItem 把工作项标 failed 并落脱敏错误（终态阶段由此统一持久化，pipeline 只推进中间阶段）。
func (s *ArchiveService) failItem(item *model.ArchiveJobItem, err error) {
	item.Phase = model.ArchiveItemFailed
	item.Error = redact.DesensitizeErr(err)
	if e := s.repo.SaveItem(item); e != nil {
		slog.Error("归档工作项标失败落库失败", "id", item.ID, "错误", e)
	}
}

// isCancelRequested 读任务当前状态判是否被请求取消（批次 / item 边界轮询，权威在 DB status=cancelling）。
func (s *ArchiveService) isCancelRequested(jobID uint) bool {
	st, err := s.repo.GetJobStatus(jobID)
	if err != nil {
		slog.Warn("归档工作器读取任务状态失败，本轮不视为取消", "id", jobID, "错误", err)
		return false
	}
	return st == model.ArchiveJobCancelling
}

// jobDetailView 装配任务详情视图（job + items）；不存在返回 ErrArchiveJobNotFound。
func (s *ArchiveService) jobDetailView(id uint) (*ArchiveJobDetailView, error) {
	job, err := s.repo.GetJob(id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, apperr.ErrArchiveJobNotFound
	}
	items, err := s.repo.Items(id)
	if err != nil {
		return nil, err
	}
	itemViews := make([]ArchiveJobItemView, 0, len(items))
	for i := range items {
		itemViews = append(itemViews, toArchiveItemView(items[i]))
	}
	return &ArchiveJobDetailView{ArchiveJobView: toArchiveJobView(*job), Items: itemViews}, nil
}

// safeDomainCount 统计某域行数（cutoff 非空=到期部分），出错记 WARN 返回 0（overview 尽力而为、不整页失败）。
func (s *ArchiveService) safeDomainCount(db *gorm.DB, dom archiveDomain, cutoff *time.Time) int64 {
	n, err := domainRowCount(db, dom, cutoff)
	if err != nil {
		slog.Warn("归档总览统计域行数失败", "域", dom.name, "错误", err)
		return 0
	}
	return n
}

// ---- 设置读取（热更） ----

func (s *ArchiveService) batchRows() int {
	v := s.settings.GetInt(SettingArchiveBatchRows)
	if v < 1 {
		v = 1
	}
	return v
}

func (s *ArchiveService) batchInterval() time.Duration {
	return time.Duration(s.settings.GetInt(SettingArchiveBatchIntervalMs)) * time.Millisecond
}

func (s *ArchiveService) sampleSize() int {
	v := s.settings.GetInt(SettingArchiveVerifySampleSize)
	if v < 1 {
		v = 1
	}
	return v
}

func (s *ArchiveService) scheduleHour() int { return s.settings.GetInt(SettingArchiveScheduleHourUTC) }

func (s *ArchiveService) autoEnabled() bool { return s.settings.GetBool(SettingArchiveAutoEnabled) }

// snapshotCutoffs 按当前保留期为全部域快照 cutoff（json {domain: RFC3339}），任务执行期不随设置热更漂移。
func (s *ArchiveService) snapshotCutoffs(now time.Time) map[string]string {
	out := make(map[string]string, len(archiveDomains))
	for _, d := range archiveDomains {
		out[d.name] = cutoffFor(now, s.settings.GetInt(d.retentionKey)).Format(time.RFC3339)
	}
	return out
}

// ---- 纯函数辅助 ----

// domainRowCount 统计某域行数：daily 汇总各日表（cutoff 非空只数到期日表），single 数区间行。
func domainRowCount(db *gorm.DB, dom archiveDomain, cutoff *time.Time) (int64, error) {
	if dom.form == archiveFormSingle {
		// 单表可能尚未在该库建（如归档库首次运行前），判存避免「no such table」噪声。
		if !db.Migrator().HasTable(dom.baseTable) {
			return 0, nil
		}
		q := db.Table(dom.baseTable)
		if cutoff != nil {
			q = q.Where(dom.timeColumn+" < ?", *cutoff)
		}
		var n int64
		err := q.Count(&n).Error
		return n, err
	}
	var tables []string
	if cutoff != nil {
		refs, err := expiredDailyTables(db, dom.baseTable, *cutoff)
		if err != nil {
			return 0, err
		}
		for _, r := range refs {
			tables = append(tables, r.name)
		}
	} else {
		names, err := allDailyTables(db, dom.baseTable)
		if err != nil {
			return 0, err
		}
		tables = names
	}
	var total int64
	for _, t := range tables {
		var n int64
		if err := db.Table(t).Count(&n).Error; err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// lastJobForDomain 在最近任务（降序）中找首个覆盖该域的任务摘要（空 domains=全部域）；无则 nil。
func lastJobForDomain(recent []model.ArchiveJob, domain string) *ArchiveJobBriefView {
	for i := range recent {
		doms := parseDomainList(recent[i].Domains)
		if len(doms) == 0 || archiveDomainsContain(doms, domain) {
			brief := toArchiveJobBrief(recent[i])
			return &brief
		}
	}
	return nil
}

// newArchiveItem 构造一个工作项（pending / skipped）。
func newArchiveItem(jobID uint, dom archiveDomain, table string, rangeTo *time.Time, phase string) model.ArchiveJobItem {
	return model.ArchiveJobItem{
		JobID: jobID, Domain: dom.name, TargetTable: table, RangeTo: rangeTo, Phase: phase,
	}
}

// archiveAudit 组装归档任务审计记录（detail 仅任务 id / 模式 / 域，绝不含数据内容）。
func archiveAudit(action string, job *model.ArchiveJob, operator string) *model.AuditLog {
	detail, _ := json.Marshal(map[string]any{
		"jobId": job.ID, "mode": job.Mode, "domains": parseDomainList(job.Domains),
	})
	return &model.AuditLog{
		Operator: operator, Action: action,
		TargetType: model.TargetTypeArchiveJob, TargetRef: fmt.Sprintf("%d", job.ID),
		Detail: string(detail), Result: model.ResultOK,
	}
}

// normalizeDomainList 归一化 domains（nil → 空切片，代表全部域）。
func normalizeDomainList(domains []string) []string {
	if domains == nil {
		return []string{}
	}
	return domains
}

// parseCutoffs 解析 cutoffs json 文本为 domain→时间（UTC）映射。
func parseCutoffs(raw string) map[string]time.Time {
	out := map[string]time.Time{}
	if raw == "" {
		return out
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return out
	}
	for k, v := range m {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			out[k] = t.UTC()
		}
	}
	return out
}

// archiveDomainsContain 判断域列表是否含某域名。
func archiveDomainsContain(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
