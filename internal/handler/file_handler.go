package handler

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"beacon/internal/apperr"
	"beacon/internal/auth"
	"beacon/internal/render"
	"beacon/internal/repository"
	"beacon/internal/service"
)

// FileHandler 处理文件树托管（通道B）的 admin 请求与 agent 同步请求，含三方覆盖集（FR-15）的 agent 投递。
type FileHandler struct {
	svc     *service.FileService
	effSvc  *service.FileEffectiveService
	ovrSvc  *service.OverrideEffectiveService
	insSvc  *service.InstanceService
	maxHold time.Duration
}

// NewFileHandler 构造处理器。
func NewFileHandler(svc *service.FileService, effSvc *service.FileEffectiveService, ovrSvc *service.OverrideEffectiveService, insSvc *service.InstanceService, maxHold time.Duration) *FileHandler {
	return &FileHandler{svc: svc, effSvc: effSvc, ovrSvc: ovrSvc, insSvc: insSvc, maxHold: maxHold}
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
		Content: req.Content, Operator: auth.Operator(r.Context()), Comment: req.Comment, ClientIP: clientIP(r),
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
	Content string `json:"content"`
	Comment string `json:"comment"`
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
	o, err := h.svc.Publish(id, req.Content, auth.Operator(r.Context()), req.Comment, clientIP(r))
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
	if err := h.svc.Delete(id, auth.Operator(r.Context()), r.URL.Query().Get("comment"), clientIP(r)); err != nil {
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
	o, err := h.svc.Rollback(id, req.ToVersion, auth.Operator(r.Context()), req.Comment, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"version": o.Version, "md5": o.ContentMD5})
}

// 导入（FR-38，multipart 上传）多文件聚合上限。单文件大小上限复用 service.MaxFileContentBytes。
const (
	// 单次导入的最大文件数（防一次塞入海量文件拖垮事务）
	maxImportFiles = 2000
	// 单次导入的最大总字节（所有文件内容累加；防内存与库表膨胀）
	maxImportTotalBytes = 64 * 1024 * 1024
	// multipart 解析驻留内存上限（超出落临时文件，由标准库管理）
	importParseMemoryBytes = 16 * 1024 * 1024
)

