package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// FileSyncManifestFile 是 agent 回传的文件同步清单项。
type FileSyncManifestFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

// FileSyncTargetResult 是目标 agent 回传的执行结果摘要。
type FileSyncTargetResult struct {
	OK               bool
	Reason           string
	BackupPath       string
	CurrentFileCount int
	ChangedFileCount int
	SkippedFileCount int
	BytesTotal       int64
	BytesDone        int64
}

type fileSyncCommandPayload struct {
	TaskID    uint   `json:"taskId"`
	BatchID   uint   `json:"batchId,omitempty"`
	TargetID  uint   `json:"targetId,omitempty"`
	Directory string `json:"directory"`
}

func (s *FileSyncService) createSourceScanCommand(tx *gorm.DB, task *model.FileSyncTask) (*model.AgentCommand, error) {
	if s.cmdRepo == nil {
		return nil, apperr.ErrInternal
	}
	payload, _ := json.Marshal(fileSyncCommandPayload{TaskID: task.ID, Directory: task.Directory})
	cmd := &model.AgentCommand{
		NamespaceCode: task.NamespaceCode, ServerID: task.SourceServerID,
		Type: model.CommandTypeFileSyncSource, Payload: string(payload),
		Status: model.CommandStatusPending, Operator: task.Operator,
	}
	return cmd, s.cmdRepo.WithTx(tx).Create(cmd)
}

// ReceiveSourceManifest 接收源 agent 扫描结果，写入源清单并标记缓存可用。
func (s *FileSyncService) ReceiveSourceManifest(commandID uint, files []FileSyncManifestFile, _ string) error {
	cmd, payload, err := s.requireFileSyncCommand(commandID, model.CommandTypeFileSyncSource)
	if err != nil {
		return err
	}
	task, err := s.requireTask(payload.TaskID)
	if err != nil {
		return err
	}
	if task.SourceCommandID != cmd.ID || model.IsFileSyncTaskTerminal(task.Status) {
		return apperr.ErrFileSyncTaskState
	}
	rows, totalBytes, err := normalizeSourceManifest(task.ID, files)
	if err != nil {
		return err
	}
	log := &model.FileSyncLog{
		TaskID: task.ID, Level: model.FileSyncLogLevelInfo,
		Message: fmt.Sprintf("源清单已缓存，文件 %d 个，总字节 %d", len(rows), totalBytes),
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		if e := repo.ReplaceFiles(task.ID, rows); e != nil {
			return e
		}
		if e := repo.MarkSourceReady(task.ID, len(rows), totalBytes); e != nil {
			return e
		}
		if ok, e := s.cmdRepo.WithTx(tx).UpdateStatus(cmd.ID, model.CommandStatusFetched,
			model.CommandStatusDone, fmt.Sprintf(`{"taskId":%d,"files":%d}`, task.ID, len(rows))); e != nil {
			return e
		} else if !ok {
			return apperr.ErrCommandNotFound
		}
		return repo.CreateLog(log)
	})
	if err != nil {
		return err
	}
	s.publishLog(log)
	if task, e := s.requireTask(task.ID); e == nil {
		s.publishTask(task)
	}
	return nil
}

// ListSourceManifest 返回任务源清单。
func (s *FileSyncService) ListSourceManifest(taskID uint) ([]FileSyncManifestFile, error) {
	if _, err := s.requireTask(taskID); err != nil {
		return nil, err
	}
	files, err := s.repo.ListFiles(taskID)
	if err != nil {
		return nil, err
	}
	out := make([]FileSyncManifestFile, 0, len(files))
	for _, f := range files {
		out = append(out, FileSyncManifestFile{Path: f.Path, Size: f.Size, Hash: f.Hash})
	}
	return out, nil
}

