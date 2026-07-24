package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/update"
)

// updateStatusCore 是状态端点测试用更新核心，仅返回预置进度。
type updateStatusCore struct {
	progress update.Progress
}

func (c *updateStatusCore) CheckForUpdate(context.Context, update.Channel, string, string, string) (update.CheckResult, error) {
	return update.CheckResult{}, nil
}

func (c *updateStatusCore) ApplyUpdate(context.Context, update.Channel, string, string, string) error {
	return nil
}

func (c *updateStatusCore) Snapshot() update.Progress { return c.progress }

func (c *updateStatusCore) TestProxy(context.Context, string) error { return nil }

func (c *updateStatusCore) RollbackAvailable() bool { return false }

func (c *updateStatusCore) Rollback(string, string) error { return nil }

// updateStatusSettings 是状态端点测试用空设置读取器。
type updateStatusSettings struct{}

func (updateStatusSettings) GetString(string) string { return "" }

func (updateStatusSettings) GetInt(string) int { return 0 }

// TestUpdateStatusRedactsPresignedDownloadError 确保状态响应不会再次暴露进度中的预签名 URL 凭据。
func TestUpdateStatusRedactsPresignedDownloadError(t *testing.T) {
	rawError := `下载资产失败: Get "https://objects.githubusercontent.com/releases/beacon?X-Amz-Signature=signature-raw&X-AMZ-CREDENTIAL=credential-raw&x-amz-security-token=session-raw&token=github-token-raw&API_KEY=github-api-key-raw&download=1": 连接被重置`
	core := &updateStatusCore{progress: update.Progress{
		Phase:         update.PhaseFailed,
		TargetVersion: "v9.9.9",
		Error:         rawError,
	}}
	h := NewUpdateHandler(service.NewUpdateService(core, updateStatusSettings{}))

	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/system/update", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态接口应返回 200，实际 %d", rec.Code)
	}
	var body progressView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析状态响应失败: %v", err)
	}
	for _, secret := range []string{
		"signature-raw", "credential-raw", "session-raw", "github-token-raw", "github-api-key-raw",
	} {
		if strings.Contains(body.Error, secret) {
			t.Errorf("状态响应泄露凭据 %q：%s", secret, body.Error)
		}
	}
	for _, diagnostic := range []string{
		"下载资产失败", "objects.githubusercontent.com/releases/beacon", "download=1", "连接被重置",
	} {
		if !strings.Contains(body.Error, diagnostic) {
			t.Errorf("状态响应丢失正常失败诊断 %q：%s", diagnostic, body.Error)
		}
	}
}
