package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// AssetHandler 处理文件资产内容预览 / diff / 敏感规则（FR-164，见 spec §5.2）+ agent 内容回传（§5.1）。
// 控制面不存文件内容：preview/diff 经命令下发向 agent 现取，内容瞬态透传前端；敏感规则匹配在控制面执行。
type AssetHandler struct {
	svc *service.AssetPreviewService
}

// NewAssetHandler 构造处理器。
func NewAssetHandler(svc *service.AssetPreviewService) *AssetHandler {
	return &AssetHandler{svc: svc}
}

// assetPreviewJS 是预览响应（键对齐 contracts AssetPreviewResponse；二进制时 content 为 null）。
type assetPreviewJS struct {
	Content   *string `json:"content"`
	Truncated bool    `json:"truncated"`
	Binary    bool    `json:"binary"`
	SHA256    string  `json:"sha256"`
	Size      int64   `json:"size"`
	Sensitive bool    `json:"sensitive"`
}

// Preview 是旧正文直出入口，永久失败关闭。
func (h *AssetHandler) Preview(w http.ResponseWriter, r *http.Request) {
	render.WriteError(w, r, apperr.ErrOperationRequiresApproval)
}

// Diff 是旧双文件正文直出入口，永久失败关闭。
func (h *AssetHandler) Diff(w http.ResponseWriter, r *http.Request) {
	render.WriteError(w, r, apperr.ErrOperationRequiresApproval)
}

// RequestPreviewApproval 为单文件内容读取创建专用审批申请，冻结文件清单版本而不读取正文。
func (h *AssetHandler) RequestPreviewApproval(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	var body struct {
		ServerID string `json:"serverId"`
		Path     string `json:"path"`
		Reason   string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	request, err := h.svc.RequestAccess(body.ServerID, body.Path, body.Reason, r.Header.Get("Idempotency-Key"), principal, clientIP(r))
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{"requestId": request.RequestID, "status": request.Status})
}

// RequestPairReadApproval 为跨服务器差异读取创建两份独立审批申请；申请中只冻结元数据与哈希。
func (h *AssetHandler) RequestPairReadApproval(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	var body struct {
		Left struct {
			ServerID string `json:"serverId"`
			Path     string `json:"path"`
		} `json:"left"`
		Right struct {
			ServerID string `json:"serverId"`
			Path     string `json:"path"`
		} `json:"right"`
		Reason string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	left, right, err := h.svc.RequestPairAccess(service.AssetRef{ServerID: body.Left.ServerID, Path: body.Left.Path}, service.AssetRef{ServerID: body.Right.ServerID, Path: body.Right.Path}, body.Reason, r.Header.Get("Idempotency-Key"), principal, clientIP(r))
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{"leftRequestId": left.RequestID, "rightRequestId": right.RequestID, "status": "pending"})
}

// ConsumePreviewGrant 仅允许原申请主体一次性消费 Agent 已回传的文件内容。
func (h *AssetHandler) ConsumePreviewGrant(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	var body struct {
		CommandID uint `json:"commandId"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.ConsumeApproved(chi.URLParam(r, "grantId"), body.CommandID, principal)
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, assetPreviewJS{Content: result.Content, Truncated: result.Truncated, Binary: result.Binary, SHA256: result.SHA256, Size: result.Size})
}

// ConsumePairReadGrant 原申请人凭任一侧 grant 触发双侧原子消费，只返回脱敏差异摘要。
func (h *AssetHandler) ConsumePairReadGrant(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	var body struct {
		CommandID uint `json:"commandId"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.ConsumePairApproved(chi.URLParam(r, "grantId"), body.CommandID, principal)
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"identical": result.Identical, "changed": result.Changed, "unsupported": result.Unsupported,
		"left":  map[string]any{"serverId": result.Left.ServerID, "path": result.Left.Path, "sha256": result.Left.SHA256, "size": result.Left.Size},
		"right": map[string]any{"serverId": result.Right.ServerID, "path": result.Right.Path, "sha256": result.Right.SHA256, "size": result.Right.Size},
	})
}

// GetSensitiveRules 处理 GET /admin/v2/assets/sensitive-rules：读当前敏感路径规则清单（无存储回内置默认）。
func (h *AssetHandler) GetSensitiveRules(w http.ResponseWriter, r *http.Request) {
	patterns, err := h.svc.GetSensitiveRules()
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"patterns": patterns})
}

// PutSensitiveRules 处理 PUT /admin/v2/assets/sensitive-rules：整体替换规则清单（缺 patterns 数组 → 400；写审计）。
func (h *AssetHandler) PutSensitiveRules(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Patterns *[]string `json:"patterns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Patterns == nil {
		render.WriteError(w, r, apperr.ErrInvalidParam) // patterns 必填（整体替换语义）
		return
	}
	patterns, err := h.svc.PutSensitiveRules(*body.Patterns, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"patterns": patterns})
}

// assetContentRequest 是 agent 回传单文件内容的请求体（FR-164 §5.1）；ns/serverId 取鉴权身份、不信请求体自报。
// 命令由 commandId 定位（不依赖回传 path）；不含 sha256/size —— 响应的 sha256/size 取清单权威值填充（见 AssetContentPayload）。
type assetContentRequest struct {
	CommandID uint   `json:"commandId"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
	Error     string `json:"error"`
}

// ReceiveContent 处理 POST /beacon/v2/agent/assets/content：接收 agent 回传内容，转存内存中继并唤醒等待的 admin。
// 归属以鉴权中间件注入的权威身份为准（防跨 agent 越权投递）；内容瞬态、绝不落库。
func (h *AssetHandler) ReceiveContent(w http.ResponseWriter, r *http.Request) {
	id, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req assetContentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	err := h.svc.ReceiveContent(id.Namespace, id.ServerID, req.CommandID, service.AssetContentPayload{
		Binary: req.Binary, Truncated: req.Truncated, Content: req.Content, Error: req.Error,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// writeAssetError 写文件资产错误：敏感路径 403 附 sensitive=true（供前端弹原因框），其余走统一脱敏错误出口。
func writeAssetError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, apperr.ErrAssetSensitivePath) {
		render.WriteJSON(w, http.StatusForbidden, map[string]any{
			"code":      apperr.ErrAssetSensitivePath.Code,
			"message":   apperr.ErrAssetSensitivePath.Message,
			"traceId":   render.TraceID(r.Context()),
			"sensitive": true,
		})
		return
	}
	render.WriteError(w, r, err)
}
