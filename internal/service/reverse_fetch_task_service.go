package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// reverseFetchTaskComment 是受管任务 ingest 落库的版本 / 审计注释（FR-58）。
const reverseFetchTaskComment = "在线实例反向抓取（受管任务）"

// ScanFile 是扫描清单的单个文件元信息（FR-58，无内容）：path + size + UTF-8 文本判定 + 是否超单文件阈值。
type ScanFile struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	IsText        bool   `json:"isText"`
	OverThreshold bool   `json:"overThreshold"`
}

// scanManifest 是落库 manifest TEXT 的结构（FR-58）：清单总览 + 逐文件元信息。
type scanManifest struct {
	TotalFiles int        `json:"totalFiles"`
	TotalBytes int64      `json:"totalBytes"`
	Skipped    int        `json:"skipped"`
	Files      []ScanFile `json:"files"`
}

// ReverseFetchTaskService 编排反向抓取受管任务（FR-58，见 ADR-0037）：建任务 + 单实例互斥、
// scan 回传存清单、submit 编排（校验选定 / 超阈值确认）、ingest 复用 FileService.Import 落库、取消、过期。
// 任务是真源、agent_command 是其执行手段；命令通道（SSE 唤醒 + 落库 + CAS）复用既有原语。
type ReverseFetchTaskService struct {
	db        *gorm.DB
	taskRepo  *repository.ReverseFetchTaskRepository
	cmdRepo   *repository.AgentCommandRepository
	fileSvc   *FileService
	auditRepo *repository.AuditLogRepository
	notifier  CommandNotifier
}

// NewReverseFetchTaskService 构造服务。
func NewReverseFetchTaskService(db *gorm.DB, taskRepo *repository.ReverseFetchTaskRepository,
	cmdRepo *repository.AgentCommandRepository, fileSvc *FileService, auditRepo *repository.AuditLogRepository) *ReverseFetchTaskService {
	return &ReverseFetchTaskService{db: db, taskRepo: taskRepo, cmdRepo: cmdRepo, fileSvc: fileSvc, auditRepo: auditRepo}
}

// SetNotifier 注入命令待办唤醒器（启动时装配；未注入则建命令后不主动唤醒）。
func (s *ReverseFetchTaskService) SetNotifier(n CommandNotifier) { s.notifier = n }

// CreateScanTask 由 admin 触发受管反向抓取：互斥查 → 事务内建任务(scanning) + 下发 scan 命令(pending) + 审计 →
// 提交后唤醒目标 agent SSE。已有非终态任务 → ErrReverseFetchTaskActive(409)。返回任务（含 id）。
func (s *ReverseFetchTaskService) CreateScanTask(ns, serverID, scope, group, target, operator, clientIP string) (*model.ReverseFetchTask, error) {
	if ns == "" || serverID == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	// 反向抓取只落 group / server 两层（沿 FR-39）：空目标先挡，完整归一校验在落库时的 normalizeScope。
	if scope != model.ScopeGroup && scope != model.ScopeServer {
		return nil, apperr.ErrInvalidScope
	}
	if group == "" || (scope == model.ScopeServer && target == "") {
		return nil, apperr.ErrInvalidScope
	}

	task := &model.ReverseFetchTask{
		NamespaceCode: ns, ServerID: serverID,
		Scope: scope, GroupCode: group, ScopeTarget: target,
		Status: model.ReverseFetchTaskScanning, Operator: operator,
	}
	scanPayload, _ := json.Marshal(ingestPayload{Mode: model.IngestModeScan, Scope: scope, Group: group, Target: target})
	cmd := &model.AgentCommand{
		NamespaceCode: ns, ServerID: serverID,
		Type: model.CommandTypeIngestPlugins, Payload: string(scanPayload),
		Status: model.CommandStatusPending, Operator: operator,
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 互斥（事务内查 + active 哨兵唯一键兜底）：已有非终态任务 → 拒。
		active, e := s.taskRepo.WithTx(tx).FindActiveByServer(ns, serverID)
		if e != nil {
			return e
		}
		if active != nil {
			return reverseFetchActiveErr(active)
		}
		if e := s.taskRepo.WithTx(tx).Create(task); e != nil {
			return e
		}
		if e := s.cmdRepo.WithTx(tx).Create(cmd); e != nil {
			return e
		}
		if e := s.taskRepo.WithTx(tx).SetScanCommandID(task.ID, cmd.ID); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns,
			Operator:      operator, Action: model.ActionFileReverseFetchScan,
			TargetType: model.TargetTypeReverseFetchTask, TargetRef: fmt.Sprintf("%d", task.ID),
			Detail: fmt.Sprintf(`{"taskId":%d,"commandId":%d,"scope":%q,"group":%q,"target":%q}`,
				task.ID, cmd.ID, scope, group, target),
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		// active 哨兵唯一键并发兜底：竞态下 Create 撞唯一约束 → 归一为活跃冲突。
		if e := s.reloadActiveOnConflict(err, ns, serverID); e != nil {
			return nil, e
		}
		return nil, err
	}
	task.ScanCommandID = cmd.ID
	if s.notifier != nil {
		s.notifier.NotifyCommand(ns, serverID)
	}
	slog.Info("建反向抓取受管任务并下发扫描命令", "namespace", ns, "serverId", serverID,
		"taskId", task.ID, "scanCommandId", cmd.ID, "scope", scope, "group", group, "target", target, "operator", operator)
	return task, nil
}

