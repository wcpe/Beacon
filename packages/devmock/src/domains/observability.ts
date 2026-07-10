// 可观测域 mock：审计（/audits）、命令观测（/commands）、告警事件（/alert-events）三页数据源。
// 第二版契约草案未覆盖这三页的端点，沿用 docs/API.md Legacy /admin/v1 契约形状
//（分页参数 page/size、响应 total+items，与 v2 的 page/pageSize 不同——已在汇报列明）。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  AlertEventItem,
  AuditAnalytics,
  AuditItem,
  CommandAnalytics,
  CommandItem,
} from '@beacon/contracts'
import { jsonError, mockGet, mockPost, paginate, pathParam, queryStr, queryTimeMs, readBody } from '../http'
import { getClusterState } from '../data/cluster'
import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { BASE_MS, createRng, pickOne } from '../support'

interface ObservabilityState {
  audits: AuditItem[]
  commands: CommandItem[]
  alertEvents: AlertEventItem[]
}

const AUDIT_ACTIONS: { action: string; targetType: string }[] = [
  { action: 'identity.approved', targetType: 'agent-identity' },
  { action: 'identity.rejected', targetType: 'agent-identity' },
  { action: 'zone.rezone.initiated', targetType: 'server' },
  { action: 'config.file.trash', targetType: 'config-file' },
  { action: 'delivery.order.start', targetType: 'change-order' },
  { action: 'delivery.order.batch_confirm', targetType: 'change-order' },
  { action: 'cross_namespace.schedule', targetType: 'namespace-trust' },
  { action: 'message.payload.view', targetType: 'message' },
  { action: 'asset.preview', targetType: 'file-asset' },
  { action: 'settings.update', targetType: 'setting' },
  { action: 'namespace_trust.grant', targetType: 'namespace-trust' },
]

const COMMAND_TYPES = ['asset_rescan', 'asset_read', 'ingest-plugins', 'tail-logs', 'resync-config'] as const
const COMMAND_STATUSES = ['pending', 'fetched', 'done', 'done', 'done', 'done', 'failed', 'expired'] as const
const OPERATORS = ['admin', 'ops-chen', 'ops-wang', 'system'] as const
const MINUTE = 60_000

function buildObservability(scenario: MockScenario): ObservabilityState {
  if (scenario === 'empty') {
    return { audits: [], commands: [], alertEvents: [] }
  }
  const cluster = getClusterState()
  const servers = cluster.servers
  if (servers.length === 0) {
    return { audits: [], commands: [], alertEvents: [] }
  }
  const rng = createRng(445566)
  const auditCount = scenario === 'huge' ? 5000 : 60
  const commandCount = scenario === 'huge' ? 3000 : 40
  const alertCount = scenario === 'huge' ? 1200 : 18

  const audits: AuditItem[] = []
  for (let i = 0; i < auditCount; i++) {
    const seed = AUDIT_ACTIONS[i % AUDIT_ACTIONS.length]
    const server = pickOne(rng, servers)
    const ok = rng() >= 0.06
    audits.push({
      id: auditCount - i,
      namespace: server.namespaceId === 1 ? 'prod' : 'test',
      operator: pickOne(rng, OPERATORS),
      action: seed.action,
      targetType: seed.targetType,
      targetRef: server.serverId,
      detail: ok ? `对 ${server.serverId} 执行 ${seed.action}（原因：例行运维）` : `对 ${server.serverId} 执行 ${seed.action} 失败：状态不允许`,
      result: ok ? 'ok' : 'fail',
      clientIp: `10.30.9.${String(1 + Math.floor(rng() * 200))}`,
      createdAt: new Date(BASE_MS - i * 7 * MINUTE).toISOString(),
    })
  }

  const commands: CommandItem[] = []
  for (let i = 0; i < commandCount; i++) {
    const server = pickOne(rng, servers)
    const status = COMMAND_STATUSES[i % COMMAND_STATUSES.length]
    const createdMs = BASE_MS - i * 11 * MINUTE
    commands.push({
      commandId: commandCount - i,
      namespace: server.namespaceId === 1 ? 'prod' : 'test',
      serverId: server.serverId,
      type: COMMAND_TYPES[i % COMMAND_TYPES.length],
      status,
      resultDetail: status === 'done' ? '执行成功' : status === 'failed' ? '执行失败：agent 回执超时' : status === 'expired' ? '命令过期未取走' : '',
      operator: pickOne(rng, OPERATORS),
      createdAt: new Date(createdMs).toISOString(),
      updatedAt: new Date(createdMs + 30_000).toISOString(),
      ageSeconds: Math.max(0, Math.floor((BASE_MS - createdMs) / 1000)),
    })
  }

  const alertEvents: AlertEventItem[] = []
  for (let i = 0; i < alertCount; i++) {
    const server = pickOne(rng, servers)
    const critical = rng() < 0.2
    // 前几条保持 open（待处理），便于展示未处理告警与写闭环入口
    const resolved = i >= 4 && rng() < 0.35
    alertEvents.push({
      id: alertCount - i,
      type: 'health-transition',
      level: critical ? 'critical' : 'warning',
      serverId: server.serverId,
      namespace: server.namespaceId === 1 ? 'prod' : 'test',
      message: critical ? `${server.serverId} 状态 lost → offline` : `${server.serverId} 状态 online → degraded`,
      detail: `${String(35 + Math.floor(rng() * 60))}s 未心跳 > ttl 30s`,
      createdAt: new Date(BASE_MS - i * 40 * MINUTE).toISOString(),
      status: resolved ? 'resolved' : 'open',
      handledBy: resolved ? 'ops-chen' : null,
      handledAt: resolved ? new Date(BASE_MS - i * 40 * MINUTE + 5 * MINUTE).toISOString() : null,
      handleNote: resolved ? '已重启 agent 恢复心跳' : null,
    })
  }

  return { audits, commands, alertEvents }
}

