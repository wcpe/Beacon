package handler

import (
	"net/http"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/alert"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// AlertReader 是站内信告警只读来源（由站内信通道实现），便于解耦与测试。
type AlertReader interface {
	List() []alert.Alert
}

// AlertHandler 处理健康告警（站内信）只读查询（FR-28）。
type AlertHandler struct {
	reader AlertReader
	scope  *service.ObservationScopeResolver
}

// SetObservationScopeResolver 装配统一观测范围解析器。
func (h *AlertHandler) SetObservationScopeResolver(resolver *service.ObservationScopeResolver) {
	h.scope = resolver
}

// NewAlertHandler 构造处理器。
func NewAlertHandler(reader AlertReader) *AlertHandler {
	return &AlertHandler{reader: reader}
}

// alertView 是告警对外视图（最新在前）。
type alertView struct {
	Namespace  string    `json:"namespace"`
	ServerID   string    `json:"serverId"`
	Address    string    `json:"address"`
	PrevStatus string    `json:"prevStatus"`
	Status     string    `json:"status"`
	At         time.Time `json:"at"`
}

// List 处理 GET /admin/v1/alerts：返回站内信最近告警（最新在前）。
func (h *AlertHandler) List(w http.ResponseWriter, r *http.Request) {
	scope, err := resolveObservationScope(r, h.scope)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := h.reader.List()
	views := make([]alertView, 0, len(items))
	for _, a := range items {
		if !scope.All && !containsString(scope.NamespaceCodes, a.Namespace) {
			continue
		}
		views = append(views, alertView{
			Namespace: a.Namespace, ServerID: a.ServerID, Address: a.Address,
			PrevStatus: a.PrevStatus, Status: a.Status, At: a.At,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
