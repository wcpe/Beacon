package handler

import (
	"encoding/json"
	"errors"
	"net/http"

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

// assetRefBody 是 diff 两侧的 (serverId, path)。
type assetRefBody struct {
	ServerID string `json:"serverId"`
	Path     string `json:"path"`
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

// assetDiffSideJS / assetDiffJS 是 diff 响应（键对齐 contracts AssetDiffResponse；identical 时两侧省略）。
type assetDiffSideJS struct {
	ServerID string `json:"serverId"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	SHA256   string `json:"sha256"`
}

type assetDiffJS struct {
	Identical bool             `json:"identical"`
	Left      *assetDiffSideJS `json:"left,omitempty"`
	Right     *assetDiffSideJS `json:"right,omitempty"`
}

// Preview 处理 POST /admin/v2/assets/preview：预览单文件内容（POST 属写方法，readonly 经 readonlyWriteGuard 403）。
// 命中敏感路径且无 reason → 403 asset_sensitive_path（响应体附 sensitive=true 供前端弹原因框）。
func (h *AssetHandler) Preview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServerID string `json:"serverId"`
		Path     string `json:"path"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	res, err := h.svc.Preview(r.Context(), service.PreviewParams{
		ServerID: body.ServerID, Path: body.Path, Reason: body.Reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, assetPreviewJS{
		Content: res.Content, Truncated: res.Truncated, Binary: res.Binary,
		SHA256: res.SHA256, Size: res.Size, Sensitive: res.Sensitive,
	})
}

// Diff 处理 POST /admin/v2/assets/diff：两侧内容 diff（哈希相同短路 identical；二进制 / 超限拒绝 asset_diff_unsupported）。
func (h *AssetHandler) Diff(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Left   assetRefBody `json:"left"`
		Right  assetRefBody `json:"right"`
		Reason string       `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	res, err := h.svc.Diff(r.Context(), service.DiffParams{
		Left:     service.AssetRef{ServerID: body.Left.ServerID, Path: body.Left.Path},
		Right:    service.AssetRef{ServerID: body.Right.ServerID, Path: body.Right.Path},
		Reason:   body.Reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	out := assetDiffJS{Identical: res.Identical}
	if res.Left != nil {
		out.Left = &assetDiffSideJS{ServerID: res.Left.ServerID, Path: res.Left.Path, Content: res.Left.Content, SHA256: res.Left.SHA256}
	}
	if res.Right != nil {
		out.Right = &assetDiffSideJS{ServerID: res.Right.ServerID, Path: res.Right.Path, Content: res.Right.Content, SHA256: res.Right.SHA256}
	}
	render.WriteJSON(w, http.StatusOK, out)
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
