// 可观测域数据获取（/commands /audits /alert-events）：真后端 /admin/v1 端点（page/size 分页，响应 total+items）。
// 查询参数与响应形状以 apps/server/internal/handler/{command_observe,audit,alert_event}_handler.go 为权威。
// 读端点用于 useQuery，写端点（告警处理）用于 useMutation；错误按脱敏 message 抛出（ADR-0057）。

import type {
  AlertEventItem,
  AlertEventStatus,
  AuditAnalytics,
  AuditItem,
  CommandAnalytics,
  CommandItem,
  Paged,
} from '@beacon/contracts'

import { buildQuery, request } from './http'

// ---- 命令观测（/commands）----

export interface CommandQuery {
  namespace?: string
  serverId?: string
  type?: string
  status?: string
  page?: number
  size?: number
}

export function fetchCommands(query: CommandQuery): Promise<Paged<CommandItem>> {
  return request('GET', `/admin/v1/commands${buildQuery({ ...query })}`)
}

export function fetchCommandAnalytics(): Promise<CommandAnalytics> {
  return request('GET', '/admin/v1/commands/analytics')
}

// ---- 审计（/audits）----

export interface AuditQuery {
  namespace?: string
  operator?: string
  action?: string
  targetType?: string
  targetRef?: string
  detailKeyword?: string
  from?: string
  to?: string
  page?: number
  size?: number
}

export function fetchAudits(query: AuditQuery): Promise<Paged<AuditItem>> {
  return request('GET', `/admin/v1/audits${buildQuery({ ...query })}`)
}

export function fetchAuditAnalytics(): Promise<AuditAnalytics> {
  return request('GET', '/admin/v1/audits/analytics')
}

/** 审计导出 URL（CSV / JSON），mock 下由浏览器直接下载 */
export function auditExportUrl(format: 'csv' | 'json'): string {
  return `/admin/v1/audits/export${buildQuery({ format })}`
}

// ---- 告警事件（/alert-events）----

export interface AlertEventQuery {
  type?: string
  level?: string
  namespace?: string
  page?: number
  size?: number
}

export function fetchAlertEvents(query: AlertEventQuery): Promise<Paged<AlertEventItem>> {
  return request('GET', `/admin/v1/alert-events${buildQuery({ ...query })}`)
}

export interface HandleAlertBody {
  status: Extract<AlertEventStatus, 'acknowledged' | 'resolved'>
  note?: string
}

/** 处理告警事件（确认 / 标记已处理）：写闭环，返回更新后的行 */
export function handleAlertEvent(id: number, body: HandleAlertBody): Promise<AlertEventItem> {
  return request('POST', `/admin/v1/alert-events/${String(id)}/handle`, body)
}
