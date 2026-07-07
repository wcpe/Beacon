package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// FileSyncHandler 处理多级灰度文件同步管理端 API（FR-129/FR-131）。
type FileSyncHandler struct {
	svc *service.FileSyncService
}

// NewFileSyncHandler 构造处理器。
func NewFileSyncHandler(svc *service.FileSyncService) *FileSyncHandler {
	return &FileSyncHandler{svc: svc}
}

type fileSyncCreateRequest struct {
	Namespace               string `json:"namespace"`
	SourceServerID          string `json:"sourceServerId"`
	Directory               string `json:"directory"`
	BatchSize               int    `json:"batchSize"`
	IntervalSec             int    `json:"intervalSec"`
	FailureThreshold        int    `json:"failureThreshold"`
	FailureThresholdPercent int    `json:"failureThresholdPercent"`
}

type fileSyncPlanRequest struct {
	TargetServerIDs []string `json:"targetServerIds"`
}

type fileSyncManifestRequest struct {
	CommandID uint                           `json:"commandId"`
	Files     []service.FileSyncManifestFile `json:"files"`
}

type fileSyncTargetResultRequest struct {
	CommandID        uint   `json:"commandId"`
	OK               bool   `json:"ok"`
	Reason           string `json:"reason"`
	BackupPath       string `json:"backupPath"`
	CurrentFileCount int    `json:"currentFileCount"`
	ChangedFileCount int    `json:"changedFileCount"`
	SkippedFileCount int    `json:"skippedFileCount"`
	BytesTotal       int64  `json:"bytesTotal"`
	BytesDone        int64  `json:"bytesDone"`
}

type fileSyncTaskView struct {
	ID                      string               `json:"id"`
	Namespace               string               `json:"namespace"`
	SourceServerID          string               `json:"sourceServerId"`
	Directory               string               `json:"directory"`
	Status                  string               `json:"status"`
	SourceReady             bool                 `json:"sourceReady"`
	SourceFileCount         int                  `json:"sourceFileCount"`
	SourceTotalBytes        int64                `json:"sourceTotalBytes"`
	BatchSize               int                  `json:"batchSize"`
	IntervalSec             int                  `json:"intervalSec"`
	FailureThresholdPercent int                  `json:"failureThresholdPercent"`
	Operator                string               `json:"operator"`
	TotalTargets            int                  `json:"totalTargets"`
	PlannedTargets          int                  `json:"plannedTargets"`
	SucceededTargets        int                  `json:"succeededTargets"`
	FailedTargets           int                  `json:"failedTargets"`
	SkippedTargets          int                  `json:"skippedTargets"`
	CurrentBatch            int                  `json:"currentBatch"`
	TotalBatches            int                  `json:"totalBatches"`
	LastError               string               `json:"lastError"`
	Batches                 []fileSyncBatchView  `json:"batches"`
	Logs                    []fileSyncLogView    `json:"logs"`
	Targets                 []fileSyncTargetView `json:"targets"`
	StartedAt               string               `json:"startedAt,omitempty"`
	FinishedAt              string               `json:"finishedAt,omitempty"`
	CreatedAt               string               `json:"createdAt"`
	UpdatedAt               string               `json:"updatedAt"`
}

type fileSyncBatchView struct {
	ID           uint   `json:"id"`
	TaskID       uint   `json:"taskId"`
	BatchNo      int    `json:"batchNo"`
	Status       string `json:"status"`
	PlannedCount int    `json:"plannedCount"`
	SuccessCount int    `json:"successCount"`
	FailedCount  int    `json:"failedCount"`
}

type fileSyncTargetView struct {
	ID               uint   `json:"id,omitempty"`
	TaskID           string `json:"taskId"`
	BatchID          uint   `json:"batchId"`
	BatchNo          int    `json:"batchNo"`
	ServerID         string `json:"serverId"`
	Namespace        string `json:"namespace"`
	Group            string `json:"group"`
	Zone             string `json:"zone"`
	Status           string `json:"status"`
	BackupPath       string `json:"backupPath"`
	CurrentFileCount int    `json:"currentFileCount"`
	ChangedFileCount int    `json:"changedFileCount"`
	SkippedFileCount int    `json:"skippedFileCount"`
	BytesTotal       int64  `json:"bytesTotal"`
	BytesDone        int64  `json:"bytesDone"`
	Error            string `json:"error"`
	UpdatedAt        string `json:"updatedAt"`
}

