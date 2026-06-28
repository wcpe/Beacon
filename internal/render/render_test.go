package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wcpe/Beacon/internal/apperr"
)

// 解析错误响应体到 errorBody。
func decodeErrBody(t *testing.T, body []byte) errorBody {
	t.Helper()
	var eb errorBody
	if err := json.Unmarshal(body, &eb); err != nil {
		t.Fatalf("解析错误体失败: %v（原始 %s）", err, body)
	}
	return eb
}

func TestWriteError_AppErr原样返回(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/x", nil)
	WriteError(rr, req, apperr.ErrNamespaceConflict)

	if rr.Code != http.StatusConflict {
		t.Fatalf("状态码 = %d，期望 %d", rr.Code, http.StatusConflict)
	}
	eb := decodeErrBody(t, rr.Body.Bytes())
	if eb.Code != "NAMESPACE_CONFLICT" || eb.Message != "同名环境已存在" {
		t.Fatalf("领域错误应原样返回，得到 code=%q message=%q", eb.Code, eb.Message)
	}
}

func TestWriteError_内部错误返回脱敏真因(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/system/update", nil)
	// 非 apperr 的内部错误：应返回脱敏后的真实原因（FR-122/ADR-0057），不再笼统「内部错误」。
	WriteError(rr, req, errors.New(`下载资产失败: 写临时文件失败: context canceled`))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d，期望 500", rr.Code)
	}
	eb := decodeErrBody(t, rr.Body.Bytes())
	if eb.Code != "INTERNAL" {
		t.Fatalf("code = %q，期望 INTERNAL", eb.Code)
	}
	// 不再是笼统「内部错误」，而是真实原因
	if eb.Message == "内部错误" {
		t.Fatal("内部错误仍返回笼统「内部错误」，未展示真实原因（违背 FR-122）")
	}
	if eb.Message != `下载资产失败: 写临时文件失败: context canceled` {
		t.Fatalf("message 应为真实原因，得到 %q", eb.Message)
	}
}

func TestWriteError_内部错误脱敏凭据(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/settings/x", nil)
	// 错误信息里混入 proxy 账密：对外展示必须打码（不泄露凭据）。
	WriteError(rr, req, fmt.Errorf(`构造出站客户端失败: proxy "http://admin:s3cr3t@10.0.0.5:7890" 不可达`))

	eb := decodeErrBody(t, rr.Body.Bytes())
	if want := `构造出站客户端失败: proxy "http://admin:***@10.0.0.5:7890" 不可达`; eb.Message != want {
		t.Fatalf("凭据未脱敏\n  得到 %q\n  期望 %q", eb.Message, want)
	}
}
