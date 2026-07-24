// 区服权威域响应契约（/admin/v2/zone-tree、servers、server-assignments、server-rezones 等）。
// 契约真源：docs/specs/v2-zone-authority.md §5；分配约束 §4.3、换区工单 §4.7。

import type { Paged } from './common'

/** 实例类型：代理（proxy）或后端子服（backend） */
export type ServerKind = 'proxy' | 'backend'

/** Agent 身份状态机取值（v2-agent-identity.md §4.3） */
export type IdentityStatus =
  | 'pending'
  | 'active'
  | 'rejected'
  | 'expired'
  | 'disabled'
  | 'conflict'
  | 'unbound'

/** namespace 信任能力域 */
export type TrustCapability = 'schedule' | 'message' | 'agent_ops'

/** conflict 状态时的冲突双方明细（身份详情端点返回） */
export interface ConflictPeer {
  bootId: string
  lastAddr: string
  lastSeenAt: string
}

/** 换区工单重确认时的预填目标（待确认列表「换区中」提示） */
export interface RezonePrefill {
  targetKind: 'zone' | 'bc_cluster'
  targetId: number
  targetName: string
}

/** zone-tree 小区节点 */
export interface ZoneTreeZone {
  id: number
  name: string
  description: string
  serverCount: number
  defaultEntryCount: number
}

/** zone-tree 大区节点 */
export interface ZoneTreeRegion {
  id: number
  name: string
  description: string
  zones: ZoneTreeZone[]
}

/** zone-tree BC 集群节点 */
export interface ZoneTreeCluster {
  id: number
  name: string
  description: string
  proxyCount: number
  regions: ZoneTreeRegion[]
}

/** GET /admin/v2/zone-tree 响应：结构树 + 未分配计数 */
export interface ZoneTreeResponse {
  namespaceId: number
  clusters: ZoneTreeCluster[]
  unassignedCount: number
}

/** server 列表项（含归属名称、默认入口、在线摘要） */
export interface ServerItem {
  id: number
  namespaceId: number
  serverId: string
  kind: ServerKind
  bcClusterId: number | null
  bcClusterName: string | null
  zoneId: number | null
  zoneName: string | null
  regionName: string | null
  pendingZoneId: number | null
  pendingZoneName: string | null
  isDefaultEntry: boolean
  draining: boolean
  online: boolean
  assigned: boolean
  createdAt: string
}

export type ServerListResponse = Paged<ServerItem>

/** 批量分配 / 换区的逐台结果（207 风格） */
export interface AssignmentResult {
  id: number
  serverId: string
  ok: boolean
  code?: string
}

export interface AssignmentResponse {
  results: AssignmentResult[]
}
