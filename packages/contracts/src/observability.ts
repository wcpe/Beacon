// 可观测域响应契约：审计（/audits）、命令观测（/commands）、告警事件（/alert-events）。
// 沿用 docs/API.md Legacy /admin/v1 契约形状（分页参数 page/size、响应 total+items）。

/** 审计行（Legacy /admin/v1/audits items 元素） */
export interface AuditItem {
  id: number
  namespace: string
  operator: string
  action: string
  targetType: string
  targetRef: string
  detail: string
  result: 'ok' | 'fail'
  clientIp: string
  createdAt: string
}

/** 审计聚合（Legacy /admin/v1/audits/analytics） */
export interface AuditAnalytics {
  from: string
  to: string
  total: number
  okCount: number
  failCount: number
  byAction: { action: string; count: number }[]
  byDay: { date: string; count: number }[]
}

/** 命令行（Legacy /admin/v1/commands items 元素） */
export interface CommandItem {
  commandId: number
  namespace: string
  serverId: string
  type: string
  status: 'pending' | 'fetched' | 'ready' | 'done' | 'failed' | 'expired'
  resultDetail: string
  operator: string
  createdAt: string
  updatedAt: string
  ageSeconds: number
}

/** 命令聚合（Legacy /admin/v1/commands/analytics） */
export interface CommandAnalytics {
  from: string
  to: string
  total: number
  byStatus: { status: string; count: number }[]
  byType: { type: string; count: number }[]
  byServer: { serverId: string; count: number }[]
  byDay: { date: string; issued: number; done: number; failed: number }[]
}

/** 告警事件处理状态（open 待处理 / acknowledged 已确认 / resolved 已处理） */
export type AlertEventStatus = 'open' | 'acknowledged' | 'resolved'

/** 告警事件行（Legacy /admin/v1/alert-events items 元素） */
export interface AlertEventItem {
  id: number
  type: 'health-transition' | 'publish-fail' | 'backend-unreachable'
  level: 'info' | 'warning' | 'critical'
  serverId: string
  namespace: string
  message: string
  detail: string
  createdAt: string
  status: AlertEventStatus
  handledBy: string | null
  handledAt: string | null
  handleNote: string | null
}
