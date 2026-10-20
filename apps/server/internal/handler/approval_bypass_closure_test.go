package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFR209LegacyDangerousHandlersFailClosed 区分仍审批的危险入口与已直执的读取入口。
func TestFR209LegacyDangerousHandlersFailClosed(t *testing.T) {
	cases := []struct {
		name       string
		h          http.HandlerFunc
		method     string
		target     string
		wantStatus int
	}{
		{"反向抓取", (&CommandHandler{}).ReverseFetch, http.MethodPost, "/admin/v1/test", http.StatusConflict},
		{"拓印", (&CommandHandler{}).Imprint, http.MethodPost, "/admin/v1/test", http.StatusConflict},
		{"拓印差异", (&CommandHandler{}).ImprintDiff, http.MethodPost, "/admin/v1/test", http.StatusConflict},
		{"文件浏览直读入口", (&BrowseHandler{}).Browse, http.MethodGet, "/admin/v1/instances/test/browse", http.StatusBadRequest},
		{"实时日志申请直执", (&AgentLogHandler{}).Request, http.MethodPost, "/admin/v1/instances/test/logs", http.StatusInternalServerError},
		{"实时日志正文直读", (&AgentLogHandler{}).Get, http.MethodGet, "/admin/v1/instances/test/logs?namespace=prod", http.StatusInternalServerError},
		{"资产预览直读", (&AssetHandler{}).Preview, http.MethodPost, "/admin/v2/assets/preview", http.StatusBadRequest},
		{"资产差异直读", (&AssetHandler{}).Diff, http.MethodPost, "/admin/v2/assets/diff", http.StatusBadRequest},
		{"反向扫描", (&ReverseFetchTaskHandler{}).CreateScanTask, http.MethodPost, "/admin/v1/test", http.StatusConflict},
		{"反向提交", (&ReverseFetchTaskHandler{}).SubmitTask, http.MethodPost, "/admin/v1/test", http.StatusConflict},
		{"反向冲突正文", (&ReverseFetchTaskHandler{}).ConflictDiff, http.MethodPost, "/admin/v1/test", http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.h(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("入口状态码不符，期望 %d，实际 %d：%s", tc.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}
