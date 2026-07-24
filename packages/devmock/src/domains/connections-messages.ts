// 连接明细与跨服消息域 mock（/admin/v2/connections*、/admin/v2/messages*）。
// 契约真源：docs/specs/v2-connection-message-storage.md §5.2；
// 查询防护：精确 ID 直查豁免；否则须显式时间范围 ≤168h（selector 可选，对齐后端全局近期）；
// payload 查看 §4.4（原因必填 + 先审计后返回）。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  CloseKind,
  ConnStatsBucket,
  ConnectionItem,
  CursorPage,
  MessageDetail,
  MessageEdgeStat,
  MessageItem,
  MessagePayloadResponse,
  MsgHop,
  MsgStatus,
} from '@beacon/contracts'
import { jsonError, mockGet, mockPost, pathParam, queryInt, queryStr, queryTimeMs, readBody } from '../http'
import { getClusterState } from '../data/cluster'
import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { BASE_MS, createRng, pickOne, pseudoSha256, uuidFrom } from '../support'

interface ConnMsgState {
  connections: ConnectionItem[]
  messages: MessageDetail[]
  payloads: Map<string, string>
}

const PLAYER_NAMES = ['Steve233', 'Alex_cn', '小白狼', 'Endermite', 'Hikari', '苦力怕骑士', 'RedstonePro', '月下客'] as const
const MSG_TYPES = ['lodestone:teleport', 'party:invite', 'chat:cross', 'economy:sync', 'guild:notify'] as const
const MSG_FAIL_REASONS = ['player_not_online', 'ack_timeout', 'queue_overflow'] as const
const MINUTE = 60_000

