package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// approvalServiceStub 是审批处理器的服务替身。
type approvalServiceStub struct {
	pageFilter service.ApprovalListFilter
	approved   string
	rejected   string
	withdrawn  string
	redeemed   string
}

func (s *approvalServiceStub) List(_ service.ApprovalListFilter, _ auth.Principal) ([]model.ApprovalRequest, error) {
	return []model.ApprovalRequest{{ID: 7, RequestID: "apr_test", Status: model.ApprovalStatusPending}}, nil
}

func (s *approvalServiceStub) ListPage(filter service.ApprovalListFilter, _ auth.Principal) ([]model.ApprovalRequest, int64, error) {
	s.pageFilter = filter
	return []model.ApprovalRequest{{ID: 7, RequestID: "apr_test", Status: model.ApprovalStatusPending}}, 7, nil
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

func (s *approvalServiceStub) RedeemCredentialSecret(ref string, _ auth.Principal) (string, error) {
	s.redeemed = ref
	return "bk_test", nil
}

// TestApprovalHandlerListPagination 解析分页参数并返回 total。
func TestApprovalHandlerListPagination(t *testing.T) {
	svc := &approvalServiceStub{}
	h := NewApprovalHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/admin/v2/approval-requests?page=2&pageSize=3", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("bob")))
	resp := httptest.NewRecorder()
	h.List(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("分页列表应返回 200，实际 %d", resp.Code)
	}
	if svc.pageFilter.Page != 2 || svc.pageFilter.PageSize != 3 || !strings.Contains(resp.Body.String(), `"total":7`) {
		t.Fatalf("分页契约不符：filter=%+v body=%s", svc.pageFilter, resp.Body.String())
	}
}

// TestApprovalHandlerListExposesSafeReadModel 验证列表只返回安全读模型、权限和服务端筛选。
func TestApprovalHandlerListExposesSafeReadModel(t *testing.T) {
	svc := &approvalServiceStub{}
	h := NewApprovalHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/admin/v2/approval-requests?namespaceId=global&keyword=归档&createdFrom=2026-08-01T00:00:00Z&createdTo=2026-08-02T00:00:00Z", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("bob")))
	resp := httptest.NewRecorder()
	h.List(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("列表应返回 200，实际 %d", resp.Code)
	}
	if !svc.pageFilter.GlobalOnly || svc.pageFilter.Keyword != "归档" || svc.pageFilter.CreatedFrom == nil || svc.pageFilter.CreatedTo == nil {
		t.Fatalf("安全筛选未传入服务层：%+v", svc.pageFilter)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil || len(body.Items) != 1 {
		t.Fatalf("列表响应解析失败：err=%v body=%s", err, resp.Body.String())
	}
	item := body.Items[0]
	if item["canApprove"] != true || item["evidenceStatus"] != "unavailable" {
		t.Fatalf("安全读模型字段不符：%+v", item)
	}
	if _, leaked := item["payload"]; leaked {
		t.Fatalf("列表不得泄露冻结载荷：%+v", item)
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

func TestApprovalHandlerRedeemCredentialSecret(t *testing.T) {
	svc := &approvalServiceStub{}
	r := chi.NewRouter()
	r.Post("/admin/v2/approval-requests/{requestId}/credential-secret/redeem", NewApprovalHandler(svc, svc).RedeemCredentialSecret)
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/approval-requests/apr_secret/credential-secret/redeem", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("alice")))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || svc.redeemed != "apr_secret" || !strings.Contains(resp.Body.String(), `"secret":"bk_test"`) {
		t.Fatalf("一次性兑换路由不符：code=%d request=%q body=%s", resp.Code, svc.redeemed, resp.Body.String())
	}
}

// TestApprovalHandlerDetailExposesActivePayloadGrantToRequester 验证成功审批仅向原申请人投影可消费授权，不泄露正文。
func TestApprovalHandlerDetailExposesActivePayloadGrantToRequester(t *testing.T) {
	db := openHandlerSQLite(t, "approval_detail_payload_grant")
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移审批授权表失败：%v", err)
	}
	now := time.Now().UTC()
	req := model.ApprovalRequest{
		RequestID: "apr_payload_grant", OperationKey: "message.payload.read", OperationKind: "message.payload.read",
		ResourceType: "message", ResourceID: "msg-42", Payload: `{"payload":"不得泄露"}`,
		FrozenPayloadSHA256: "frozen", Status: model.ApprovalStatusSucceeded,
		RequesterType: auth.PrincipalKindHuman, RequesterID: "alice", RequestedBy: "human:alice",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("创建审批请求失败：%v", err)
	}
	grant := model.SensitiveAccessGrant{
		GrantID: "sag_payload_grant", ApprovalRequestID: req.RequestID,
		RequesterType: auth.PrincipalKindHuman, RequesterID: "alice", Operation: req.OperationKind,
		TargetRef: "message/msg-42", ContentVersionHash: "frozen", MaxUses: 1,
		Status: model.SensitiveAccessGrantStatusActive, ExpiresAt: now.Add(time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建敏感内容授权失败：%v", err)
	}
	approval := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), nil)
	approval.SetSensitiveAccessGrantStore(repository.NewSensitiveAccessGrantRepository(db))
	r := chi.NewRouter()
	r.Get("/admin/v2/approval-requests/{requestId}", NewApprovalHandler(approval).Detail)

	requestDetail := func(principal auth.Principal) map[string]any {
		httpReq := httptest.NewRequest(http.MethodGet, "/admin/v2/approval-requests/"+req.RequestID, nil)
		httpReq = httpReq.WithContext(auth.WithPrincipal(httpReq.Context(), principal))
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httpReq)
		if resp.Code != http.StatusOK {
			t.Fatalf("详情应返回 200，实际 %d：%s", resp.Code, resp.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析详情失败：%v", err)
		}
		if strings.Contains(resp.Body.String(), "不得泄露") {
			t.Fatalf("审批详情不得泄露 payload：%s", resp.Body.String())
		}
		return body
	}

	requester := requestDetail(auth.HumanPrincipal("alice"))
	grantView, ok := requester["sensitiveAccessGrant"].(map[string]any)
	if !ok || grantView["grantId"] != grant.GrantID {
		t.Fatalf("原申请人详情应返回可消费授权：%+v", requester)
	}
	other := requestDetail(auth.HumanPrincipal("reviewer"))
	if _, exists := other["sensitiveAccessGrant"]; exists {
		t.Fatalf("非申请人详情不得返回授权引用：%+v", other)
	}
}

func TestApprovalHandlerRedeemCredentialSecretRequiresExplicitRedeemer(t *testing.T) {
	svc := &approvalServiceStub{}
	r := chi.NewRouter()
	r.Post("/admin/v2/approval-requests/{requestId}/credential-secret/redeem", NewApprovalHandler(svc).RedeemCredentialSecret)
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/approval-requests/apr_secret/credential-secret/redeem", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("alice")))
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusGone || svc.redeemed != "" {
		t.Fatalf("未显式注入兑换器时必须 fail-closed：code=%d redeemed=%q", resp.Code, svc.redeemed)
	}
}
