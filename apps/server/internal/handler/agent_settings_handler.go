package handler

import (
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// AgentSettingsHandler 处理 agent 拉取自身运行配置的数据面端点（FR-277，GET /beacon/v2/agent/settings）。
//
// 身份一律取自鉴权中间件注入的权威绑定（agentauth.FromContext），**不接受**任何 query / body
// 自报 namespace 或 serverId：下发配置的读取权若可伪造，任一 agent 就能读到别台服的运行参数
// （含 override.command-whitelist 这类高危项），等于把单机配置当成集群公开信息。
type AgentSettingsHandler struct {
	svc *service.AgentSettingsService
}

// NewAgentSettingsHandler 构造处理器。
func NewAgentSettingsHandler(svc *service.AgentSettingsService) *AgentSettingsHandler {
	return &AgentSettingsHandler{svc: svc}
}

// Settings 处理 GET /beacon/v2/agent/settings：返回本机作用域的生效值 + 最后更新时间。
//
// 返回体不含任何「生效来源」信息：agent 只需要知道「Web 下发了什么」，它与本地 env /
// config.yml / 代码默认的比较在 agent 侧完成（三层优先级在那边实现，见 PushedConfigPolicy）。
// 服务端在这里多做的唯一一件事是**键级拒绝**（禁区键一律不返回并记审计）。
func (h *AgentSettingsHandler) Settings(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	view, err := h.svc.Pull(identity)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}
