package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLegacyZoneAssignmentWriteReturnsMigrated 验证 V1 指派写处理器只返回迁移错误、不调用 v2 审批适配器。
// 归属真源已迁到 server.zone_id，写 v1 退役表会「成功返回但无人认」，故两个写端点恒回 410 引导改用 v2。
func TestLegacyZoneAssignmentWriteReturnsMigrated(t *testing.T) {
	handlers := []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{name: "PUT 指派", method: http.MethodPut, handler: (&ZoneHandler{}).Assign},
		{name: "DELETE 取消指派", method: http.MethodDelete, handler: (&ZoneHandler{}).Unassign},
	}
	for _, tt := range handlers {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			// 零值 ZoneHandler（v2svc 为 nil）：若处理器仍走审批适配器会 panic，据此守护「不再受理」。
			req := httptest.NewRequest(tt.method, "/admin/v1/zones/assignments", nil)
			tt.handler(recorder, req)

			var body map[string]any
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("解析迁移错误响应失败: %v", err)
			}
			if recorder.Code != http.StatusGone || body["code"] != "zone_assignment_migrated" {
				t.Fatalf("V1 指派写端点应返回 410 zone_assignment_migrated，实际 %d：%v", recorder.Code, body)
			}
		})
	}
}
