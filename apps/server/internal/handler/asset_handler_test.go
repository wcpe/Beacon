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
