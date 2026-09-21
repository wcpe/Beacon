// Agent 身份域响应契约（/admin/v2/agent-identities*）。
// 契约真源：docs/specs/v2-agent-identity.md §5.2；状态机 §4.3。

import type { ConflictPeer, RezonePrefill, ServerKind } from './cluster'
import type { Paged } from './common'

/** 身份列表项（GET /admin/v2/agent-identities 的 items 元素） */
export interface AgentIdentityItem {
  identityId: string
  namespaceId: number
  /** 未分配 serverId 的 pending 身份必须是 null，不能以空串伪装。 */
  serverId: string | null
  kind: ServerKind
  status: string
  bootId: string | null
  lastAddr: string | null
  /** agent 上报的服务器工作目录绝对路径（FR-226）；旧 agent 未上报为空串。 */
  serverWorkDir: string | null
  agentVersion: string | null
  pendingExpiresAt: string | null
  boundAt: string | null
  /** 控制面记录的绑定来源；缺失表示旧控制面尚未提供。 */
  bindingSource: string | null
  /** 旧本地绑定迁移状态；缺失表示旧控制面尚未提供。 */
  migrationState: string | null
  /** 旧本地绑定迁移完成时间。 */
  legacyMigratedAt: string | null
  statusChangedAt: string
  conflictReason: string | null
}

/** 身份详情（额外携带冲突双方与换区预填目标） */
export interface AgentIdentityDetail extends AgentIdentityItem {
  /** 控制面签发的绑定摘要，不含 token；未签发或旧控制面响应为 null。 */
  bindingFingerprint: string | null
  conflictPeers: ConflictPeer[] | null
  rezonePrefill: RezonePrefill | null
  /** 地址列表由详情端点独占返回；列表接口保留单地址投影，避免扩大热列表负担。 */
  endpoints: AgentEndpoint[]
}

/** Agent 上报后由控制面推导的可达地址事实。 */
export interface AgentEndpoint {
  endpointKey: string
  ordinal: number
  reportedBindHost: string
  reportedPort: number
  detectedAddress: string
  overrideAddress: string | null
  effectiveAddress: string
  source: 'detected' | 'override'
  active: boolean
  lastSeenAt: string
}

export type AgentIdentityListResponse = Paged<AgentIdentityItem>
