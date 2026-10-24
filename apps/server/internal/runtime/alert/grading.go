package alert

import "github.com/wcpe/Beacon/apps/server/internal/model"

// 角色常量（FR-231）：proxy（kind=proxy）/ lobby（lobby_cluster_id != NULL，与 ADR-0075 一致）/
// shared_pool（shared_pool_id != NULL，FR-281）/ backend（其余）。
// 不设 entry 角色（ADR-0083：入口服 = 大厅成员，is_entry 本期不做）。
//
// shared_pool 与 lobby 的告警级别**相同**（都是「玩家链路的关键节点」，offline/lost → critical），
// 但角色分开是为了让告警文案、筛选与运维动作能落到正确的池上——把共享游戏池的故障
// 标成「大厅故障」会把运维引向错误的排查方向。
const (
	RoleProxy      = "proxy"
	RoleLobby      = "lobby"
	RoleSharedPool = "shared_pool"
	RoleBackend    = "backend"
)

// GradeAlert 是告警分级判定矩阵（FR-231 §3，纯函数、无副作用、确定性）：
//
//	健康级别    proxy / lobby    backend
//	offline     critical         warning
//	lost        critical         warning
//	degraded    warning          info
//
// NULL 语义：healthLevel 未知 → info（安全兜底）；role 未知 / 空 → 按 backend 规则（验收：无角色信息时安全降级）；
// override 非空（人工覆盖）→ 以人工值为准，矩阵不参与。
func GradeAlert(healthLevel, role string, override *string) string {
	if override != nil && *override != "" {
		return *override
	}
	if role != RoleProxy && role != RoleLobby && role != RoleSharedPool {
		role = RoleBackend
	}
	switch healthLevel {
	case "offline", "lost":
		if role == RoleBackend {
			return model.AlertLevelWarning
		}
		return model.AlertLevelCritical
	case "degraded":
		if role == RoleBackend {
			return model.AlertLevelInfo
		}
		return model.AlertLevelWarning
	default:
		return model.AlertLevelInfo
	}
}

// RoleOf 把控制面权威事实解析为分级角色（FR-231 §3）：kind=proxy → proxy；
// lobby_cluster_id != NULL（非零）→ lobby；shared_pool_id != NULL（非零）→ shared_pool；其余 → backend。
//
// 保留三参重载的向后兼容：既有调用方只关心「是不是关键节点」，两者分级相同，
// 故不带 sharedPoolID 时退化为 lobby 语义（不会把关键节点误降级成 backend）。
func RoleOf(kind string, lobbyClusterID uint) string {
	return RoleOfPool(kind, lobbyClusterID, 0)
}

// RoleOfPool 是带共享游戏池的角色解析（FR-281）。两个池归属由归属四选一保证不会同时非零。
func RoleOfPool(kind string, lobbyClusterID, sharedPoolID uint) string {
	if kind == model.ServerKindProxy {
		return RoleProxy
	}
	if lobbyClusterID != 0 {
		return RoleLobby
	}
	if sharedPoolID != 0 {
		return RoleSharedPool
	}
	return RoleBackend
}
