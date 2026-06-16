package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"beacon/internal/apperr"
	"beacon/internal/render"
	"beacon/internal/repository"
	"beacon/internal/service"
)

// FileHandler 处理文件树托管（通道B）的 admin 请求与 agent 同步请求。
type FileHandler struct {
	svc     *service.FileService
	effSvc  *service.FileEffectiveService
	insSvc  *service.InstanceService
	maxHold time.Duration
}

// NewFileHandler 构造处理器。
func NewFileHandler(svc *service.FileService, effSvc *service.FileEffectiveService, insSvc *service.InstanceService, maxHold time.Duration) *FileHandler {
	return &FileHandler{svc: svc, effSvc: effSvc, insSvc: insSvc, maxHold: maxHold}
}

// fileView 是文件对象对外视图（content 仅详情返回）。
type fileView struct {
	ID          uint      `json:"id"`
	Namespace   string    `json:"namespace"`
	Group       string    `json:"group"`
	Path        string    `json:"path"`
	ScopeLevel  string    `json:"scopeLevel"`
	ScopeTarget string    `json:"scopeTarget"`
	Version     int64     `json:"version"`
	MD5         string    `json:"md5"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Content     string    `json:"content,omitempty"`
}

// fileRevisionView 是文件历史版本对外视图。
type fileRevisionView struct {
	Version        int64     `json:"version"`
	MD5            string    `json:"md5"`
	Operator       string    `json:"operator"`
	Comment        string    `json:"comment"`
	SourceRevision *uint     `json:"sourceRevision"`
	CreatedAt      time.Time `json:"createdAt"`
	Content        string    `json:"content,omitempty"`
}

// List 处理 GET /admin/v1/files。
func (h *FileHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	objs, err := h.svc.List(repository.FileFilter{
		Namespace:  q.Get("namespace"),
		Group:      q.Get("group"),
		Path:       q.Get("path"),
		ScopeLevel: q.Get("scopeLevel"),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]fileView, 0, len(objs))
	for _, o := range objs {
		views = append(views, toFileView(o.ID, o.NamespaceCode, o.GroupCode, o.Path, o.ScopeLevel, o.ScopeTarget, o.Version, o.ContentMD5, o.Enabled, o.UpdatedAt))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// Get 处理 GET /admin/v1/files/{id}（含 content）。
func (h *FileHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	o, err := h.svc.Get(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	v := toFileView(o.ID, o.NamespaceCode, o.GroupCode, o.Path, o.ScopeLevel, o.ScopeTarget, o.Version, o.ContentMD5, o.Enabled, o.UpdatedAt)
	v.Content = o.Content
	render.WriteJSON(w, http.StatusOK, v)
}

// fileCreateRequest 是新建文件对象的请求体。
type fileCreateRequest struct {
	Namespace   string `json:"namespace"`
	Group       string `json:"group"`
	Path        string `json:"path"`
	ScopeLevel  string `json:"scopeLevel"`
	ScopeTarget string `json:"scopeTarget"`
	Content     string `json:"content"`
	Operator    string `json:"operator"`
	Comment     string `json:"comment"`
}

// Create 处理 POST /admin/v1/files。
func (h *FileHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req fileCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	o, err := h.svc.Create(service.CreateFileParams{
		Namespace: req.Namespace, Group: req.Group, Path: req.Path,
		ScopeLevel: req.ScopeLevel, ScopeTarget: req.ScopeTarget,
		Content: req.Content, Operator: req.Operator, Comment: req.Comment, ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	v := toFileView(o.ID, o.NamespaceCode, o.GroupCode, o.Path, o.ScopeLevel, o.ScopeTarget, o.Version, o.ContentMD5, o.Enabled, o.UpdatedAt)
	v.Content = o.Content
	render.WriteJSON(w, http.StatusCreated, v)
}

// filePublishRequest 是发布文件新版本的请求体。
type filePublishRequest struct {
	Content  string `json:"content"`
	Operator string `json:"operator"`
	Comment  string `json:"comment"`
}

// Publish 处理 PUT /admin/v1/files/{id}。
func (h *FileHandler) Publish(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req filePublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	o, err := h.svc.Publish(id, req.Content, req.Operator, req.Comment, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"version": o.Version, "md5": o.ContentMD5})
}

// Delete 处理 DELETE /admin/v1/files/{id}（软删）。
func (h *FileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	operator := r.URL.Query().Get("operator")
	if err := h.svc.Delete(id, operator, r.URL.Query().Get("comment"), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ListRevisions 处理 GET /admin/v1/files/{id}/revisions。
func (h *FileHandler) ListRevisions(w http.ResponseWriter, r *http.Request) {
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
	views := make([]fileRevisionView, 0, len(revs))
	for _, rev := range revs {
		views = append(views, fileRevisionView{
			Version: rev.Version, MD5: rev.ContentMD5, Operator: rev.Operator,
			Comment: rev.Comment, SourceRevision: rev.SourceRevision, CreatedAt: rev.CreatedAt,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// GetRevision 处理 GET /admin/v1/files/{id}/revisions/{version}（含 content）。
func (h *FileHandler) GetRevision(w http.ResponseWriter, r *http.Request) {
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
	render.WriteJSON(w, http.StatusOK, fileRevisionView{
		Version: rev.Version, MD5: rev.ContentMD5, Operator: rev.Operator,
		Comment: rev.Comment, SourceRevision: rev.SourceRevision, CreatedAt: rev.CreatedAt, Content: rev.Content,
	})
}

// fileRollbackRequest 是文件回滚请求体。
type fileRollbackRequest struct {
	ToVersion int64  `json:"toVersion"`
	Operator  string `json:"operator"`
	Comment   string `json:"comment"`
}

// Rollback 处理 POST /admin/v1/files/{id}/rollback。
func (h *FileHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req fileRollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	o, err := h.svc.Rollback(id, req.ToVersion, req.Operator, req.Comment, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"version": o.Version, "md5": o.ContentMD5})
}

// manifestEntryView 是 manifest 中单个文件的 path→md5 条目（不含内容）。
type manifestEntryView struct {
	Path string `json:"path"`
	MD5  string `json:"md5"`
}

// Manifest 处理 GET /beacon/v1/agent/files/manifest（长轮询）。
// agent 带当前 fileTreeMd5；变了 200 返回新 manifest（path→md5，不含内容），未变到超时 304。
func (h *FileHandler) Manifest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, serverID, agentMD5 := q.Get("namespace"), q.Get("serverId"), q.Get("md5")
	if ns == "" || serverID == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	groupHint, err := h.insSvc.RequireRegistered(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err) // 未注册 → 404 NOT_REGISTERED
		return
	}
	timeout := h.maxHold
	if ms := q.Get("timeoutMs"); ms != "" {
		if v, e := strconv.Atoi(ms); e == nil && v > 0 {
			if d := time.Duration(v) * time.Millisecond; d < timeout {
				timeout = d // 取 min(客户端 timeoutMs, 服务端上限)
			}
		}
	}
	tree, changed, err := h.effSvc.WaitFileTree(r.Context(), ns, serverID, groupHint, agentMD5, timeout)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if !changed {
		w.WriteHeader(http.StatusNotModified) // 304：无变更到超时
		return
	}
	entries := make([]manifestEntryView, 0, len(tree.Files))
	for _, f := range tree.Files {
		entries = append(entries, manifestEntryView{Path: f.Path, MD5: f.MD5})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"namespace": tree.Namespace, "serverId": tree.ServerID,
		"group": tree.Group, "zone": nilIfEmpty(tree.Zone),
		"fileTreeMd5": tree.FileTreeMD5, "files": entries,
	})
}

// Content 处理 GET /beacon/v1/agent/files/content?namespace=&serverId=&path=。
// agent 比对 manifest 后逐个取变更文件的整文件内容。
func (h *FileHandler) Content(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, serverID, path := q.Get("namespace"), q.Get("serverId"), q.Get("path")
	if ns == "" || serverID == "" || path == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	groupHint, err := h.insSvc.RequireRegistered(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	tree, err := h.effSvc.Resolve(ns, serverID, groupHint)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	for _, f := range tree.Files {
		if f.Path == path {
			render.WriteJSON(w, http.StatusOK, map[string]any{
				"path": f.Path, "md5": f.MD5, "content": f.Content,
			})
			return
		}
	}
	render.WriteError(w, r, apperr.ErrFileNotFound) // 该 path 不在有效文件树
}

// toFileView 组装文件对象基础视图（不含 content）。
func toFileView(id uint, ns, group, path, scopeLevel, scopeTarget string, version int64, md5 string, enabled bool, updatedAt time.Time) fileView {
	return fileView{
		ID: id, Namespace: ns, Group: group, Path: path,
		ScopeLevel: scopeLevel, ScopeTarget: scopeTarget,
		Version: version, MD5: md5, Enabled: enabled, UpdatedAt: updatedAt,
	}
}
