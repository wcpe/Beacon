package service

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
)

const (
	fileSyncRoleBukkit      = "bukkit"
	fileSyncMaxLogTail      = 200
	defaultFileSyncBlobRoot = ".beacon/file-sync-blobs"
)

// CreateFileSyncTaskParams 是创建文件同步任务入参。
type CreateFileSyncTaskParams struct {
	Namespace               string
	SourceServerID          string
	Directory               string
	BatchSize               int
	IntervalSec             int
	FailureThresholdPercent int
	Operator                string
	ClientIP                string
}

// FileSyncEventSink 是管理台 SSE 写出口。
type FileSyncEventSink interface {
	Send(FileSyncEvent) error
}

// FileSyncService 编排文件同步任务的控制面骨架（FR-129/FR-131）。
type FileSyncService struct {
	db        *gorm.DB
	repo      *repository.FileSyncRepository
	cmdRepo   *repository.AgentCommandRepository
	instSvc   *InstanceService
	auditRepo *repository.AuditLogRepository
	events    *FileSyncEventHub
	notifier  CommandNotifier
	blobRoot  string
}

// NewFileSyncService 构造服务。
func NewFileSyncService(db *gorm.DB, repo *repository.FileSyncRepository, instSvc *InstanceService,
	auditRepo *repository.AuditLogRepository, events *FileSyncEventHub) *FileSyncService {
	return &FileSyncService{
		db: db, repo: repo, instSvc: instSvc, auditRepo: auditRepo, events: events,
		blobRoot: filepath.Clean(defaultFileSyncBlobRoot),
	}
}

// SetCommandQueue 注入文件同步使用的既有 agent 命令队列与唤醒器。
func (s *FileSyncService) SetCommandQueue(repo *repository.AgentCommandRepository, notifier CommandNotifier) {
	s.cmdRepo = repo
	s.notifier = notifier
}

// SetBlobRoot 覆盖本地 blob 缓存根目录，主要供测试隔离。
func (s *FileSyncService) SetBlobRoot(root string) {
	if strings.TrimSpace(root) == "" {
		return
	}
	s.blobRoot = filepath.Clean(root)
}

// NormalizeFileSyncDirectory 归一并校验服务器根内相对目录。
// 本函数只做字符串级安全闸；符号链接逃逸须在 agent 以真实路径再次校验。
func NormalizeFileSyncDirectory(input string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" || strings.Contains(raw, "\\") || strings.Contains(raw, ":") {
		return "", apperr.ErrInvalidPath
	}
	if strings.HasPrefix(raw, "//") || path.IsAbs(raw) {
		return "", apperr.ErrInvalidPath
	}
	parts := strings.Split(raw, "/")
	for _, part := range parts {
		if part == ".." || isWindowsReservedName(part) {
			return "", apperr.ErrInvalidPath
		}
	}
	clean := path.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || path.IsAbs(clean) {
		return "", apperr.ErrInvalidPath
	}
	return clean, nil
}

