package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// defaultMessageStatsWindowMs 是 messages/stats 缺 from/to 时的默认回看窗口（最近 1h，对齐连接流默认）。
// 前端 /topology 边聚合不带时间窗，由后端按此默认窗口聚合近期异常链路（spec §4.5）。
const defaultMessageStatsWindowMs = int64(time.Hour / time.Millisecond)

// V2MessageAdminHandler 处理跨服消息的管理面查询端点（FR-149/150，见 spec §5.2）：
// 列表（messageId/correlationId 直查或条件游标分页）/ 单条详情（hops + 关联摘要）/ 异常链路聚合 /
// payload 受控查看（权限 + 原因 + 先审计）。响应形状对齐 contracts MessageItem/MessageDetail/
// MessageEdgeStat/MessagePayloadResponse（camelCase 键逐字匹配，列表与详情永不含 payload）。
type V2MessageAdminHandler struct {
	queryS   *service.MessageQueryService
	payloadS *service.MessagePayloadService
	settings *service.SettingsService // 冷查询读 archive.cold-query-max-days（FR-152）
}

// NewV2MessageAdminHandler 构造处理器。
func NewV2MessageAdminHandler(queryS *service.MessageQueryService, payloadS *service.MessagePayloadService, settings *service.SettingsService) *V2MessageAdminHandler {
	return &V2MessageAdminHandler{queryS: queryS, payloadS: payloadS, settings: settings}
}

// messageItemJS 是消息元数据列表项（键对齐 contracts MessageItem，**永不含 payload**）。
// 广播聚合字段（FR-180）为 additive 键：仅广播行输出（omitempty），定向 / 按玩家行键集合不变。
type messageItemJS struct {
	MessageID         string  `json:"messageId"`
	NamespaceID       uint    `json:"namespaceId"`
	SourceServerID    string  `json:"sourceServerId"`
	MsgType           string  `json:"msgType"`
	TargetKind        string  `json:"targetKind"`
	TargetServerID    *string `json:"targetServerId"`
	TargetPlayer      *string `json:"targetPlayer"`
	TargetZone        *string `json:"targetZone,omitempty"`
	FanoutTotal       *int    `json:"fanoutTotal,omitempty"`
	DeliveredCount    *int    `json:"deliveredCount,omitempty"`
	FailedCount       *int    `json:"failedCount,omitempty"`
	ExpiredCount      *int    `json:"expiredCount,omitempty"`
	ResolvedServerID  *string `json:"resolvedServerId"`
	TargetNamespaceID *uint   `json:"targetNamespaceId"`
	CrossNamespace    bool    `json:"crossNamespace"`
	CorrelationID     *string `json:"correlationId"`
	Status            string  `json:"status"`
	FailReason        *string `json:"failReason"`
	CreatedAt         string  `json:"createdAt"`
	DispatchedAt      *string `json:"dispatchedAt"`
	DeliveredAt       *string `json:"deliveredAt"`
	DurationMs        *int64  `json:"durationMs"`
	HopCount          int     `json:"hopCount"`
	PayloadSize       int     `json:"payloadSize"`
	PayloadStored     bool    `json:"payloadStored"`
}

// msgHopJS 是链路 hop 事件（键对齐 contracts MsgHop；不含内部 reason 字段）。
type msgHopJS struct {
	Seq    int    `json:"seq"`
	Node   string `json:"node"`
	Event  string `json:"event"`
	At     string `json:"at"`
	CostMs int64  `json:"costMs,omitempty"`
}

// correlatedJS 是 RPC 往返关联消息摘要（键对齐 contracts MessageDetail.correlated）。
type correlatedJS struct {
	MessageID string `json:"messageId"`
	MsgType   string `json:"msgType"`
	Status    string `json:"status"`
}

// messageDetailJS 是消息详情（元数据 + hops 链路 + 关联摘要，键对齐 contracts MessageDetail）。
type messageDetailJS struct {
	messageItemJS
	Hops       []msgHopJS    `json:"hops"`
	Correlated *correlatedJS `json:"correlated"`
}

