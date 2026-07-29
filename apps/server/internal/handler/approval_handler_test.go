package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// approvalServiceStub 是审批处理器的服务替身。
type approvalServiceStub struct {
	requested authz.Operation
	approved  string
	rejected  string
	withdrawn string
}

func (s *approvalServiceStub) Request(op authz.Operation, _ map[string]any, _ auth.Principal, _ string) (model.ApprovalRequest, error) {
	s.requested = op
	return model.ApprovalRequest{ID: 7, RequestID: "apr_test", OperationKey: op.Kind, OperationKind: op.Kind, ResourceType: op.Resource, ResourceID: op.ResourceID, Status: model.ApprovalStatusPending}, nil
}

func (s *approvalServiceStub) List(_ service.ApprovalListFilter, _ auth.Principal) ([]model.ApprovalRequest, error) {
	return []model.ApprovalRequest{{ID: 7, RequestID: "apr_test", Status: model.ApprovalStatusPending}}, nil
}

func (s *approvalServiceStub) Detail(ref string, _ auth.Principal) (model.ApprovalRequest, error) {
	return model.ApprovalRequest{ID: 7, RequestID: ref, Status: model.ApprovalStatusPending}, nil
}

func (s *approvalServiceStub) Approve(ref string, _ auth.Principal, _ string) (model.ApprovalRequest, error) {
	s.approved = ref
	return model.ApprovalRequest{ID: 7, RequestID: ref, Status: model.ApprovalStatusSucceeded}, nil
}

func (s *approvalServiceStub) Reject(ref string, _ auth.Principal, _ string, reason string) (model.ApprovalRequest, error) {
	s.rejected = ref
	return model.ApprovalRequest{ID: 7, RequestID: ref, Status: model.ApprovalStatusRejected, RejectReason: reason}, nil
}

func (s *approvalServiceStub) Withdraw(ref string, _ auth.Principal, _ string) (model.ApprovalRequest, error) {
	s.withdrawn = ref
	return model.ApprovalRequest{ID: 7, RequestID: ref, Status: model.ApprovalStatusWithdrawn}, nil
}

// TestApprovalHandlerRequest 解析统一提审请求并优先使用 header 幂等键。
func TestApprovalHandlerRequest(t *testing.T) {
	svc := &approvalServiceStub{}
	h := NewApprovalHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/approval-requests", strings.NewReader(`{"operationKey":"delivery.approve","resourceType":"change-order","resourceId":"42","idempotencyKey":"body-idem","reason":"需要执行","parameters":{"id":42}}`))
	req.Header.Set("Idempotency-Key", "header-idem")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("alice")))
	w := httptest.NewRecorder()
	h.Request(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("提审应返回 202，实际 %d", w.Code)
	}
	if svc.requested.Kind != authz.OperationDeliveryApprove || svc.requested.Resource != model.TargetTypeChangeOrder || svc.requested.ResourceID != "42" || svc.requested.IdempotencyKey != "header-idem" || svc.requested.Reason != "需要执行" {
		t.Fatalf("提审操作解析不符：%+v", svc.requested)
	}
	if got := w.Header().Get("Location"); got != "/admin/v2/approval-requests/apr_test" {
		t.Fatalf("Location 不符：%q", got)
	}
}

// TestApprovalHandlerListDetailApproveRejectWithdraw 解析新正式路由并调用服务。
func TestApprovalHandlerListDetailApproveRejectWithdraw(t *testing.T) {
	svc := &approvalServiceStub{}
	h := NewApprovalHandler(svc)
	r := chi.NewRouter()
	r.Get("/admin/v2/approval-requests", h.List)
	r.Get("/admin/v2/approval-requests/{requestId}", h.Detail)
	r.Post("/admin/v2/approval-requests/{requestId}/approve", h.Approve)
	r.Post("/admin/v2/approval-requests/{requestId}/reject", h.Reject)
	r.Post("/admin/v2/approval-requests/{requestId}/withdraw", h.Withdraw)

	principal := auth.HumanPrincipal("bob")
	for _, tc := range []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/admin/v2/approval-requests", "", http.StatusOK},
		{http.MethodGet, "/admin/v2/approval-requests/apr_11", "", http.StatusOK},
		{http.MethodPost, "/admin/v2/approval-requests/apr_11/approve", "", http.StatusAccepted},
		{http.MethodPost, "/admin/v2/approval-requests/apr_12/reject", `{"reason":"风险过高"}`, http.StatusOK},
		{http.MethodPost, "/admin/v2/approval-requests/apr_13/withdraw", "", http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		if resp.Code != tc.want {
			t.Fatalf("%s %s 状态应为 %d，实际 %d", tc.method, tc.path, tc.want, resp.Code)
		}
	}
	if svc.approved != "apr_11" || svc.rejected != "apr_12" || svc.withdrawn != "apr_13" {
		t.Fatalf("路径参数传递不符：approve=%s reject=%s withdraw=%s", svc.approved, svc.rejected, svc.withdrawn)
	}
}