func isWindowsReservedName(part string) bool {
	name := strings.TrimSpace(strings.TrimRight(part, ". "))
	if name == "" || strings.Contains(name, ".") {
		name = strings.Split(name, ".")[0]
	}
	switch strings.ToUpper(name) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

// CreateTask 创建任务真源并写审计 / 日志。
func (s *FileSyncService) CreateTask(p CreateFileSyncTaskParams) (*model.FileSyncTask, error) {
	if p.Namespace == "" || p.SourceServerID == "" || p.Operator == "" ||
		p.BatchSize <= 0 || p.IntervalSec < 0 || p.FailureThresholdPercent < 0 || p.FailureThresholdPercent > 100 {
		return nil, apperr.ErrInvalidParam
	}
	directory, err := NormalizeFileSyncDirectory(p.Directory)
	if err != nil {
		return nil, err
	}
	if err := s.requireOnlineBukkit(p.Namespace, p.SourceServerID, apperr.ErrFileSyncSourceInvalid); err != nil {
		return nil, err
	}

	task := &model.FileSyncTask{
		NamespaceCode: p.Namespace, SourceServerID: p.SourceServerID, Directory: directory,
		Status: model.FileSyncTaskStatusScanning, BatchSize: p.BatchSize, IntervalSec: p.IntervalSec,
		FailureThresholdPercent: p.FailureThresholdPercent, Operator: p.Operator,
	}
	log := &model.FileSyncLog{
		TaskID: task.ID, Level: model.FileSyncLogLevelInfo,
		Message: fmt.Sprintf("已创建文件同步任务，源服务器 %s，目录 %s", p.SourceServerID, directory),
	}
	var sourceCommandID uint
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		if e := repo.CreateTask(task); e != nil {
			return e
		}
		if cmd, e := s.createSourceScanCommand(tx, task); e != nil {
			return e
		} else if cmd != nil {
			sourceCommandID = cmd.ID
			task.SourceCommandID = cmd.ID
			if e := repo.SetSourceCommandID(task.ID, cmd.ID); e != nil {
				return e
			}
		}
		log.TaskID = task.ID
		if e := repo.CreateLog(log); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: p.Namespace, Operator: p.Operator, Action: model.ActionFileSyncCreate,
			TargetType: model.TargetTypeFileSyncTask, TargetRef: fmt.Sprintf("%d", task.ID),
			Detail: fmt.Sprintf(`{"taskId":%d,"sourceServerId":%q,"directory":%q,"batchSize":%d,"intervalSec":%d,"failureThresholdPercent":%d}`,
				task.ID, p.SourceServerID, directory, p.BatchSize, p.IntervalSec, p.FailureThresholdPercent),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	if sourceCommandID != 0 && s.notifier != nil {
		s.notifier.NotifyCommand(p.Namespace, p.SourceServerID)
	}
	s.publishLog(log)
	s.publishTask(task)
	return task, nil
}

// PlanTargets 去重校验目标，并按任务批大小写入批次 / 目标规划。
func (s *FileSyncService) PlanTargets(taskID uint, targetServerIDs []string, operator, clientIP string) (*model.FileSyncTask, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.FileSyncTaskStatusScanning && task.Status != model.FileSyncTaskStatusCached &&
		task.Status != model.FileSyncTaskStatusDraft && task.Status != model.FileSyncTaskStatusPlanned {
		return nil, apperr.ErrFileSyncTaskState
	}
	targets, err := s.normalizeTargets(task, targetServerIDs)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, apperr.ErrFileSyncNoTargets
	}
	batchCount := (len(targets) + task.BatchSize - 1) / task.BatchSize

	log := &model.FileSyncLog{
		TaskID: task.ID, Level: model.FileSyncLogLevelInfo,
		Message: fmt.Sprintf("已规划 %d 个目标，分为 %d 个批次", len(targets), batchCount),
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		if e := repo.ClearPlan(task.ID); e != nil {
			return e
		}
		for i := 0; i < batchCount; i++ {
			from := i * task.BatchSize
			to := from + task.BatchSize
			if to > len(targets) {
				to = len(targets)
			}
			batch := &model.FileSyncBatch{
				TaskID: task.ID, BatchNo: i + 1, Status: model.FileSyncBatchStatusPending,
				PlannedCount: to - from,
			}
			if e := repo.CreateBatch(batch); e != nil {
				return e
			}
			rows := make([]model.FileSyncTarget, 0, to-from)
			for _, serverID := range targets[from:to] {
				rows = append(rows, model.FileSyncTarget{
					TaskID: task.ID, BatchID: batch.ID, BatchNo: batch.BatchNo,
					ServerID: serverID, Status: model.FileSyncTargetStatusPending,
				})
			}
			if e := repo.CreateTargets(rows); e != nil {
				return e
			}
		}
		ok, e := repo.UpdateTaskPlan(task.ID, len(targets), batchCount)
		if e != nil {
			return e
		}
		if !ok {
			return apperr.ErrFileSyncTaskState
		}
		if e := repo.CreateLog(log); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: task.NamespaceCode, Operator: operator, Action: model.ActionFileSyncPlan,
			TargetType: model.TargetTypeFileSyncTask, TargetRef: fmt.Sprintf("%d", task.ID),
			Detail: fmt.Sprintf(`{"taskId":%d,"targets":%d,"batches":%d}`, task.ID, len(targets), batchCount),
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	s.publishLog(log)
	return s.getAndPublish(task.ID)
}

// Start 启动任务并下发首个批次的目标命令。
func (s *FileSyncService) Start(taskID uint, operator, clientIP string) (*model.FileSyncTask, error) {
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, err
	}
	if !task.SourceReady {
		return nil, apperr.ErrFileSyncTaskState
	}
	now := time.Now().UTC()
	started, err := s.transition(taskID, []string{model.FileSyncTaskStatusPlanned}, model.FileSyncTaskStatusRunning,
		&now, nil, operator, clientIP, model.ActionFileSyncStart, "文件同步任务已启动")
	if err != nil {
		return nil, err
	}
	if err := s.dispatchNextBatch(taskID); err != nil {
		return nil, err
	}
	return started, nil
}

