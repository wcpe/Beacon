package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2ConnectionAdminHandler 处理连接明细的管理面查询端点（FR-145，见 spec §5.2）：
// 列表（connId 直查或条件游标分页）/ 单条详情 / 时间桶聚合。响应形状对齐 contracts ConnectionItem /
// CursorPage / ConnStatsBucket（camelCase 键逐字匹配）。
type V2ConnectionAdminHandler struct {
	svc *service.ConnQueryService
}

// NewV2ConnectionAdminHandler 构造处理器。
func NewV2ConnectionAdminHandler(svc *service.ConnQueryService) *V2ConnectionAdminHandler {
	return &V2ConnectionAdminHandler{svc: svc}
}

// connectionItemJS 是连接明细列表项 / 详情（键对齐 contracts ConnectionItem，camelCase）。
type connectionItemJS struct {
	ConnID               string  `json:"connId"`
	NamespaceID          uint    `json:"namespaceId"`
	ProxyServerID        string  `json:"proxyServerId"`
	PlayerUUID           string  `json:"playerUuid"`
	PlayerName           string  `json:"playerName"`
	ClientIP             string  `json:"clientIp"`
	ProtocolVersion      int     `json:"protocolVersion"`
	OpenedAt             string  `json:"openedAt"`
	ClosedAt             *string `json:"closedAt"`
	DurationMs           *int64  `json:"durationMs"`
	Status               string  `json:"status"`
	CloseKind            *string `json:"closeKind"`
	CloseReason          *string `json:"closeReason"`
	FirstBackendServerID *string `json:"firstBackendServerId"`
	LastBackendServerID  *string `json:"lastBackendServerId"`
	BackendSwitchCount   int     `json:"backendSwitchCount"`
}

// connStatsBucketJS 是连接流时间桶（键对齐 contracts ConnStatsBucket）。
type connStatsBucketJS struct {
	StartAt        string `json:"startAt"`
	Opens          int    `json:"opens"`
	Closes         int    `json:"closes"`
	AbnormalCloses int    `json:"abnormalCloses"`
	EstimatedOpen  int    `json:"estimatedOpen"`
}

// List 处理 GET /admin/v2/connections：connId 精确直查或条件游标分页（查询防护见 §4.3）。
func (h *V2ConnectionAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	page, err := h.svc.List(service.ListConnectionsParams{
		ConnID:      q.Get("connId"),
		ServerID:    q.Get("serverId"),
		PlayerUUID:  q.Get("playerUuid"),
		Status:      q.Get("status"),
		CloseKind:   q.Get("closeKind"),
		NamespaceID: namespaceID,
		FromMs:      parseISOms(q.Get("from")),
		ToMs:        parseISOms(q.Get("to")),
		Cursor:      intQuery(q.Get("cursor")),
		Limit:       intQuery(q.Get("limit")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]connectionItemJS, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, connectionItem(&page.Items[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": nullableStr(page.NextCursor)})
}

// Detail 处理 GET /admin/v2/connections/{connId}：单条连接详情，未命中 404 connection_not_found。
func (h *V2ConnectionAdminHandler) Detail(w http.ResponseWriter, r *http.Request) {
	row, err := h.svc.Detail(chi.URLParam(r, "connId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, connectionItem(&row))
}

// Stats 处理 GET /admin/v2/connections/stats：连接流时间桶聚合（缺 from/to 默认最近 1h，dashboard 玩家流数据源）。
func (h *V2ConnectionAdminHandler) Stats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	toMs := parseISOms(q.Get("to"))
	if toMs <= 0 {
		toMs = time.Now().UTC().UnixMilli()
	}
	fromMs := parseISOms(q.Get("from"))
	if fromMs <= 0 {
		fromMs = toMs - int64(time.Hour/time.Millisecond)
	}
	buckets, err := h.svc.Stats(service.ConnStatsParams{
		ServerID: q.Get("serverId"), FromMs: fromMs, ToMs: toMs, Bucket: q.Get("bucket"),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	out := make([]connStatsBucketJS, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, connStatsBucketJS{
			StartAt: isoFromMs(b.StartMs), Opens: b.Opens, Closes: b.Closes,
			AbnormalCloses: b.AbnormalCloses, EstimatedOpen: b.EstimatedOpen,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"buckets": out})
}

// connectionItem 把连接会话行映射为对外列表项：可空字段（closeKind/closeReason/backend）空串显 null，
// closedAt/durationMs 未断开显 null（对齐 contracts / devmock 语义）。
func connectionItem(row *model.ConnDetail) connectionItemJS {
	item := connectionItemJS{
		ConnID:               row.ConnID,
		NamespaceID:          row.NamespaceID,
		ProxyServerID:        row.ProxyServerID,
		PlayerUUID:           row.PlayerUUID,
		PlayerName:           row.PlayerName,
		ClientIP:             row.ClientIP,
		ProtocolVersion:      row.ProtocolVersion,
		OpenedAt:             isoFromMs(row.OpenedAt.UnixMilli()),
		DurationMs:           row.DurationMs,
		Status:               row.Status,
		CloseKind:            nullableStr(row.CloseKind),
		CloseReason:          nullableStr(row.CloseReason),
		FirstBackendServerID: nullableStr(row.FirstBackendServerID),
		LastBackendServerID:  nullableStr(row.LastBackendServerID),
		BackendSwitchCount:   row.BackendSwitchCount,
	}
	if row.ClosedAt != nil {
		iso := isoFromMs(row.ClosedAt.UnixMilli())
		item.ClosedAt = &iso
	}
	return item
}
