package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2ConfigCenterHandler 处理配置中心 V2 管理面端点（FR-160/161，spec §5 全 17 端点）。
// 薄 handler——只解析请求、调 ConfigCenterService、经 render 统一写出；
// 响应形状由 service 视图逐字对齐 packages/contracts config-center.ts，此处直出不再二次映射。
// 全部写端点的专项审计由 service 在事务内自记（validate 只读校验刻意不审计，spec §4.4）。
type V2ConfigCenterHandler struct {
	svc *service.ConfigCenterService
}

// NewV2ConfigCenterHandler 构造处理器。
func NewV2ConfigCenterHandler(svc *service.ConfigCenterService) *V2ConfigCenterHandler {
	return &V2ConfigCenterHandler{svc: svc}
}

// writeConfigCenterError 统一错误出口：schema 违例附逐条 {path,message}（spec §4.4），其余走 render。
func writeConfigCenterError(w http.ResponseWriter, r *http.Request, err error) {
	var sve *service.ConfigSchemaViolationError
	if errors.As(err, &sve) {
		render.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"code":    apperr.ErrConfigSchemaViolation.Code,
			"message": apperr.ErrConfigSchemaViolation.Message,
			"traceId": render.TraceID(r.Context()),
			"errors":  sve.Violations,
		})
		return
	}
	render.WriteError(w, r, err)
}

// decodeOptionalBody 解析可选 JSON 请求体（空体合法；有体但非法 JSON → 参数错误）。
func decodeOptionalBody(r *http.Request, dst any) error {
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return apperr.ErrInvalidParam
}

