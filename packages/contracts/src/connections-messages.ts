// 连接明细与跨服消息域响应契约（/admin/v2/connections*、messages*）。
// 契约真源：docs/specs/v2-connection-message-storage.md §5.2。

export type ConnStatus = 'open' | 'closed'
export type CloseKind = 'quit' | 'kick' | 'timeout' | 'proxy_shutdown' | 'error'
export type MsgStatus = 'accepted' | 'dispatched' | 'delivered' | 'failed' | 'expired'

/** 连接明细行（§3.2 字段全集，camelCase） */
export interface ConnectionItem {
  connId: string
  namespaceId: number
  proxyServerId: string
  playerUuid: string
  playerName: string
  clientIp: string
  protocolVersion: number
  openedAt: string
  closedAt: string | null
  durationMs: number | null
  status: ConnStatus
  closeKind: CloseKind | null
  closeReason: string | null
  firstBackendServerId: string | null
  lastBackendServerId: string | null
  backendSwitchCount: number
}

/** 游标分页响应（连接 / 消息明细例外于 page/pageSize 约定） */
export interface CursorPage<T> {
  items: T[]
  nextCursor: string | null
}

/** 消息链路 hop 事件 */
export interface MsgHop {
  seq: number
  node: string
  event: 'sent' | 'received' | 'resolved' | 'dispatched' | 'delivered' | 'failed'
  at: string
  costMs?: number
}

/** 消息元数据（永不含 payload） */
export interface MessageItem {
  messageId: string
  namespaceId: number
  sourceServerId: string
  msgType: string
  targetKind: 'server' | 'player'
  targetServerId: string | null
  targetPlayer: string | null
  resolvedServerId: string | null
  targetNamespaceId: number | null
  crossNamespace: boolean
  correlationId: string | null
  status: MsgStatus
  failReason: string | null
  createdAt: string
  dispatchedAt: string | null
  deliveredAt: string | null
  durationMs: number | null
  hopCount: number
  payloadSize: number
  payloadStored: boolean
}

/** 消息详情：元数据 + hops 链路 + 关联消息摘要 */
export interface MessageDetail extends MessageItem {
  hops: MsgHop[]
  correlated: { messageId: string; msgType: string; status: MsgStatus } | null
}

/** payload 查看响应（权限 + 原因 + 先审计） */
export interface MessagePayloadResponse {
  payload: string
  sha256: string
  size: number
}

/** 连接 / 玩家流时间桶聚合 */
export interface ConnStatsBucket {
  startAt: string
  opens: number
  closes: number
  abnormalCloses: number
  estimatedOpen: number
}

/** 消息异常链路聚合（/topology 数据源） */
export interface MessageEdgeStat {
  sourceServerId: string
  resolvedServerId: string
  total: number
  failed: number
  expired: number
  failRatePercent: number
  p95DurationMs: number
  topFailReasons: { reason: string; count: number }[]
  sampleMessageIds: string[]
}
