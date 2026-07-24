package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2AssetsHandler 处理文件资产索引端点（FR-163，见 spec §5）：
// agent 面清单上报（token↔namespace + identity 鉴权中间件注入权威身份）+ 管理面搜索 / 概要 / 比对 / 重扫。
// handler 只做解码 + 取注入身份 / 认证操作者 + 调服务；校验 / 事务全在服务层，响应键 camelCase 逐字对齐 contracts。
type V2AssetsHandler struct {
	svc *service.AssetService
}

// NewV2AssetsHandler 构造处理器。
func NewV2AssetsHandler(svc *service.AssetService) *V2AssetsHandler {
	return &V2AssetsHandler{svc: svc}
}

// ---- agent 面：清单上报 ----

// manifestUpsertJS 是上报 upsert 文件项（对齐 spec §5.1 upserts[]）。
type manifestUpsertJS struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	MtimeMs int64  `json:"mtimeMs"`
	IsText  bool   `json:"isText"`
}

// manifestReportRequest 是 POST /beacon/v2/agent/assets/manifest 请求体（camelCase，对齐 §5.1）。
type manifestReportRequest struct {
	Mode           string             `json:"mode"`
	ScannedAt      string             `json:"scannedAt"`
	ScanDurationMs int                `json:"scanDurationMs"`
	Truncated      bool               `json:"truncated"`
	BaseDigest     string             `json:"baseDigest"`
	Upserts        []manifestUpsertJS `json:"upserts"`
	Deleted        []string           `json:"deleted"`
	UploadID       string             `json:"uploadId"`
	Seq            int                `json:"seq"`
	EOF            bool               `json:"eof"`
}

// Manifest 处理 POST /beacon/v2/agent/assets/manifest：接收增量 / 全量清单上报。
// 200 {digest, fileCount}；delta 基线失配 / 全量分片乱序 → 409 asset_manifest_out_of_sync；参数错误 400。
func (h *V2AssetsHandler) Manifest(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		// 未经中间件注入身份即到达（装配错误）；按未授权兜底，不泄露内部细节。
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req manifestReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.ApplyManifest(service.ManifestReportParams{
		Identity:       identity,
		Mode:           req.Mode,
		ScannedAt:      parseScannedAt(req.ScannedAt),
		ScanDurationMs: req.ScanDurationMs,
		Truncated:      req.Truncated,
		BaseDigest:     req.BaseDigest,
		Upserts:        toServiceUpserts(req.Upserts),
		Deleted:        req.Deleted,
		UploadID:       req.UploadID,
		Seq:            req.Seq,
		EOF:            req.EOF,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"digest": result.Digest, "fileCount": result.FileCount})
}

