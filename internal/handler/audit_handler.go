package handler

import (
	"net/http"
	"strconv"
	"time"

	"beacon/internal/render"
	"beacon/internal/repository"
	"beacon/internal/service"
)

// AuditHandler 处理审计查询。
type AuditHandler struct {
	svc *service.AuditService
}

// NewAuditHandler 构造处理器。
func NewAuditHandler(svc *service.AuditService) *AuditHandler {
	return &AuditHandler{svc: svc}
}

// auditView 是审计对外视图。
type auditView struct {
	ID         uint      `json:"id"`
	Namespace  string    `json:"namespace"`
	Operator   string    `json:"operator"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType"`
	TargetRef  string    `json:"targetRef"`
	Detail     string    `json:"detail"`
	Result     string    `json:"result"`
	ClientIP   string    `json:"clientIp"`
	CreatedAt  time.Time `json:"createdAt"`
}

// List 处理 GET /admin/v1/audits（分页 + 过滤，时间倒序）。
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	items, total, err := h.svc.List(repository.AuditFilter{
		Namespace:  q.Get("namespace"),
		Operator:   q.Get("operator"),
		Action:     q.Get("action"),
		TargetType: q.Get("targetType"),
		TargetRef:  q.Get("targetRef"),
		From:       parseRFC3339(q.Get("from")),
		To:         parseRFC3339(q.Get("to")),
		Page:       page,
		Size:       size,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]auditView, 0, len(items))
	for _, a := range items {
		views = append(views, auditView{
			ID: a.ID, Namespace: a.NamespaceCode, Operator: a.Operator, Action: a.Action,
			TargetType: a.TargetType, TargetRef: a.TargetRef, Detail: a.Detail,
			Result: a.Result, ClientIP: a.ClientIP, CreatedAt: a.CreatedAt,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"total": total, "items": views})
}

// parseRFC3339 解析 RFC3339 时间；空或非法返回零值（不设界）。
func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
