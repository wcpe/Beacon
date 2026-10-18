package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAssetHandlerPairReadRequiresService 验证双侧直读缺少领域服务时失败关闭。
func TestAssetHandlerPairReadRequiresService(t *testing.T) {
	h := &AssetHandler{}
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/assets/pair-read/approval-requests", strings.NewReader(`{"left":{"serverId":"a","path":"a.yml"},"right":{"serverId":"b","path":"b.yml"},"reason":"核验"}`))
	resp := httptest.NewRecorder()
	h.RequestPairReadApproval(resp, req)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("缺少服务装配时应失败关闭，实际 %d", resp.Code)
	}
}

// TestAssetHandlerPreviewAcceptsNamespaceID 预览请求接受数值 namespaceId，并校验其 JSON 类型。
func TestAssetHandlerPreviewAcceptsNamespaceID(t *testing.T) {
	h := NewAssetHandler(nil)
	valid := httptest.NewRequest(http.MethodPost, "/admin/v2/assets/preview", strings.NewReader(`{"namespaceId":2,"serverId":"shared","path":"a.yml"}`))
	validResponse := httptest.NewRecorder()
	h.Preview(validResponse, valid)
	if validResponse.Code != http.StatusInternalServerError {
		t.Fatalf("数值 namespaceId 应通过请求解析后到达服务装配检查，实际 %d", validResponse.Code)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/admin/v2/assets/preview", strings.NewReader(`{"namespaceId":"staging","serverId":"shared","path":"a.yml"}`))
	invalidResponse := httptest.NewRecorder()
	h.Preview(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("非数值 namespaceId 应拒绝，实际 %d", invalidResponse.Code)
	}
}