// parseScannedAt 解析上报的扫描时刻；空 / 非法退回当前 UTC（服务层再兜底）。
func parseScannedAt(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// toServiceUpserts 把请求体 upserts 映射为服务层入参（字段直传，不在 handler 做业务归一化）。
func toServiceUpserts(in []manifestUpsertJS) []service.ManifestUpsert {
	out := make([]service.ManifestUpsert, 0, len(in))
	for _, u := range in {
		out = append(out, service.ManifestUpsert{
			Path: u.Path, SHA256: u.SHA256, Size: u.Size, MtimeMs: u.MtimeMs, IsText: u.IsText,
		})
	}
	return out
}

// ---- 管理面：搜索 ----

// assetItemJS 是资产行视图（键对齐 contracts AssetItem，camelCase）。
type assetItemJS struct {
	ServerID    string `json:"serverId"`
	NamespaceID uint   `json:"namespaceId"`
	Path        string `json:"path"`
	Ext         string `json:"ext"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	MtimeMs     int64  `json:"mtimeMs"`
	IsText      bool   `json:"isText"`
	ScannedAt   string `json:"scannedAt"`
}

// Search 处理 GET /admin/v2/assets：组合条件分页搜索（namespaceId 必填，见 §4.4）。
func (h *V2AssetsHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Search(service.AssetSearchParams{
		NamespaceID: namespaceID,
		ServerID:    q.Get("serverId"),
		PathPrefix:  q.Get("pathPrefix"),
		Name:        q.Get("name"),
		Ext:         q.Get("ext"),
		SHA256:      q.Get("sha256"),
		Page:        intQuery(q.Get("page")),
		PageSize:    intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]assetItemJS, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, toAssetItemJS(res.Items[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": res.Total})
}

// toAssetItemJS 组装资产行视图（scannedAt 转 RFC3339）。
func toAssetItemJS(v service.AssetItemView) assetItemJS {
	return assetItemJS{
		ServerID: v.ServerID, NamespaceID: v.NamespaceID, Path: v.Path, Ext: v.Ext,
		SHA256: v.SHA256, Size: v.Size, MtimeMs: v.MtimeMs, IsText: v.IsText,
		ScannedAt: v.ScannedAt.UTC().Format(time.RFC3339),
	}
}

// ---- 管理面：扫描概要 ----

// scanStatusItemJS 是概要视图（键对齐 contracts AssetScanStatusItem）。
type scanStatusItemJS struct {
	ServerID       string `json:"serverId"`
	NamespaceID    uint   `json:"namespaceId"`
	ManifestDigest string `json:"manifestDigest"`
	FileCount      int    `json:"fileCount"`
	TotalSize      int64  `json:"totalSize"`
	Truncated      bool   `json:"truncated"`
	ScannedAt      string `json:"scannedAt"`
	ScanDurationMs int    `json:"scanDurationMs"`
}

// ScanStatus 处理 GET /admin/v2/assets/scan-status：分页列每服扫描概要（见 §5.2）。
func (h *V2AssetsHandler) ScanStatus(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	res, err := h.svc.ScanStatus(service.ScanStatusParams{
		NamespaceID: namespaceID,
		ServerID:    q.Get("serverId"),
		Page:        intQuery(q.Get("page")),
		PageSize:    intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]scanStatusItemJS, 0, len(res.Items))
	for i := range res.Items {
		v := res.Items[i]
		items = append(items, scanStatusItemJS{
			ServerID: v.ServerID, NamespaceID: v.NamespaceID, ManifestDigest: v.ManifestDigest,
			FileCount: v.FileCount, TotalSize: v.TotalSize, Truncated: v.Truncated,
			ScannedAt: v.ScannedAt.UTC().Format(time.RFC3339), ScanDurationMs: v.ScanDurationMs,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": res.Total})
}

// ---- 管理面：跨服比对 ----

// compareMemberJS / compareGroupJS 对齐 contracts AssetCompareGroup。
type compareMemberJS struct {
	ServerID  string `json:"serverId"`
	MtimeMs   int64  `json:"mtimeMs"`
	ScannedAt string `json:"scannedAt"`
}

type compareGroupJS struct {
	SHA256  string            `json:"sha256"`
	Size    int64             `json:"size"`
	Servers []compareMemberJS `json:"servers"`
}

// Compare 处理 GET /admin/v2/assets/compare：跨服同路径哈希分组 + 缺失服（见 §4.4）。
// 范围：serverIds（逗号分隔业务串）/ zoneId 二选一，均空即整 namespace。
func (h *V2AssetsHandler) Compare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	zoneID, err := optionalUintQuery(q.Get("zoneId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Compare(service.CompareParams{
		NamespaceID: namespaceID,
		Path:        q.Get("path"),
		ZoneID:      zoneID,
		ServerIDs:   splitCSV(q.Get("serverIds")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	groups := make([]compareGroupJS, 0, len(res.Groups))
	for i := range res.Groups {
		g := res.Groups[i]
		members := make([]compareMemberJS, 0, len(g.Servers))
		for j := range g.Servers {
			m := g.Servers[j]
			members = append(members, compareMemberJS{
				ServerID: m.ServerID, MtimeMs: m.MtimeMs, ScannedAt: m.ScannedAt.UTC().Format(time.RFC3339),
			})
		}
		groups = append(groups, compareGroupJS{SHA256: g.SHA256, Size: g.Size, Servers: members})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"path": res.Path, "groups": groups, "missing": res.Missing,
	})
}

// ---- 管理面：批量重扫 ----

// rescanRequest 是 POST /admin/v2/assets/rescan 请求体（对齐 contracts RescanBody）。
type rescanRequest struct {
	NamespaceID uint     `json:"namespaceId"`
	ServerIDs   []string `json:"serverIds"`
	Force       bool     `json:"force"`
}

// rescanResultJS 是单服重扫结果（commandId 为 string|null，对齐 contracts AssetRescanResponse）。
type rescanResultJS struct {
	ServerID  string  `json:"serverId"`
	CommandID *string `json:"commandId"`
	Offline   bool    `json:"offline"`
}

// Rescan 处理 POST /admin/v2/assets/rescan：批量下发 asset-rescan 命令（离线服标记不阻断整批，见 §5.2）。
func (h *V2AssetsHandler) Rescan(w http.ResponseWriter, r *http.Request) {
	var req rescanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	res, err := h.svc.Rescan(service.RescanParams{
		NamespaceID: req.NamespaceID,
		ServerIDs:   req.ServerIDs,
		Force:       req.Force,
		Operator:    auth.Operator(r.Context()),
		ClientIP:    clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	results := make([]rescanResultJS, 0, len(res.Results))
	for i := range res.Results {
		item := res.Results[i]
		results = append(results, rescanResultJS{
			ServerID: item.ServerID, CommandID: commandIDToString(item.CommandID), Offline: item.Offline,
		})
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{"results": results})
}

// commandIDToString 把命令数字 id 转为字符串指针（离线服 nil → JSON null）。
func commandIDToString(id *uint) *string {
	if id == nil {
		return nil
	}
	s := strconv.FormatUint(uint64(*id), 10)
	return &s
}

// splitCSV 按逗号切分并去空白 / 空项（serverIds 查询参数解析）。
func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