// ReceiveScan 接收 agent 回传的扫描清单（mode=scan）：命令须属某 scanning 任务 → 存 manifest + 计数、
// 任务→pending-review、命令→done。scan 永不因任何超限文件失败（无内容、不受上限约束）。
func (s *ReverseFetchTaskService) ReceiveScan(commandID uint, files []ScanFile, _ string) error {
	cmd, task, err := s.requireTaskForCommand(commandID, model.IngestModeScan, model.ReverseFetchTaskScanning)
	if err != nil {
		return err
	}

	var totalBytes int64
	overThreshold := 0
	for _, f := range files {
		totalBytes += f.Size
		if f.OverThreshold {
			overThreshold++
		}
	}
	manifest := scanManifest{TotalFiles: len(files), TotalBytes: totalBytes, Skipped: 0, Files: files}
	manifestJSON, merr := json.Marshal(manifest)
	if merr != nil {
		return apperr.ErrInternal
	}

	ok, err := s.taskRepo.SaveManifest(task.ID, string(manifestJSON), len(files), overThreshold, 0)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrReverseFetchTaskState // 被并发迁移，非 scanning
	}
	if _, e := s.cmdRepo.UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusDone,
		fmt.Sprintf(`{"taskId":%d,"files":%d}`, task.ID, len(files))); e != nil {
		slog.Warn("扫描命令置 done 失败（清单已落库）", "commandId", cmd.ID, "原因", e)
	}
	slog.Info("反向抓取扫描清单已落库", "taskId", task.ID, "commandId", cmd.ID,
		"files", len(files), "overThreshold", overThreshold)
	return nil
}