// Pause 暂停后续批次。
func (s *FileSyncService) Pause(taskID uint, operator, clientIP string) (*model.FileSyncTask, error) {
	return s.transition(taskID, []string{model.FileSyncTaskStatusRunning}, model.FileSyncTaskStatusPaused,
		nil, nil, operator, clientIP, model.ActionFileSyncPause, "文件同步任务已暂停")
}

// Resume 继续任务。
func (s *FileSyncService) Resume(taskID uint, operator, clientIP string) (*model.FileSyncTask, error) {
	task, err := s.transition(taskID, []string{model.FileSyncTaskStatusPaused}, model.FileSyncTaskStatusRunning,
		nil, nil, operator, clientIP, model.ActionFileSyncResume, "文件同步任务已继续")
	if err != nil {
		return nil, err
	}
	if err := s.dispatchNextBatch(taskID); err != nil {
		return nil, err
	}
	return task, nil
}

// Terminate 紧急终止任务。
func (s *FileSyncService) Terminate(taskID uint, operator, clientIP string) (*model.FileSyncTask, error) {
	now := time.Now().UTC()
	task, err := s.transition(taskID, []string{
		model.FileSyncTaskStatusDraft, model.FileSyncTaskStatusPlanned,
		model.FileSyncTaskStatusScanning, model.FileSyncTaskStatusCached,
		model.FileSyncTaskStatusRunning, model.FileSyncTaskStatusPaused,
	}, model.FileSyncTaskStatusTerminated, nil, &now, operator, clientIP,
		model.ActionFileSyncTerminate, "文件同步任务已终止")
	if err != nil {
		return nil, err
	}
	if e := s.repo.SkipPendingTargets(taskID); e != nil {
		return nil, e
	}
	return task, nil
}