// Import 处理 POST /admin/v1/files/import（FR-38）：把一份目录批量上传到某组（scope=group）。
// 表单：namespace、group 文本字段 + 多个 files 部件；各文件相对 path 取自与 files 等长的 paths 文本字段（按提交顺序对齐）。
// handler 只解析 multipart 并做数量 / 总量早校验，交 service 在事务内原子落地（path 安全与单文件大小由 service 校验）。
func (h *FileHandler) Import(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(importParseMemoryBytes); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	ns := r.FormValue("namespace")
	group := r.FormValue("group")
	comment := r.FormValue("comment")
	fileHeaders := r.MultipartForm.File["files"]
	paths := r.MultipartForm.Value["paths"]
	if ns == "" || group == "" || len(fileHeaders) == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	// paths 必须与 files 等长且一一对应（防错位导致内容落到错误 path）
	if len(paths) != len(fileHeaders) {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if len(fileHeaders) > maxImportFiles {
		render.WriteError(w, r, apperr.ErrTooManyFiles)
		return
	}

	files := make([]service.ImportFile, 0, len(fileHeaders))
	var total int64
	for i, fh := range fileHeaders {
		content, err := readMultipartFile(fh)
		if err != nil {
			render.WriteError(w, r, apperr.ErrInvalidParam)
			return
		}
		total += int64(len(content))
		if total > maxImportTotalBytes {
			render.WriteError(w, r, apperr.ErrContentTooLarge)
			return
		}
		files = append(files, service.ImportFile{Path: paths[i], Content: content})
	}

	res, err := h.svc.Import(service.ImportFilesParams{
		Namespace: ns, Group: group, Files: files,
		Operator: auth.Operator(r.Context()), Comment: comment, ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"files": len(files), "created": res.Created, "updated": res.Updated,
	})
}

// readMultipartFile 读取单个上传文件部件的整文件内容；超单文件上限即拒（防超大文件入库）。
func readMultipartFile(fh *multipart.FileHeader) (string, error) {
	if fh.Size > service.MaxFileContentBytes {
		return "", apperr.ErrContentTooLarge
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	// 限读到上限 +1 字节，借此兜住 Size 头不可信时的超限
	data, err := io.ReadAll(io.LimitReader(f, service.MaxFileContentBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > service.MaxFileContentBytes {
		return "", apperr.ErrContentTooLarge
	}
	return string(data), nil
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

// overrideSetEntryView 是 override 投递中单个适用覆盖集的对外视图（FR-15，不含成员内容）。
type overrideSetEntryView struct {
	// 覆盖集名称（agent 取成员内容时回传定位）
	Name string `json:"name"`
	// 目标插件根目录（相对 plugins），agent 落盘根
	TargetRoot string `json:"targetRoot"`
	// 受限重载命令（可空表示不下发命令；是否真正派发由 agent 本地白名单把关）
	ReloadCommand string `json:"reloadCommand"`
	// 成员文件相对 path 清单（内容走 override-sets/content 取）
	Members []string `json:"members"`
}

// OverrideManifest 处理 GET /beacon/v1/agent/override-sets（长轮询，FR-15 投递）。
// agent 带当前 overrideMd5；变了 200 返回适用覆盖集（目标根 + 重载命令 + 成员 path，不含内容），未变到超时 304。
// 与文件长轮询复用同一唤醒集合（同属通道B），但与配置 md5、fileTreeMd5 相互独立（见 ADR-0010/0011）。
func (h *FileHandler) OverrideManifest(w http.ResponseWriter, r *http.Request) {
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
	eff, changed, err := h.ovrSvc.WaitOverride(r.Context(), ns, serverID, groupHint, agentMD5, timeout)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if !changed {
		w.WriteHeader(http.StatusNotModified) // 304：无变更到超时
		return
	}
	entries := make([]overrideSetEntryView, 0, len(eff.Sets))
	for _, s := range eff.Sets {
		entries = append(entries, overrideSetEntryView{
			Name: s.Name, TargetRoot: s.TargetRoot, ReloadCommand: s.ReloadCommand, Members: s.MemberPaths,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"namespace": eff.Namespace, "serverId": eff.ServerID,
		"group": eff.Group, "zone": nilIfEmpty(eff.Zone),
		"overrideMd5": eff.OverrideMD5, "sets": entries,
	})
}

// OverrideContent 处理 GET /beacon/v1/agent/override-sets/content?namespace=&serverId=&set=&path=（FR-15）。
// agent 比对覆盖集成员 manifest 后逐个取成员整文件内容，落盘到该集 targetRoot（经 OverrideApplier）。
func (h *FileHandler) OverrideContent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, serverID, setName, path := q.Get("namespace"), q.Get("serverId"), q.Get("set"), q.Get("path")
	if ns == "" || serverID == "" || setName == "" || path == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	groupHint, err := h.insSvc.RequireRegistered(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	file, err := h.ovrSvc.MemberContent(ns, serverID, groupHint, setName, path)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if file == nil {
		render.WriteError(w, r, apperr.ErrFileNotFound) // 该成员不在本 server 适用覆盖集
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"set": setName, "path": file.Path, "md5": file.MD5, "content": file.Content,
	})
}

// toFileView 组装文件对象基础视图（不含 content）。
func toFileView(id uint, ns, group, path, scopeLevel, scopeTarget string, version int64, md5 string, enabled bool, updatedAt time.Time) fileView {
	return fileView{
		ID: id, Namespace: ns, Group: group, Path: path,
		ScopeLevel: scopeLevel, ScopeTarget: scopeTarget,
		Version: version, MD5: md5, Enabled: enabled, UpdatedAt: updatedAt,
	}
}
