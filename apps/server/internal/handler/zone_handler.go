package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// ZoneHandler 处理 zone 指派 CRUD 与汇总；默认入口只读列表由 v2 真源解析（ADR-0067）。
type ZoneHandler struct {
	svc *service.ZoneService
	// v2 控制面服务：默认入口真源 server.is_default_entry 的只读解析（v1 列表端点兼容）
	v2svc *service.V2ControlPlaneService
}

// NewZoneHandler 构造处理器。
func NewZoneHandler(svc *service.ZoneService, v2svc *service.V2ControlPlaneService) *ZoneHandler {
	return &ZoneHandler{svc: svc, v2svc: v2svc}
}

// assignmentView 是 zone 指派对外视图。
type assignmentView struct {
	Namespace string    `json:"namespace"`
	ServerID  string    `json:"serverId"`
	Group     string    `json:"group"`
	Zone      string    `json:"zone"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toAssignmentView(a model.ZoneAssignment) assignmentView {
	return assignmentView{
		Namespace: a.NamespaceCode, ServerID: a.ServerID, Group: a.GroupCode,
		Zone: a.ZoneCode, Note: a.Note, UpdatedAt: a.UpdatedAt,
	}
}

// zoneStatView 是 zone 维度汇总视图。
type zoneStatView struct {
	Group       string `json:"group"`
	Zone        string `json:"zone"`
	ServerCount int    `json:"serverCount"`
	OnlineCount int    `json:"onlineCount"`
}

// ListAssignments 处理 GET /admin/v1/zones/assignments。
func (h *ZoneHandler) ListAssignments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := h.svc.ListAssignments(q.Get("namespace"), q.Get("group"), q.Get("zone"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]assignmentView, 0, len(list))
	for _, a := range list {
		views = append(views, toAssignmentView(a))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// assignRequest 是新增/改派请求体（operator 由认证态派生，不接收手填）。
type assignRequest struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
	Group     string `json:"group"`
	Zone      string `json:"zone"`
	Note      string `json:"note"`
}

// Assign 处理 PUT /admin/v1/zones/assignments（upsert）。
func (h *ZoneHandler) Assign(w http.ResponseWriter, r *http.Request) {
	var req assignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	a, err := h.svc.Assign(req.Namespace, req.ServerID, req.Group, req.Zone, auth.Operator(r.Context()), req.Note, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toAssignmentView(*a))
}

// Unassign 处理 DELETE /admin/v1/zones/assignments?namespace=&serverId=。
func (h *ZoneHandler) Unassign(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := h.svc.Unassign(q.Get("namespace"), q.Get("serverId"), auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// defaultEntryView 是小区默认入口对外视图（FR-48；真源 v2 server.is_default_entry，ADR-0067）。
type defaultEntryView struct {
	Namespace       string    `json:"namespace"`
	Group           string    `json:"group"`
	Zone            string    `json:"zone"`
	DefaultServerID string    `json:"defaultServerId"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// ListDefaultEntries 处理 GET /admin/v1/zones/default-entry（按 namespace[/group] 列默认入口，FR-48）。
// 写操作走 v2（分配勾选 / PUT /admin/v2/servers/{id}/default-entry），v1 写端点已移除。
func (h *ZoneHandler) ListDefaultEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := h.v2svc.ListDefaultEntries(q.Get("namespace"), q.Get("group"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]defaultEntryView, 0, len(list))
	for _, e := range list {
		views = append(views, defaultEntryView{
			Namespace: e.Namespace, Group: e.Group, Zone: e.Zone,
			DefaultServerID: e.DefaultServerID, UpdatedAt: e.UpdatedAt,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// Summary 处理 GET /admin/v1/zones（zone 维度汇总）。
func (h *ZoneHandler) Summary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	stats, err := h.svc.Summary(q.Get("namespace"), q.Get("group"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]zoneStatView, 0, len(stats))
	for _, s := range stats {
		views = append(views, zoneStatView{Group: s.Group, Zone: s.Zone, ServerCount: s.ServerCount, OnlineCount: s.OnlineCount})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}
