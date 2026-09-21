package handler

import (
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/render"
)

// MCPConfigHandler 暴露 MCP 入口部署配置的只读视图。
//
// 这些配置项全部是启动项（改后须重启控制面），后端不提供写入端点；
// 该端点只为管理台提供"当前 MCP 是否启用、入口地址、信任边界"的可观测面，
// 便于运维在不登录服务器读配置文件的情况下判断外部 Agent 为何连不上。
type MCPConfigHandler struct{ cfg config.MCPConfig }

// NewMCPConfigHandler 构造 MCP 配置只读处理器。
func NewMCPConfigHandler(cfg config.MCPConfig) *MCPConfigHandler {
	return &MCPConfigHandler{cfg: cfg}
}

// mcpConfigView 是配置的脱敏视图。
//
// 安全约束：绝不回显 agent-token（不属本结构，也无从取值）；publicBaseURL 本身即公网
// 地址、CIDR 与 Host 白名单亦为部署拓扑信息，均可安全展示。
type mcpConfigView struct {
	Enabled               bool     `json:"enabled"`
	PublicBaseURL         string   `json:"publicBaseUrl"`
	TrustedProxyCIDRs     []string `json:"trustedProxyCidrs"`
	AllowInsecureInternal bool     `json:"allowInsecureInternal"`
	AllowedHosts          []string `json:"allowedHosts"`
	AllowApprovalDecide   bool     `json:"allowApprovalDecide"`
	AllowMachineRegister  bool     `json:"allowMachineRegister"`
	// DirectMode 为 true 表示内网明文直连（无 TLS 反代），与 NewMCPProxyPolicy 的判定口径一致。
	DirectMode bool `json:"directMode"`
}

// Get 处理 GET /admin/v2/mcp/config。
//
// 无论 MCP 是否启用都返回 200：enabled=false 时前端据此展示"入口未启用"的配置指引，
// 而不是把未启用当成错误。
func (h *MCPConfigHandler) Get(w http.ResponseWriter, _ *http.Request) {
	render.WriteJSON(w, http.StatusOK, mcpConfigView{
		Enabled:               h.cfg.Enabled,
		PublicBaseURL:         h.cfg.PublicBaseURL,
		TrustedProxyCIDRs:     nonNilStrings(h.cfg.TrustedProxyCIDRs),
		AllowInsecureInternal: h.cfg.AllowInsecureInternal,
		AllowedHosts:          nonNilStrings(h.cfg.AllowedHosts),
		AllowApprovalDecide:   h.cfg.AllowApprovalDecide,
		AllowMachineRegister:  h.cfg.AllowMachineRegister,
		DirectMode:            directMode(h.cfg),
	})
}

// directMode 复刻 server.NewMCPProxyPolicy 的直连判定：显式允许内网明文且未配置反代网段。
// 必须同时判 Enabled——未启用时策略对象提前返回空 policy、Direct() 为 false，
// 且"未挂载的入口"没有模式可言；漏判会让前端展示与实际行为不符的模式预览。
// 改动此处必须同步 server/mcp_proxy_policy.go 的同名判定。
func directMode(cfg config.MCPConfig) bool {
	return cfg.Enabled && cfg.AllowInsecureInternal && len(cfg.TrustedProxyCIDRs) == 0
}

// nonNilStrings 把 nil 切片规范化为空切片，保证 JSON 序列化为 [] 而非 null，
// 让前端无需为 null 单独分支。
func nonNilStrings(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}
