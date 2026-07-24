// namespace 隔离域响应契约（/admin/v2/namespaces、namespace-trusts*）。
// 契约真源：docs/specs/v2-namespace-isolation.md §5。

import type { TrustCapability } from './cluster'
import type { Paged } from './common'

/** namespace 列表项：附 server 数、bc_cluster 数与双向生效信任数 */
export interface NamespaceItem {
  id: number
  name: string
  description: string
  serverCount: number
  bcClusterCount: number
  activeTrustCount: number
  createdAt: string
}

export type NamespaceListResponse = Paged<NamespaceItem>

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
