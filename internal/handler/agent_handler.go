package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"beacon/internal/apperr"
	"beacon/internal/render"
	"beacon/internal/runtime"
	"beacon/internal/service"
)

// tagParamPrefix 是发现端点自定义元数据过滤查询参数前缀（tag.<key>=<value>，FR-29）。
const tagParamPrefix = "tag."

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
	// Backends 是 bc 上报的当前后端子服 serverId 集合（可选，仅 bc 填、旧 agent/bukkit 缺键 → nil，向后兼容，FR-36）。
	Backends []string `json:"backends,omitempty"`
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
		Address: req.Address, Version: req.Version, Capacity: req.Capacity, Weight: req.Weight, Metadata: req.Metadata,
		Backends: req.Backends, ClientIP: clientIP(r),
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
	// configDirty 为可选优化提示位：P1 有意不实现，变更感知由长轮询负责，故恒返 false（提示位归档 P2，agent 不依赖它）。
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "ttlSec": ttlSec, "configDirty": false})
}

// cpuLoadUnavailable 是 cpuLoad 缺失时的缺省哨兵（不可用，与 agent 约定一致，FR-32 / ADR-0023）。
const cpuLoadUnavailable = -1.0

// reportRequest 是状态上报请求体（人数 / TPS / 内存 / CPU 均仅展示，不参与决策，FR-32）。
// CPULoad 用指针区分「旧 agent 缺键」与「显式上报 0」：缺键时由 applyDefaults 缺省为 -1.0（不可用）。
type reportRequest struct {
	Namespace   string   `json:"namespace"`
	ServerID    string   `json:"serverId"`
	AppliedMD5  string   `json:"appliedMd5"`
	PlayerCount int      `json:"playerCount"`
	TPS         float64  `json:"tps"`
	MemUsed     int64    `json:"memUsed"` // 旧 agent 缺键 → 解析为 0（向后兼容）
	MemMax      int64    `json:"memMax"`  // 旧 agent 缺键 → 解析为 0（向后兼容）
	CPULoad     *float64 `json:"cpuLoad"` // 旧 agent 缺键 → applyDefaults 后缺省 -1.0（不可用）
	// Backends 是 bc 上报的当前后端子服 serverId 集合（FR-36）。用指针区分「缺键」与「显式空集」：
	// nil=旧 agent/bukkit 不报（保留原集合不动）；非空指针=bc 显式上报（含空集，即清空）。
	Backends *[]string `json:"backends,omitempty"`
	// Proxy 是 bc 专属负载指标（FR-34）。用指针区分「缺键」与「上报」：
	// nil=旧 agent/bukkit 不报（不刷新 Proxy 字段）；非 nil=bc 上报（刷新）。
	Proxy *proxyReport `json:"proxy,omitempty"`
}

// proxyReport 是 report 请求体中的 BC 专属指标子对象（FR-34，仅 bc 填）。
// 全部基础数值；缺省字段解析为 0（JSON 数值缺键），backendAvgLatencyMs 缺键解析为 0 不影响展示判定。
type proxyReport struct {
	OnlineConnections   int     `json:"onlineConnections"`
	ThreadCount         int     `json:"threadCount"`
	UptimeMs            int64   `json:"uptimeMs"`
	BackendUp           int     `json:"backendUp"`
	BackendTotal        int     `json:"backendTotal"`
	BackendAvgLatencyMs float64 `json:"backendAvgLatencyMs"`
}

// toRuntime 把请求体 proxy 子对象映射为运行态 BC 指标；nil 接收者返回 nil（缺键不刷新）。
func (p *proxyReport) toRuntime() *runtime.ProxyMetrics {
	if p == nil {
		return nil
	}
	return &runtime.ProxyMetrics{
		OnlineConnections:   p.OnlineConnections,
		ThreadCount:         p.ThreadCount,
		UptimeMs:            p.UptimeMs,
		BackendUp:           p.BackendUp,
		BackendTotal:        p.BackendTotal,
		BackendAvgLatencyMs: p.BackendAvgLatencyMs,
	}
}

// applyDefaults 为旧 agent 缺失的 cpuLoad 填入不可用哨兵 -1.0（内存键缺失天然为 0，无需处理）。
func (req *reportRequest) applyDefaults() {
	if req.CPULoad == nil {
		v := cpuLoadUnavailable
		req.CPULoad = &v
	}
}

// cpuLoadOrUnavailable 返回归一化后的 CPU 负载（applyDefaults 后始终非空）。
func (req *reportRequest) cpuLoadOrUnavailable() float64 {
	if req.CPULoad == nil {
		return cpuLoadUnavailable
	}
	return *req.CPULoad
}

// Report 处理 POST /beacon/v1/agent/report。
func (h *AgentHandler) Report(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	req.applyDefaults()
	if err := h.svc.Report(service.ReportParams{
		Namespace: req.Namespace, ServerID: req.ServerID, AppliedMD5: req.AppliedMD5,
		PlayerCount: req.PlayerCount, TPS: req.TPS, MemUsed: req.MemUsed, MemMax: req.MemMax,
		CPULoad: req.cpuLoadOrUnavailable(), Backends: req.Backends, Proxy: req.Proxy.toRuntime(),
	}); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Discover 处理 GET /beacon/v1/agent/discovery（仅返回可用实例：online+degraded）。
// 支持按 role/zone/group 与自定义元数据 tag 过滤；tag 以重复查询参数 tag.<key>=<value> 传入（多 tag 取交集，FR-29）。
func (h *AgentHandler) Discover(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	insts := h.svc.Discover(runtime.Filter{
		Namespace: q.Get("namespace"), Group: q.Get("group"), Zone: q.Get("zone"), Role: q.Get("role"),
		Tags: parseTagParams(q),
	})
	render.WriteJSON(w, http.StatusOK, map[string]any{"instances": toInstanceViews(insts)})
}

// parseTagParams 从查询串解析 tag.<key>=<value> 形式的自定义元数据过滤条件；无 tag 返回 nil（不过滤）。
func parseTagParams(q url.Values) map[string]string {
	var tags map[string]string
	for key, vals := range q {
		if !strings.HasPrefix(key, tagParamPrefix) || len(vals) == 0 {
			continue
		}
		name := key[len(tagParamPrefix):]
		if name == "" {
			continue
		}
		if tags == nil {
			tags = make(map[string]string)
		}
		// 同名 tag 取最后一个值（与标准查询参数取值一致）。
		tags[name] = vals[len(vals)-1]
	}
	return tags
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
