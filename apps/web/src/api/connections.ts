// 连接消息域数据获取：/admin/v2/connections/stats（/dashboard 玩家流 / 连接流趋势）与
// /admin/v2/messages/{messageId}/payload（/topology 消息 payload 受控查看），真后端均已交付。
// 聚合 / 列表永不含 payload；payload 仅经受控查看端点（原因必填 + 先审计后返回）按需获取。

import type { MessagePayloadResponse } from '@beacon/contracts'

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
