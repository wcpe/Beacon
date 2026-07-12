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

// V2MessageHandler 处理 v2 agent 跨服消息端点（FR-149/150，见 spec §5.1）：send 上行 / poll 长轮询下行 / ack 回执。
// handler 只做解码 + 取注入身份 + 时间归一 + 调服务；寻址 / 信任 / 中转 / 落库全在服务层，请求 goroutine 不碰 DB。
type V2MessageHandler struct {
	svc *service.MessageService
}

// NewV2MessageHandler 构造处理器。
func NewV2MessageHandler(svc *service.MessageService) *V2MessageHandler {
	return &V2MessageHandler{svc: svc}
}

// msgSendRequest 是 POST /beacon/v2/agent/messages/send 的请求体（camelCase，对齐 spec §5.1；
// targetZone 为广播 zone 级定向的 additive 键，FR-180）。
type msgSendRequest struct {
	MessageID        string `json:"messageId"`
	MsgType          string `json:"msgType"`
	TargetKind       string `json:"targetKind"`
	TargetServerID   string `json:"targetServerId"`
	TargetPlayerUUID string `json:"targetPlayerUuid"`
	TargetZone       string `json:"targetZone"`
	CorrelationID    string `json:"correlationId"`
	Payload          string `json:"payload"`
	SentAt           string `json:"sentAt"`
}

// Send 处理 POST /beacon/v2/agent/messages/send：上行一条跨服消息。
// 200 {messageId, status}；跨域无信任 403；payload 超限 400 payload_too_large；目标无效 200 status=failed。
func (h *V2MessageHandler) Send(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req msgSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.Send(service.MessageSendParams{
		Identity: identity, MessageID: req.MessageID, MsgType: req.MsgType,
		TargetKind: req.TargetKind, TargetServerID: req.TargetServerID,
		TargetPlayerUUID: req.TargetPlayerUUID, TargetZone: req.TargetZone,
		CorrelationID: req.CorrelationID,
		Payload:       req.Payload, SentAtMs: parseISOms(req.SentAt),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"messageId": result.MessageID,
		"status":    result.Status,
	})
}

// msgPollRequest 是 POST /beacon/v2/agent/messages/poll 的请求体。
type msgPollRequest struct {
	WaitSec int `json:"waitSec"`
	Max     int `json:"max"`
}

// Poll 处理 POST /beacon/v2/agent/messages/poll：长轮询取本服待投消息。
// 200 {messages:[...]}；无消息超时 204。
func (h *V2MessageHandler) Poll(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req msgPollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	msgs := h.svc.PollMessages(r.Context(), identity, req.WaitSec, req.Max)
	if len(msgs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		item := map[string]any{
			"messageId":      m.MessageID,
			"msgType":        m.MsgType,
			"sourceServerId": m.SourceServerID,
			"payload":        m.Payload,
			"createdAt":      isoFromMs(m.CreatedAtMs),
		}
		if m.CorrelationID != "" {
			item["correlationId"] = m.CorrelationID
		}
		if m.Broadcast {
			// 广播投递标记（additive 键，FR-180）：定向消息不带，agent 据此路由 topic 订阅分发。
			item["broadcast"] = true
		}
		out = append(out, item)
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"messages": out})
}

// msgAckRequest 是 POST /beacon/v2/agent/messages/ack 的请求体。
type msgAckRequest struct {
	Results []msgAckResultJSON `json:"results"`
}

// msgAckResultJSON 是单条回执。
type msgAckResultJSON struct {
	MessageID     string `json:"messageId"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	DeliveredAt   string `json:"deliveredAt"`
	HandlerCostMs int    `json:"handlerCostMs"`
}

// Ack 处理 POST /beacon/v2/agent/messages/ack：批量回执。200 {applied, ignored}（未知 messageId 计 ignored）。
func (h *V2MessageHandler) Ack(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req msgAckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	applied, ignored := h.svc.AckMessages(identity, toServiceAckResults(req.Results))
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"applied": applied,
		"ignored": ignored,
	})
}

// toServiceAckResults 把请求体回执映射为服务层回执（deliveredAt ISO 归一为毫秒）。
func toServiceAckResults(in []msgAckResultJSON) []service.AckResult {
	out := make([]service.AckResult, 0, len(in))
	for _, r := range in {
		out = append(out, service.AckResult{
			MessageID: r.MessageID, Status: r.Status, Reason: r.Reason,
			DeliveredAtMs: parseISOms(r.DeliveredAt), HandlerCostMs: r.HandlerCostMs,
		})
	}
	return out
}

// isoFromMs 把毫秒时刻格式化为 UTC ISO8601（毫秒精度）；非正值返回空串。
func isoFromMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}
