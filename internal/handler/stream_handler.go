package handler

import (
	"net/http"

	"beacon/internal/apperr"
	"beacon/internal/render"
	"beacon/internal/service"
	"beacon/internal/sse"
)

// StreamHandler 处理 agent 单条 SSE 推送流（取代配置/文件树/覆盖集三条长轮询，见 ADR-0015）。
//
// 连接建立时 agent 上报各通道当前 md5（查询参数）；本处理器校验注册后挂起连接，
// 委托 StreamService 完成"连接即对账补增量 + 转直播只发变更通知"。
type StreamHandler struct {
	svc       *service.InstanceService
	streamSvc *service.StreamService
}

// NewStreamHandler 构造处理器。
func NewStreamHandler(svc *service.InstanceService, streamSvc *service.StreamService) *StreamHandler {
	return &StreamHandler{svc: svc, streamSvc: streamSvc}
}

// Stream 处理 GET /beacon/v1/agent/stream（held-open，text/event-stream）。
func (h *StreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, serverID := q.Get("namespace"), q.Get("serverId")
	if ns == "" || serverID == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
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