// StoreBlob 流式写入源 blob，并校验 sha256。
func (s *FileSyncService) StoreBlob(taskID uint, hash string, reader io.Reader) error {
	if _, err := s.requireTask(taskID); err != nil {
		return err
	}
	if !isSHA256Hex(hash) {
		return apperr.ErrInvalidParam
	}
	dir := s.blobDir(taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return s.writeBlobFile(dir, hash, reader)
}

// OpenBlob 打开已缓存 blob；调用方负责关闭文件。
func (s *FileSyncService) OpenBlob(taskID uint, hash string) (*os.File, int64, error) {
	if _, err := s.requireTask(taskID); err != nil {
		return nil, 0, err
	}
	if !isSHA256Hex(hash) {
		return nil, 0, apperr.ErrInvalidParam
	}
	f, err := os.Open(s.blobPath(taskID, hash))
	if os.IsNotExist(err) {
		return nil, 0, apperr.ErrFileNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (s *FileSyncService) writeBlobFile(dir, hash string, reader io.Reader) error {
	target := filepath.Join(dir, hash)
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp(dir, hash+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	sum := sha256.New()
	if _, err = io.Copy(tmp, io.TeeReader(reader, sum)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != strings.ToLower(hash) {
		return apperr.ErrInvalidParam
	}
	return os.Rename(tmpName, target)
}

func (s *FileSyncService) blobDir(taskID uint) string {
	return filepath.Join(s.blobRoot, fmt.Sprintf("%d", taskID))
}

func (s *FileSyncService) blobPath(taskID uint, hash string) string {
	return filepath.Join(s.blobDir(taskID), strings.ToLower(hash))
}

func normalizeSourceManifest(taskID uint, files []FileSyncManifestFile) ([]model.FileSyncFile, int64, error) {
	if len(files) == 0 {
		return nil, 0, apperr.ErrInvalidParam
	}
	seen := make(map[string]struct{}, len(files))
	rows := make([]model.FileSyncFile, 0, len(files))
	var total int64
	for _, f := range files {
		row, err := normalizeSourceManifestFile(taskID, f)
		if err != nil {
			return nil, 0, err
		}
		if _, ok := seen[row.Path]; ok {
			return nil, 0, apperr.ErrInvalidParam
		}
		seen[row.Path] = struct{}{}
		total += row.Size
		rows = append(rows, row)
	}
	return rows, total, nil
}

func normalizeSourceManifestFile(taskID uint, f FileSyncManifestFile) (model.FileSyncFile, error) {
	clean, err := NormalizeFileSyncFilePath(f.Path)
	if err != nil || f.Size < 0 || !isSHA256Hex(f.Hash) {
		return model.FileSyncFile{}, apperr.ErrInvalidParam
	}
	hash := strings.ToLower(f.Hash)
	return model.FileSyncFile{TaskID: taskID, Path: clean, Size: f.Size, Hash: hash, BlobKey: hash}, nil
}

// NormalizeFileSyncFilePath 归一并校验同步目录内文件相对路径。
func NormalizeFileSyncFilePath(input string) (string, error) {
	clean, err := NormalizeFileSyncDirectory(input)
	if err != nil || clean == "." {
		return "", apperr.ErrInvalidPath
	}
	if strings.HasSuffix(input, "/") || path.Base(clean) == "." {
		return "", apperr.ErrInvalidPath
	}
	return clean, nil
}

func isSHA256Hex(value string) bool {
	raw := strings.ToLower(strings.TrimSpace(value))
	if len(raw) != 64 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func (s *FileSyncService) requireFileSyncCommand(commandID uint, wantType string) (*model.AgentCommand, fileSyncCommandPayload, error) {
	if s.cmdRepo == nil {
		return nil, fileSyncCommandPayload{}, apperr.ErrInternal
	}
	cmd, err := s.cmdRepo.FindByID(commandID)
	if err != nil {
		return nil, fileSyncCommandPayload{}, err
	}
	if cmd == nil || cmd.Type != wantType || cmd.Status != model.CommandStatusFetched {
		return nil, fileSyncCommandPayload{}, apperr.ErrCommandNotFound
	}
	var payload fileSyncCommandPayload
	if json.Unmarshal([]byte(cmd.Payload), &payload) != nil || payload.TaskID == 0 {
		return nil, fileSyncCommandPayload{}, apperr.ErrCommandNotFound
	}
	return cmd, payload, nil
}

func (s *FileSyncService) dispatchNextBatch(taskID uint) error {
	task, err := s.requireTask(taskID)
	if err != nil {
		return err
	}
	if task.Status != model.FileSyncTaskStatusRunning && task.Status != model.FileSyncTaskStatusPaused {
		return nil
	}
	batches, err := s.repo.ListBatches(task.ID)
	if err != nil {
		return err
	}
	if fileSyncHasRunningBatch(batches) {
		return nil
	}
	next := firstPendingFileSyncBatch(batches)
	if next == nil {
		return s.completeTaskIfSettled(task)
	}
	if task.Status != model.FileSyncTaskStatusRunning {
		return nil
	}
	return s.dispatchBatch(task, next)
}

func fileSyncHasRunningBatch(batches []model.FileSyncBatch) bool {
	for _, batch := range batches {
		if batch.Status == model.FileSyncBatchStatusRunning {
			return true
		}
	}
	return false
}

func firstPendingFileSyncBatch(batches []model.FileSyncBatch) *model.FileSyncBatch {
	for i := range batches {
		if batches[i].Status == model.FileSyncBatchStatusPending {
			return &batches[i]
		}
	}
	return nil
}

func (s *FileSyncService) dispatchBatch(task *model.FileSyncTask, batch *model.FileSyncBatch) error {
	files, err := s.repo.ListFiles(task.ID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return apperr.ErrFileSyncTaskState
	}
	targets, err := s.repo.ListTargetsByBatch(batch.ID)
	if err != nil {
		return err
	}
	return s.createTargetCommands(task, batch, targets)
}

func (s *FileSyncService) createTargetCommands(task *model.FileSyncTask, batch *model.FileSyncBatch, targets []model.FileSyncTarget) error {
	if s.cmdRepo == nil {
		return apperr.ErrInternal
	}
	now := time.Now().UTC()
	var notify []string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		if e := repo.UpdateBatchStatus(batch.ID, model.FileSyncBatchStatusRunning, &now, nil); e != nil {
			return e
		}
		for _, target := range targets {
			if target.Status != model.FileSyncTargetStatusPending {
				continue
			}
			cmd, e := s.createTargetCommand(tx, task, batch, &target)
			if e != nil {
				return e
			}
			if ok, e := repo.MarkTargetDispatched(target.ID, cmd.ID, now); e != nil {
				return e
			} else if ok {
				notify = append(notify, target.ServerID)
			}
		}
		return repo.CreateLog(&model.FileSyncLog{
			TaskID: task.ID, BatchID: batch.ID, Level: model.FileSyncLogLevelInfo,
			Message: fmt.Sprintf("批次 %d 已下发 %d 台目标", batch.BatchNo, len(notify)),
		})
	})
	if err != nil {
		return err
	}
	for _, serverID := range notify {
		if s.notifier != nil {
			s.notifier.NotifyCommand(task.NamespaceCode, serverID)
		}
	}
	return nil
}

func (s *FileSyncService) createTargetCommand(tx *gorm.DB, task *model.FileSyncTask,
	batch *model.FileSyncBatch, target *model.FileSyncTarget) (*model.AgentCommand, error) {
	payload, _ := json.Marshal(fileSyncCommandPayload{
		TaskID: task.ID, BatchID: batch.ID, TargetID: target.ID, Directory: task.Directory,
	})
	cmd := &model.AgentCommand{
		NamespaceCode: task.NamespaceCode, ServerID: target.ServerID,
		Type: model.CommandTypeFileSyncApply, Payload: string(payload),
		Status: model.CommandStatusPending, Operator: task.Operator,
	}
	return cmd, s.cmdRepo.WithTx(tx).Create(cmd)
}

// ReceiveTargetResult 接收目标 agent 应用结果并推进批次 / 任务状态。
func (s *FileSyncService) ReceiveTargetResult(commandID uint, result FileSyncTargetResult) error {
	cmd, payload, err := s.requireFileSyncCommand(commandID, model.CommandTypeFileSyncApply)
	if err != nil {
		return err
	}
	task, target, err := s.requireTargetResultScope(cmd, payload)
	if err != nil {
		return err
	}
	log, advance, err := s.finishTargetResult(cmd, task, target, result)
	if err != nil {
		return err
	}
	s.publishLog(log)
	if fresh, e := s.repo.GetTarget(target.ID); e == nil && fresh != nil {
		s.publishTarget(fresh)
	}
	if advance.dispatch {
		s.scheduleNextBatch(task.ID, advance.delaySec)
	}
	return nil
}

func (s *FileSyncService) requireTargetResultScope(cmd *model.AgentCommand,
	payload fileSyncCommandPayload) (*model.FileSyncTask, *model.FileSyncTarget, error) {
	task, err := s.requireTask(payload.TaskID)
	if err != nil {
		return nil, nil, err
	}
	if task.Status != model.FileSyncTaskStatusRunning && task.Status != model.FileSyncTaskStatusPaused {
		return nil, nil, apperr.ErrFileSyncTaskState
	}
	target, err := s.repo.GetTarget(payload.TargetID)
	if err != nil {
		return nil, nil, err
	}
	if target == nil || target.TaskID != task.ID || target.CommandID != cmd.ID {
		return nil, nil, apperr.ErrCommandNotFound
	}
	return task, target, nil
}

func (s *FileSyncService) finishTargetResult(cmd *model.AgentCommand, task *model.FileSyncTask,
	target *model.FileSyncTarget, result FileSyncTargetResult) (*model.FileSyncLog, fileSyncBatchAdvance, error) {
	status, cmdStatus, reason := statusFromTargetResult(result)
	now := time.Now().UTC()
	log := &model.FileSyncLog{
		TaskID: task.ID, BatchID: target.BatchID, ServerID: target.ServerID,
		Level: targetLogLevel(status), Message: targetLogMessage(target.ServerID, status, reason),
	}
	var advance fileSyncBatchAdvance
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var e error
		advance, e = s.finishTargetResultLocked(tx, cmd, task, target, result, status, cmdStatus, reason, now, log)
		return e
	})
	if err != nil {
		return nil, fileSyncBatchAdvance{}, err
	}
	return log, advance, nil
}

func (s *FileSyncService) finishTargetResultLocked(tx *gorm.DB, cmd *model.AgentCommand, task *model.FileSyncTask,
	target *model.FileSyncTarget, result FileSyncTargetResult, status, cmdStatus, reason string,
	now time.Time, log *model.FileSyncLog) (fileSyncBatchAdvance, error) {
	repo := s.repo.WithTx(tx)
	ok, err := repo.FinishTarget(target.ID, status, result.BackupPath, result.CurrentFileCount,
		result.ChangedFileCount, result.SkippedFileCount, result.BytesTotal, result.BytesDone, reason, now)
	if err != nil || !ok {
		return fileSyncBatchAdvance{}, errOrState(err)
	}
	if ok, err = s.cmdRepo.WithTx(tx).UpdateStatus(cmd.ID, model.CommandStatusFetched, cmdStatus,
		fileSyncCommandResultDetail(task.ID, target.ID, status, reason)); err != nil || !ok {
		return fileSyncBatchAdvance{}, errOrCommand(err)
	}
	if err = repo.CreateLog(log); err != nil {
		return fileSyncBatchAdvance{}, err
	}
	return s.advanceBatchAfterTargetLocked(repo, task, target.BatchID, now)
}

func errOrState(err error) error {
	if err != nil {
		return err
	}
	return apperr.ErrFileSyncTaskState
}

func errOrCommand(err error) error {
	if err != nil {
		return err
	}
	return apperr.ErrCommandNotFound
}

func fileSyncCommandResultDetail(taskID, targetID uint, status, reason string) string {
	if reason == "" {
		return fmt.Sprintf(`{"taskId":%d,"targetId":%d,"status":%q}`, taskID, targetID, status)
	}
	return fmt.Sprintf(`{"taskId":%d,"targetId":%d,"status":%q,"error":%q}`, taskID, targetID, status, reason)
}

func (s *FileSyncService) advanceBatchAfterTargetLocked(repo *repository.FileSyncRepository,
	task *model.FileSyncTask, batchID uint, now time.Time) (fileSyncBatchAdvance, error) {
	targets, err := repo.ListTargetsByBatch(batchID)
	if err != nil {
		return fileSyncBatchAdvance{}, err
	}
	stats := summarizeFileSyncTargets(targets)
	if err = repo.UpdateBatchCounts(batchID, stats.success, stats.failed); err != nil {
		return fileSyncBatchAdvance{}, err
	}
	if !stats.done {
		return fileSyncBatchAdvance{}, nil
	}
	batchStatus := model.FileSyncBatchStatusSucceeded
	if stats.failed > 0 {
		batchStatus = model.FileSyncBatchStatusFailed
	}
	if err = repo.UpdateBatchStatus(batchID, batchStatus, nil, &now); err != nil {
		return fileSyncBatchAdvance{}, err
	}
	return s.advanceTaskAfterBatchLocked(repo, task, stats, now)
}

func (s *FileSyncService) advanceTaskAfterBatchLocked(repo *repository.FileSyncRepository,
	task *model.FileSyncTask, stats fileSyncTargetStats, now time.Time) (fileSyncBatchAdvance, error) {
	if exceedsFileSyncFailureThreshold(stats.failed, stats.total, task.FailureThresholdPercent) {
		return fileSyncBatchAdvance{}, s.breakCircuitLocked(repo, task.ID, now)
	}
	batches, err := repo.ListBatches(task.ID)
	if err != nil {
		return fileSyncBatchAdvance{}, err
	}
	delaySec := 0
	if firstPendingFileSyncBatch(batches) != nil {
		delaySec = task.IntervalSec
	}
	return fileSyncBatchAdvance{dispatch: true, delaySec: delaySec}, nil
}

func (s *FileSyncService) breakCircuitLocked(repo *repository.FileSyncRepository, taskID uint, now time.Time) error {
	if ok, err := repo.UpdateTaskStatusCAS(taskID, []string{
		model.FileSyncTaskStatusRunning, model.FileSyncTaskStatusPaused,
	}, model.FileSyncTaskStatusCircuitBroken, nil, &now); err != nil || !ok {
		return errOrState(err)
	}
	if err := repo.SkipPendingTargets(taskID); err != nil {
		return err
	}
	return repo.CreateLog(&model.FileSyncLog{
		TaskID: taskID, Level: model.FileSyncLogLevelError,
		Message: "批次失败率超过阈值，已触发熔断并停止后续批次",
	})
}

type fileSyncBatchAdvance struct {
	dispatch bool
	delaySec int
}

type fileSyncTargetStats struct {
	total   int
	success int
	failed  int
	done    bool
}

func summarizeFileSyncTargets(targets []model.FileSyncTarget) fileSyncTargetStats {
	stats := fileSyncTargetStats{total: len(targets), done: true}
	for _, target := range targets {
		switch target.Status {
		case model.FileSyncTargetStatusSucceeded:
			stats.success++
		case model.FileSyncTargetStatusFailed:
			stats.failed++
		default:
			stats.done = false
		}
	}
	return stats
}

func exceedsFileSyncFailureThreshold(failed, total, threshold int) bool {
	return total > 0 && failed*100 > threshold*total
}

func (s *FileSyncService) completeTaskIfSettled(task *model.FileSyncTask) error {
	targets, err := s.repo.ListTargets(task.ID)
	if err != nil {
		return err
	}
	stats := summarizeFileSyncTargets(targets)
	if !stats.done {
		return nil
	}
	status := model.FileSyncTaskStatusSucceeded
	if stats.failed > 0 {
		status = model.FileSyncTaskStatusFailed
	}
	now := time.Now().UTC()
	ok, err := s.repo.UpdateTaskStatusCAS(task.ID, []string{
		model.FileSyncTaskStatusRunning, model.FileSyncTaskStatusPaused,
	}, status, nil, &now)
	if err != nil || !ok {
		return errOrState(err)
	}
	if fresh, e := s.requireTask(task.ID); e == nil {
		s.publishTask(fresh)
	}
	return nil
}

func statusFromTargetResult(result FileSyncTargetResult) (string, string, string) {
	if result.OK {
		return model.FileSyncTargetStatusSucceeded, model.CommandStatusDone, ""
	}
	return model.FileSyncTargetStatusFailed, model.CommandStatusFailed, sanitizeFileSyncError(result.Reason)
}

func targetLogLevel(status string) string {
	if status == model.FileSyncTargetStatusFailed {
		return model.FileSyncLogLevelError
	}
	return model.FileSyncLogLevelInfo
}

func targetLogMessage(serverID, status, reason string) string {
	if status == model.FileSyncTargetStatusFailed {
		return fmt.Sprintf("目标 %s 同步失败：%s", serverID, reason)
	}
	return fmt.Sprintf("目标 %s 同步成功", serverID)
}

func sanitizeFileSyncError(reason string) string {
	text := strings.TrimSpace(reason)
	if text == "" {
		return "agent 执行失败（未提供原因）"
	}
	if len(text) > 480 {
		return text[:480]
	}
	return text
}

func (s *FileSyncService) scheduleNextBatch(taskID uint, delaySec int) {
	if delaySec <= 0 {
		if err := s.dispatchNextBatch(taskID); err != nil {
			slog.Warn("下发下一批文件同步目标失败", "taskId", taskID, "原因", err)
		}
		return
	}
	go func() {
		time.Sleep(time.Duration(delaySec) * time.Second)
		if err := s.dispatchNextBatch(taskID); err != nil {
			slog.Warn("等待后下发下一批文件同步目标失败", "taskId", taskID, "原因", err)
		}
	}()
}
