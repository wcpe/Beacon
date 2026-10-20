package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

func TestFileSyncTaskDetailViewIncludesSourcePlanAndTransferFields(t *testing.T) {
	now := time.Now().UTC()
	task := &model.FileSyncTask{
		ID: 42, NamespaceCode: "prod", SourceServerID: "source-1", Directory: "plugins/demo",
		Status: model.FileSyncTaskStatusPlanned, SourceReady: true, SourceFileCount: 3,
		SourceTotalBytes: 4096, BatchSize: 2, IntervalSec: 0, FailureThresholdPercent: 50,
		Operator: "admin", TargetCount: 2, BatchCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	batches := []model.FileSyncBatch{{
		ID: 7, TaskID: task.ID, BatchNo: 1, Status: model.FileSyncBatchStatusPending,
		PlannedCount: 2, SuccessCount: 1, FailedCount: 0,
	}}
	targets := []model.FileSyncTarget{{
		ID: 9, TaskID: task.ID, BatchID: 7, BatchNo: 1, ServerID: "target-1",
		Status: model.FileSyncTargetStatusSucceeded, BackupPath: ".beacon/backups/task-42/target-1",
		CurrentFileCount: 10, ChangedFileCount: 2, SkippedFileCount: 8,
		BytesTotal: 2048, BytesDone: 2048, UpdatedAt: now,
	}}

	view := toFileSyncTaskDetailView(task, batches, targets, nil)

	if !view.SourceReady || view.SourceFileCount != 3 || view.SourceTotalBytes != 4096 {
		t.Fatalf("详情应包含源清单恢复字段，实际：%+v", view)
	}
	if len(view.Batches) != 1 || view.Batches[0].BatchNo != 1 || view.Batches[0].PlannedCount != 2 {
		t.Fatalf("详情应包含批次预览字段，实际：%+v", view.Batches)
	}
	if len(view.Targets) != 1 || view.Targets[0].BackupPath == "" ||
		view.Targets[0].BytesTotal != 2048 || view.Targets[0].BytesDone != 2048 {
		t.Fatalf("目标视图应包含备份与传输字段，实际：%+v", view.Targets)
	}
}

func TestFileSyncBlobUploadDownloadStreamsBytes(t *testing.T) {
	h, taskID := newFileSyncBlobTestHandler(t)
	r := chi.NewRouter()
	r.Put("/file-sync/{taskId}/blobs/{hash}", h.UploadBlob)
	r.Get("/file-sync/{taskId}/blobs/{hash}", h.DownloadBlob)

	content := []byte("binary\x00content\nwith-stream")
	hash := sha256.Sum256(content)
	hashText := hex.EncodeToString(hash[:])
	upload := fileSyncAgentRequest(http.MethodPut,
		fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hashText), "source", bytes.NewReader(content))
	uploadResp := httptest.NewRecorder()
	r.ServeHTTP(uploadResp, upload)
	if uploadResp.Code != http.StatusNoContent {
		t.Fatalf("上传 blob 应返回 204，实际 %d：%s", uploadResp.Code, uploadResp.Body.String())
	}

	download := fileSyncAgentRequest(http.MethodGet,
		fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hashText), "target", nil)
	downloadResp := httptest.NewRecorder()
	r.ServeHTTP(downloadResp, download)
	got, err := io.ReadAll(downloadResp.Result().Body)
	if err != nil {
		t.Fatalf("读取下载响应失败：%v", err)
	}
	if downloadResp.Code != http.StatusOK {
		t.Fatalf("下载 blob 应返回 200，实际 %d：%s", downloadResp.Code, string(got))
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("下载内容应与上传字节一致，实际 %q", string(got))
	}
	if downloadResp.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("下载 blob 应使用二进制内容类型，实际 %s", downloadResp.Header().Get("Content-Type"))
	}
}

func newFileSyncBlobTestHandler(t *testing.T) (*FileSyncHandler, uint) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 sqlite 失败：%v", err)
	}
	if err := db.AutoMigrate(&model.FileSyncTask{}, &model.FileSyncTarget{}); err != nil {
		t.Fatalf("迁移测试表失败：%v", err)
	}
	task := &model.FileSyncTask{
		NamespaceCode: "prod", SourceServerID: "source-1", Directory: "plugins/demo",
		Status: model.FileSyncTaskStatusCached, BatchSize: 1, IntervalSec: 0,
		FailureThresholdPercent: 20, Operator: "admin",
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("创建测试任务失败：%v", err)
	}
	// 下载侧要求身份在该任务的计划目标内，因此补一条目标行。
	target := &model.FileSyncTarget{TaskID: task.ID, BatchNo: 1, ServerID: "target-1", Status: model.FileSyncTargetStatusPending}
	if err := db.Create(target).Error; err != nil {
		t.Fatalf("创建测试目标失败：%v", err)
	}
	svc := service.NewFileSyncService(db, repository.NewFileSyncRepository(db), nil, nil, service.NewFileSyncEventHub())
	svc.SetBlobRoot(t.TempDir())
	h := NewFileSyncHandler(svc)
	// 数据面要求权威身份：这里注入一个固定校验器，测试用 source-1 身份上传、target-1 身份下载。
	h.SetReportAuthenticator(stubFileSyncReportAuth{})
	return h, task.ID
}

// stubFileSyncReportAuth 按请求头里的身份 ID 返回预置身份，供 handler 级测试使用。
type stubFileSyncReportAuth struct{}

func (stubFileSyncReportAuth) AuthenticateAgentReport(_, identityID, _, _ string) (agentauth.Identity, error) {
	switch identityID {
	case "":
		return agentauth.Identity{}, apperr.ErrUnauthorized
	case "source":
		return agentauth.Identity{Namespace: "prod", ServerID: "source-1", IdentityID: "source"}, nil
	case "target":
		return agentauth.Identity{Namespace: "prod", ServerID: "target-1", IdentityID: "target"}, nil
	default:
		return agentauth.Identity{}, apperr.ErrUnauthorized
	}
}

// fileSyncAgentRequest 构造带身份头的请求。
func fileSyncAgentRequest(method, target, identityID string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("X-Beacon-Token", "test-token")
	req.Header.Set("X-Beacon-Identity", identityID)
	req.Header.Set("X-Beacon-Boot", "boot-1")
	return req
}
