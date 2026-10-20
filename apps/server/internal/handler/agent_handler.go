package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// tagParamPrefix 是发现端点自定义元数据过滤查询参数前缀（tag.<key>=<value>，FR-29）。
const tagParamPrefix = "tag."

// AgentHandler 处理 agent 侧请求（register / heartbeat / config.effective / report / discovery）。
type AgentHandler struct {
	svc      *service.InstanceService
	effSvc   *service.EffectiveService
	settings longpollSettings // 长轮询挂起上限从设置 store 读，热生效（FR-61）
	// reportAuth 解析心跳 / 上报 / 声明 / 配置 / 发现各端点的调用者权威身份。
	reportAuth agentReportAuthenticator
}

// NewAgentHandler 构造处理器。
func NewAgentHandler(svc *service.InstanceService, effSvc *service.EffectiveService, settings longpollSettings) *AgentHandler {
	return &AgentHandler{svc: svc, effSvc: effSvc, settings: settings}
}

// SetReportAuthenticator 注入 agent 数据面读写下端点的权威身份校验器。
func (h *AgentHandler) SetReportAuthenticator(authn agentReportAuthenticator) {
	h.reportAuth = authn
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
	// AgentVersion 是 agent 自身构建版本（可选，FR-86，见 ADR-0039）：agent 注册自报、旧 agent 缺键 → 空串，向后兼容。
	AgentVersion string `json:"agentVersion"`
	// Backends 是 bc 上报的当前后端子服 serverId 集合（可选，仅 bc 填、旧 agent/bukkit 缺键 → nil，向后兼容，FR-36）。
	Backends []string `json:"backends,omitempty"`
}

// registerResponse 是注册响应（未分配时 resolvedZone 为 null）。
// machineRegistered / identityId / boundAt 仅机器注册通道（FR-222）命中时非空，常规注册保持原状（旧 agent 缺键即可）。
type registerResponse struct {
	InstanceKey          string  `json:"instanceKey"`
	ResolvedGroup        string  `json:"resolvedGroup"`
	ResolvedZone         *string `json:"resolvedZone"`
	HeartbeatIntervalSec int     `json:"heartbeatIntervalSec"`
	TTLSec               int     `json:"ttlSec"`
	Assigned             bool    `json:"assigned"`
	// MachineRegistered 标记本次注册经机器注册通道直落 active 并完成绑定（FR-222）。
	MachineRegistered bool `json:"machineRegistered,omitempty"`
	// IdentityID 是机器注册生成 / 复用的 agent 身份标识（仅 machineRegistered=true 时回带）。
	IdentityID string `json:"identityId,omitempty"`
	// BoundAt 是机器注册的激活绑定时刻（仅 machineRegistered=true 时回带）。
	BoundAt *time.Time `json:"boundAt,omitempty"`
}

