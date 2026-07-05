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

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

func TestFileSyncBlobUploadDownloadStreamsBytes(t *testing.T) {
	h, taskID := newFileSyncBlobTestHandler(t)
	r := chi.NewRouter()
	r.Put("/file-sync/{taskId}/blobs/{hash}", h.UploadBlob)
	r.Get("/file-sync/{taskId}/blobs/{hash}", h.DownloadBlob)

	content := []byte("binary\x00content\nwith-stream")
	hash := sha256.Sum256(content)
	hashText := hex.EncodeToString(hash[:])
	upload := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hashText), bytes.NewReader(content))
	uploadResp := httptest.NewRecorder()
	r.ServeHTTP(uploadResp, upload)
	if uploadResp.Code != http.StatusNoContent {
		t.Fatalf("上传 blob 应返回 204，实际 %d：%s", uploadResp.Code, uploadResp.Body.String())
	}

	download := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hashText), nil)
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
	if err := db.AutoMigrate(&model.FileSyncTask{}); err != nil {
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
	svc := service.NewFileSyncService(db, repository.NewFileSyncRepository(db), nil, nil, service.NewFileSyncEventHub())
	svc.SetBlobRoot(t.TempDir())
	return NewFileSyncHandler(svc), task.ID
}