type fileSyncLogView struct {
	ID        uint   `json:"id,omitempty"`
	TaskID    string `json:"taskId"`
	BatchNo   int    `json:"batchNo"`
	ServerID  string `json:"serverId,omitempty"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	CreatedAt string `json:"createdAt"`
}

// Create 处理 POST /admin/v1/file-sync/tasks。
func (h *FileSyncHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req fileSyncCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	threshold := req.FailureThresholdPercent
	if threshold == 0 {
		threshold = req.FailureThreshold
	}
	task, err := h.svc.CreateTask(service.CreateFileSyncTaskParams{
		Namespace: req.Namespace, SourceServerID: req.SourceServerID, Directory: req.Directory,
		BatchSize: req.BatchSize, IntervalSec: req.IntervalSec, FailureThresholdPercent: threshold,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	h.writeTaskDetail(w, r, http.StatusCreated, task.ID)
}

// List 处理 GET /admin/v1/file-sync/tasks。
func (h *FileSyncHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tasks, err := h.svc.List(q.Get("namespace"), q.Get("status"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]fileSyncTaskView, 0, len(tasks))
	for i := range tasks {
		items = append(items, toFileSyncTaskView(&tasks[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// Get 处理 GET /admin/v1/file-sync/tasks/{id}。
func (h *FileSyncHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	task, batches, targets, logs, err := h.svc.Get(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toFileSyncTaskDetailView(task, batches, targets, logs))
}

// Plan 处理 POST /admin/v1/file-sync/tasks/{id}/plan。
func (h *FileSyncHandler) Plan(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req fileSyncPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	task, err := h.svc.PlanTargets(id, req.TargetServerIDs, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	h.writeTaskDetail(w, r, http.StatusOK, task.ID)
}

// Start 处理 POST /admin/v1/file-sync/tasks/{id}/start。
func (h *FileSyncHandler) Start(w http.ResponseWriter, r *http.Request) {
	h.control(w, r, (*service.FileSyncService).Start)
}

// Pause 处理 POST /admin/v1/file-sync/tasks/{id}/pause。
func (h *FileSyncHandler) Pause(w http.ResponseWriter, r *http.Request) {
	h.control(w, r, (*service.FileSyncService).Pause)
}

// Resume 处理 POST /admin/v1/file-sync/tasks/{id}/resume。
func (h *FileSyncHandler) Resume(w http.ResponseWriter, r *http.Request) {
	h.control(w, r, (*service.FileSyncService).Resume)
}

// Terminate 处理 POST /admin/v1/file-sync/tasks/{id}/terminate。
func (h *FileSyncHandler) Terminate(w http.ResponseWriter, r *http.Request) {
	h.control(w, r, (*service.FileSyncService).Terminate)
}

func (h *FileSyncHandler) control(w http.ResponseWriter, r *http.Request,
	fn func(*service.FileSyncService, uint, string, string) (*model.FileSyncTask, error)) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	task, err := fn(h.svc, id, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	h.writeTaskDetail(w, r, http.StatusOK, task.ID)
}

// Events 处理 GET /admin/v1/file-sync/tasks/{id}/events。
func (h *FileSyncHandler) Events(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	afterID, err := parseAfterLogID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if _, _, _, _, err := h.svc.Get(id); err != nil {
		render.WriteError(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		render.WriteError(w, r, apperr.ErrStreamingUnsupported)
		return
	}
	writeFileSyncSSEHeaders(w)
	flusher.Flush()
	_ = h.svc.RunEvents(r.Context(), id, afterID, &fileSyncFlushSink{w: w, flusher: flusher})
}

// ReceiveManifest 处理源 agent 的文件清单回传。
func (h *FileSyncHandler) ReceiveManifest(w http.ResponseWriter, r *http.Request) {
	if _, ok := parseUintParam(w, r, "taskId"); !ok {
		return
	}
	var req fileSyncManifestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CommandID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if err := h.svc.ReceiveSourceManifest(req.CommandID, req.Files, clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UploadBlob 处理源 agent 流式上传文件 blob。
func (h *FileSyncHandler) UploadBlob(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseUintParam(w, r, "taskId")
	if !ok {
		return
	}
	if err := h.svc.StoreBlob(taskID, chi.URLParam(r, "hash"), r.Body); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DownloadBlob 处理目标 agent 流式下载文件 blob。
func (h *FileSyncHandler) DownloadBlob(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseUintParam(w, r, "taskId")
	if !ok {
		return
	}
	file, size, err := h.svc.OpenBlob(taskID, chi.URLParam(r, "hash"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

// TargetResult 处理目标 agent 的同步结果回传。
func (h *FileSyncHandler) TargetResult(w http.ResponseWriter, r *http.Request) {
	if _, ok := parseUintParam(w, r, "taskId"); !ok {
		return
	}
	var req fileSyncTargetResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CommandID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result := service.FileSyncTargetResult{
		OK: req.OK, Reason: req.Reason, BackupPath: req.BackupPath,
		CurrentFileCount: req.CurrentFileCount, ChangedFileCount: req.ChangedFileCount,
		SkippedFileCount: req.SkippedFileCount, BytesTotal: req.BytesTotal, BytesDone: req.BytesDone,
	}
	if err := h.svc.ReceiveTargetResult(req.CommandID, result); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileSyncHandler) writeTaskDetail(w http.ResponseWriter, r *http.Request, status int, taskID uint) {
	task, batches, targets, logs, err := h.svc.Get(taskID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, status, toFileSyncTaskDetailView(task, batches, targets, logs))
}

func parseAfterLogID(r *http.Request) (uint, error) {
	raw := r.URL.Query().Get("afterLogId")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, apperr.ErrInvalidParam
	}
	return uint(n), nil
}

func writeFileSyncSSEHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream; charset=utf-8")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("Connection", "keep-alive")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

type fileSyncFlushSink struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (s *fileSyncFlushSink) Send(evt service.FileSyncEvent) error {
	raw, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", evt.Type, raw); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func toFileSyncTaskView(task *model.FileSyncTask) fileSyncTaskView {
	return fileSyncTaskView{
		ID: fmt.Sprintf("%d", task.ID), Namespace: task.NamespaceCode, SourceServerID: task.SourceServerID,
		Directory: task.Directory, Status: task.Status, BatchSize: task.BatchSize,
		SourceReady: task.SourceReady, SourceFileCount: task.SourceFileCount, SourceTotalBytes: task.SourceTotalBytes,
		IntervalSec: task.IntervalSec, FailureThresholdPercent: task.FailureThresholdPercent,
		Operator: task.Operator, TotalTargets: task.TargetCount, PlannedTargets: task.TargetCount,
		TotalBatches: task.BatchCount, Batches: []fileSyncBatchView{}, Logs: []fileSyncLogView{}, Targets: []fileSyncTargetView{},
		StartedAt: formatOptionalTime(task.StartedAt), FinishedAt: formatOptionalTime(task.FinishedAt),
		CreatedAt: task.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: task.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toFileSyncTaskDetailView(task *model.FileSyncTask, batches []model.FileSyncBatch,
	targets []model.FileSyncTarget, logs []model.FileSyncLog) fileSyncTaskView {
	view := toFileSyncTaskView(task)
	view.Batches = toFileSyncBatchViews(batches)
	view.Targets = toFileSyncTargetViews(task.NamespaceCode, targets)
	view.Logs = toFileSyncLogViews(logs)
	view.SucceededTargets = countFileSyncTargets(targets, model.FileSyncTargetStatusSucceeded)
	view.FailedTargets = countFileSyncTargets(targets, model.FileSyncTargetStatusFailed)
	view.SkippedTargets = countFileSyncTargets(targets, model.FileSyncTargetStatusSkipped)
	view.CurrentBatch = currentFileSyncBatch(task.Status, batches)
	return view
}

func toFileSyncBatchViews(batches []model.FileSyncBatch) []fileSyncBatchView {
	views := make([]fileSyncBatchView, 0, len(batches))
	for _, batch := range batches {
		views = append(views, fileSyncBatchView{
			ID: batch.ID, TaskID: batch.TaskID, BatchNo: batch.BatchNo, Status: batch.Status,
			PlannedCount: batch.PlannedCount, SuccessCount: batch.SuccessCount, FailedCount: batch.FailedCount,
		})
	}
	return views
}

func toFileSyncTargetViews(namespace string, targets []model.FileSyncTarget) []fileSyncTargetView {
	views := make([]fileSyncTargetView, 0, len(targets))
	for _, target := range targets {
		views = append(views, fileSyncTargetView{
			ID: target.ID, TaskID: fmt.Sprintf("%d", target.TaskID), BatchID: target.BatchID, BatchNo: target.BatchNo,
			ServerID: target.ServerID, Namespace: namespace, Status: target.Status, CurrentFileCount: target.CurrentFileCount,
			ChangedFileCount: target.ChangedFileCount, SkippedFileCount: target.SkippedFileCount,
			BackupPath: target.BackupPath, BytesTotal: target.BytesTotal, BytesDone: target.BytesDone,
			Error: target.LastError, UpdatedAt: target.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return views
}

func toFileSyncLogViews(logs []model.FileSyncLog) []fileSyncLogView {
	views := make([]fileSyncLogView, 0, len(logs))
	for _, log := range logs {
		views = append(views, fileSyncLogView{
			ID: log.ID, TaskID: fmt.Sprintf("%d", log.TaskID), ServerID: log.ServerID,
			Level: strings.ToUpper(log.Level), Message: log.Message, CreatedAt: log.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return views
}

func countFileSyncTargets(targets []model.FileSyncTarget, status string) int {
	count := 0
	for _, target := range targets {
		if target.Status == status {
			count++
		}
	}
	return count
}

func currentFileSyncBatch(status string, batches []model.FileSyncBatch) int {
	if status != model.FileSyncTaskStatusRunning && status != model.FileSyncTaskStatusPaused {
		return 0
	}
	for _, batch := range batches {
		if batch.Status == model.FileSyncBatchStatusRunning {
			return batch.BatchNo
		}
	}
	for _, batch := range batches {
		if batch.Status == model.FileSyncBatchStatusPending {
			return batch.BatchNo
		}
	}
	return 0
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
