package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLegacyNamespaceDeleteReturnsMigrated 验证 V1/V2 旧删除处理器只返回迁移错误，不调用服务层。
func TestLegacyNamespaceDeleteReturnsMigrated(t *testing.T) {
	handlers := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "V1", handler: (&NamespaceHandler{}).Delete},
		{name: "V2", handler: (&V2ControlPlaneHandler{}).DeleteNamespace},
	}
	for _, tt := range handlers {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/admin/"+tt.name+"/namespaces/1", nil)
			tt.handler(recorder, req)

			var body map[string]any
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("解析迁移错误响应失败: %v", err)
			}
			if recorder.Code != http.StatusGone || body["code"] != "namespace_delete_migrated" {
				t.Fatalf("旧删除应返回 410 namespace_delete_migrated，实际 %d：%v", recorder.Code, body)
			}
		})
	}
}
