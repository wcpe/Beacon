package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/redact"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// FileHandler 处理文件树托管（通道B）的 admin 请求与 agent 同步请求，含三方覆盖集（FR-15）的 agent 投递。
type FileHandler struct {
	svc      *service.FileService
	effSvc   *service.FileEffectiveService
	ovrSvc   *service.OverrideEffectiveService
	insSvc   *service.InstanceService
	settings longpollSettings // 长轮询挂起上限从设置 store 读，热生效（FR-61）
}

// NewFileHandler 构造处理器。
func NewFileHandler(svc *service.FileService, effSvc *service.FileEffectiveService, ovrSvc *service.OverrideEffectiveService, insSvc *service.InstanceService, settings longpollSettings) *FileHandler {
	return &FileHandler{svc: svc, effSvc: effSvc, ovrSvc: ovrSvc, insSvc: insSvc, settings: settings}
}

// fileView 是文件对象对外视图（content 仅详情返回）。
type fileView struct {
	ID          uint   `json:"id"`
	Namespace   string `json:"namespace"`
	Group       string `json:"group"`
	Path        string `json:"path"`
	ScopeLevel  string `json:"scopeLevel"`
	ScopeTarget string `json:"scopeTarget"`
	Version     int64  `json:"version"`
	MD5         string `json:"md5"`
	Enabled     bool   `json:"enabled"`
	// 整文件覆盖豁免（FR-44）：true 则该结构化文件强制整文件覆盖、不深合并。
	WholeFileOverride bool      `json:"wholeFileOverride"`
	UpdatedAt         time.Time `json:"updatedAt"`
	Content           string    `json:"content,omitempty"`
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
		views = append(views, toFileView(o.ID, o.NamespaceCode, o.GroupCode, o.Path, o.ScopeLevel, o.ScopeTarget, o.Version, o.ContentMD5, o.Enabled, o.UpdatedAt, o.WholeFileOverride))
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
	v := toFileView(o.ID, o.NamespaceCode, o.GroupCode, o.Path, o.ScopeLevel, o.ScopeTarget, o.Version, o.ContentMD5, o.Enabled, o.UpdatedAt, o.WholeFileOverride)
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
	Reason      string `json:"reason"`
	// 整文件覆盖豁免（FR-44）：true 则该结构化文件强制整文件覆盖、不深合并。缺省 false。
	WholeFileOverride bool `json:"wholeFileOverride"`
	// git 导出敏感排除（FR-47）：true 则该文件不导出到 git 镜像（库内保留、下发不变）。缺省 false。
	SensitiveExcluded bool `json:"sensitiveExcluded"`
}

