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

// Preview 直接读取一个文件资产并由 service 先记元数据审计；请求可用 namespaceId 与 serverId 联合定位。
func (h *AssetHandler) Preview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NamespaceID uint   `json:"namespaceId"`
		ServerID    string `json:"serverId"`
		Path        string `json:"path"`
		Reason      string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	h.writePreview(w, r, body.NamespaceID, body.ServerID, body.Path, body.Reason)
}

// Diff 直接比较两侧文件资产，left/right 可分别提供 namespaceId 与 serverId，正文只随本次响应返回。
func (h *AssetHandler) Diff(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Left struct {
			NamespaceID uint   `json:"namespaceId"`
			ServerID    string `json:"serverId"`
			Path        string `json:"path"`
		} `json:"left"`
		Right struct {
			NamespaceID uint   `json:"namespaceId"`
			ServerID    string `json:"serverId"`
			Path        string `json:"path"`
		} `json:"right"`
		Reason string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	h.writeDiff(w, r, service.DiffParams{
		Left:   service.AssetRef{NamespaceID: body.Left.NamespaceID, ServerID: body.Left.ServerID, Path: body.Left.Path},
		Right:  service.AssetRef{NamespaceID: body.Right.NamespaceID, ServerID: body.Right.ServerID, Path: body.Right.Path},
		Reason: body.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
}

// RequestPreviewApproval 保留旧路径名，直接返回文件预览结果。
func (h *AssetHandler) RequestPreviewApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NamespaceID uint   `json:"namespaceId"`
		ServerID    string `json:"serverId"`
		Path        string `json:"path"`
		Reason      string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	h.writePreview(w, r, body.NamespaceID, body.ServerID, body.Path, body.Reason)
}

// RequestPairReadApproval 保留旧路径名，直接返回两侧文件差异。
func (h *AssetHandler) RequestPairReadApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Left struct {
			NamespaceID uint   `json:"namespaceId"`
			ServerID    string `json:"serverId"`
			Path        string `json:"path"`
		} `json:"left"`
		Right struct {
			NamespaceID uint   `json:"namespaceId"`
			ServerID    string `json:"serverId"`
			Path        string `json:"path"`
		} `json:"right"`
		Reason string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	h.writeDiff(w, r, service.DiffParams{
		Left:   service.AssetRef{NamespaceID: body.Left.NamespaceID, ServerID: body.Left.ServerID, Path: body.Left.Path},
		Right:  service.AssetRef{NamespaceID: body.Right.NamespaceID, ServerID: body.Right.ServerID, Path: body.Right.Path},
		Reason: body.Reason, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
}

// ConsumePreviewGrant 兼容旧路径，以命令定位器直接重新读取文件，不校验 grant。
func (h *AssetHandler) ConsumePreviewGrant(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CommandID uint   `json:"commandId"`
		Reason    string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.CommandID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if h == nil || h.svc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	result, err := h.svc.PreviewFromCommand(r.Context(), chi.URLParam(r, "serverId"), body.CommandID, body.Reason, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	writePreviewResult(w, result)
}

// ConsumePairReadGrant 兼容旧路径，以 pair grant 定位两侧目标并直接比较。
func (h *AssetHandler) ConsumePairReadGrant(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CommandID uint   `json:"commandId"`
		Reason    string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.CommandID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if h == nil || h.svc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	result, err := h.svc.DiffFromPairGrant(r.Context(), chi.URLParam(r, "grantId"), body.CommandID, body.Reason, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	writePairDiffResult(w, result)
}

func (h *AssetHandler) writePreview(w http.ResponseWriter, r *http.Request, namespaceID uint, serverID, path, reason string) {
	if h == nil || h.svc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	result, err := h.svc.Preview(r.Context(), service.PreviewParams{
		NamespaceID: namespaceID, ServerID: serverID, Path: path, Reason: reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	writePreviewResult(w, result)
}

func writePreviewResult(w http.ResponseWriter, result *service.AssetPreviewResult) {
	render.WriteJSON(w, http.StatusOK, assetPreviewJS{
		Content: result.Content, Truncated: result.Truncated, Binary: result.Binary,
		SHA256: result.SHA256, Size: result.Size, Sensitive: result.Sensitive,
	})
}

func (h *AssetHandler) writeDiff(w http.ResponseWriter, r *http.Request, params service.DiffParams) {
	if h == nil || h.svc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	params.Operator = auth.Operator(r.Context())
	params.ClientIP = clientIP(r)
	result, err := h.svc.Diff(r.Context(), params)
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"identical": result.Identical, "left": assetDiffSideJSON(result.Left), "right": assetDiffSideJSON(result.Right),
	})
}

func assetDiffSideJSON(side *service.AssetDiffSide) any {
	if side == nil {
		return nil
	}
	return map[string]any{"serverId": side.ServerID, "path": side.Path, "content": side.Content, "sha256": side.SHA256}
}

func writePairDiffResult(w http.ResponseWriter, result *service.AssetPairDiffResult) {
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
