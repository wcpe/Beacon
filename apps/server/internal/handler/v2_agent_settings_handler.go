package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2AgentSettingsHandler 处理 agent 运行配置的下发侧读写（FR-277，/admin/v2/agent-settings）。
//
// 只读拒写由鉴权中间件统一裁决（readonly 角色经 readonlyWriteGuard 对 PUT 一律 403），
// 本处理器不碰角色判断，与 settings / health-weights 各处理器同口径。
type V2AgentSettingsHandler struct {
	svc *service.AgentSettingsService
}

// NewV2AgentSettingsHandler 构造处理器。
func NewV2AgentSettingsHandler(svc *service.AgentSettingsService) *V2AgentSettingsHandler {
	return &V2AgentSettingsHandler{svc: svc}
}

// List 处理 GET /admin/v2/agent-settings?namespaceId=X&serverId=Y：
// 返回该作用域的生效配置（逐键带来源：server 级覆盖 / namespace 级默认 / 未设置）。
//
// serverId 省略即读 namespace 级默认——这正是「这台服没单独设过，环境默认是什么」，
// 也是页面在编辑单服配置前的基线。
func (h *V2AgentSettingsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if namespaceID == 0 {
		// namespaceId 必填：没有它无法确定作用域，默认成 0 只会读出一个空视图，掩盖调用方漏参。
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.ListEffective(namespaceID, q.Get("serverId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// v2AgentSettingsPutRequest 是批量写入请求体（operator / clientIP 由认证态与请求派生，忽略手填）。
type v2AgentSettingsPutRequest struct {
	// 环境主键（必填）
	NamespaceID uint `json:"namespaceId"`
	// 目标 serverId；省略或空串 = 写 namespace 级默认
	ServerID string `json:"serverId"`
	// 键 → 值；**显式 null 表示撤销该作用域的覆盖**（回到上一级 / 本地配置）。
	// 用 map[string]*string 承接是为了区分三种语义：缺键（不处理）/ null（撤销）/ ""（空串，
	// 会被拒——agent 侧把空白值当成「未下发」，存进去等于写了个永不生效的值）。
	Values map[string]*string `json:"values"`
	// 二次确认：必须与 GET 返回的 scopeLabel 逐字一致（`环境码` 或 `环境码/serverId`）。
	// 该字段存在的唯一目的，是让「以为在改这台服」与「实际改了整个环境」无法悄悄混过去。
	ConfirmationScope string `json:"confirmationScope"`
	// 变更原因（入审计）
	Reason string `json:"reason"`
}

// Put 处理 PUT /admin/v2/agent-settings：批量写入 / 撤销某作用域的配置项。
//
// 整批在一个事务内完成（逐键 upsert / delete + 逐键审计），任一键失败则整批不落地——
// 半套配置生效比整套不生效更难排查。
func (h *V2AgentSettingsHandler) Put(w http.ResponseWriter, r *http.Request) {
	var req v2AgentSettingsPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.UpdateBatch(service.AgentSettingsWriteParams{
		NamespaceID: req.NamespaceID, ServerID: req.ServerID, Values: req.Values,
		ConfirmationScope: req.ConfirmationScope, Reason: req.Reason,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, result)
}
