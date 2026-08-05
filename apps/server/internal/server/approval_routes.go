package server

import "github.com/go-chi/chi/v5"

// registerApprovalRoutes 注册统一审批管理端点。
func registerApprovalRoutes(r chi.Router, h Handlers) {
	if h.Approval == nil {
		return
	}
	r.Get("/approval-requests", h.Approval.List)
	r.Get("/approval-requests/{requestId}", h.Approval.Detail)
	r.Post("/approval-requests/{requestId}/approve", h.Approval.Approve)
	r.Post("/approval-requests/{requestId}/reject", h.Approval.Reject)
	r.Post("/approval-requests/{requestId}/withdraw", h.Approval.Withdraw)
	r.Post("/approval-requests/{requestId}/credential-secret/redeem", h.Approval.RedeemCredentialSecret)
	r.Get("/approvals", h.Approval.List)
	r.Get("/approvals/{id}", h.Approval.Detail)
	r.Post("/approvals/{id}/approve", h.Approval.Approve)
	r.Post("/approvals/{id}/reject", h.Approval.Reject)
	r.Post("/approvals/{id}/withdraw", h.Approval.Withdraw)
	r.Post("/approvals/{id}/credential-secret/redeem", h.Approval.RedeemCredentialSecret)
}
