package handler

import (
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/sse"
)

// StreamHandler 处理 agent 单条 SSE 推送流（取代配置/文件树/覆盖集三条长轮询，见 ADR-0015）。
//
// 连接建立时 agent 上报各通道当前 md5（查询参数）；本处理器校验注册后挂起连接，
// 委托 StreamService 完成"连接即对账补增量 + 转直播只发变更通知"。
type StreamHandler struct {
	svc       *service.InstanceService
	streamSvc *service.StreamService
	// reportAuth 解析推送流订阅者的权威身份。
	reportAuth agentReportAuthenticator
}

// NewStreamHandler 构造处理器。
func NewStreamHandler(svc *service.InstanceService, streamSvc *service.StreamService) *StreamHandler {
	return &StreamHandler{svc: svc, streamSvc: streamSvc}
}

// SetReportAuthenticator 注入推送流订阅者的权威身份校验器。
func (h *StreamHandler) SetReportAuthenticator(authn agentReportAuthenticator) {
	h.reportAuth = authn
}

// Stream 处理 GET /beacon/v1/agent/stream（held-open，text/event-stream）。
//
// 必须绑定身份：本流推送的是「该实例的配置 / 文件树 / 覆盖集 / 拓扑已变更」通知（含 md5 与变更范围），
// 订阅归属仅由 query 自报。共享 token 下不绑定身份，任一 agent 都能订阅别人的变更流，
// 从而旁路拿到他服的配置变动节奏与文件清单指纹。故连接建立前先取权威身份并收敛归属。
func (h *StreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// 入参契约不变：namespace / serverId 必须显式给出（缺参仍是 400 INVALID_PARAM）。
	if q.Get("namespace") == "" || q.Get("serverId") == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	identity, ok := authenticateAgentReport(w, r, h.reportAuth)
	if !ok {
		return
	}
	ns, serverID, ok := bindAgentTarget(w, r, identity, q.Get("namespace"), q.Get("serverId"))
	if !ok {
		return
	}
	groupHint, err := h.svc.RequireRegistered(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err) // 未注册 → 404 NOT_REGISTERED（与长轮询一致）
		return
	}

	// 流式写出必需 Flusher；不支持则按 500 拒绝（标准库 ResponseWriter 一般都实现）。
	flusher, ok := w.(http.Flusher)
	if !ok {
		render.WriteError(w, r, apperr.ErrStreamingUnsupported)
		return
	}

	// SSE 标准响应头：禁缓存、保持连接、关代理缓冲（X-Accel-Buffering 让 nginx 不缓冲，见 OPERATIONS 反代注意）。
	h.writeSSEHeaders(w)
	flusher.Flush()

	reported := service.ChannelMD5{
		Config:   q.Get("configMd5"),
		File:     q.Get("fileMd5"),
		Override: q.Get("overrideMd5"),
		Topology: q.Get("topologyMd5"),
	}
	sink := &flushSink{w: w, flusher: flusher}
	// 同步阻塞直到客户端断连（r.Context 取消）/服务关停/写出失败；连接级失败不再向已劫持的流写错误体。
	_ = h.streamSvc.Run(r.Context(), ns, serverID, groupHint, reported, sink)
}

// writeSSEHeaders 写 SSE 必需响应头。
func (h *StreamHandler) writeSSEHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream; charset=utf-8")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("Connection", "keep-alive")
	// 关闭 nginx 等反向代理的响应缓冲，保证事件即时到达 agent（见 ADR-0015 决策 10）。
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// flushSink 把 SSE 事件写到 ResponseWriter 并立即 flush，实现 service.EventSink。
type flushSink struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// Send 编码并写出一条事件，写后 flush；写失败（客户端断连）返回 error 让编排循环退出。
func (s *flushSink) Send(e sse.Event) error {
	if _, err := s.w.Write([]byte(sse.Encode(e))); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}
