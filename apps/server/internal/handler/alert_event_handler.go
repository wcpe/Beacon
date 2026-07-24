package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// AlertEventHandler 处理告警事件历史查询与处理工作流（FR-89 只读留痕，FR-157 确认 / 标记已处理，见 ADR-0041/ADR-0064）。
type AlertEventHandler struct {
	svc *service.AlertEventService
}

// NewAlertEventHandler 构造处理器。
func NewAlertEventHandler(svc *service.AlertEventService) *AlertEventHandler {
	return &AlertEventHandler{svc: svc}
}

// alertEventView 是告警事件对外视图（小驼峰，逐字匹配 packages/contracts AlertEventItem）。
// handledBy / handledAt / handleNote 未处理时序列化为 null（用指针），status 恒非空。
type alertEventView struct {
	ID         uint       `json:"id"`
	Type       string     `json:"type"`
	Level      string     `json:"level"`
	ServerID   string     `json:"serverId"`
	Namespace  string     `json:"namespace"`
	Message    string     `json:"message"`
	Detail     string     `json:"detail"`
	CreatedAt  time.Time  `json:"createdAt"`
	Status     string     `json:"status"`
	HandledBy  *string    `json:"handledBy"`
	HandledAt  *time.Time `json:"handledAt"`
	HandleNote *string    `json:"handleNote"`
}

// toAlertEventView 把模型转对外视图；空串的处理人 / 说明映射为 null（契约为 string | null）。
func toAlertEventView(e model.AlertEvent) alertEventView {
	return alertEventView{
		ID: e.ID, Type: e.Type, Level: e.Level, ServerID: e.ServerID,
		Namespace: e.Namespace, Message: e.Message, Detail: e.Detail, CreatedAt: e.CreatedAt,
		Status:     e.Status,
		HandledBy:  ptrIfNotEmpty(e.HandledBy),
		HandledAt:  e.HandledAt,
		HandleNote: ptrIfNotEmpty(e.HandleNote),
	}
}

// ptrIfNotEmpty 空串返回 nil（→ json null），非空返回指针（→ json 字符串），对齐契约 string | null。
func ptrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// List 处理 GET /admin/v1/alert-events（分页 + 过滤，时间倒序）。
func (h *AlertEventHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	items, total, err := h.svc.List(repository.AlertEventFilter{
		Type:      q.Get("type"),
		Level:     q.Get("level"),
		Namespace: q.Get("namespace"),
		From:      parseRFC3339(q.Get("from")),
		To:        parseRFC3339(q.Get("to")),
		Page:      page,
		Size:      size,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]alertEventView, 0, len(items))
	for _, e := range items {
		views = append(views, toAlertEventView(e))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"total": total, "items": views})
}

// handleAlertRequest 是处理告警的请求体（FR-157，见 ADR-0064）。
// 兼容两种字段措辞：前端契约 HandleAlertBody 用 status(acknowledged/resolved) + note；
// ADR-0064 措辞用 action(acknowledge/resolve) + handleNote。二者归一后交 service。
type handleAlertRequest struct {
	Status     string `json:"status"`
	Action     string `json:"action"`
	Note       string `json:"note"`
	HandleNote string `json:"handleNote"`
}

// action 取归一后的处理动作：优先 status（前端契约），回退 action（ADR 措辞）。
func (req handleAlertRequest) action() string {
	if req.Status != "" {
		return req.Status
	}
	return req.Action
}

// note 取归一后的处置说明：优先 note（前端契约），回退 handleNote（ADR 措辞）。
func (req handleAlertRequest) note() string {
	if req.Note != "" {
		return req.Note
	}
	return req.HandleNote
}

// Handle 处理 POST /admin/v1/alert-events/{id}/handle（FR-157，见 ADR-0064）：确认 / 标记已处理。
// 走 adminAuth → readonlyWriteGuard → auditWrite 链（写方法，readonly 403）；service 内在事务中更新状态 + 写专项审计。
func (h *AlertEventHandler) Handle(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req handleAlertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	updated, err := h.svc.Handle(id, req.action(), req.note(), auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toAlertEventView(*updated))
}
