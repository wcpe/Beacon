package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// AlertEventHandler 处理告警事件历史只读查询（FR-89，见 ADR-0041）。
type AlertEventHandler struct {
	svc *service.AlertEventService
}

// NewAlertEventHandler 构造处理器。
func NewAlertEventHandler(svc *service.AlertEventService) *AlertEventHandler {
	return &AlertEventHandler{svc: svc}
}

// alertEventView 是告警事件对外视图（小驼峰）。
type alertEventView struct {
	ID        uint      `json:"id"`
	Type      string    `json:"type"`
	Level     string    `json:"level"`
	ServerID  string    `json:"serverId"`
	Namespace string    `json:"namespace"`
	Message   string    `json:"message"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"createdAt"`
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
		views = append(views, alertEventView{
			ID: e.ID, Type: e.Type, Level: e.Level, ServerID: e.ServerID,
			Namespace: e.Namespace, Message: e.Message, Detail: e.Detail, CreatedAt: e.CreatedAt,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"total": total, "items": views})
}