// Submit 提交选定集（FR-58）：任务须 pending-review；校验选定（超阈值文件须确认、文件数兜底）→
// 事务内存 selectedPaths + 下发 submit 命令(pending) + 任务→fetching + 审计 → 提交后唤醒。
func (s *ReverseFetchTaskService) Submit(taskID uint, selectedPaths []string, confirmOverThreshold bool, operator, clientIP string) (*model.ReverseFetchTask, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.ReverseFetchTaskPendingReview {
		return nil, apperr.ErrReverseFetchTaskState
	}
	if len(selectedPaths) == 0 {
		return nil, apperr.ErrInvalidParam
	}
	if len(selectedPaths) > MaxImportFiles {
		return nil, apperr.ErrTooManyFiles
	}

	// 解析清单，校验选定 path：须在清单内、非 jar；超阈值文件须 confirmOverThreshold 才纳入（只拒该文件不拒整批）。
	manifestByPath, perr := parseManifestByPath(task.Manifest)
	if perr != nil {
		return nil, perr
	}
	clean, verr := validateSelected(selectedPaths, manifestByPath, confirmOverThreshold)
	if verr != nil {
		return nil, verr
	}

	selectedJSON, _ := json.Marshal(clean)
	submitPayload, _ := json.Marshal(ingestPayload{
		Mode: model.IngestModeSubmit, Scope: task.Scope, Group: task.GroupCode, Target: task.ScopeTarget,
		SelectedPaths: clean,
	})
	cmd := &model.AgentCommand{
		NamespaceCode: task.NamespaceCode, ServerID: task.ServerID,
		Type: model.CommandTypeIngestPlugins, Payload: string(submitPayload),
		Status: model.CommandStatusPending, Operator: operator,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.cmdRepo.WithTx(tx).Create(cmd); e != nil {
			return e
		}
		ok, e := s.taskRepo.WithTx(tx).SaveSelected(task.ID, string(selectedJSON), len(clean), cmd.ID)
		if e != nil {
			return e
		}
		if !ok {
			return apperr.ErrReverseFetchTaskState // 并发已迁移，非 pending-review
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: task.NamespaceCode,
			Operator:      operator, Action: model.ActionFileReverseFetchSubmit,
			TargetType: model.TargetTypeReverseFetchTask, TargetRef: fmt.Sprintf("%d", task.ID),
			Detail: fmt.Sprintf(`{"taskId":%d,"commandId":%d,"selected":%d}`, task.ID, cmd.ID, len(clean)),
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	if s.notifier != nil {
		s.notifier.NotifyCommand(task.NamespaceCode, task.ServerID)
	}
	slog.Info("反向抓取受管任务提交选定集并下发抓取命令", "taskId", task.ID, "submitCommandId", cmd.ID,
		"selected", len(clean), "operator", operator)
	task.Status = model.ReverseFetchTaskFetching
	task.SelectedCount = len(clean)
	task.SubmitCommandID = cmd.ID
	return task, nil
}

// ReceiveSubmitIngest 接收 agent 回传的选定集内容（mode=submit）：命令须属某 fetching 任务 →
// 任务→ingesting → 复用 FileService.Import 落库（scope/group/target 来自任务）→ 任务→done、命令→done、审计。
func (s *ReverseFetchTaskService) ReceiveSubmitIngest(commandID uint, files []ImportFile, clientIP string) (*ImportResult, error) {
	cmd, task, err := s.requireTaskForCommand(commandID, model.IngestModeSubmit, model.ReverseFetchTaskFetching)
	if err != nil {
		return nil, err
	}
	// 入库前再校验（双保险）：非空、文件数 / 总量上限、排除 .jar（单文件大小 / path 安全交 Import 兜底）。
	if verr := validateIngestFiles(files); verr != nil {
		s.failTask(task, cmd, verr.Error())
		return nil, verr
	}
	// 任务 fetching→ingesting（落库中）。
	if ok, e := s.taskRepo.UpdateStatus(task.ID, model.ReverseFetchTaskFetching, model.ReverseFetchTaskIngesting); e != nil {
		return nil, e
	} else if !ok {
		return nil, apperr.ErrReverseFetchTaskState
	}

	result, ierr := s.fileSvc.Import(ImportFilesParams{
		Namespace: task.NamespaceCode, Group: task.GroupCode,
		ScopeLevel: task.Scope, ScopeTarget: task.ScopeTarget,
		Files: files, Operator: cmd.Operator, Comment: reverseFetchTaskComment, ClientIP: clientIP,
	})
	if ierr != nil {
		// ingesting → failed（已离开 fetching，按 ingesting 前态终结）。
		s.failTaskFrom(task, model.ReverseFetchTaskIngesting, cmd, ierr.Error())
		return nil, ierr
	}

	now := time.Now().UTC()
	if ok, e := s.taskRepo.MarkTerminal(task.ID, model.ReverseFetchTaskIngesting, model.ReverseFetchTaskDone,
		fmt.Sprintf(`{"files":%d,"created":%d,"updated":%d}`, len(files), result.Created, result.Updated), false, now); e != nil {
		return nil, e
	} else if !ok {
		return nil, apperr.ErrReverseFetchTaskState
	}
	if _, e := s.cmdRepo.UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusDone,
		fmt.Sprintf(`{"taskId":%d,"files":%d}`, task.ID, len(files))); e != nil {
		slog.Warn("提交命令置 done 失败（已落库）", "commandId", cmd.ID, "原因", e)
	}
	if e := s.auditRepo.Create(&model.AuditLog{
		NamespaceCode: task.NamespaceCode,
		Operator:      cmd.Operator, Action: model.ActionFileReverseFetchIngest,
		TargetType: model.TargetTypeReverseFetchTask, TargetRef: fmt.Sprintf("%d", task.ID),
		Detail: fmt.Sprintf(`{"taskId":%d,"files":%d,"created":%d,"updated":%d}`,
			task.ID, len(files), result.Created, result.Updated),
		Result: model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		slog.Warn("反向抓取入库审计写入失败（已落库）", "taskId", task.ID, "原因", e)
	}
	slog.Info("反向抓取受管任务入库完成", "taskId", task.ID, "commandId", cmd.ID,
		"files", len(files), "created", result.Created, "updated", result.Updated)
	return result, nil
}

// Cancel 人工取消非终态任务 → cancelled（清空清单瞬态 + 审计）。终态任务 → ErrReverseFetchTaskState。
func (s *ReverseFetchTaskService) Cancel(taskID uint, operator, clientIP string) (*model.ReverseFetchTask, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, err
	}
	if model.IsReverseFetchTaskTerminal(task.Status) {
		return nil, apperr.ErrReverseFetchTaskState
	}
	now := time.Now().UTC()
	ok, err := s.taskRepo.MarkTerminal(task.ID, task.Status, model.ReverseFetchTaskCancelled, "已取消", true, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.ErrReverseFetchTaskState // 并发已终结
	}
	if e := s.auditRepo.Create(&model.AuditLog{
		NamespaceCode: task.NamespaceCode,
		Operator:      operator, Action: model.ActionFileReverseFetchCancel,
		TargetType: model.TargetTypeReverseFetchTask, TargetRef: fmt.Sprintf("%d", task.ID),
		Detail: fmt.Sprintf(`{"taskId":%d,"from":%q}`, task.ID, task.Status),
		Result: model.ResultOK, ClientIP: clientIP,
	}); e != nil {
		slog.Warn("反向抓取取消审计写入失败（已取消）", "taskId", task.ID, "原因", e)
	}
	slog.Info("反向抓取受管任务已取消", "taskId", task.ID, "from", task.Status, "operator", operator)
	task.Status = model.ReverseFetchTaskCancelled
	return task, nil
}

// Get 取任务详情（任意状态，供任务台 / 进度轮询）。
func (s *ReverseFetchTaskService) Get(taskID uint) (*model.ReverseFetchTask, error) {
	return s.requireTask(taskID)
}

// List 列出任务（ns / serverId / status 过滤，最新在前）。
func (s *ReverseFetchTaskService) List(ns, serverID, status string) ([]model.ReverseFetchTask, error) {
	return s.taskRepo.List(ns, serverID, status)
}

// ExpireStale 把陈旧非终态任务标 expired 并清空清单瞬态（由后台清理器周期触发）。
func (s *ReverseFetchTaskService) ExpireStale(before time.Time) (int64, error) {
	return s.taskRepo.ExpireStale(before, time.Now().UTC())
}

// requireTask 取任务、不存在即 ErrReverseFetchTaskNotFound。
func (s *ReverseFetchTaskService) requireTask(taskID uint) (*model.ReverseFetchTask, error) {
	task, err := s.taskRepo.GetByID(taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, apperr.ErrReverseFetchTaskNotFound
	}
	return task, nil
}

// requireTaskForCommand 校验回传命令属某受管任务的指定阶段：命令须存在 + fetched + 指定 mode；
// 其引用任务须存在 + 处指定状态。返回命令与任务。
func (s *ReverseFetchTaskService) requireTaskForCommand(commandID uint, mode, taskStatus string) (*model.AgentCommand, *model.ReverseFetchTask, error) {
	cmd, err := s.cmdRepo.FindByID(commandID)
	if err != nil {
		return nil, nil, err
	}
	if cmd == nil || cmd.Type != model.CommandTypeIngestPlugins {
		return nil, nil, apperr.ErrCommandNotFound
	}
	if cmd.Status != model.CommandStatusFetched {
		return nil, nil, apperr.ErrCommandNotFound // 已完成 / 失败 / 过期 / 未拉取，均不可回传
	}
	var payload ingestPayload
	if json.Unmarshal([]byte(cmd.Payload), &payload) != nil || payload.Mode != mode {
		return nil, nil, apperr.ErrCommandNotFound
	}
	task, err := s.findTaskByCommand(commandID, mode)
	if err != nil {
		return nil, nil, err
	}
	if task == nil || task.Status != taskStatus {
		return nil, nil, apperr.ErrReverseFetchTaskState
	}
	return cmd, task, nil
}

// findTaskByCommand 据命令 id + 阶段反查所属任务（scan 命令在 scan_command_id、submit 命令在 submit_command_id）。
func (s *ReverseFetchTaskService) findTaskByCommand(commandID uint, mode string) (*model.ReverseFetchTask, error) {
	var t model.ReverseFetchTask
	col := "scan_command_id"
	if mode == model.IngestModeSubmit {
		col = "submit_command_id"
	}
	err := s.db.Where(col+" = ?", commandID).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// failTask 把任务从其当前态、命令从 fetched 一并终结为 failed（best-effort，记因摘要）。
func (s *ReverseFetchTaskService) failTask(task *model.ReverseFetchTask, cmd *model.AgentCommand, reason string) {
	s.failTaskFrom(task, task.Status, cmd, reason)
}

// failTaskFrom 把任务从指定前态终结为 failed、命令置 failed（best-effort）。
func (s *ReverseFetchTaskService) failTaskFrom(task *model.ReverseFetchTask, expect string, cmd *model.AgentCommand, reason string) {
	if _, e := s.taskRepo.MarkTerminal(task.ID, expect, model.ReverseFetchTaskFailed, reason, true, time.Now().UTC()); e != nil {
		slog.Warn("标记反向抓取任务失败态出错", "taskId", task.ID, "原因", e)
	}
	if _, e := s.cmdRepo.UpdateStatus(cmd.ID, model.CommandStatusFetched, model.CommandStatusFailed,
		fmt.Sprintf(`{"error":%q}`, reason)); e != nil {
		slog.Warn("标记命令失败态出错", "commandId", cmd.ID, "原因", e)
	}
}

// reloadActiveOnConflict 在建任务事务命中 active 哨兵唯一键冲突时，重查活跃任务返回活跃冲突错误（并发兜底）。
// 非唯一键冲突则原样让调用方返回原错误（返回 nil）。
func (s *ReverseFetchTaskService) reloadActiveOnConflict(err error, ns, serverID string) error {
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil
	}
	active, e := s.taskRepo.FindActiveByServer(ns, serverID)
	if e != nil {
		return e
	}
	if active != nil {
		return reverseFetchActiveErr(active)
	}
	return apperr.ErrReverseFetchTaskActive
}

// reverseFetchActiveErr 构造带活跃任务 id / 状态提示的活跃冲突错误（引导先完成或取消）。
func reverseFetchActiveErr(active *model.ReverseFetchTask) error {
	return &apperr.Error{
		Code:    apperr.ErrReverseFetchTaskActive.Code,
		Status:  apperr.ErrReverseFetchTaskActive.Status,
		Message: fmt.Sprintf("该实例已有活跃反向抓取任务（id=%d, status=%s），请先完成或取消", active.ID, active.Status),
	}
}

// parseManifestByPath 解析任务 manifest TEXT 为 path→ScanFile 映射（供 submit 校验选定 path）。
func parseManifestByPath(manifest string) (map[string]ScanFile, error) {
	if strings.TrimSpace(manifest) == "" {
		return nil, apperr.ErrReverseFetchTaskState // 清单已清空（过期）/ 未到，不能提交
	}
	var m scanManifest
	if json.Unmarshal([]byte(manifest), &m) != nil {
		return nil, apperr.ErrInternal
	}
	byPath := make(map[string]ScanFile, len(m.Files))
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	return byPath, nil
}

// validateSelected 校验选定 path 子集：须在清单内、非 jar；超阈值文件须 confirm 才纳入（否则收集后整体拒、列出）。
// 返回去重后的选定集；任一不合法即整次拒（不下发命令）。
func validateSelected(selected []string, byPath map[string]ScanFile, confirm bool) ([]string, error) {
	seen := make(map[string]struct{}, len(selected))
	clean := make([]string, 0, len(selected))
	var notInManifest, unconfirmedOver []string
	for _, p := range selected {
		cleanPath, err := normalizePath(p)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[cleanPath]; dup {
			continue
		}
		seen[cleanPath] = struct{}{}
		f, ok := byPath[cleanPath]
		if !ok {
			notInManifest = append(notInManifest, cleanPath)
			continue
		}
		if strings.HasSuffix(strings.ToLower(cleanPath), ".jar") {
			return nil, apperr.ErrInvalidPath // 清单本不含 jar；防御性兜底
		}
		if f.OverThreshold && !confirm {
			unconfirmedOver = append(unconfirmedOver, cleanPath)
			continue
		}
		clean = append(clean, cleanPath)
	}
	if len(notInManifest) > 0 {
		return nil, apperr.ErrInvalidParam // 选定 path 不在扫描清单内
	}
	if len(unconfirmedOver) > 0 {
		return nil, overThresholdNotConfirmedErr(unconfirmedOver)
	}
	if len(clean) == 0 {
		return nil, apperr.ErrInvalidParam
	}
	return clean, nil
}

// overThresholdNotConfirmedErr 构造列出未确认超阈值文件的 400 错误（只拒该文件、提示需确认）。
func overThresholdNotConfirmedErr(paths []string) error {
	return &apperr.Error{
		Code:    apperr.ErrOverThresholdNotConfirmed.Code,
		Status:  apperr.ErrOverThresholdNotConfirmed.Status,
		Message: fmt.Sprintf("选定集含超单文件阈值的文件，须显式确认才能纳入：%s", strings.Join(paths, ", ")),
	}
}