// List 处理 GET /admin/v2/config-files：分页文件列表（不含回收站），namespaceId 必填；
// 带 serverId 时只列对该 server 有生效贡献的文件并附有效 hash。
func (h *V2ConfigCenterHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil || namespaceID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.ListFiles(service.ConfigFileListQuery{
		NamespaceID: namespaceID, Keyword: q.Get("keyword"), ServerRef: q.Get("serverId"),
		Page: intQuery(q.Get("page")), PageSize: intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// createConfigFileRequest 是创建文件请求体。
type createConfigFileRequest struct {
	NamespaceID    uint     `json:"namespaceId"`
	Name           string   `json:"name"`
	Format         string   `json:"format"`
	Description    string   `json:"description"`
	SchemaJSON     string   `json:"schemaJson"`
	SensitivePaths []string `json:"sensitivePaths"`
}

// Create 处理 POST /admin/v2/config-files：201 文件详情；重名 409 CONFIG_FILE_DUPLICATE。
func (h *V2ConfigCenterHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createConfigFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.CreateFile(service.CreateConfigFileRequest{
		NamespaceID: req.NamespaceID, Name: req.Name, Format: req.Format,
		Description: req.Description, SchemaJSON: req.SchemaJSON, SensitivePaths: req.SensitivePaths,
	}, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, view)
}

// Trash 处理 GET /admin/v2/config-files/trash：回收站分页列表，namespaceId 必填。
func (h *V2ConfigCenterHandler) Trash(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil || namespaceID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.ListTrash(service.ConfigFileListQuery{
		NamespaceID: namespaceID, Keyword: q.Get("keyword"),
		Page: intQuery(q.Get("page")), PageSize: intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Get 处理 GET /admin/v2/config-files/{id}：文件元数据 + 各层覆盖概览。
func (h *V2ConfigCenterHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.GetFileDetail(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// patchConfigFileRequest 是元数据更新请求体（指针区分「未携带」与「置空」；改敏感路径需 reason）。
type patchConfigFileRequest struct {
	Description    *string   `json:"description"`
	SchemaJSON     *string   `json:"schemaJson"`
	SensitivePaths *[]string `json:"sensitivePaths"`
	Reason         string    `json:"reason"`
}

// Patch 处理 PATCH /admin/v2/config-files/{id}：更新描述 / schema / 敏感路径；schema 非法 400。
func (h *V2ConfigCenterHandler) Patch(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req patchConfigFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	update := service.UpdateConfigFileRequest{
		Description: req.Description, SchemaJSON: req.SchemaJSON, Reason: req.Reason,
	}
	if req.SensitivePaths != nil {
		update.HasSensitive = true
		update.SensitivePaths = *req.SensitivePaths
	}
	view, err := h.svc.UpdateFile(id, update, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// reasonRequest 是携带原因的通用请求体。
type reasonRequest struct {
	Reason string `json:"reason"`
}

// Delete 处理 DELETE /admin/v2/config-files/{id}：移入回收站（软删除，版本链保留），204。
func (h *V2ConfigCenterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req reasonRequest
	if err := decodeOptionalBody(r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.TrashFile(id, req.Reason, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusNoContent, nil)
}

// Restore 处理 POST /admin/v2/config-files/{id}/restore：200 文件详情；名称被占用 409。
func (h *V2ConfigCenterHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.RestoreFile(id, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Purge 处理 POST /admin/v2/config-files/{id}/purge：物理删除连带版本链，原因必填，204。
func (h *V2ConfigCenterHandler) Purge(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req reasonRequest
	if err := decodeOptionalBody(r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.PurgeFile(id, req.Reason, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusNoContent, nil)
}

// Scopes 处理 GET /admin/v2/config-files/{id}/scopes：各贡献链概览。
func (h *V2ConfigCenterHandler) Scopes(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.GetScopes(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// ListVersions 处理 GET /admin/v2/config-files/{id}/versions：某链版本分页列表（scopeLevel/scopeRefId 必填对）。
func (h *V2ConfigCenterHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	q := r.URL.Query()
	scopeRefID, err := optionalUintQuery(q.Get("scopeRefId"))
	if err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.ListVersions(id, q.Get("scopeLevel"), scopeRefID, intQuery(q.Get("page")), intQuery(q.Get("pageSize")))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// saveVersionRequest 是保存新版本请求体（basedOnVersionId 传链当前 head id，链空传 null）。
type saveVersionRequest struct {
	ScopeLevel       string `json:"scopeLevel"`
	ScopeRefID       uint   `json:"scopeRefId"`
	Content          string `json:"content"`
	Remark           string `json:"remark"`
	BasedOnVersionID *uint  `json:"basedOnVersionId"`
}

// SaveVersion 处理 POST /admin/v2/config-files/{id}/versions：保存即定稿新版本，201 {versionId, versionNo, contentHash}；
// 失败码见 spec §4.2（400 语法 / schema / 无变化 / 占位符、409 并发、422 超限）。
func (h *V2ConfigCenterHandler) SaveVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req saveVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.SaveVersion(id, service.SaveVersionRequest{
		ScopeLevel: req.ScopeLevel, ScopeRefID: req.ScopeRefID, Content: req.Content,
		Remark: req.Remark, BasedOnVersionID: req.BasedOnVersionID,
	}, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		writeConfigCenterError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, view)
}

// GetVersion 处理 GET /admin/v2/config-versions/{versionId}：版本详情（content 脱敏）。
func (h *V2ConfigCenterHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	versionID, ok := parseUintParam(w, r, "versionId")
	if !ok {
		return
	}
	view, err := h.svc.GetVersion(versionID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// configRollbackRequest 是配置中心 V2 回退请求体。
type configRollbackRequest struct {
	Remark string `json:"remark"`
}

// Rollback 处理 POST /admin/v2/config-versions/{versionId}/rollback：基于历史版本生成新版本，201；
// schema 已收紧致历史内容不再合法时 400 明示原因（spec §4.6）。
func (h *V2ConfigCenterHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	versionID, ok := parseUintParam(w, r, "versionId")
	if !ok {
		return
	}
	var req configRollbackRequest
	if err := decodeOptionalBody(r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	view, err := h.svc.RollbackVersion(versionID, req.Remark, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		writeConfigCenterError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, view)
}

// RemoveScope 处理 DELETE /admin/v2/config-files/{id}/scopes/{scopeLevel}/{scopeRefId}：
// 撤销某层贡献（追加 removal 版本），原因必填，201；head 已撤销则 400。
func (h *V2ConfigCenterHandler) RemoveScope(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	scopeRefID, ok := parseUintParam(w, r, "scopeRefId")
	if !ok {
		return
	}
	var req reasonRequest
	if err := decodeOptionalBody(r, &req); err != nil {
		render.WriteError(w, r, err)
		return
	}
	view, err := h.svc.RemoveScopeContribution(id, chi.URLParam(r, "scopeLevel"), scopeRefID, req.Reason,
		auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, view)
}

// validateRequest 是实时校验请求体。
type validateRequest struct {
	ScopeLevel string `json:"scopeLevel"`
	Content    string `json:"content"`
}

// Validate 处理 POST /admin/v2/config-files/{id}/validate：{valid, errors:[{path,message}]}，不落库不审计。
func (h *V2ConfigCenterHandler) Validate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req validateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.ValidateContent(id, req.ScopeLevel, req.Content)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Effective 处理 GET /admin/v2/config-files/{id}/effective：有效配置预览（目标四选一，都不传 = 仅 namespace 基线）。
func (h *V2ConfigCenterHandler) Effective(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	target, err := effectiveTargetFromQuery(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	view, err := h.svc.Effective(id, target)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// effectiveTargetFromQuery 解析有效预览目标（serverId / zoneId / regionId / bcClusterId 四选一）。
func effectiveTargetFromQuery(r *http.Request) (service.ConfigEffectiveTarget, error) {
	q := r.URL.Query()
	zoneID, err := optionalUintQuery(q.Get("zoneId"))
	if err != nil {
		return service.ConfigEffectiveTarget{}, apperr.ErrInvalidParam
	}
	regionID, err := optionalUintQuery(q.Get("regionId"))
	if err != nil {
		return service.ConfigEffectiveTarget{}, apperr.ErrInvalidParam
	}
	bcClusterID, err := optionalUintQuery(q.Get("bcClusterId"))
	if err != nil {
		return service.ConfigEffectiveTarget{}, apperr.ErrInvalidParam
	}
	return service.ConfigEffectiveTarget{
		ServerRef: q.Get("serverId"), ZoneID: zoneID, RegionID: regionID, BCClusterID: bcClusterID,
	}, nil
}

// Diff 处理 GET /admin/v2/config-files/{id}/diff：left/right 描述符任意组合的键级 diff（脱敏）。
func (h *V2ConfigCenterHandler) Diff(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	q := r.URL.Query()
	left, right := q.Get("left"), q.Get("right")
	if left == "" || right == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.Diff(id, left, right)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}
