package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAssetHandlerPairReadRequiresPrincipal 验证双侧读取申请不会因缺失登录主体而触及领域服务。
func TestAssetHandlerPairReadRequiresPrincipal(t *testing.T) {
	h := &AssetHandler{}
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/assets/pair-read/approval-requests", strings.NewReader(`{"left":{"serverId":"a","path":"a.yml"},"right":{"serverId":"b","path":"b.yml"},"reason":"核验"}`))
	resp := httptest.NewRecorder()
	h.RequestPairReadApproval(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("缺失主体应拒绝双侧读取申请，实际 %d", resp.Code)
	}
}
