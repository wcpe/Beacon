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

// TestAssetHandlerConsumePreviewGrantResolvesFromCommand 验证旧 grant 消费端点
// 不再要求路径里的 serverId（该路由只有 {grantId}），目标一律取自命令本身。
// 此前此处读 chi.URLParam("serverId") 恒为空串，导致该端点永久 400 且不写审计。
func TestAssetHandlerConsumePreviewGrantResolvesFromCommand(t *testing.T) {
	h := NewAssetHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/assets/preview/grants/7/consume", strings.NewReader(`{"commandId":42,"reason":"核验"}`))
	// 路由只声明 {grantId}：故意不注入任何 URL 参数，模拟真实 chi 上下文
	resp := httptest.NewRecorder()
	h.ConsumePreviewGrant(resp, req)
	// 请求解析通过后应到达「服务未装配」检查（500），而不是因缺 serverId 判 400
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("应通过参数解析并因服务未装配失败关闭，实际 %d", resp.Code)
	}

	missingCommand := httptest.NewRequest(http.MethodPost, "/admin/v2/assets/preview/grants/7/consume", strings.NewReader(`{"reason":"核验"}`))
	missingResp := httptest.NewRecorder()
	h.ConsumePreviewGrant(missingResp, missingCommand)
	if missingResp.Code != http.StatusBadRequest {
		t.Fatalf("缺 commandId 应 400，实际 %d", missingResp.Code)
	}
}
