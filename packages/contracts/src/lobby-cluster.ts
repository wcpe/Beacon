// 大厅集群独立契约：大厅不属于大区/小区结构树，只承接 BC 的首次代理连接。
import type { Paged } from './common'
import type { HealthLevel } from './metrics-health'

/** 命名空间内唯一大厅集群的摘要。 */
export interface LobbyClusterSummary {
  id: number
  namespaceId: number
  namespaceName: string
  memberCount: number
  schedulableCount: number
  ready: boolean
}

export type LobbyClusterListResponse = Paged<LobbyClusterSummary>

/** 大厅成员运行态；成员只能是 Bukkit 后端服。 */
export interface LobbyMember {
  serverId: string
  online: boolean
  playerCount: number
  score: number
  level: HealthLevel
  schedulable: boolean
  reasons: string[]
  onlineCount: number
  maxOnline: number
  draining: boolean
}

/** GET /admin/v2/lobby-clusters/{id} 响应。 */
export interface LobbyClusterDetail extends LobbyClusterSummary {
  members: LobbyMember[]
  memberTotal: number
}

/** 大厅、业务小区与未分配间的单服原子迁移目标。null 表示迁出至未分配。 */
export type ServerPlacementTarget =
  | { kind: 'lobby_cluster'; id: number }
  | { kind: 'zone'; id: number }
  | null

export interface ServerPlacementTransferBody {
  serverId: string
  target: ServerPlacementTarget
  reason: string
}

export interface ServerPlacementTransferResponse {
  serverId: string
  namespaceId: number
  placementKind: '' | 'lobby_cluster' | 'zone'
  lobbyClusterId: number | null
  zoneId: number | null
  isDefaultEntry: boolean
  draining: boolean
}
