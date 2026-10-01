package handler

import (
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
)

// V2RosterHandler 处理 v2 agent 玩家名册读端点（GET /beacon/v2/agent/player-roster）。
//
// 供 agent 侧 roster() 门面取数（ADR-0063 决策 4：名册权威已迁至控制面，子服 agent 物理上看不到异服玩家）：
// 返回**调用方所属 namespace** 的在线名册（玩家名 → 所在 serverId），zone / 服的细分过滤由 agent 侧在本地做。
// handler 只做「取已鉴权身份 + 读内存名册 + 序列化」：归属 namespace 取自中间件注入的权威身份，
// **绝不读任何请求参数**（namespace 是强隔离边界）；名册读取全在内存 Store 内完成，请求 goroutine 不碰 DB。
type V2RosterHandler struct {
	store *roster.Store
}

// NewV2RosterHandler 构造处理器；store 为控制面进程内名册（由连接明细 open/close 驱动、进程重启后重建）。
func NewV2RosterHandler(store *roster.Store) *V2RosterHandler {
	return &V2RosterHandler{store: store}
}

// playerRosterResponse 是 GET /beacon/v2/agent/player-roster 的响应体（键逐字对齐契约）。
type playerRosterResponse struct {
	Namespace string            `json:"namespace"`
	Count     int               `json:"count"`
	Players   map[string]string `json:"players"`
}

// PlayerRoster 处理 GET /beacon/v2/agent/player-roster：200 {namespace, count, players}。
//
// players 为该 namespace 的「玩家名 → serverId」全量快照；本域无人在线时返回空对象 + count=0
// （不是 404——agent 侧据此把本地名册清空，与「暂时无人」而非「端点不可用」区分开）。
func (h *V2RosterHandler) PlayerRoster(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	players := h.store.SnapshotByNamespace(identity.NamespaceID)
	render.WriteJSON(w, http.StatusOK, playerRosterResponse{
		Namespace: identity.Namespace,
		Count:     len(players),
		Players:   players,
	})
}
