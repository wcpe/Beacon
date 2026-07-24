package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2ConnectionHandler 处理 v2 agent 连接明细批量上报端点（FR-145，见 spec §5.1）。
// handler 只做解码 + 取注入身份 + 时间归一 + 调服务；校验 / 名册 / 入队全在服务层，请求 goroutine 不碰 DB。
type V2ConnectionHandler struct {
	svc *service.ConnIngestService
}

// NewV2ConnectionHandler 构造处理器。
func NewV2ConnectionHandler(svc *service.ConnIngestService) *V2ConnectionHandler {
	return &V2ConnectionHandler{svc: svc}
}

// connBatchRequest 是 POST /beacon/v2/agent/connections/batch 的请求体（camelCase，对齐 spec §5.1）。
type connBatchRequest struct {
	BootID       string          `json:"bootId"`
	DroppedCount int             `json:"droppedCount"`
	Events       []connEventJSON `json:"events"`
}

// connEventJSON 是单条采集事件（open / close），时间为 UTC ISO8601 文本。
type connEventJSON struct {
	Kind               string `json:"kind"`
	ConnID             string `json:"connId"`
	PlayerUUID         string `json:"playerUuid"`
	PlayerName         string `json:"playerName"`
	ClientIP           string `json:"clientIp"`
	ProtocolVersion    int    `json:"protocolVersion"`
	OpenedAt           string `json:"openedAt"`
	ClosedAt           string `json:"closedAt"`
	CloseKind          string `json:"closeKind"`
	CloseReason        string `json:"closeReason"`
	FirstBackend       string `json:"firstBackend"`
	LastBackend        string `json:"lastBackend"`
	BackendSwitchCount int    `json:"backendSwitchCount"`
}

// Batch 处理 POST /beacon/v2/agent/connections/batch：接收 proxy 采集的连接 open / close 事件批。
// 202 {accepted, duplicated}；队列满 429 conn_ingest_busy；结构非法 / 非 proxy 身份 400。
func (h *V2ConnectionHandler) Batch(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req connBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.Ingest(service.ConnBatchParams{
		Identity:     identity,
		BootID:       req.BootID,
		DroppedCount: req.DroppedCount,
		Events:       toServiceConnEvents(req.Events),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{
		"accepted":   result.Accepted,
		"duplicated": result.Duplicated,
	})
}

// toServiceConnEvents 把请求体事件映射为服务层入参（ISO8601 时间归一为毫秒；解析失败置 0 由服务回退）。
func toServiceConnEvents(in []connEventJSON) []service.ConnEventInput {
	out := make([]service.ConnEventInput, 0, len(in))
	for _, e := range in {
		out = append(out, service.ConnEventInput{
			Kind: e.Kind, ConnID: e.ConnID, PlayerUUID: e.PlayerUUID, PlayerName: e.PlayerName,
			ClientIP: e.ClientIP, ProtocolVersion: e.ProtocolVersion,
			OpenedAtMs: parseISOms(e.OpenedAt), ClosedAtMs: parseISOms(e.ClosedAt),
			CloseKind: e.CloseKind, CloseReason: e.CloseReason,
			FirstBackend: e.FirstBackend, LastBackend: e.LastBackend, BackendSwitchCount: e.BackendSwitchCount,
		})
	}
	return out
}

// parseISOms 解析 UTC ISO8601 时间文本为毫秒；空 / 非法返回 0（服务层据 conn_id 内嵌时间回退）。
func parseISOms(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UTC().UnixMilli()
}