const getObservabilityState: () => ObservabilityState = defineScenarioStore(buildObservability)

function inWindow(createdAt: string, fromMs: number | null, toMs: number | null): boolean {
  const ts = Date.parse(createdAt)
  if (fromMs !== null && ts < fromMs) {
    return false
  }
  if (toMs !== null && ts > toMs) {
    return false
  }
  return true
}

function dayOf(createdAt: string): string {
  return createdAt.slice(0, 10)
}

export const observabilityHandlers: HttpHandler[] = [
  // 审计导出（CSV / JSON 全量，Legacy 形状）
  mockGet('/admin/v1/audits/export', ({ request }) => {
    const url = new URL(request.url)
    const format = queryStr(url, 'format') ?? 'csv'
    if (format !== 'csv' && format !== 'json') {
      return jsonError(400, 'INVALID_PARAM', 'format 仅支持 csv / json')
    }
    const rows = getObservabilityState().audits
    if (format === 'json') {
      return HttpResponse.json(rows)
    }
    const header = 'id,namespace,operator,action,targetType,targetRef,detail,result,clientIp,createdAt'
    const lines = rows.map((r) =>
      [r.id, r.namespace, r.operator, r.action, r.targetType, r.targetRef, JSON.stringify(r.detail), r.result, r.clientIp, r.createdAt].join(','),
    )
    return new HttpResponse([header, ...lines].join('\n'), {
      headers: {
        'Content-Type': 'text/csv',
        'Content-Disposition': 'attachment; filename="audits-mock.csv"',
      },
    })
  }),

  // 审计活动聚合
  mockGet('/admin/v1/audits/analytics', ({ request }) => {
    const url = new URL(request.url)
    const toMs = queryTimeMs(url, 'to') ?? BASE_MS
    const fromMs = queryTimeMs(url, 'from') ?? toMs - 30 * 86_400_000
    const rows = getObservabilityState().audits.filter((r) => inWindow(r.createdAt, fromMs, toMs))
    const byAction = new Map<string, number>()
    const byDay = new Map<string, number>()
    for (const row of rows) {
      byAction.set(row.action, (byAction.get(row.action) ?? 0) + 1)
      byDay.set(dayOf(row.createdAt), (byDay.get(dayOf(row.createdAt)) ?? 0) + 1)
    }
    const analytics: AuditAnalytics = {
      from: new Date(fromMs).toISOString(),
      to: new Date(toMs).toISOString(),
      total: rows.length,
      okCount: rows.filter((r) => r.result === 'ok').length,
      failCount: rows.filter((r) => r.result === 'fail').length,
      byAction: [...byAction.entries()].map(([action, count]) => ({ action, count })).sort((a, b) => b.count - a.count),
      byDay: [...byDay.entries()].map(([date, count]) => ({ date, count })).sort((a, b) => (a.date < b.date ? -1 : 1)),
    }
    return HttpResponse.json(analytics)
  }),

  // 分页审计（Legacy page/size 参数）
  mockGet('/admin/v1/audits', ({ request }) => {
    const url = new URL(request.url)
    const namespace = queryStr(url, 'namespace')
    const operator = queryStr(url, 'operator')
    const action = queryStr(url, 'action')
    const actionPrefix = queryStr(url, 'actionPrefix')
    const targetRef = queryStr(url, 'targetRef')
    const detailKeyword = queryStr(url, 'detailKeyword')
    const fromMs = queryTimeMs(url, 'from')
    const toMs = queryTimeMs(url, 'to')
    const rows = getObservabilityState().audits.filter((row) => {
      if (namespace !== null && row.namespace !== namespace) {
        return false
      }
      if (operator !== null && row.operator !== operator) {
        return false
      }
      if (action !== null && row.action !== action) {
        return false
      }
      if (actionPrefix !== null && !row.action.startsWith(actionPrefix)) {
        return false
      }
      if (targetRef !== null && row.targetRef !== targetRef) {
        return false
      }
      if (detailKeyword !== null && !row.detail.includes(detailKeyword)) {
        return false
      }
      return inWindow(row.createdAt, fromMs, toMs)
    })
    const { items, total } = paginate(rows, url, { sizeParam: 'size' })
    return HttpResponse.json({ items, total })
  }),

  // 命令活动聚合
  mockGet('/admin/v1/commands/analytics', ({ request }) => {
    const url = new URL(request.url)
    const toMs = queryTimeMs(url, 'to') ?? BASE_MS
    const fromMs = queryTimeMs(url, 'from') ?? toMs - 30 * 86_400_000
    const rows = getObservabilityState().commands.filter((r) => inWindow(r.createdAt, fromMs, toMs))
    const byStatus = new Map<string, number>()
    const byType = new Map<string, number>()
    const byServer = new Map<string, number>()
    const byDay = new Map<string, { issued: number; done: number; failed: number }>()
    for (const row of rows) {
      byStatus.set(row.status, (byStatus.get(row.status) ?? 0) + 1)
      byType.set(row.type, (byType.get(row.type) ?? 0) + 1)
      byServer.set(row.serverId, (byServer.get(row.serverId) ?? 0) + 1)
      const day = dayOf(row.createdAt)
      const entry = byDay.get(day) ?? { issued: 0, done: 0, failed: 0 }
      entry.issued += 1
      if (row.status === 'done') {
        entry.done += 1
      }
      if (row.status === 'failed' || row.status === 'expired') {
        entry.failed += 1
      }
      byDay.set(day, entry)
    }
    const analytics: CommandAnalytics = {
      from: new Date(fromMs).toISOString(),
      to: new Date(toMs).toISOString(),
      total: rows.length,
      byStatus: [...byStatus.entries()].map(([status, count]) => ({ status, count })).sort((a, b) => b.count - a.count),
      byType: [...byType.entries()].map(([type, count]) => ({ type, count })).sort((a, b) => b.count - a.count),
      byServer: [...byServer.entries()]
        .map(([serverId, count]) => ({ serverId, count }))
        .sort((a, b) => b.count - a.count)
        .slice(0, 10),
      byDay: [...byDay.entries()].map(([date, entry]) => ({ date, ...entry })).sort((a, b) => (a.date < b.date ? -1 : 1)),
    }
    return HttpResponse.json(analytics)
  }),

  // 分页命令元数据（实时队列复用：status=pending / fetched）
  mockGet('/admin/v1/commands', ({ request }) => {
    const url = new URL(request.url)
    const namespace = queryStr(url, 'namespace')
    const serverId = queryStr(url, 'serverId')
    const type = queryStr(url, 'type')
    const status = queryStr(url, 'status')
    const fromMs = queryTimeMs(url, 'from')
    const toMs = queryTimeMs(url, 'to')
    const rows = getObservabilityState().commands.filter((row) => {
      if (namespace !== null && row.namespace !== namespace) {
        return false
      }
      if (serverId !== null && row.serverId !== serverId) {
        return false
      }
      if (type !== null && row.type !== type) {
        return false
      }
      if (status !== null && row.status !== status) {
        return false
      }
      return inWindow(row.createdAt, fromMs, toMs)
    })
    const { items, total } = paginate(rows, url, { sizeParam: 'size' })
    return HttpResponse.json({ items, total })
  }),

  // 告警事件分页列表（持久化告警历史）
  mockGet('/admin/v1/alert-events', ({ request }) => {
    const url = new URL(request.url)
    const type = queryStr(url, 'type')
    const level = queryStr(url, 'level')
    const namespace = queryStr(url, 'namespace')
    const fromMs = queryTimeMs(url, 'from')
    const toMs = queryTimeMs(url, 'to')
    const rows = getObservabilityState().alertEvents.filter((row) => {
      if (type !== null && row.type !== type) {
        return false
      }
      if (level !== null && row.level !== level) {
        return false
      }
      if (namespace !== null && row.namespace !== namespace) {
        return false
      }
      return inWindow(row.createdAt, fromMs, toMs)
    })
    const { items, total } = paginate(rows, url, { sizeParam: 'size' })
    return HttpResponse.json({ items, total })
  }),

  // 处理告警事件（确认 / 处理写闭环）：更新状态 + 处理人 / 时间 / 备注，返回更新后的行
  mockPost('/admin/v1/alert-events/:id/handle', async (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const row = getObservabilityState().alertEvents.find((r) => r.id === id)
    if (!row) {
      return jsonError(404, 'NOT_FOUND', '告警事件不存在')
    }
    const body = await readBody<{ status?: string; note?: string }>(info.request)
    const status = body.status
    if (status !== 'acknowledged' && status !== 'resolved') {
      return jsonError(400, 'INVALID_PARAM', 'status 仅支持 acknowledged / resolved')
    }
    if (status === 'resolved' && (!body.note || body.note.trim() === '')) {
      return jsonError(400, 'MISSING_REASON', '标记已处理必须填写处理备注')
    }
    row.status = status
    row.handledBy = 'admin'
    row.handledAt = new Date(BASE_MS).toISOString()
    row.handleNote = body.note?.trim() ?? null
    return HttpResponse.json(row)
  }),
]
