// 连接消息域数据获取：/admin/v2/connections*（明细列表 / 详情 / 玩家流聚合）与
// /admin/v2/messages*（明细列表 / 详情 / payload 受控查看），真后端均已交付。
// 聚合 / 列表永不含 payload；payload 仅经受控查看端点（原因必填 + 先审计后返回）按需获取。
// 列表查询防护（spec §4.3）：精确 ID 直查免时间范围；否则必须 serverId 或 playerUuid + 时间范围 ≤168h；
// includeArchived 冷查询强制时间范围 ≤ 冷查询上限（FR-152）。

import type {
  ConnectionItem,
  CursorPage,
  MessageDetail,
  MessageItem,
  MessagePayloadResponse,
} from '@beacon/contracts'

import { buildQuery, request } from './http'

/** 连接 / 玩家流时间桶（connections/stats 响应） */
export interface ConnStatsBucket {
  startAt: string
  opens: number
  closes: number
  abnormalCloses: number
  estimatedOpen: number
}

export interface ConnStatsResponse {
  buckets: ConnStatsBucket[]
}

export interface ConnStatsQuery {
  // 时间窗（RFC3339），缺省服务端按最近 1h
  from?: string
  to?: string
  // 时间桶粒度：1m / 5m
  bucket?: '1m' | '5m'
  // 限定代理服务器
  serverId?: string
}

/** 连接 / 玩家流时间桶聚合（dashboard 玩家流卡片） */
export function fetchConnStats(query: ConnStatsQuery): Promise<ConnStatsResponse> {
  return request('GET', `/admin/v2/connections/stats${buildQuery({ ...query })}`)
}

/** 受控查看消息 payload：原因必填（≤255 字），后端先写审计再返回内容（spec §4.4） */
export function viewMessagePayload(messageId: string, reason: string): Promise<MessagePayloadResponse> {
  return request('POST', `/admin/v2/messages/${encodeURIComponent(messageId)}/payload`, { reason })
}

// ---- 连接明细列表 / 详情（FR-181）----

export interface ConnectionsQuery {
  // 精确 connId 直查（免时间范围与选择性过滤）
  connId?: string
  // 无精确 ID 时必须至少一个选择性过滤（serverId 匹配代理 / 首后端 / 末后端；playerUuid 精确）
  serverId?: string
  playerUuid?: string
  // 状态 / 断开类别过滤
  status?: string
  closeKind?: string
  namespaceId?: number
  // 时间范围（RFC3339）：条件查询必填且跨度 ≤168h；冷查询 ≤ 冷查询上限
  from?: string
  to?: string
  // 游标（热 = 偏移量令牌；冷 = keyset 令牌。前端只透传不解析）
  cursor?: string
  limit?: number
  // 冷查询（FR-152）：为 true 时跨热 / 冷并表
  includeArchived?: boolean
}

/** 连接明细游标分页查询（/connections 页） */
export function fetchConnections(query: ConnectionsQuery): Promise<CursorPage<ConnectionItem>> {
  return request('GET', `/admin/v2/connections${buildQuery({ ...query })}`)
}

/** 单连接详情 */
export function fetchConnectionDetail(connId: string): Promise<ConnectionItem> {
  return request('GET', `/admin/v2/connections/${encodeURIComponent(connId)}`)
}

// ---- 消息链路列表 / 详情（FR-181；元数据永不含 payload）----

export interface MessagesQuery {
  // 精确 messageId / correlationId 直查（免时间范围与选择性过滤）
  messageId?: string
  correlationId?: string
  // 无精确 ID 时必须至少一个选择性过滤（serverId 匹配来源 / 解析目标 / 定向目标）
  serverId?: string
  playerUuid?: string
  // 状态 / 类型 / 寻址 / 跨域过滤
  status?: string
  msgType?: string
  targetKind?: string
  crossNamespace?: boolean
  namespaceId?: number
  // 时间范围（RFC3339）：条件查询必填且跨度 ≤168h；冷查询 ≤ 冷查询上限
  from?: string
  to?: string
  cursor?: string
  limit?: number
  includeArchived?: boolean
}

/** 消息链路游标分页查询（/messages 页；元数据不含 payload） */
export function fetchMessages(query: MessagesQuery): Promise<CursorPage<MessageItem>> {
  return request('GET', `/admin/v2/messages${buildQuery({ ...query })}`)
}

/** 单消息详情（hops 链路 + 关联消息摘要） */
export function fetchMessageDetail(messageId: string): Promise<MessageDetail> {
  return request('GET', `/admin/v2/messages/${encodeURIComponent(messageId)}`)
}