function buildState(scenario: MockScenario): ConnMsgState {
  if (scenario === 'empty') {
    return { connections: [], messages: [], payloads: new Map() }
  }
  const cluster = getClusterState()
  const proxies = cluster.servers.filter((s) => s.kind === 'proxy')
  const backends = cluster.servers.filter((s) => s.kind === 'backend' && s.zoneId !== null)
  if (proxies.length === 0 || backends.length === 0) {
    return { connections: [], messages: [], payloads: new Map() }
  }
  const rng = createRng(897123)
  const connCount = scenario === 'huge' ? 4000 : 36
  const msgCount = scenario === 'huge' ? 4000 : 48

  const connections: ConnectionItem[] = []
  for (let i = 0; i < connCount; i++) {
    const proxy = pickOne(rng, proxies)
    const first = pickOne(rng, backends)
    const last = rng() < 0.6 ? first : pickOne(rng, backends)
    const openedMs = BASE_MS - Math.floor(rng() * 45 * MINUTE)
    const open = rng() < 0.35
    const durationMs = open ? null : Math.floor(rng() * 30 * MINUTE)
    const closeKind: CloseKind | null = open ? null : rng() < 0.82 ? 'quit' : pickOne(rng, ['kick', 'timeout', 'error'] as const)
    connections.push({
      connId: uuidFrom(`conn:${String(i)}`),
      namespaceId: proxy.namespaceId,
      proxyServerId: proxy.serverId,
      playerUuid: uuidFrom(`player:${String(i % 24)}`),
      playerName: PLAYER_NAMES[i % PLAYER_NAMES.length],
      clientIp: `1.86.${String(Math.floor(rng() * 200))}.${String(Math.floor(rng() * 250))}`,
      protocolVersion: 767,
      openedAt: new Date(openedMs).toISOString(),
      closedAt: open || durationMs === null ? null : new Date(openedMs + durationMs).toISOString(),
      durationMs,
      status: open ? 'open' : 'closed',
      closeKind,
      closeReason: closeKind === 'timeout' ? '读取超时：客户端 30 秒无响应' : closeKind === 'error' ? '连接异常中断' : closeKind === 'kick' ? '被管理员踢出' : null,
      firstBackendServerId: first.serverId,
      lastBackendServerId: open ? first.serverId : last.serverId,
      backendSwitchCount: first.serverId === last.serverId ? 0 : 1 + Math.floor(rng() * 3),
    })
  }
  connections.sort((a, b) => (a.openedAt < b.openedAt ? 1 : -1))

  // 超大量场景消息集中在前几台后端：4000 条摊到 1200 台会稀释到单服无法翻页，
  // 集中后任一热门 serverId（如首台后端）检索即可稳定演示游标分页。
  const msgBackends = scenario === 'huge' ? backends.slice(0, 8) : backends
  const messages: MessageDetail[] = []
  const payloads = new Map<string, string>()
  for (let i = 0; i < msgCount; i++) {
    const source = pickOne(rng, msgBackends)
    const target = pickOne(rng, msgBackends)
    const createdMs = BASE_MS - Math.floor(rng() * 30 * MINUTE)
    const messageId = uuidFrom(`msg:${String(i)}`)
    const byPlayer = rng() < 0.3
    const roll = rng()
    const status: MsgStatus = roll < 0.78 ? 'delivered' : roll < 0.86 ? 'failed' : roll < 0.92 ? 'expired' : roll < 0.97 ? 'dispatched' : 'accepted'
    const failReason = status === 'failed' ? pickOne(rng, MSG_FAIL_REASONS) : status === 'expired' ? 'ttl_exceeded' : null
    const dispatched = status === 'delivered' || status === 'dispatched' || status === 'failed'
    const delivered = status === 'delivered'
    const totalMs = 20 + Math.floor(rng() * 400)
    const payload = JSON.stringify({ type: MSG_TYPES[i % MSG_TYPES.length], seq: i, data: { world: 'world', x: 100 + i } })
    payloads.set(messageId, payload)
    const hops: MsgHop[] = [
      { seq: 0, node: source.serverId, event: 'sent', at: new Date(createdMs - 3).toISOString() },
      { seq: 1, node: 'beacon', event: 'received', at: new Date(createdMs).toISOString(), costMs: 3 },
    ]
    if (byPlayer) {
      hops.push({ seq: hops.length, node: 'beacon', event: 'resolved', at: new Date(createdMs + 1).toISOString(), costMs: 1 })
    }
    if (dispatched) {
      hops.push({ seq: hops.length, node: 'beacon', event: 'dispatched', at: new Date(createdMs + Math.floor(totalMs / 2)).toISOString(), costMs: Math.floor(totalMs / 2) })
    }
    if (delivered) {
      hops.push({ seq: hops.length, node: target.serverId, event: 'delivered', at: new Date(createdMs + totalMs).toISOString(), costMs: Math.ceil(totalMs / 2) })
    }
    if (status === 'failed') {
      hops.push({ seq: hops.length, node: target.serverId, event: 'failed', at: new Date(createdMs + totalMs).toISOString(), costMs: Math.ceil(totalMs / 2) })
    }
    messages.push({
      messageId,
      namespaceId: source.namespaceId,
      sourceServerId: source.serverId,
      msgType: MSG_TYPES[i % MSG_TYPES.length],
      targetKind: byPlayer ? 'player' : 'server',
      targetServerId: byPlayer ? null : target.serverId,
      targetPlayer: byPlayer ? uuidFrom(`player:${String(i % 24)}`) : null,
      resolvedServerId: status === 'failed' && failReason === 'player_not_online' ? null : target.serverId,
      targetNamespaceId: null,
      crossNamespace: rng() < 0.05,
      correlationId: i % 9 === 4 ? uuidFrom(`msg:${String(i - 1)}`) : null,
      status,
      failReason,
      createdAt: new Date(createdMs).toISOString(),
      dispatchedAt: dispatched ? new Date(createdMs + Math.floor(totalMs / 2)).toISOString() : null,
      deliveredAt: delivered ? new Date(createdMs + totalMs).toISOString() : null,
      durationMs: delivered ? totalMs : null,
      hopCount: 1,
      payloadSize: payload.length,
      payloadStored: true,
      hops,
      correlated: null,
    })
  }
  messages.sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1))
  return { connections, messages, payloads }
}