// Get 查询任务详情。
func (s *FileSyncService) Get(taskID uint) (*model.FileSyncTask, []model.FileSyncBatch, []model.FileSyncTarget, []model.FileSyncLog, error) {
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	batches, err := s.repo.ListBatches(task.ID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	targets, err := s.repo.ListTargets(task.ID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	logs, err := s.repo.ListLogsAfter(task.ID, 0, fileSyncMaxLogTail)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return task, batches, targets, logs, nil
}

// List 查询任务列表。
func (s *FileSyncService) List(namespace, status string) ([]model.FileSyncTask, error) {
	return s.repo.ListTasks(namespace, status)
}

// RunEvents 回放持久化日志尾部，并继续通过内存 hub 推送实时事件。
func (s *FileSyncService) RunEvents(ctx context.Context, taskID, afterLogID uint, sink FileSyncEventSink) error {
	if _, err := s.requireTask(taskID); err != nil {
		return err
	}
	ch := s.events.Register(taskID)
	defer s.events.Deregister(taskID, ch)

	logs, err := s.repo.ListLogsAfter(taskID, afterLogID, fileSyncMaxLogTail)
	if err != nil {
		return err
	}
	for _, log := range logs {
		if err := sink.Send(eventFromLog(log)); err != nil {
			return err
		}
	}
	for {
		select {
		case evt := <-ch:
			if err := sink.Send(evt); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *FileSyncService) transition(taskID uint, from []string, to string, startedAt, finishedAt *time.Time,
	operator, clientIP, action, message string) (*model.FileSyncTask, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, err
	}
	if model.IsFileSyncTaskTerminal(task.Status) {
		return nil, apperr.ErrFileSyncTaskState
	}
	log := &model.FileSyncLog{TaskID: taskID, Level: model.FileSyncLogLevelInfo, Message: message}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		ok, e := repo.UpdateTaskStatusCAS(taskID, from, to, startedAt, finishedAt)
		if e != nil {
			return e
		}
		if !ok {
			return apperr.ErrFileSyncTaskState
		}
		if e := repo.CreateLog(log); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: task.NamespaceCode, Operator: operator, Action: action,
			TargetType: model.TargetTypeFileSyncTask, TargetRef: fmt.Sprintf("%d", taskID),
			Detail: fmt.Sprintf(`{"taskId":%d,"from":%q,"to":%q}`, taskID, task.Status, to),
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	s.publishLog(log)
	return s.getAndPublish(taskID)
}

func (s *FileSyncService) getAndPublish(taskID uint) (*model.FileSyncTask, error) {
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, err
	}
	s.publishTask(task)
	return task, nil
}

func (s *FileSyncService) publishTask(task *model.FileSyncTask) {
	if s.events == nil || task == nil {
		return
	}
	s.events.Publish(task.ID, FileSyncEvent{
		Type: FileSyncEventTypeTask, TaskID: task.ID, Status: task.Status,
		Task: &FileSyncTaskEvent{
			ID: fmt.Sprintf("%d", task.ID), Status: task.Status,
			SourceReady: task.SourceReady, SourceFileCount: task.SourceFileCount,
			SourceTotalBytes: task.SourceTotalBytes, TotalTargets: task.TargetCount,
			PlannedTargets: task.TargetCount, CurrentBatch: 0, TotalBatches: task.BatchCount,
			FailureThresholdPercent: task.FailureThresholdPercent,
			UpdatedAt:               task.UpdatedAt.UTC().Format(time.RFC3339),
		},
		CreatedAt: task.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

func (s *FileSyncService) publishLog(log *model.FileSyncLog) {
	if s.events == nil || log == nil {
		return
	}
	s.events.Publish(log.TaskID, eventFromLog(*log))
}

func (s *FileSyncService) publishTarget(target *model.FileSyncTarget) {
	if s.events == nil || target == nil {
		return
	}
	s.events.Publish(target.TaskID, FileSyncEvent{
		Type: FileSyncEventTypeTarget, TaskID: target.TaskID,
		Target: &FileSyncTargetEvent{
			TaskID: fmt.Sprintf("%d", target.TaskID), BatchID: target.BatchID, BatchNo: target.BatchNo,
			ServerID: target.ServerID, Status: target.Status, BackupPath: target.BackupPath,
			CurrentFileCount: target.CurrentFileCount, ChangedFileCount: target.ChangedFileCount,
			SkippedFileCount: target.SkippedFileCount, BytesTotal: target.BytesTotal, BytesDone: target.BytesDone,
			Error: target.LastError, UpdatedAt: target.UpdatedAt.UTC().Format(time.RFC3339),
		},
	})
}

func (s *FileSyncService) requireTask(taskID uint) (*model.FileSyncTask, error) {
	task, err := s.repo.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, apperr.ErrFileSyncTaskNotFound
	}
	return task, nil
}

func (s *FileSyncService) normalizeTargets(task *model.FileSyncTask, targetServerIDs []string) ([]string, error) {
	seen := make(map[string]struct{}, len(targetServerIDs))
	targets := make([]string, 0, len(targetServerIDs))
	for _, raw := range targetServerIDs {
		serverID := strings.TrimSpace(raw)
		if serverID == "" || serverID == task.SourceServerID {
			continue
		}
		if _, ok := seen[serverID]; ok {
			continue
		}
		seen[serverID] = struct{}{}
		if err := s.requireOnlineBukkit(task.NamespaceCode, serverID, apperr.ErrFileSyncTargetInvalid); err != nil {
			return nil, err
		}
		targets = append(targets, serverID)
	}
	return targets, nil
}

func (s *FileSyncService) requireOnlineBukkit(namespace, serverID string, invalid error) error {
	inst, err := s.instSvc.Get(namespace, serverID)
	if err != nil {
		return invalid
	}
	if inst.Role != fileSyncRoleBukkit || inst.Status != runtime.StatusOnline {
		return invalid
	}
	return nil
}

func eventFromLog(log model.FileSyncLog) FileSyncEvent {
	return FileSyncEvent{
		Type: FileSyncEventTypeLog, TaskID: log.TaskID, LogID: log.ID,
		BatchID: log.BatchID, ServerID: log.ServerID, Level: log.Level,
		Message: log.Message, CreatedAt: log.CreatedAt.UTC().Format(time.RFC3339),
	}
}
