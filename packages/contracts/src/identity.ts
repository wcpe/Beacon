// Agent 身份域响应契约（/admin/v2/agent-identities*）。
// 契约真源：docs/specs/v2-agent-identity.md §5.2；状态机 §4.3。

import type { ConflictPeer, RezonePrefill, ServerKind } from './cluster'
import type { Paged } from './common'

/** 身份列表项（GET /admin/v2/agent-identities 的 items 元素） */
export interface AgentIdentityItem {
  identityId: string
  namespaceId: number
  serverId: string
  kind: ServerKind
  status: string
  bootId: string | null
  lastAddr: string | null
  agentVersion: string | null
  pendingExpiresAt: string | null
  boundAt: string | null
  statusChangedAt: string
  conflictReason: string | null
}

/** 身份详情（额外携带冲突双方与换区预填目标） */
export interface AgentIdentityDetail extends AgentIdentityItem {
  conflictPeers: ConflictPeer[] | null
  rezonePrefill: RezonePrefill | null
}

export type AgentIdentityListResponse = Paged<AgentIdentityItem>