const getConnMsgState: () => ConnMsgState = defineScenarioStore(buildState)

const MAX_RANGE_MS = 168 * 3_600_000

/** 查询防护：精确 ID 直查豁免；否则须显式有序时间范围 ≤168h（无 selector 时允许全局近期）。 */
function guardError(url: URL): Response | null {
  const fromMs = queryTimeMs(url, 'from')
  const toMs = queryTimeMs(url, 'to')
  if (fromMs === null || toMs === null || fromMs > toMs) {
    return jsonError(400, 'query_guard_violation', '必须携带显式时间范围 from/to')
  }
  if (toMs - fromMs > MAX_RANGE_MS) {
    return jsonError(400, 'query_guard_violation', '时间范围超过 168 小时上限，请缩小范围')
  }
  return null
}

function cursorSlice<T>(rows: T[], url: URL): CursorPage<T> {
  const cursor = queryInt(url, 'cursor', 0)
  const limit = Math.min(queryInt(url, 'limit', 50), 200)
  const start = cursor
  const items = rows.slice(start, start + limit)
  const next = start + limit < rows.length ? String(start + limit) : null
  return { items, nextCursor: next }
}

function toMessageItem(row: MessageDetail): MessageItem {
  const item: MessageItem & { hops?: unknown; correlated?: unknown } = { ...row }
  delete item.hops
  delete item.correlated
  return item
}

