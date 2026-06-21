package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// OverrideSetHandler 处理三方插件文件覆盖兼容（FR-15）的 admin 请求：
// 覆盖集 CRUD/发布/历史/回滚 + 发布前 dry-run 只读预览。挂在 /admin/v1 鉴权中间件之后。
type OverrideSetHandler struct {
	svc *service.OverrideSetService
}

// NewOverrideSetHandler 构造处理器。
func NewOverrideSetHandler(svc *service.OverrideSetService) *OverrideSetHandler {
	return &OverrideSetHandler{svc: svc}
}

// overrideSetView 是覆盖集对外视图。
type overrideSetView struct {
	ID            uint      `json:"id"`
	Namespace     string    `json:"namespace"`
	Group         string    `json:"group"`
	Name          string    `json:"name"`
	ScopeLevel    string    `json:"scopeLevel"`
	ScopeTarget   string    `json:"scopeTarget"`
	TargetRoot    string    `json:"targetRoot"`
	ReloadCommand string    `json:"reloadCommand"`
	Mode          string    `json:"mode"`
	Version       int64     `json:"version"`
	Enabled       bool      `json:"enabled"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// overrideSetRevisionView 是覆盖集历史版本对外视图。
type overrideSetRevisionView struct {
	Version        int64     `json:"version"`
	TargetRoot     string    `json:"targetRoot"`
	ReloadCommand  string    `json:"reloadCommand"`
	Operator       string    `json:"operator"`
	Comment        string    `json:"comment"`
	SourceRevision *uint     `json:"sourceRevision"`
	CreatedAt      time.Time `json:"createdAt"`
}

// List 处理 GET /admin/v1/override-sets。
func (h *OverrideSetHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sets, err := h.svc.List(repository.OverrideSetFilter{
		Namespace:  q.Get("namespace"),
		Group:      q.Get("group"),
		ScopeLevel: q.Get("scopeLevel"),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]overrideSetView, 0, len(sets))
	for _, s := range sets {
		views = append(views, toOverrideSetView(s.ID, s.NamespaceCode, s.GroupCode, s.Name, s.ScopeLevel, s.ScopeTarget, s.TargetRoot, s.ReloadCommand, s.Mode, s.Version, s.Enabled, s.UpdatedAt))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// Get 处理 GET /admin/v1/override-sets/{id}。
func (h *OverrideSetHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	s, err := h.svc.Get(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toOverrideSetView(s.ID, s.NamespaceCode, s.GroupCode, s.Name, s.ScopeLevel, s.ScopeTarget, s.TargetRoot, s.ReloadCommand, s.Mode, s.Version, s.Enabled, s.UpdatedAt))
}

// overrideSetCreateRequest 是新建覆盖集的请求体。
type overrideSetCreateRequest struct {
	Namespace     string `json:"namespace"`
	Group         string `json:"group"`
	Name          string `json:"name"`
	ScopeLevel    string `json:"scopeLevel"`
	ScopeTarget   string `json:"scopeTarget"`
	TargetRoot    string `json:"targetRoot"`
	ReloadCommand string `json:"reloadCommand"`
	Comment       string `json:"comment"`
}

// Create 处理 POST /admin/v1/override-sets。
func (h *OverrideSetHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req overrideSetCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	s, err := h.svc.Create(service.CreateOverrideSetParams{
		Namespace: req.Namespace, Group: req.Group, Name: req.Name,
		ScopeLevel: req.ScopeLevel, ScopeTarget: req.ScopeTarget,
		TargetRoot: req.TargetRoot, ReloadCommand: req.ReloadCommand,
		Operator: auth.Operator(r.Context()), Comment: req.Comment, ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, toOverrideSetView(s.ID, s.NamespaceCode, s.GroupCode, s.Name, s.ScopeLevel, s.ScopeTarget, s.TargetRoot, s.ReloadCommand, s.Mode, s.Version, s.Enabled, s.UpdatedAt))
}

// overrideSetPublishRequest 是发布覆盖集新版本的请求体。
type overrideSetPublishRequest struct {
	TargetRoot    string `json:"targetRoot"`
	ReloadCommand string `json:"reloadCommand"`
	Comment       string `json:"comment"`
}

// Publish 处理 PUT /admin/v1/override-sets/{id}。
func (h *OverrideSetHandler) Publish(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req overrideSetPublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	s, err := h.svc.Publish(id, service.PublishOverrideSetParams{
		TargetRoot: req.TargetRoot, ReloadCommand: req.ReloadCommand,
		Operator: auth.Operator(r.Context()), Comment: req.Comment, ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"version": s.Version, "targetRoot": s.TargetRoot})
}

// Delete 处理 DELETE /admin/v1/override-sets/{id}（软删）。
func (h *OverrideSetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.Delete(id, auth.Operator(r.Context()), r.URL.Query().Get("comment"), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ListRevisions 处理 GET /admin/v1/override-sets/{id}/revisions。
func (h *OverrideSetHandler) ListRevisions(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	revs, err := h.svc.ListRevisions(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]overrideSetRevisionView, 0, len(revs))
	for _, rev := range revs {
		views = append(views, overrideSetRevisionView{
			Version: rev.Version, TargetRoot: rev.TargetRoot, ReloadCommand: rev.ReloadCommand,
			Operator: rev.Operator, Comment: rev.Comment, SourceRevision: rev.SourceRevision, CreatedAt: rev.CreatedAt,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// overrideSetRollbackRequest 是覆盖集回滚请求体。
type overrideSetRollbackRequest struct {
	ToVersion int64  `json:"toVersion"`
	Comment   string `json:"comment"`
}

// Rollback 处理 POST /admin/v1/override-sets/{id}/rollback。
func (h *OverrideSetHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req overrideSetRollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	s, err := h.svc.Rollback(id, req.ToVersion, auth.Operator(r.Context()), req.Comment, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"version": s.Version, "targetRoot": s.TargetRoot})
}

// DryRun 处理 GET /admin/v1/override-sets/{id}/dry-run（只读预览，不落任何东西）。
func (h *OverrideSetHandler) DryRun(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	preview, err := h.svc.DryRun(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"targetRoot":        preview.TargetRoot,
		"reloadCommand":     preview.ReloadCommand,
		"commandFirstToken": preview.CommandFirstToken,
		"memberPaths":       preview.MemberPaths,
	})
}

// toOverrideSetView 组装覆盖集对外视图。
func toOverrideSetView(id uint, ns, group, name, scopeLevel, scopeTarget, targetRoot, reloadCommand, mode string, version int64, enabled bool, updatedAt time.Time) overrideSetView {
	return overrideSetView{
		ID: id, Namespace: ns, Group: group, Name: name,
		ScopeLevel: scopeLevel, ScopeTarget: scopeTarget,
		TargetRoot: targetRoot, ReloadCommand: reloadCommand, Mode: mode,
		Version: version, Enabled: enabled, UpdatedAt: updatedAt,
	}
}
