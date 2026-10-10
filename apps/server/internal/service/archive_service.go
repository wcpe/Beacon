package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/pkg/lockguard"
	"github.com/wcpe/Beacon/apps/server/internal/redact"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// archiveScheduleTick 是工作器调度检查周期：每分钟检查是否到达 schedule-hour-utc 自动触发点。
const archiveScheduleTick = time.Minute

// archiveLongQueryTimeout 是归档**数据面**语句的期限（P1-2 修复），与 HTTP 路径的 call 预算刻意分开。
//
// 为什么必须单独放宽（不这样做就是功能回归）：store 的连接等待防护给每条语句套了
// `call-timeout-ms`（默认 5s），而 `database/sql` 把这个 ctx 同时绑到「等连接」「语句执行」与
// 「Rows 生存期」上。5s 这个取值是按**在线业务语句**定的（实测中位 1.482ms），但归档的存在理由
// 就是大表，其验证步有若干**按设计无 LIMIT 的全区间查询**：`countRows` 的 `COUNT(*)`、
// `orderedPKs` 的全量主键 `Pluck`、`hashRows` 的 `IN ?` 大集合、`applyRange` 覆盖的「全部未归档历史」。
// 防护上线后这些查询在 5s 处被掐断 → `runVerify` 失败 → item 判 failed → 归档任务失败：
// 表越大越必然失败，恰好把归档能力从「慢」变成「不可用」。
//
// 取值 30 分钟与 `store` 的 bootstrapTimeout 同源同理由（都是「一条语句扫/改整表」这一类），
// 不再新增配置项：两者的风险口径一致，出现第三个同类需求时再抽成配置。
//
// 为什么仍然**有界**（不能直接取消期限）：§2A 的全部价值就是「把无限挂起变成快速失败」，
// 归档 worker 若允许无界等待，一条病态语句就能永久占住连接并让整个归档单卡在 running。
// 30 分钟远超正常全表扫描（量级上比 §2A 的实测语句大 6 个数量级），到点仍会失败并落 item 错误。
//
// 为什么不用「分块 + 游标推进」替代（评审给出的备选）：`orderedPKs` 的全集是
// `pickArchiveSample` 在**主键升序全集**上确定性取样的输入，分块会改变取样集合、
// 进而改变校验语义（同一任务重跑可能取到不同样本），属校验设计的重写而非防护层修补；
// 且 `countRows` 的相等性判据本就是「整区间行数」，分块相加还要处理搬运期间的并发增删。
// 风险收益不划算，故取「显式长期限」这条改动面最小、语义不变的路径。
const archiveLongQueryTimeout = 30 * time.Minute

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
	//
	// 用 lockguard.Mutex 而非 sync.Mutex（P1-3）：本锁的持有期间**确实会做 DB 访问**
	// （HasActiveJob / GetJob / hotDB.Transaction，见三处调用点），故必须纳入「持锁做 DB」守卫的
	// 观测面——否则守卫自称的「系统性防护」对它并不成立，日后真有「持连接等本锁」的路径出现时
	// 也无人报警。观测代价为零（守卫默认关、开启时才走慢路径），换来的是这个反模式机器可见。
	mu lockguard.Mutex
	// lastAutoDay 记本日已尝试的自动触发 UTC 日（仅 worker goroutine 读写，无需锁）。
	lastAutoDay string
	// lockGuard 是 AttachLockDBGuard 挂上的观测句柄（未装配时为 nil）。
	// 它不参与任何业务判定，只让「观测是否真的生效」可被核验（见该方法的说明）。
	lockGuard *lockguard.Watcher
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