export const connectionsMessagesHandlers: HttpHandler[] = [
  // 连接 / 玩家流时间桶聚合（dashboard 玩家流卡片）
  mockGet('/admin/v2/connections/stats', ({ request }) => {
    const url = new URL(request.url)
    const toMs = queryTimeMs(url, 'to') ?? BASE_MS
    const fromMs = queryTimeMs(url, 'from') ?? toMs - 3_600_000
    const bucketMs = (queryStr(url, 'bucket') ?? '1m') === '5m' ? 5 * MINUTE : MINUTE
    const serverId = queryStr(url, 'serverId')
    const rows = getConnMsgState().connections.filter(
      (c) => serverId === null || c.proxyServerId === serverId,
    )
    const buckets: ConnStatsBucket[] = []
    const count = Math.min(Math.floor((toMs - fromMs) / bucketMs), 300)
    for (let i = 0; i < count; i++) {
      const start = fromMs + i * bucketMs
      const end = start + bucketMs
      const opens = rows.filter((c) => {
        const t = Date.parse(c.openedAt)
        return t >= start && t < end
      }).length
      const closes = rows.filter((c) => {
        if (c.closedAt === null) {
          return false
        }
        const t = Date.parse(c.closedAt)
        return t >= start && t < end
      }).length
      const abnormal = rows.filter((c) => {
        if (c.closedAt === null || (c.closeKind !== 'timeout' && c.closeKind !== 'error')) {
          return false
        }
        const t = Date.parse(c.closedAt)
        return t >= start && t < end
      }).length
      buckets.push({
        startAt: new Date(start).toISOString(),
        opens,
        closes,
        abnormalCloses: abnormal,
        estimatedOpen: rows.filter((c) => Date.parse(c.openedAt) <= end && (c.closedAt === null || Date.parse(c.closedAt) > end)).length,
      })
    }
    return HttpResponse.json({ buckets })
  }),

  // 连接明细查询（查询防护 + 游标分页）
  mockGet('/admin/v2/connections', ({ request }) => {
    const url = new URL(request.url)
    const state = getConnMsgState()
    const connId = queryStr(url, 'connId')
    if (connId !== null) {
      const row = state.connections.find((c) => c.connId === connId)
      return HttpResponse.json({ items: row ? [row] : [], nextCursor: null } satisfies CursorPage<ConnectionItem>)
    }
    const guard = guardError(url)
    if (guard !== null) {
      return guard
    }
    const serverId = queryStr(url, 'serverId')
    const playerUuid = queryStr(url, 'playerUuid')
    const status = queryStr(url, 'status')
    const closeKind = queryStr(url, 'closeKind')
    const namespaceId = queryStr(url, 'namespaceId')
    const fromMs = queryTimeMs(url, 'from') ?? 0
    const toMs = queryTimeMs(url, 'to') ?? BASE_MS
    const rows = state.connections.filter((c) => {
      const openedMs = Date.parse(c.openedAt)
      if (openedMs < fromMs || openedMs > toMs) {
        return false
      }
      if (
        serverId !== null &&
        c.proxyServerId !== serverId &&
        c.firstBackendServerId !== serverId &&
        c.lastBackendServerId !== serverId
      ) {
        return false
      }
      if (playerUuid !== null && c.playerUuid !== playerUuid) {
        return false
      }
      if (status !== null && c.status !== status) {
        return false
      }
      if (closeKind !== null && c.closeKind !== closeKind) {
        return false
      }
      if (namespaceId !== null && String(c.namespaceId) !== namespaceId) {
        return false
      }
      return true
    })
    return HttpResponse.json(cursorSlice(rows, url))
  }),

  // 单连接详情
  mockGet('/admin/v2/connections/:connId', (info) => {
    const row = getConnMsgState().connections.find((c) => c.connId === pathParam(info, 'connId'))
    if (!row) {
      return jsonError(404, 'connection_not_found', '连接不存在')
    }
    return HttpResponse.json(row)
  }),

  // 异常链路聚合（/topology 数据源）
  mockGet('/admin/v2/messages/stats', ({ request }) => {
    const url = new URL(request.url)
    const groupBy = queryStr(url, 'groupBy') ?? 'edge'
    const messages = getConnMsgState().messages
    if (groupBy === 'type') {
      const byType = new Map<string, { total: number; failed: number }>()
      for (const m of messages) {
        const entry = byType.get(m.msgType) ?? { total: 0, failed: 0 }
        entry.total += 1
        if (m.status === 'failed' || m.status === 'expired') {
          entry.failed += 1
        }
        byType.set(m.msgType, entry)
      }
      return HttpResponse.json({
        types: [...byType.entries()].map(([msgType, v]) => ({ msgType, ...v })),
      })
    }
    const edges = new Map<string, MessageEdgeStat & { durations: number[] }>()
    for (const m of messages) {
      const target = m.resolvedServerId ?? '(未解析)'
      const key = `${m.sourceServerId}→${target}`
      let edge = edges.get(key)
      if (!edge) {
        edge = {
          sourceServerId: m.sourceServerId,
          resolvedServerId: target,
          total: 0,
          failed: 0,
          expired: 0,
          failRatePercent: 0,
          p95DurationMs: 0,
          topFailReasons: [],
          sampleMessageIds: [],
          durations: [],
        }
        edges.set(key, edge)
      }
      edge.total += 1
      if (m.status === 'failed') {
        edge.failed += 1
      }
      if (m.status === 'expired') {
        edge.expired += 1
      }
      if (m.durationMs !== null) {
        edge.durations.push(m.durationMs)
      }
      if ((m.status === 'failed' || m.status === 'expired') && edge.sampleMessageIds.length < 5) {
        edge.sampleMessageIds.push(m.messageId)
      }
      if (m.failReason !== null) {
        const existing = edge.topFailReasons.find((r) => r.reason === m.failReason)
        if (existing) {
          existing.count += 1
        } else {
          edge.topFailReasons.push({ reason: m.failReason, count: 1 })
        }
      }
    }
    const result = [...edges.values()].map((edge) => {
      const sorted = [...edge.durations].sort((a, b) => a - b)
      const p95 = sorted.length === 0 ? 0 : sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * 0.95))]
      const item: MessageEdgeStat & { durations?: number[] } = {
        ...edge,
        failRatePercent: edge.total === 0 ? 0 : Math.round(((edge.failed + edge.expired) / edge.total) * 1000) / 10,
        p95DurationMs: p95,
        topFailReasons: [...edge.topFailReasons].sort((a, b) => b.count - a.count).slice(0, 3),
      }
      delete item.durations
      return item
    })
    return HttpResponse.json({ edges: result })
  }),

  // 消息元数据检索（永不含 payload；查询防护同连接）
  mockGet('/admin/v2/messages', ({ request }) => {
    const url = new URL(request.url)
    const state = getConnMsgState()
    const messageId = queryStr(url, 'messageId')
    const correlationId = queryStr(url, 'correlationId')
    if (messageId !== null || correlationId !== null) {
      const rows = state.messages.filter(
        (m) =>
          (messageId !== null && m.messageId === messageId) ||
          (correlationId !== null && (m.correlationId === correlationId || m.messageId === correlationId)),
      )
      return HttpResponse.json({ items: rows.map(toMessageItem), nextCursor: null } satisfies CursorPage<MessageItem>)
    }
    const guard = guardError(url)
    if (guard !== null) {
      return guard
    }
    const serverId = queryStr(url, 'serverId')
    const playerUuid = queryStr(url, 'playerUuid')
    const status = queryStr(url, 'status')
    const msgType = queryStr(url, 'msgType')
    const crossNamespace = queryStr(url, 'crossNamespace')
    const namespaceId = queryStr(url, 'namespaceId')
    const fromMs = queryTimeMs(url, 'from') ?? 0
    const toMs = queryTimeMs(url, 'to') ?? BASE_MS
    const rows = state.messages
      .filter((m) => {
        const createdMs = Date.parse(m.createdAt)
        if (createdMs < fromMs || createdMs > toMs) {
          return false
        }
        if (serverId !== null && m.sourceServerId !== serverId && m.resolvedServerId !== serverId && m.targetServerId !== serverId) {
          return false
        }
        if (playerUuid !== null && m.targetPlayer !== playerUuid) {
          return false
        }
        if (status !== null && m.status !== status) {
          return false
        }
        if (msgType !== null && m.msgType !== msgType) {
          return false
        }
        if (crossNamespace !== null && String(m.crossNamespace) !== crossNamespace) {
          return false
        }
        if (namespaceId !== null && String(m.namespaceId) !== namespaceId) {
          return false
        }
        return true
      })
      .map(toMessageItem)
    return HttpResponse.json(cursorSlice(rows, url))
  }),

  // 消息详情 + hops 链路（payload 仅元信息）
  mockGet('/admin/v2/messages/:messageId', (info) => {
    const state = getConnMsgState()
    const row = state.messages.find((m) => m.messageId === pathParam(info, 'messageId'))
    if (!row) {
      return jsonError(404, 'message_not_found', '消息不存在')
    }
    const correlated =
      row.correlationId === null
        ? null
        : (state.messages.find((m) => m.messageId === row.correlationId) ?? null)
    const detail: MessageDetail = {
      ...row,
      correlated: correlated
        ? { messageId: correlated.messageId, msgType: correlated.msgType, status: correlated.status }
        : null,
    }
    return HttpResponse.json(detail)
  }),

  // 查看 payload（原因必填 + 先审计后返回；mock 校验原因即放行）
  mockPost('/admin/v2/messages/:messageId/payload', async (info) => {
    const state = getConnMsgState()
    const messageId = pathParam(info, 'messageId')
    const row = state.messages.find((m) => m.messageId === messageId)
    if (!row) {
      return jsonError(404, 'message_not_found', '消息不存在')
    }
    const body = await readBody<{ reason?: string }>(info.request)
    if (!body.reason || body.reason.length > 255) {
      return jsonError(400, 'missing_reason', '查看 payload 必须填写原因（≤255 字）')
    }
    const payload = state.payloads.get(messageId) ?? ''
    return HttpResponse.json({
      payload,
      sha256: pseudoSha256(payload),
      size: payload.length,
    } satisfies MessagePayloadResponse)
  }),
]
