package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"beacon/internal/apperr"
	"beacon/internal/auth"
	"beacon/internal/merge"
	"beacon/internal/model"
	"beacon/internal/render"
	"beacon/internal/repository"
	"beacon/internal/service"
)

// ConfigHandler 处理配置中心相关的 admin 请求。
type ConfigHandler struct {
	svc    *service.ConfigService
	effSvc *service.EffectiveService
}

// NewConfigHandler 构造处理器。effSvc 供 admin 只读有效配置预览（FR-22）。
func NewConfigHandler(svc *service.ConfigService, effSvc *service.EffectiveService) *ConfigHandler {
	return &ConfigHandler{svc: svc, effSvc: effSvc}
}

// configView 是配置项对外视图（content 仅详情返回）。
type configView struct {
	ID          uint      `json:"id"`
	Namespace   string    `json:"namespace"`
	Group       string    `json:"group"`
	DataID      string    `json:"dataId"`
	ScopeLevel  string    `json:"scopeLevel"`
	ScopeTarget string    `json:"scopeTarget"`
	Format      string    `json:"format"`
	Version     int64     `json:"version"`
	MD5         string    `json:"md5"`
	Enabled     bool      `json:"enabled"`
	Sensitive   bool      `json:"sensitive"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Content     string    `json:"content,omitempty"`
}

// revisionView 是历史版本对外视图。
type revisionView struct {
	Version        int64     `json:"version"`
	MD5            string    `json:"md5"`
	Operator       string    `json:"operator"`
	Comment        string    `json:"comment"`
	SourceRevision *uint     `json:"sourceRevision"`
	CreatedAt      time.Time `json:"createdAt"`
	Content        string    `json:"content,omitempty"`
}

// List 处理 GET /admin/v1/configs。
func (h *ConfigHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := h.svc.List(repository.ConfigFilter{
		Namespace:  q.Get("namespace"),
		Group:      q.Get("group"),
		DataID:     q.Get("dataId"),
		ScopeLevel: q.Get("scopeLevel"),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]configView, 0, len(items))
	for i := range items {
		views = append(views, toView(&items[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// Get 处理 GET /admin/v1/configs/{id}（含 content）。
func (h *ConfigHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	it, err := h.svc.Get(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	v := toView(it)
	v.Content = it.Content
	render.WriteJSON(w, http.StatusOK, v)
}

// configCreateRequest 是新建配置项的请求体（operator 由认证态派生，不接收手填）。
type configCreateRequest struct {
	Namespace   string `json:"namespace"`
	Group       string `json:"group"`
	DataID      string `json:"dataId"`
	ScopeLevel  string `json:"scopeLevel"`
	ScopeTarget string `json:"scopeTarget"`
	Format      string `json:"format"`
	Content     string `json:"content"`
	Comment     string `json:"comment"`
	// 是否敏感项：为真则 content 加密入库（at-rest，FR-20）
	Sensitive bool `json:"sensitive"`
}

// Create 处理 POST /admin/v1/configs。
func (h *ConfigHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req configCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	it, err := h.svc.Create(service.CreateConfigParams{
		Namespace: req.Namespace, Group: req.Group, DataID: req.DataID,
		ScopeLevel: req.ScopeLevel, ScopeTarget: req.ScopeTarget, Format: req.Format,
		Content: req.Content, Operator: auth.Operator(r.Context()), Comment: req.Comment, ClientIP: clientIP(r),
		Sensitive: req.Sensitive,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	v := toView(it)
	v.Content = it.Content
	render.WriteJSON(w, http.StatusCreated, v)
}

// publishRequest 是发布新版本的请求体（operator 由认证态派生，不接收手填）。
type publishRequest struct {
	Content string `json:"content"`
	Comment string `json:"comment"`
}

// Publish 处理 PUT /admin/v1/configs/{id}。
func (h *ConfigHandler) Publish(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req publishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	it, err := h.svc.Publish(id, req.Content, auth.Operator(r.Context()), req.Comment, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"version": it.Version, "md5": it.ContentMD5})
}

// Delete 处理 DELETE /admin/v1/configs/{id}（软删）。
func (h *ConfigHandler) Delete(w http.ResponseWriter, r *http.Request) {
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

// ListRevisions 处理 GET /admin/v1/configs/{id}/revisions。
func (h *ConfigHandler) ListRevisions(w http.ResponseWriter, r *http.Request) {
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
	views := make([]revisionView, 0, len(revs))
	for _, rev := range revs {
		views = append(views, revisionView{
			Version: rev.Version, MD5: rev.ContentMD5, Operator: rev.Operator,
			Comment: rev.Comment, SourceRevision: rev.SourceRevision, CreatedAt: rev.CreatedAt,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// GetRevision 处理 GET /admin/v1/configs/{id}/revisions/{version}（含 content）。
func (h *ConfigHandler) GetRevision(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	version, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 64)
	if err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	rev, err := h.svc.GetRevision(id, version)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, revisionView{
		Version: rev.Version, MD5: rev.ContentMD5, Operator: rev.Operator,
		Comment: rev.Comment, SourceRevision: rev.SourceRevision, CreatedAt: rev.CreatedAt, Content: rev.Content,
	})
}

// rollbackRequest 是回滚请求体（operator 由认证态派生，不接收手填）。
type rollbackRequest struct {
	ToVersion int64  `json:"toVersion"`
	Comment   string `json:"comment"`
}

// Rollback 处理 POST /admin/v1/configs/{id}/rollback。
func (h *ConfigHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req rollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	it, err := h.svc.Rollback(id, req.ToVersion, auth.Operator(r.Context()), req.Comment, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"version": it.Version, "md5": it.ContentMD5})
}

// Diff 处理 GET /admin/v1/configs/{id}/diff?from=&to=。
func (h *ConfigHandler) Diff(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	from, err1 := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, err2 := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if err1 != nil || err2 != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	fromContent, toContent, err := h.svc.Diff(id, from, to)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"fromVersion": from, "toVersion": to,
		"fromContent": fromContent, "toContent": toContent,
	})
}

// keyProvenanceView 是某叶子键的来源层视图（path 为嵌套键路径）。
type keyProvenanceView struct {
	Path  []string `json:"path"`
	Scope string   `json:"scope"`
}

// effectiveConfigItemView 是有效配置中某 dataId 的合并结果 + 逐键来源。
type effectiveConfigItemView struct {
	DataID    string              `json:"dataId"`
	Format    string              `json:"format"`
	MD5       string              `json:"md5"`
	Content   string              `json:"content"`
	Sources   []keyProvenanceView `json:"sources"`
	Deletions []keyProvenanceView `json:"deletions"`
}

// Effective 处理 GET /admin/v1/configs/effective?namespace=&serverId=&group=&zone=。
// 只读预览某目标合并后的有效配置（逐键来源 + 被减量删除的键），与 agent 下发等价，
// 但不挂长轮询、不强制注册，可预览未注册/假定指派的目标（FR-22，见 ADR-0013）。
func (h *ConfigHandler) Effective(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	serverID := q.Get("serverId")
	group := q.Get("group")
	// 至少需要 namespace + (serverId 或 group) 才能定位覆盖链目标
	if ns == "" || (serverID == "" && group == "") {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	eff, err := h.effSvc.ResolveWithProvenance(ns, serverID, group, q.Get("zone"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]effectiveConfigItemView, 0, len(eff.Items))
	for _, it := range eff.Items {
		items = append(items, effectiveConfigItemView{
			DataID: it.DataID, Format: it.Format, MD5: it.MD5, Content: it.Content,
			Sources: toProvViews(it.Sources), Deletions: toProvViews(it.Deletions),
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"namespace": eff.Namespace, "serverId": eff.ServerID,
		"group": eff.Group, "zone": eff.Zone, "md5": eff.MD5, "items": items,
	})
}

// toProvViews 把 service 层的逐键来源转为对外视图。
func toProvViews(ps []merge.KeyProvenance) []keyProvenanceView {
	out := make([]keyProvenanceView, 0, len(ps))
	for _, p := range ps {
		out = append(out, keyProvenanceView{Path: p.Path, Scope: p.Scope})
	}
	return out
}

// parseID 解析路径参数 {id}。
func parseID(r *http.Request) (uint, error) {
	n, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return 0, apperr.ErrInvalidParam
	}
	return uint(n), nil
}

// toView 组装配置项基础视图（不含 content；content 仅详情接口单独填）。
func toView(it *model.ConfigItem) configView {
	return configView{
		ID: it.ID, Namespace: it.NamespaceCode, Group: it.GroupCode, DataID: it.DataID,
		ScopeLevel: it.ScopeLevel, ScopeTarget: it.ScopeTarget, Format: it.Format,
		Version: it.Version, MD5: it.ContentMD5, Enabled: it.Enabled, Sensitive: it.Sensitive,
		UpdatedAt: it.UpdatedAt,
	}
}
