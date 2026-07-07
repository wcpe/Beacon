// 连接消息域数据获取（/dashboard 玩家流 / 连接流趋势）：走 mock /admin/v2/connections/stats。
// 只取聚合时间桶（不涉及玩家名单 / payload）。

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
