// namespace 隔离域响应契约（/admin/v2/namespaces、namespace-trusts*）。
// 契约真源：docs/specs/v2-namespace-isolation.md §5。

import type { TrustCapability } from './cluster'
import type { Paged } from './common'
import type { LifecycleAction, LifecycleStatus, TombstoneSummary } from './lifecycle'

/** namespace 列表项：附 server 数、bc_cluster 数与双向生效信任数 */
export interface NamespaceItem {
  id: number
  /** 兼容窗内旧 name 仍等于稳定 code。 */
  name: string
  code?: string
  displayName?: string
  description: string
  serverCount: number
  bcClusterCount: number
  activeTrustCount: number
  lifecycle?: LifecycleStatus
  effectiveActive?: boolean
  tombstone?: TombstoneSummary | null
  createdAt: string
}

export type NamespaceListResponse = Paged<NamespaceItem>

/** namespace 生命周期影响预览；分类计数来自服务端有界聚合。 */
export interface NamespaceLifecycleImpact {
  namespaceId: number
  code: string
  action: LifecycleAction
  currentLifecycle: LifecycleStatus
  targetLifecycle: LifecycleStatus
  serverCount: number
  identityCount: number
  activeServerCount: number
  impactHash: string
  summary: string[]
}

/** 创建 namespace 的响应：一次性明文接入 token 只在此返回 */
export interface NamespaceCreated extends NamespaceItem {
  accessToken: string
}

/** 信任行视图（含授予 / 收回人、时间、原因） */
export interface NamespaceTrustItem {
  id: number
  fromNamespaceId: number
  toNamespaceId: number
  fromNamespaceName: string
  toNamespaceName: string
  capability: TrustCapability
  status: 'active' | 'revoked'
  note: string
  grantedBy: string
  grantedAt: string
  revokedBy: string | null
  revokedAt: string | null
  revokeReason: string | null
}

export type NamespaceTrustListResponse = Paged<NamespaceTrustItem>