// msgFailReasonJS / msgEdgeStatJS 是异常链路边聚合（键对齐 contracts MessageEdgeStat）。
type msgFailReasonJS struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type msgEdgeStatJS struct {
	SourceServerID   string            `json:"sourceServerId"`
	ResolvedServerID string            `json:"resolvedServerId"`
	Total            int               `json:"total"`
	Failed           int               `json:"failed"`
	Expired          int               `json:"expired"`
	FailRatePercent  float64           `json:"failRatePercent"`
	P95DurationMs    int64             `json:"p95DurationMs"`
	TopFailReasons   []msgFailReasonJS `json:"topFailReasons"`
	SampleMessageIDs []string          `json:"sampleMessageIds"`
}

// msgTypeStatJS 是按消息类型计数（devmock 内联形态 {msgType,total,failed}）。
type msgTypeStatJS struct {
	MsgType string `json:"msgType"`
	Total   int    `json:"total"`
	Failed  int    `json:"failed"`
}

// payloadResponseJS 是 payload 查看响应（键对齐 contracts MessagePayloadResponse）。
type payloadResponseJS struct {
	Payload string `json:"payload"`
	SHA256  string `json:"sha256"`
	Size    int    `json:"size"`
}

// List 处理 GET /admin/v2/messages：messageId/correlationId 精确直查或条件游标分页（永不含 payload，查询防护见 §4.3）。
func (h *V2MessageAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	crossNS, err := optionalBoolQuery(q.Get("crossNamespace"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	fromMs, toMs := parseISOms(q.Get("from")), parseISOms(q.Get("to"))
	includeArchived := coldQueryRequested(q)
	// 冷查询强制时间范围校验（messageId/correlationId 直查免范围，交服务层直查分支）。
	if includeArchived && q.Get("messageId") == "" && q.Get("correlationId") == "" {
		if err := validateColdQueryRange(fromMs, toMs, coldQueryMaxDays(h.settings)); err != nil {
			render.WriteError(w, r, err)
			return
		}
	}
	page, err := h.queryS.List(service.ListMessagesParams{
		MessageID:       q.Get("messageId"),
		CorrelationID:   q.Get("correlationId"),
		ServerID:        q.Get("serverId"),
		PlayerUUID:      q.Get("playerUuid"),
		Status:          q.Get("status"),
		MsgType:         q.Get("msgType"),
		TargetKind:      q.Get("targetKind"),
		CrossNamespace:  crossNS,
		NamespaceID:     namespaceID,
		FromMs:          fromMs,
		ToMs:            toMs,
		Cursor:          intQuery(q.Get("cursor")),
		Limit:           intQuery(q.Get("limit")),
		IncludeArchived: includeArchived,
		ColdCursor:      q.Get("cursor"),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]messageItemJS, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, messageItem(&page.Items[i]))
	}
	body := map[string]any{"items": items, "nextCursor": nullableStr(page.NextCursor)}
	if includeArchived {
		body["includeArchived"] = true // 冷查询结果元信息（spec §4.4）
	}
	render.WriteJSON(w, http.StatusOK, body)
}

// Detail 处理 GET /admin/v2/messages/{messageId}：元数据 + hops 链路 + 关联摘要（payload 仅元信息，未命中 404）。
func (h *V2MessageAdminHandler) Detail(w http.ResponseWriter, r *http.Request) {
	res, err := h.queryS.Detail(chi.URLParam(r, "messageId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	detail := messageDetailJS{
		messageItemJS: messageItem(&res.Trace),
		Hops:          parseHops(res.Trace.Hops),
	}
	if res.Correlated != nil {
		detail.Correlated = &correlatedJS{
			MessageID: res.Correlated.MessageID, MsgType: res.Correlated.MsgType, Status: res.Correlated.Status,
		}
	}
	render.WriteJSON(w, http.StatusOK, detail)
}

// Stats 处理 GET /admin/v2/messages/stats：groupBy=type 返回 {types}，其余（edge/默认）返回 {edges}（/topology 数据源）。
func (h *V2MessageAdminHandler) Stats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	toMs := parseISOms(q.Get("to"))
	if toMs <= 0 {
		toMs = time.Now().UTC().UnixMilli()
	}
	fromMs := parseISOms(q.Get("from"))
	if fromMs <= 0 {
		fromMs = toMs - defaultMessageStatsWindowMs
	}
	result, err := h.queryS.Stats(service.MessageStatsParams{GroupBy: q.Get("groupBy"), FromMs: fromMs, ToMs: toMs})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if result.GroupBy == "type" {
		types := make([]msgTypeStatJS, 0, len(result.Types))
		for _, t := range result.Types {
			types = append(types, msgTypeStatJS{MsgType: t.MsgType, Total: t.Total, Failed: t.Failed})
		}
		render.WriteJSON(w, http.StatusOK, map[string]any{"types": types})
		return
	}
	edges := make([]msgEdgeStatJS, 0, len(result.Edges))
	for i := range result.Edges {
		edges = append(edges, edgeStat(&result.Edges[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"edges": edges})
}

// Payload 处理 POST /admin/v2/messages/{messageId}/payload：原因必填 → 先写审计后返回内容（无权限由 readonlyWriteGuard 403）。
func (h *V2MessageAdminHandler) Payload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	// 解析失败（空 / 坏体）等同缺原因，交由服务层按 missing_reason 400 裁决。
	_ = json.NewDecoder(r.Body).Decode(&body)
	res, err := h.payloadS.View(service.ViewPayloadParams{
		MessageID: chi.URLParam(r, "messageId"),
		Reason:    body.Reason,
		Operator:  auth.Operator(r.Context()),
		ClientIP:  clientIP(r),
		TraceID:   render.TraceID(r.Context()),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, payloadResponseJS{Payload: res.Payload, SHA256: res.SHA256, Size: res.Size})
}

// messageItem 把消息元数据行映射为对外列表项：可空字段（target/resolved/correlation/failReason）空串显 null，
// 未派发/未送达时间显 null（对齐 contracts / devmock 语义）。**绝不含 payload 内容**。
func messageItem(row *model.MsgTrace) messageItemJS {
	item := messageItemJS{
		MessageID:         row.MessageID,
		NamespaceID:       row.NamespaceID,
		SourceServerID:    row.SourceServerID,
		MsgType:           row.MsgType,
		TargetKind:        row.TargetKind,
		TargetServerID:    nullableStr(row.TargetServerID),
		TargetPlayer:      nullableStr(row.TargetPlayer),
		TargetZone:        row.TargetZone,
		FanoutTotal:       row.FanoutTotal,
		DeliveredCount:    row.DeliveredCount,
		FailedCount:       row.FailedCount,
		ExpiredCount:      row.ExpiredCount,
		ResolvedServerID:  nullableStr(row.ResolvedServerID),
		TargetNamespaceID: row.TargetNamespaceID,
		CrossNamespace:    row.CrossNamespace,
		CorrelationID:     nullableStr(row.CorrelationID),
		Status:            row.Status,
		FailReason:        nullableStr(row.FailReason),
		CreatedAt:         isoFromMs(row.CreatedAt.UnixMilli()),
		DurationMs:        row.DurationMs,
		HopCount:          row.HopCount,
		PayloadSize:       row.PayloadSize,
		PayloadStored:     row.PayloadStored,
	}
	if row.DispatchedAt != nil {
		iso := isoFromMs(row.DispatchedAt.UnixMilli())
		item.DispatchedAt = &iso
	}
	if row.DeliveredAt != nil {
		iso := isoFromMs(row.DeliveredAt.UnixMilli())
		item.DeliveredAt = &iso
	}
	return item
}

// parseHops 解析 msg_trace.hops JSON 数组文本为对外 hop 列表；空 / 非法文本按空数组处理（防坏行拖垮查询）。
func parseHops(raw string) []msgHopJS {
	hops := []msgHopJS{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &hops)
	}
	if hops == nil {
		hops = []msgHopJS{}
	}
	return hops
}

// edgeStat 把边聚合结果映射为对外形态（topFailReasons / sampleMessageIds 保证非 nil 切片）。
func edgeStat(e *service.MsgEdgeStat) msgEdgeStatJS {
	reasons := make([]msgFailReasonJS, 0, len(e.TopFailReasons))
	for _, r := range e.TopFailReasons {
		reasons = append(reasons, msgFailReasonJS{Reason: r.Reason, Count: r.Count})
	}
	samples := e.SampleMessageIDs
	if samples == nil {
		samples = []string{}
	}
	return msgEdgeStatJS{
		SourceServerID: e.SourceServerID, ResolvedServerID: e.ResolvedServerID,
		Total: e.Total, Failed: e.Failed, Expired: e.Expired,
		FailRatePercent: e.FailRatePercent, P95DurationMs: e.P95DurationMs,
		TopFailReasons: reasons, SampleMessageIDs: samples,
	}
}