// Register 处理 POST /beacon/v1/agent/data-plane/attach（FR-233 更名后的规范路径，见 ADR-0084）。
// 旧路径 POST /beacon/v1/agent/register 保留一个版本周期作兼容别名，与本端点共用同一 handler；
// 两者运行时行为完全一致，仅旧路径在路由层额外回带 Deprecation / Link 响应头。
//
// 本端点承担两件事：① 数据面挂载（写内存 registry + instance.register 审计，服务全部心跳 / 配置 /
// 文件 / 命令等数据面端点）；② 机器注册直落（FR-222）——请求命中 agentTokenMiddleware 的共享 token 分支时
// 被判定为「受信内部调用方」，开启 mcp.allow-machine-register 后把身份直落 active 并绑定 serverId。
// 注意与 v2 /beacon/v2/agent/register（身份状态机 pending→审批→active）的分工：机器注册直落**不在** v2 端点，
// 见对应 handler 注释与 docs/specs/internal-trust-channel.md。
func (h *AgentHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	trustedInternal := agentauth.IsTrustedInternal(r.Context())
	// 归属校验：注册同时是数据面挂载入口，若不校验归属，任一持 namespace token 的实例都能把
	// 自己注册进**任意环境**的实例列表与拓扑，并自定 address / role / capacity / weight / backends，
	// 污染 /discovery 与落位建议；按 < TTL 周期重注册他人 serverId 还会让真 agent 永久注册失败。
	//
	// 为什么可以在这里校验（ADR-0094 决策 5）：共享 token 的机器注册通道是引导入口，此时可能尚无
	// 可校验身份，保持原样放行；而其余请求是经 agentTokenMiddleware 的 v2 身份分支进来的——
	// 该分支的 AuthenticateAgentV2 已要求身份 active、serverId 已绑定、boot 与 DB 一致，
	// 所以调用者必然持有完整权威身份，自报归属必须与之一致。
	if !trustedInternal {
		identity, ok := resolveAgentIdentity(r, h.reportAuth)
		if !ok {
			// 校验器缺失或身份不可解析：fail-closed，不能退回信任自报归属。
			render.WriteError(w, r, apperr.ErrUnauthorized)
			return
		}
		if req.Namespace != identity.Namespace || req.ServerID != identity.ServerID {
			// 复用 ErrIdentityBindingMismatch（409）：语义就是「身份绑定与自报 namespace / serverId 不一致」，
			// 与 v2 侧同码同文案，运维一眼能看出是部署参数填错而非权限问题。
			// 不用 404：这里比的是调用方**对自己**的声明，不涉及他人资源存在性，不构成枚举面。
			render.WriteError(w, r, apperr.ErrIdentityBindingMismatch)
			return
		}
	}
	res, err := h.svc.Register(service.RegisterParams{
		Namespace: req.Namespace, ServerID: req.ServerID, Role: req.Role, GroupHint: req.GroupHint,
		Address: req.Address, Version: req.Version, AgentVersion: req.AgentVersion,
		Capacity: req.Capacity, Weight: req.Weight, Metadata: req.Metadata,
		Backends: req.Backends, ClientIP: clientIP(r),
		// 机器注册分支依据（FR-222，spec §3.3）：调用方类型取自 agentTokenMiddleware 注入的 context 标记
		// （仅命中 X-Beacon-Token 共享 token 的请求带），**不来自请求体**，调用方无法伪造。
		TrustedInternal: trustedInternal,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	view := registerResponse{
		InstanceKey: res.InstanceKey, ResolvedGroup: res.ResolvedGroup, ResolvedZone: nilIfEmpty(res.ResolvedZone),
		HeartbeatIntervalSec: res.HeartbeatIntervalSec, TTLSec: res.TTLSec, Assigned: res.Assigned,
	}
	// 机器注册通道命中（FR-222）：回带权威绑定事实，供内部调用方核对绑定结果。
	if res.MachineRegister != nil {
		view.MachineRegistered = true
		view.IdentityID = res.MachineRegister.IdentityID
		boundAt := res.MachineRegister.BoundAt
		view.BoundAt = &boundAt
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// heartbeatRequest 是心跳请求体。
type heartbeatRequest struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
}

// Heartbeat 处理 POST /beacon/v1/agent/heartbeat。
//
// 必须绑定身份：心跳决定「谁还活着」。共享 token 下若照信请求体的 namespace/serverId，
// 任一 agent 都能替**别人**续命——把已挂掉的实例永久保活成 online（调度继续往里塞人），
// 或反手把别人的实例认成自己的。故先取 DB 权威身份，再把请求自报归属收敛到身份。
func (h *AgentHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	identity, ok := authenticateAgentReport(w, r, h.reportAuth)
	if !ok {
		return
	}
	ns, serverID, ok := bindAgentTarget(w, r, identity, req.Namespace, req.ServerID)
	if !ok {
		return
	}
	ttlSec, err := h.svc.Heartbeat(ns, serverID)
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
//
// 必须绑定身份：上报值（人数 / TPS / 内存 / CPU / bc 后端子服集合）虽只展示不决策，
// 但归错台就是错事实——任一 agent 可把别人的在线人数刷成 0、把别人的实例标成高负载，
// 运维照着错误事实排查。身份取 DB 权威绑定，不认请求体自报的 namespace/serverId。
func (h *AgentHandler) Report(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	req.applyDefaults()
	identity, ok := authenticateAgentReport(w, r, h.reportAuth)
	if !ok {
		return
	}
	ns, serverID, ok := bindAgentTarget(w, r, identity, req.Namespace, req.ServerID)
	if !ok {
		return
	}
	if err := h.svc.Report(service.ReportParams{
		Namespace: ns, ServerID: serverID, AppliedMD5: req.AppliedMD5,
		PlayerCount: req.PlayerCount, TPS: req.TPS, MemUsed: req.MemUsed, MemMax: req.MemMax,
		CPULoad: req.cpuLoadOrUnavailable(), Backends: req.Backends, Proxy: req.Proxy.toRuntime(),
	}); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// declarationRequest 是节点自声明刷新请求体（FR-243，见 ADR-0086）。
// 两个声明字段均为「缺键 = 不刷新该项」的部分刷新语义，故都用可区分「缺键」的类型承载：
//   - Capacity 用指针：缺键 nil（不刷新）与显式 0（合法容量）严格区分（同 reportRequest 的 CPULoad 惯例）；
//   - Labels 用 map：缺键 nil（不刷新）与显式 `{}`（解码为非 nil 空 map，= 清空全部标签）区分。
//
// Namespace / ServerID 只用于定位**自己**：身份判定沿用 agentTokenMiddleware 与在册判定，不信任请求体自报的归属。
type declarationRequest struct {
	Namespace string            `json:"namespace"`
	ServerID  string            `json:"serverId"`
	Capacity  *int              `json:"capacity,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// Declaration 处理 POST /beacon/v1/agent/declaration（FR-243）。
// 声明刷新成功回 200 + 生效后的 capacity / labels（自证）；未注册 / 未挂载数据面 404 NOT_REGISTERED；
// 格式非法、超界、capacity < 0、两字段全缺 400 INVALID_PARAM（失败分类见 spec §3.4）。
//
// 必须绑定身份：声明会写进调度准入读的标签真源（FR-244）。共享 token 下若认请求体自报归属，
// 任一 agent 就能替别人改写 capacity / labels，把别服伪装成高容量骗调度，或反向压低别服权重。
func (h *AgentHandler) Declaration(w http.ResponseWriter, r *http.Request) {
	var req declarationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	identity, ok := authenticateAgentReport(w, r, h.reportAuth)
	if !ok {
		return
	}
	ns, serverID, ok := bindAgentTarget(w, r, identity, req.Namespace, req.ServerID)
	if !ok {
		return
	}
	res, err := h.svc.Declare(service.DeclarationParams{
		Namespace: ns, ServerID: serverID, Capacity: req.Capacity, Labels: req.Labels,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "capacity": res.Capacity, "labels": res.Labels})
}

// Discover 处理 GET /beacon/v1/agent/discovery（仅返回可用实例：online+degraded）。
// 支持按 role/zone/group 与自定义元数据 tag 过滤；tag 以重复查询参数 tag.<key>=<value> 传入（多 tag 取交集，FR-29）。
//
// 必须绑定身份：发现结果是全集群在线清单（含他服 host/port 与自声明标签），
// 且原实现在 namespace 缺省时会把**所有环境**一起吐出来。故把作用域收敛到调用者身份所属 namespace，
// 显式跨环境请求直接拒（404 不泄露存在性）。
func (h *AgentHandler) Discover(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	identity, ok := authenticateAgentReport(w, r, h.reportAuth)
	if !ok {
		return
	}
	ns, ok := bindAgentNamespace(w, r, identity, q.Get("namespace"))
	if !ok {
		return
	}
	insts := h.svc.Discover(runtime.Filter{
		Namespace: ns, Group: q.Get("group"), Zone: q.Get("zone"), Role: q.Get("role"),
		Tags: parseTagParams(q),
	})
	// 健康原因渲染上下文（FR-81）：复用 settings 读当前阈值（与健康扫描同源），now 取 UTC 墙钟。
	hc := newHealthRenderCtx(time.Now().UTC(), h.settings)
	render.WriteJSON(w, http.StatusOK, map[string]any{"instances": toInstanceViews(insts, h.svc.DefaultEntrySet(ns), h.svc.LobbyClusterMemberSet(ns), hc)})
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
//
// 必须绑定身份：本端点会回**配置正文**。共享 token 下若认 query 自报的 namespace/serverId，
// 任一 agent 换个参数就能读走别服的有效配置（含敏感项的实际生效值）。故先取权威身份，
// 再把 query 归属收敛到身份；不一致按 404 拒，不暴露目标是否存在。
func (h *AgentHandler) Effective(w http.ResponseWriter, r *http.Request) {
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
	agentMD5 := q.Get("md5")
	groupHint, err := h.svc.RequireRegistered(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err) // 未注册 → 404 NOT_REGISTERED
		return
	}
	timeout := resolveHoldTimeout(h.settings, q.Get("timeoutMs"))
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