// Create 处理 POST /admin/v1/files。
func (h *FileHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req fileCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestCreate(service.CreateFileParams{
		Namespace: req.Namespace, Group: req.Group, Path: req.Path,
		ScopeLevel: req.ScopeLevel, ScopeTarget: req.ScopeTarget,
		Content: req.Content, Operator: auth.Operator(r.Context()), Comment: req.Comment, ClientIP: clientIP(r),
		WholeFileOverride: req.WholeFileOverride, SensitiveExcluded: req.SensitiveExcluded,
	}, req.Reason, r.Header.Get("Idempotency-Key"), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// filePublishRequest 是发布文件新版本的请求体。
type filePublishRequest struct {
	Content string `json:"content"`
	Comment string `json:"comment"`
	Reason  string `json:"reason"`
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
	ticket, err := h.svc.RequestPublish(id, req.Content, req.Reason, r.Header.Get("Idempotency-Key"), auth.Operator(r.Context()), req.Comment, clientIP(r), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// Delete 处理 DELETE /admin/v1/files/{id}（软删）。
func (h *FileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	ticket, err := h.svc.RequestDelete(id, r.URL.Query().Get("reason"), r.Header.Get("Idempotency-Key"), auth.Operator(r.Context()), r.URL.Query().Get("comment"), clientIP(r), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// Batch 处理 POST /admin/v1/files/batch（FR-74）：把一组文件对象的删除 / 禁用 / 启用在一个事务内原子完成。
// body {action: "delete"|"disable"|"enable", ids: [int]}；非法 action / 空 ids → 400；每项各记一条审计。
func (h *FileHandler) Batch(w http.ResponseWriter, r *http.Request) {
	req, err := decodeBatchRequest(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	operator, clientIP := auth.Operator(r.Context()), clientIP(r)
	var ticket service.FileApprovalTicket
	switch req.Action {
	case batchActionDelete:
		ticket, err = h.svc.RequestBatchDelete(req.IDs, req.Reason, r.Header.Get("Idempotency-Key"), operator, clientIP, requestPrincipal(r))
	case batchActionDisable:
		ticket, err = h.svc.RequestBatchSetEnabled(req.IDs, false, req.Reason, r.Header.Get("Idempotency-Key"), operator, clientIP, requestPrincipal(r))
	case batchActionEnable:
		ticket, err = h.svc.RequestBatchSetEnabled(req.IDs, true, req.Reason, r.Header.Get("Idempotency-Key"), operator, clientIP, requestPrincipal(r))
	}
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
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
	Reason    string `json:"reason"`
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
	ticket, err := h.svc.RequestRollback(id, req.ToVersion, req.Reason, r.Header.Get("Idempotency-Key"), auth.Operator(r.Context()), req.Comment, clientIP(r), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// 导入表单字段名（FR-38，与前端提交口径一致）。
const (
	importFieldNamespace = "namespace"
	importFieldGroup     = "group"
	importFieldComment   = "comment"
	importFieldReason    = "reason"
	// paths 与 files 按提交顺序一一对应（第 i 个 paths 描述第 i 个 files）。
	importFieldPaths = "paths"
	importFieldFiles = "files"
)

const (
	// importMaxParts 是单次导入请求允许的最大部件数：每个文件占 files + paths 两个 part，另留少量标量字段余量。
	// 流式解析绕开了标准库 mime/multipart 的 maxParts = 1000（详见 parseImportForm），故必须自设部件数护栏：
	// files 与 paths 各自的数量上限管不住未知部件的数量，不设即等于允许超大 body 的垃圾部件被逐个无界解析。
	importMaxParts = 2*service.MaxImportFiles + importMaxExtraParts
	// importMaxExtraParts 是 files / paths 之外允许的标量字段余量（namespace / group / comment / reason 等）。
	importMaxExtraParts = 16
	// importTextPartMaxBytes 是单个文本部件（namespace / group / comment / reason / paths）的字节上限。
	// 流式解析下每个部件都直接读进内存，故必须有显式预算：相对 path 自身无长度上限（见 service.normalizePath），
	// 4KiB 足以容纳任何正常路径与运维说明，同时堵死「单个超大文本部件打爆内存」这条路径。
	importTextPartMaxBytes = 4 * 1024
	// importBodyMaxBytes 是导入请求体的总字节上限：合法请求的净内容不超过「聚合字节上限 + 全部文本部件预算」，
	// 故在两者之和上再留 1 MiB 余量给 multipart 边界与部件头，正常请求不会被误判为超大 body。
	importBodyMaxBytes = service.MaxImportTotalBytes + importMaxParts*importTextPartMaxBytes + 1024*1024
)

// importForm 是导入表单（FR-38）的流式解析结果。
type importForm struct {
	Namespace string
	Group     string
	Comment   string
	Reason    string
	// Files 按 files 部件出现顺序排列，Path 由等长对齐的 paths 部件回填。
	Files []service.ImportFile
}

// Import 处理 POST /admin/v1/files/import（FR-38）：把一份目录批量上传到某组（scope=group）。
// 表单：namespace、group 文本字段 + 多个 files 部件；各文件相对 path 取自与 files 等长的 paths 文本字段（按提交顺序对齐）。
// handler 只解析 multipart 并做数量 / 总量早校验，交 service 在事务内原子落地（path 安全与单文件大小由 service 校验）。
// 单次导入的文件数 / 总字节上限复用 service.MaxImportFiles / service.MaxImportTotalBytes，单文件大小复用
// service.MaxFileContentBytes（与 FR-39 反向抓取同一套上限）。
func (h *FileHandler) Import(w http.ResponseWriter, r *http.Request) {
	// 请求体总字节护栏：流式解析不再有标准库的落盘兜底，故先给整个 body 设上限（见 importBodyMaxBytes）。
	r.Body = http.MaxBytesReader(w, r.Body, importBodyMaxBytes)
	form, err := parseImportForm(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}

	ticket, err := h.svc.RequestImport(service.ImportFilesParams{
		Namespace: form.Namespace, Group: form.Group, Files: form.Files,
		Operator: auth.Operator(r.Context()), Comment: form.Comment, ClientIP: clientIP(r),
	}, form.Reason, r.Header.Get("Idempotency-Key"), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// parseImportForm 流式解析导入 multipart 请求，并做字段 / paths / files 的对齐与上限校验。
//
// 为什么不用 r.ParseMultipartForm：标准库 mime/multipart 内部有硬编码的 maxParts = 1000（不可配置），
// 而每个导入文件占 files + paths 两个 part，故部件数一到「2 × 文件数 + 文本字段数 > 1000」即报
// ErrMessageTooLarge——实到 497～498 个文件就撞墙（文本字段越多上限越低，1999 个文件的声明上限从不达标），
// 且该失败被笼统归成「参数错误」。r.MultipartReader 逐部件流式读取，无此类部件数上限。
//
// 内存与落盘保护：流式模式下标准库的「内存阈值 + 落临时文件」兜底不再生效，改为逐层显式预算——
//  1. 文本部件按 importTextPartMaxBytes 限读；
//  2. 文件部件按 service.MaxFileContentBytes 限读（限读上限 + 1 字节，据此判定超限），
//     超大部件只会被拒，不会整份驻留内存；
//  3. 文件累计字节按 service.MaxImportTotalBytes 累积校验，超限立即中断读取；
//  4. 第 service.MaxImportFiles + 1 个 files 部件在其字节被读取之前即拒；
//  5. 部件总数按 importMaxParts 限（拦住未知部件无界解析），请求体总量另由 Import 的 MaxBytesReader 兜底。
//
// 故最坏内存驻留 ≈ 聚合字节上限 + 单个文件上限，且全程不落任何临时文件（无需 RemoveAll 清理）。
func parseImportForm(r *http.Request) (*importForm, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, errImportParseFailed(err)
	}
	form := &importForm{}
	var paths []string
	var total int64
	var parts int
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errImportParseFailed(err)
		}
		parts++
		if parts > importMaxParts {
			_ = part.Close()
			return nil, errImportTooManyParts(importMaxParts)
		}
		err = readImportPart(part, form, &paths, &total)
		// 读完即关：Close 会丢弃本部件剩余字节，避免残留缓冲与未释放的资源（无临时文件可清理）。
		_ = part.Close()
		if err != nil {
			return nil, err
		}
	}
	if form.Namespace == "" || form.Group == "" || len(form.Files) == 0 {
		return nil, apperr.ErrInvalidParam
	}
	// paths 必须与 files 等长且一一对应（防错位导致内容落到错误 path）
	if len(paths) != len(form.Files) {
		return nil, errImportPathsMisaligned(len(form.Files), len(paths))
	}
	for i := range form.Files {
		form.Files[i].Path = paths[i]
	}
	return form, nil
}

// readImportPart 按部件名分流读取单个 multipart 部件。
// 未知部件整体跳过（下一次 NextPart 调用会丢弃其剩余字节），对表单扩展字段保持宽容且不占用内存预算。
// 为什么打 WARN：旧实现只取 Value["paths"] / File["files"]，未知部件被标准库收进 Value 后**静默忽略**；
// 流式方案下同样跳过，但「字段名拼错」这类提交会表现为「文件数为 0」的笼统参数错误、难以定位，
// 故把部件名打进日志，让拼写问题可被发现。
func readImportPart(part *multipart.Part, form *importForm, paths *[]string, total *int64) error {
	name := part.FormName()
	switch name {
	case importFieldNamespace, importFieldGroup, importFieldComment, importFieldReason, importFieldPaths:
		value, err := readImportTextPart(part)
		if err != nil {
			return err
		}
		setImportTextField(name, form, paths, value)
		return nil
	case importFieldFiles:
		return appendImportFile(part, form, total)
	default:
		slog.Warn("导入请求含未知 multipart 部件，已跳过（可能是字段名拼写错误）", "部件名", name)
		return nil
	}
}

// setImportTextField 把已解析出的文本值按字段名写入表单；paths 按出现顺序追加，与 files 部件顺序对齐。
func setImportTextField(name string, form *importForm, paths *[]string, value string) {
	switch name {
	case importFieldNamespace:
		form.Namespace = value
	case importFieldGroup:
		form.Group = value
	case importFieldComment:
		form.Comment = value
	case importFieldReason:
		form.Reason = value
	case importFieldPaths:
		*paths = append(*paths, value)
	}
}

// appendImportFile 读取一个 files 部件、校验后追加到表单。
// 先判数量再读内容：文件数已到上限时，连超限那一个部件的字节都不读，直接拒。
func appendImportFile(part *multipart.Part, form *importForm, total *int64) error {
	if len(form.Files) >= service.MaxImportFiles {
		return errImportTooManyFiles(len(form.Files) + 1)
	}
	content, err := readImportFilePart(part)
	if err != nil {
		return err
	}
	*total += int64(len(content))
	if *total > service.MaxImportTotalBytes {
		return apperr.ErrContentTooLarge
	}
	form.Files = append(form.Files, service.ImportFile{Content: content})
	return nil
}

// readImportTextPart 读取单个文本部件；超 importTextPartMaxBytes 即拒（防超大文本部件驻留内存）。
func readImportTextPart(part *multipart.Part) (string, error) {
	// 限读到上限 + 1 字节：既能读出「已超限」这一事实，又不会把超大部件整份读进内存
	data, err := io.ReadAll(io.LimitReader(part, importTextPartMaxBytes+1))
	if err != nil {
		return "", errImportParseFailed(err)
	}
	if len(data) > importTextPartMaxBytes {
		return "", apperr.New(apperr.ErrInvalidParam.Status, apperr.ErrInvalidParam.Code,
			fmt.Sprintf("导入表单文本字段超出上限（单字段上限 %d 字节）", importTextPartMaxBytes))
	}
	return string(data), nil
}

// readImportFilePart 读取单个上传文件部件的整文件内容；超单文件上限即拒（防超大文件入库）。
// 限读到上限 + 1 字节，借此兜住无 Content-Length 可依赖的超大部件：最坏只读 1 MiB + 1 字节即返回错误。
func readImportFilePart(part *multipart.Part) (string, error) {
	data, err := io.ReadAll(io.LimitReader(part, service.MaxFileContentBytes+1))
	if err != nil {
		return "", errImportParseFailed(err)
	}
	if len(data) > service.MaxFileContentBytes {
		return "", apperr.ErrContentTooLarge
	}
	return string(data), nil
}

// errImportParseFailed 构造带底层原因的导入解析错误：沿用 INVALID_PARAM 码与 400 状态（对外契约不变），
// 但把标准库给出的真实原因脱敏后带出去，避免调用方只看到笼统「参数错误」而无从定位（ADR-0057）。
//
// 为什么不写死「须为 multipart/form-data 且 boundary 合法」这类前缀：本函数还被**请求体超上限**
// （MaxBytesReader 的 MaxBytesError，见 Import 的 body 护栏）复用，那种情况格式完全正确、
// 只是太大。写死单一原因会让运维把「文件太大」读成「格式不对」，与 ADR-0057「不得笼统化」相抵。
// 故只用中性前缀，把判定交给后面的底层原因。
func errImportParseFailed(cause error) error {
	return apperr.New(apperr.ErrInvalidParam.Status, apperr.ErrInvalidParam.Code,
		"导入请求解析失败："+redact.DesensitizeErr(cause))
}

// errImportTooManyFiles 构造带实际上限的文件数超限错误：码与状态沿用 TOO_MANY_FILES / 422，
// 但把上限数字带进说明——调用方据此才能判断「该拆批导入」，而不是误以为参数格式有误。
func errImportTooManyFiles(count int) error {
	return apperr.New(apperr.ErrTooManyFiles.Status, apperr.ErrTooManyFiles.Code,
		fmt.Sprintf("单次导入文件数超出上限：上限 %d 个，本次第 %d 个文件已被拒，请拆分后分批导入",
			service.MaxImportFiles, count))
}

// errImportTooManyParts 构造部件总数超限错误（对外仍是 INVALID_PARAM，说明里讲清部件数与文件数的关系）。
func errImportTooManyParts(limit int) error {
	return apperr.New(apperr.ErrInvalidParam.Status, apperr.ErrInvalidParam.Code,
		fmt.Sprintf("导入请求部件数超出上限（上限 %d 个 part：每个文件占 files、paths 两个 part）；"+
			"文件数上限为 %d 个", limit, service.MaxImportFiles))
}

// errImportPathsMisaligned 构造 paths 与 files 数量不一致的参数错误（带上两个数量便于直接定位错位提交）。
func errImportPathsMisaligned(files, paths int) error {
	return apperr.New(apperr.ErrInvalidParam.Status, apperr.ErrInvalidParam.Code,
		fmt.Sprintf("paths 与 files 数量不一致：files %d 个，paths %d 个，两者须等长且按提交顺序一一对应",
			files, paths))
}

// effectiveFileView 是有效文件树中某文件的合并结果 + 逐文件/逐键来源（admin 只读预览，FR-45）。
type effectiveFileView struct {
	Path string `json:"path"`
	MD5  string `json:"md5"`
	// 合并后整文件内容（结构化深合并结果 / 整文件覆盖取 winner）
	Content string `json:"content"`
	// 是否整文件覆盖模式（非结构化 / 标 wholeFileOverride 豁免 / 坏内容回退）
	WholeFile bool `json:"wholeFile"`
	// 逐叶子键来源（结构化文件按键；整文件模式为单条空路径 = winner 层）
	Sources []keyProvenanceView `json:"sources"`
	// 被某层 null 减量删除且最终不存在的键（整文件模式恒空）
	Deletions []keyProvenanceView `json:"deletions"`
}

// Effective 处理 GET /admin/v1/files/effective?namespace=&serverId=&group=&zone=。
// 只读预览某目标合并后的有效文件树（逐文件合并结果 + 结构化文件逐键来源 / 整文件来源层 + 是否豁免整文件），
// 与 agent 经 files/content 拿到的逐一致，但不挂长轮询、不强制注册（FR-45，沿用 FR-22/ADR-0013 模式）。
func (h *FileHandler) Effective(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	serverID := q.Get("serverId")
	group := q.Get("group")
	// 至少需要 namespace + (serverId 或 group) 才能定位覆盖链目标
	if ns == "" || (serverID == "" && group == "") {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	tree, err := h.effSvc.ResolveWithProvenance(ns, serverID, group, q.Get("zone"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	files := make([]effectiveFileView, 0, len(tree.Files))
	for _, f := range tree.Files {
		files = append(files, effectiveFileView{
			Path: f.Path, MD5: f.MD5, Content: f.Content, WholeFile: f.WholeFile,
			Sources: toProvViews(f.Sources), Deletions: toProvViews(f.Deletions),
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"namespace": tree.Namespace, "serverId": tree.ServerID,
		"group": tree.Group, "zone": nilIfEmpty(tree.Zone),
		"fileTreeMd5": tree.FileTreeMD5, "files": files,
	})
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
	timeout := resolveHoldTimeout(h.settings, q.Get("timeoutMs"))
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
	timeout := resolveHoldTimeout(h.settings, q.Get("timeoutMs"))
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
func toFileView(id uint, ns, group, path, scopeLevel, scopeTarget string, version int64, md5 string, enabled bool, updatedAt time.Time, wholeFileOverride bool) fileView {
	return fileView{
		ID: id, Namespace: ns, Group: group, Path: path,
		ScopeLevel: scopeLevel, ScopeTarget: scopeTarget,
		Version: version, MD5: md5, Enabled: enabled, UpdatedAt: updatedAt,
		WholeFileOverride: wholeFileOverride,
	}
}
