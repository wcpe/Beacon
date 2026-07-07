package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFileEffectiveParamValidation 验证 GET /admin/v1/files/effective 的参数早校验：
// 缺 namespace、或 serverId 与 group 都空 → 400（在触达 service 前返回，故 handler 可不接 service）。
func TestFileEffectiveParamValidation(t *testing.T) {
	h := &FileHandler{} // 仅测参数校验早返回分支，不调用 effSvc
	cases := []struct {
		name  string
		query string
	}{
		{"全缺", ""},
		{"仅 namespace", "?namespace=prod"},
		{"仅 serverId 缺 namespace", "?serverId=s1"},
		{"仅 group 缺 namespace", "?group=area1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/admin/v1/files/effective"+c.query, nil)
			w := httptest.NewRecorder()
			h.Effective(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("应返回 400，实际 %d（body=%s）", w.Code, w.Body.String())
			}
		})
	}
}
