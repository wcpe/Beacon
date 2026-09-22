package alert

import "github.com/wcpe/Beacon/apps/server/internal/model"

// 角色常量（FR-231）：proxy（kind=proxy）/ lobby（lobby_cluster_id != NULL，与 ADR-0075 一致）/ backend（其余）。
// 不设 entry 角色（ADR-0083：入口服 = 大厅成员，is_entry 本期不做）。
const (
	RoleProxy   = "proxy"
	RoleLobby   = "lobby"
	RoleBackend = "backend"
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
	if role != RoleProxy && role != RoleLobby {
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

// RoleOf 把控制面权威事实解析为分级角色（FR-231 §3）：kind=proxy → proxy；lobby_cluster_id != NULL（非零）→ lobby；其余 → backend。
func RoleOf(kind string, lobbyClusterID uint) string {
	if kind == model.ServerKindProxy {
		return RoleProxy
	}
	if lobbyClusterID != 0 {
		return RoleLobby
	}
	return RoleBackend
}
