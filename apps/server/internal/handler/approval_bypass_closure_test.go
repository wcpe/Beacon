package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFR209LegacyDangerousHandlersFailClosed 锁定旧入口不会在未批准时下发命令或返回正文。
func TestFR209LegacyDangerousHandlersFailClosed(t *testing.T) {
	cases := []struct {
		name string
		h    http.HandlerFunc
	}{
		{"反向抓取", (&CommandHandler{}).ReverseFetch},
		{"拓印", (&CommandHandler{}).Imprint},
		{"拓印差异", (&CommandHandler{}).ImprintDiff},
		{"文件浏览", (&BrowseHandler{}).Browse},
		{"实时日志申请", (&AgentLogHandler{}).Request},
		{"实时日志正文", (&AgentLogHandler{}).Get},
		{"资产预览", (&AssetHandler{}).Preview},
		{"资产差异", (&AssetHandler{}).Diff},
		{"反向扫描", (&ReverseFetchTaskHandler{}).CreateScanTask},
		{"反向提交", (&ReverseFetchTaskHandler{}).SubmitTask},
		{"反向冲突正文", (&ReverseFetchTaskHandler{}).ConflictDiff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.h(rec, httptest.NewRequest(http.MethodPost, "/admin/v1/test", nil))
			if rec.Code != http.StatusConflict {
				t.Fatalf("旧危险入口应失败关闭为 409，实际 %d：%s", rec.Code, rec.Body.String())
			}
		})
	}
}