// AttachLockDBGuard 把「持锁期间发起 DB 访问」守卫挂到本服务的热库连接上，观测 s.mu。
//
// 与交付编排器同名方法同形（见 delivery_orchestrator.go）：由启动装配调用一次，
// 守卫默认关、经设置项 debug.lock-db-guard-enabled 热开，命中记 ERROR 日志并指认调用点。
//
// 为什么归档域也要挂（P1-3）：本服务是**第二处**「持锁做 DB」的落点，而守卫此前只观测交付域的
// s.mu ——「系统性防护」的说法在第二处服务上并不成立。挂上之后：
//   - 该模式从此机器可见（开启守卫即审计这三条路径）；
//   - 日后若出现「持连接等 archiveService.mu」的对偶路径，守卫会直接指认，而不是等它复现成事故。
//
// 刻意**不返回错误**（同交付域口径）：装配失败只记 WARN——它只是诊断能力，
// 绝不是控制面可用性（乃至归档功能）的前提。
//
// 保留 Watcher 句柄（不只在生产里挂完即弃）的原因：观测面**是否真的挂上了**只能由 Watcher
// 自己的计数证明——挂载失败只记 WARN、不报错，若句柄被丢弃，「守卫已生效」就没有可核验的依据
// （测试尤其需要它，见 archive_lockguard_test.go）。
func (s *ArchiveService) AttachLockDBGuard() {
	w, err := lockguard.Attach(s.hotDB, &s.mu)
	if err != nil {
		slog.Warn("归档域锁内 DB 访问守卫装配失败（诊断能力降级，不影响归档功能）", "错误", err)
		return
	}
	w.OnViolation(lockguard.LogViolation)
	s.lockGuard = w
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
//
// 持 s.mu 做 DB 是**已知例外**（P1-3 的处置），此处记录为什么保留、以及为什么它不构成环：
//
//	保留的原因——单飞判据没有 DB 兜底。「至多一个活跃任务」目前只由本处的
//	`HasActiveJob`（check-then-act）在一个进程内维护，表上**没有**能让数据库来定序的
//	唯一约束（活跃集是「status IN (pending,running,cancelling)」这一条件集合，sqlite 与
//	mysql 的可移植写法都表达不出部分唯一索引）。若按方案 A 把 DB 移出锁外、改由 CAS 定序，
//	两个并发 CreateJob 会双双读到「无活跃任务」并各自插入一条——把「至多一个」降级成「通常一个」，
//	这是**语义削弱**而非加固，故不可取（评审给出的 A 路径以 CAS 可定序为前提，此处前提不成立）。
//
//	不构成环的原因——本锁没有「持连接等它」的对偶路径。本服务三处锁内 DB 都是**同 goroutine
//	顺序调用**（取锁 → 查/写 → 放锁），全程不跨调用等另一把锁；而归档 worker 的搬运循环
//	（runJob / runCopy / runVerify / runDelete）**从不取本锁**，故不存在「持连接 → 等 mu」。
//	对照 P0 事故：那里的环是「审批持连接等 deliveryOrchestrator.mu」×「tick 持 mu 等连接」，
//	本服务缺了前一半。
//
//	既然不构成环，为什么还要挂守卫：见 mu 字段与 AttachLockDBGuard 的说明——它属**潜伏风险**
//	（反模式留在第二处服务、且此前未被观测），纳入观测后一旦真有对偶路径出现即被指认。
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

// archivePingBudget 是归档可达性探测的调用侧上限（与 store 的默认 call 预算同量级，见其说明）。
//
// 为什么这里也要给期限：包装层确实自带预算，但 `store.NewBoundedPinger` 在包装层未启用时会
// 回退到原生池（无兜底期限）。调用侧统一给一层，探测「永不挂起」就与注入了哪种池无关。
const archivePingBudget = 5 * time.Second

// reachable 归档库当前是否可达（archiveDB 非 nil 且带预算探测通过）；overview / 创建 / 重试据此判可用。
//
// 为什么必须带预算（P1-1）：本函数在 `/archive/overview`、创建与重试三个入口都会走到，
// 而原生 `(*sql.DB).Ping()` 内部用 `context.Background()`——归档库连接池被占满时它会**无界等待**，
// 于是「归档库不可达」这个本该优雅降级的判据，反倒成了挂起控制面请求的入口。
// 探测失败即判不可达（保守方向正确：宁可降级也不挂住）。
func (s *ArchiveService) reachable() bool {
	if s.archiveDB == nil {
		return false
	}
	return s.ping(s.archiveDB) == nil
}

// ping 对给定连接做一次**带预算**的连通性探测。
//
// 优先用 store.BoundedPinger（继承 call 预算，池耗尽时报可读的等连接超时）；
// 拿不到包装层时（两侧预算被显式关闭、或调用方传入了非本层装配的 *gorm.DB）回退到
// `sqlDB.PingContext`——它没有本层兜底期限，但调用方 ctx 仍可中断，比裸 Ping() 强。
func (s *ArchiveService) ping(db *gorm.DB) error {
	if db == nil {
		return errors.New("归档库连接为空")
	}
	ctx, cancel := context.WithTimeout(context.Background(), archivePingBudget)
	defer cancel()
	if p := store.NewBoundedPinger(db); p != nil {
		return p.PingContext(ctx)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
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
	// 数据面语句改用长期限（见 archiveLongQueryTimeout）：整单共用一条 ctx，任务收尾时统一取消。
	// 为什么在整单范围内共用而不是逐语句新建：归档的「区间」概念跨语句存在（countRows 与
	// orderedPKs 必须看到同一区间），逐语句各自的期限只会让失败点变得不可预测；一条 ctx
	// 贯穿整单，语义是「这一个任务最多跑这么久」，与运维对归档任务的心理模型一致。
	// 基线用 context.Background()：worker 的 ctx 会随关停取消（见 drainActive 的 ctx.Err 检查），
	// 而归档单在关停时**刻意保持 running** 以便下次启动续跑——若把数据面期限挂在关停 ctx 上，
	// 一次重启就会把跑了一半的整单连语句一起掐断，与「断点续跑」的设计相悖。
	cancelLong := runner.bindLongBudget(context.Background())
	defer cancelLong()

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

// expandItems 按 cutoffs 快照展开工作项（日表逐张 / 单表按区间；无到期数据的日表生成 skipped item）。
//
// **长查询例外（评审 P2-1）**：单表分支按设计做「发生时间 < cutoff」的**全区间 COUNT(*)**——
// 与归档 runner 同属大表聚合，不能受 store 的 call 预算（默认 5s）约束，否则大部署下建单即失败。
// 故本方法自带 archiveLongQueryTimeout 期限（与 runner 的 bindLongBudget 同源同值），
// 用 WithContext 绑定到**展开期的两条读查询**（日表清单 `expiredDailyTables` 与单表全区间 COUNT）上；
// 期限随方法返回即 cancel 释放。收尾的 `CreateItems` / `Items` 是小写入，留在默认预算内即可
// （它们是插入与主键回读，不随表规模增长）。
//
// **未纳入本例外的是 `Overview` 的三条计数**：那是交互式概览页，让页面等 30 分钟不可接受，
// 故有意保留 5s 有界读（`safeDomainCount` 已吞错记 WARN、降级为 0），属**有意保留**而非遗漏。
func (s *ArchiveService) expandItems(job *model.ArchiveJob) ([]model.ArchiveJobItem, error) {
	// 展开步含大表 COUNT(*)，按长查询口径给期限（见方法注释）。
	ctx, cancel := context.WithTimeout(context.Background(), archiveLongQueryTimeout)
	defer cancel()
	hotDB := s.hotDB.WithContext(ctx)
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
			refs, err := expiredDailyTables(hotDB, d.baseTable, cutoff)
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
		if err := applyDomainFilter(hotDB.Table(d.baseTable), d).
			Where(d.timeColumn+" < ?", rangeTo).Count(&cnt).Error; err != nil {
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
// 一并施加域的附加行过滤（extraWhere）：overview 的 hotRows / archivedRows / expiredRows 三栏
// 与归档实际可搬运集合同口径，否则「到期量」会包含永不归档的待办行。
func domainRowCount(db *gorm.DB, dom archiveDomain, cutoff *time.Time) (int64, error) {
	if dom.form == archiveFormSingle {
		// 单表可能尚未在该库建（如归档库首次运行前），判存避免「no such table」噪声。
		if !db.Migrator().HasTable(dom.baseTable) {
			return 0, nil
		}
		q := applyDomainFilter(db.Table(dom.baseTable), dom)
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
		if err := applyDomainFilter(db.Table(t), dom).Count(&n).Error; err != nil {
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
