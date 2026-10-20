package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// newFileSyncIdentityTestHandler 构造带权威身份校验器与既有任务的 handler 组合。
// 任务：源 source-1、计划目标 target-1；用于验证四条 v1 数据面端点的身份与归属校验。
func newFileSyncIdentityTestHandler(t *testing.T) (*chi.Mux, uint) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 sqlite 失败：%v", err)
	}
	if err := db.AutoMigrate(&model.FileSyncTask{}, &model.FileSyncTarget{}, &model.AgentCommand{}); err != nil {
		t.Fatalf("迁移测试表失败：%v", err)
	}
	task := &model.FileSyncTask{
		NamespaceCode: "prod", SourceServerID: "source-1", Directory: "plugins/demo",
		Status: model.FileSyncTaskStatusPlanned, BatchSize: 1, IntervalSec: 0,
		FailureThresholdPercent: 20, Operator: "admin",
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("创建测试任务失败：%v", err)
	}
	if err := db.Create(&model.FileSyncTarget{TaskID: task.ID, BatchNo: 1, ServerID: "target-1", Status: model.FileSyncTargetStatusPending}).Error; err != nil {
		t.Fatalf("创建测试目标失败：%v", err)
	}

	svc := service.NewFileSyncService(db, repository.NewFileSyncRepository(db), nil, nil, service.NewFileSyncEventHub())
	svc.SetBlobRoot(t.TempDir())
	h := NewFileSyncHandler(svc)
	h.SetReportAuthenticator(stubFileSyncReportAuth{})

	r := chi.NewRouter()
	r.Post("/file-sync/{taskId}/manifest", h.ReceiveManifest)
	r.Put("/file-sync/{taskId}/blobs/{hash}", h.UploadBlob)
	r.Get("/file-sync/{taskId}/blobs/{hash}", h.DownloadBlob)
	r.Post("/file-sync/{taskId}/targets/result", h.TargetResult)
	return r, task.ID
}

// TestFileSyncDataPlaneRequiresConfirmedIdentity 验证四条 v1 数据面端点在缺身份时一律 401。
//
// 动机：这些端点原先只凭共享 token 就接受请求，而 taskId / commandId 都可枚举，
// 任一 agent 因此能写他人清单、灌 blob、改他人任务状态。
func TestFileSyncDataPlaneRequiresConfirmedIdentity(t *testing.T) {
	r, taskID := newFileSyncIdentityTestHandler(t)
	hash := strings.Repeat("a", 64)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"manifest", http.MethodPost, fmt.Sprintf("/file-sync/%d/manifest", taskID), `{"commandId":1,"files":[]}`},
		{"blob-upload", http.MethodPut, fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hash), "x"},
		{"blob-download", http.MethodGet, fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hash), ""},
		{"target-result", http.MethodPost, fmt.Sprintf("/file-sync/%d/targets/result", taskID), `{"commandId":1,"ok":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 不带任何身份头：必须 401，而不是「当作某台已接入的 agent」放行。
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-Beacon-Token", "test-token")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("缺身份头应 401，实际 %d：%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestFileSyncBlobUploadRejectsNonSourceAgent 验证只有任务的源实例可以上传 blob。
func TestFileSyncBlobUploadRejectsNonSourceAgent(t *testing.T) {
	r, taskID := newFileSyncIdentityTestHandler(t)
	hash := strings.Repeat("b", 64)
	req := fileSyncAgentRequest(http.MethodPut,
		fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hash), "target", strings.NewReader("payload"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非源实例上传应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestFileSyncBlobDownloadRejectsNonTargetAgent 验证只有计划内目标可以下载 blob。
func TestFileSyncBlobDownloadRejectsNonTargetAgent(t *testing.T) {
	r, taskID := newFileSyncIdentityTestHandler(t)
	hash := strings.Repeat("c", 64)
	// source-1 是内容持有者，但不在目标集合内：放开等于允许源自行扩权读缓存。
	req := fileSyncAgentRequest(http.MethodGet,
		fmt.Sprintf("/file-sync/%d/blobs/%s", taskID, hash), "source", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非目标实例下载应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestFileSyncManifestRejectsUnknownIdentity 验证未知身份（非本环境或未确认）被拒。
func TestFileSyncManifestRejectsUnknownIdentity(t *testing.T) {
	r, taskID := newFileSyncIdentityTestHandler(t)
	body, _ := json.Marshal(map[string]any{"commandId": 1, "files": []any{}})
	req := fileSyncAgentRequest(http.MethodPost,
		fmt.Sprintf("/file-sync/%d/manifest", taskID), "unknown", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未知身份应 401，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
