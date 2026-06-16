package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"beacon/internal/apperr"
	"beacon/internal/render"
	"beacon/internal/runtime"
	"beacon/internal/service"
)

// AgentHandler 处理 agent 侧请求（register / heartbeat / config.effective / report / discovery）。
type AgentHandler struct {
	svc     *service.InstanceService
	effSvc  *service.EffectiveService
	maxHold time.Duration
}

// NewAgentHandler 构造处理器。
func NewAgentHandler(svc *service.InstanceService, effSvc *service.EffectiveService, maxHold time.Duration) *AgentHandler {
	return &AgentHandler{svc: svc, effSvc: effSvc, maxHold: maxHold}
}

// registerRequest 是注册请求体（capacity/weight 顶层、metadata 自定义、无 canary）。
type registerRequest struct {
	Namespace string            `json:"namespace"`
	ServerID  string            `json:"serverId"`
	Role      string            `json:"role"`
	GroupHint string            `json:"groupHint"`
	Address   string            `json:"address"`
	Version   string            `json:"version"`
	Capacity  int               `json:"capacity"`
	Weight    int               `json:"weight"`
	Metadata  map[string]string `json:"metadata"`
}

// registerResponse 是注册响应（未分配时 resolvedZone 为 null）。
type registerResponse struct {
	InstanceKey          string  `json:"instanceKey"`
	ResolvedGroup        string  `json:"resolvedGroup"`
	ResolvedZone         *string `json:"resolvedZone"`
	HeartbeatIntervalSec int     `json:"heartbeatIntervalSec"`
	TTLSec               int     `json:"ttlSec"`
	Assigned             bool    `json:"assigned"`
}

// Register 处理 POST /beacon/v1/agent/register。
func (h *AgentHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	res, err := h.svc.Register(service.RegisterParams{
		Namespace: req.Namespace, ServerID: req.ServerID, Role: req.Role, GroupHint: req.GroupHint,
		Address: req.Address, Version: req.Version, Capacity: req.Capacity, Weight: req.Weight, Metadata: req.Metadata, ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, registerResponse{
		InstanceKey: res.InstanceKey, ResolvedGroup: res.ResolvedGroup, ResolvedZone: nilIfEmpty(res.ResolvedZone),
		HeartbeatIntervalSec: res.HeartbeatIntervalSec, TTLSec: res.TTLSec, Assigned: res.Assigned,
	})
}

// heartbeatRequest 是心跳请求体。
type heartbeatRequest struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
}

// Heartbeat 处理 POST /beacon/v1/agent/heartbeat。
func (h *AgentHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ttlSec, err := h.svc.Heartbeat(req.Namespace, req.ServerID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	// configDirty 为优化提示位，长轮询本身能感知变更；M2 暂返 false
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "ttlSec": ttlSec, "configDirty": false})
}

// reportRequest 是状态上报请求体（playerCount/tps 仅展示）。
type reportRequest struct {
	Namespace   string  `json:"namespace"`
	ServerID    string  `json:"serverId"`
	AppliedMD5  string  `json:"appliedMd5"`
	PlayerCount int     `json:"playerCount"`
	TPS         float64 `json:"tps"`
}

// Report 处理 POST /beacon/v1/agent/report。
func (h *AgentHandler) Report(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if err := h.svc.Report(req.Namespace, req.ServerID, req.AppliedMD5, req.PlayerCount, req.TPS); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Discover 处理 GET /beacon/v1/agent/discovery（仅返回在线实例）。
func (h *AgentHandler) Discover(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	insts := h.svc.Discover(runtime.Filter{
		Namespace: q.Get("namespace"), Group: q.Get("group"), Zone: q.Get("zone"), Role: q.Get("role"),
	})
	render.WriteJSON(w, http.StatusOK, map[string]any{"instances": toInstanceViews(insts)})
}

// effectiveItemView 是有效配置中单个 dataId 的视图。
type effectiveItemView struct {
	DataID  string `json:"dataId"`
	Format  string `json:"format"`
	MD5     string `json:"md5"`
	Content string `json:"content"`
}

// Effective 处理 GET /beacon/v1/agent/config/effective（长轮询）。
// 当前 md5 ≠ 请求 md5 → 立即 200；挂起期间被唤醒且重算后变化 → 200；超时无变化 → 304。
func (h *AgentHandler) Effective(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, serverID, agentMD5 := q.Get("namespace"), q.Get("serverId"), q.Get("md5")
	if ns == "" || serverID == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	groupHint, err := h.svc.RequireRegistered(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err) // 未注册 → 404 NOT_REGISTERED
		return
	}
	timeout := h.maxHold
	if ms := q.Get("timeoutMs"); ms != "" {
		if v, e := strconv.Atoi(ms); e == nil && v > 0 {
			if d := time.Duration(v) * time.Millisecond; d < timeout {
				timeout = d // 取 min(客户端 timeoutMs, 服务端上限)
			}
		}
	}
	eff, changed, err := h.effSvc.WaitEffective(r.Context(), ns, serverID, groupHint, agentMD5, timeout)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if !changed {
		w.WriteHeader(http.StatusNotModified) // 304：无变更到超时
		return
	}
	items := make([]effectiveItemView, 0, len(eff.Items))
	for _, it := range eff.Items {
		items = append(items, effectiveItemView{DataID: it.DataID, Format: it.Format, MD5: it.MD5, Content: it.Content})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"namespace": eff.Namespace, "serverId": eff.ServerID,
		"group": eff.Group, "zone": nilIfEmpty(eff.Zone),
		"md5": eff.MD5, "items": items,
	})
}

// nilIfEmpty 把空串转为 nil（JSON 输出 null）。
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
