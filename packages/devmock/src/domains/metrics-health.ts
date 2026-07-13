// 指标健康调度域 mock（/admin/v2/metrics*、/admin/v2/health*、/admin/v2/sched-decisions*、health-weights）。
// 契约真源：docs/specs/v2-metrics-health-scheduling.md §5.2；schedulable 原因码 §4.5、健康因子 §4.4。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  HealthDetail,
  HealthFactor,
  HealthItem,
  HealthLevel,
  HealthSnapshotPoint,
  HealthSnapshotsResponse,
  HealthWeightsConfig,
  HealthWeightsResponse,
  HealthWeightsRev,
  MetricsSeriesPoint,
  MetricsSeriesResponse,
  MetricsSummary,
  Paged,
  SchedDecisionDetail,
  SchedDecisionItem,
  SchedDecisionSummary,
} from '@beacon/contracts'
import {
  coldRangeError,
  coldRequested,
  cursorPaginate,
  jsonError,
  mockGet,
  mockPut,
  paginate,
  pathParam,
  queryMsFlexible,
  queryStr,
  queryInt,
  queryTimeMs,
  readBody,
} from '../http'
import { getClusterState, type ClusterState, type ServerRow } from '../data/cluster'
import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { BASE_MS, createRng, hashString, isoOffset, pickOne, uuidFrom } from '../support'

const DEFAULT_WEIGHTS: HealthWeightsConfig = {
  weights: { tps: 30, cpu: 20, capacity: 20, conn: 10, latency: 10, alert: 10 },
  normalize: {
    tpsGood: 19.5,
    tpsBad: 10,
    cpuGood: 40,
    cpuBad: 90,
    capGood: 0.6,
    capBad: 0.95,
    connSoftLimit: 2000,
    latGoodMs: 50,
    latBadMs: 500,
    alertPenalty: 25,
  },
  levels: { healthyMin: 80, degradedMin: 50 },
}

interface WeightsState {
  revs: HealthWeightsRev[]
}

const getWeightsState: () => WeightsState = defineScenarioStore((scenario: MockScenario) => {
  if (scenario === 'empty') {
    return { revs: [{ rev: 1, config: DEFAULT_WEIGHTS, operator: 'system', createdAt: isoOffset(-30 * 86_400_000) }] }
  }
  return {
    revs: [
      { rev: 1, config: DEFAULT_WEIGHTS, operator: 'system', createdAt: isoOffset(-30 * 86_400_000) },
      {
        rev: 2,
        config: {
          ...DEFAULT_WEIGHTS,
          weights: { ...DEFAULT_WEIGHTS.weights, tps: 35, cpu: 15 },
        },
        operator: 'ops-chen',
        createdAt: isoOffset(-7 * 86_400_000),
      },
    ],
  }
})

/** 由 serverId 确定性推导健康分（同一会话稳定） */
function baseScoreOf(server: ServerRow): number {
  if (!server.online) {
    return 0
  }
  const h = hashString(`score:${server.serverId}`) % 100
  if (h < 78) {
    return 82 + (h % 18) // 多数 healthy（82-99）
  }
  if (h < 93) {
    return 55 + (h % 25) // 少数 degraded
  }
  return 20 + (h % 28) // 极少数 unhealthy
}

function levelOf(score: number, config: HealthWeightsConfig): HealthLevel {
  if (score >= config.levels.healthyMin) {
    return 'healthy'
  }
  if (score >= config.levels.degradedMin) {
    return 'degraded'
  }
  return 'unhealthy'
}

/** 按 §4.5 全枚举推导 schedulable 原因码（可叠加） */
function reasonsOf(state: ClusterState, server: ServerRow, level: HealthLevel): string[] {
  const reasons: string[] = []
  if (server.kind !== 'backend') {
    reasons.push('kind_not_schedulable')
  }
  const identity = state.identities.find(
    (row) => row.namespaceId === server.namespaceId && row.serverId === server.serverId,
  )
  if (identity?.status === 'pending') {
    reasons.push('pending_confirm')
  }
  if (identity?.status === 'disabled') {
    reasons.push('disabled')
  }
  if (server.kind === 'backend' && server.zoneId === null) {
    reasons.push('unassigned')
  }
  if (server.draining) {
    reasons.push('draining')
  }
  if (!server.online) {
    reasons.push('lost')
  }
  if (level === 'unhealthy') {
    reasons.push('unhealthy')
  }
  return reasons
}

function healthItemOf(state: ClusterState, server: ServerRow): HealthItem {
  const config = currentWeights().config
  const score = baseScoreOf(server)
  const level = server.online ? levelOf(score, config) : 'unhealthy'
  const reasons = reasonsOf(state, server, level)
  return {
    serverId: server.serverId,
    namespaceId: server.namespaceId,
    kind: server.kind,
    zoneName: server.zoneId === null ? null : (state.zones.find((z) => z.id === server.zoneId)?.name ?? null),
    score,
    level,
    schedulable: reasons.length === 0,
    reasons,
    sampledAtMs: BASE_MS - 5_000,
  }
}

function currentWeights(): HealthWeightsRev {
  const revs = getWeightsState().revs
  return revs[revs.length - 1]
}

function factorsOf(server: ServerRow, score: number): HealthFactor[] {
  const rng = createRng(hashString(`factor:${server.serverId}`))
  const backend = server.kind === 'backend'
  const jitter = () => Math.round(rng() * 15)
  return [
    { factor: 'tps', raw: backend ? 19.6 - rng() * 3 : 0, normalized: Math.min(100, score + jitter()), weight: 30, applicable: backend },
    { factor: 'cpu', raw: 30 + rng() * 40, normalized: Math.min(100, score + jitter()), weight: 20, applicable: true },
    { factor: 'capacity', raw: rng() * 0.8, normalized: Math.min(100, score + jitter()), weight: 20, applicable: backend },
    { factor: 'conn', raw: backend ? 0 : Math.round(rng() * 1500), normalized: Math.min(100, score + jitter()), weight: 10, applicable: !backend },
    { factor: 'latency', raw: 40 + rng() * 200, normalized: Math.min(100, score + jitter()), weight: 10, applicable: true },
    { factor: 'alert', raw: 0, normalized: 100, weight: 10, applicable: true },
  ]
}

// ---- 调度决策数据集 ----

interface DecisionState {
  decisions: SchedDecisionDetail[]
}

const FAIL_REASONS = ['no_candidate', 'zone_not_found', 'cross_namespace'] as const
const PURPOSES = ['lobby-transfer', 'party-join', 'rtp-target', 'arena-match'] as const

function buildDecisions(scenario: MockScenario): DecisionState {
  if (scenario === 'empty') {
    return { decisions: [] }
  }
  const state = getClusterState()
  const backends = state.servers.filter((s) => s.kind === 'backend' && s.zoneId !== null)
  if (backends.length === 0) {
    return { decisions: [] }
  }
  const count = scenario === 'huge' ? 3200 : 48
  const rng = createRng(20260708)
  const decisions: SchedDecisionDetail[] = []
  for (let i = 0; i < count; i++) {
    const requester = pickOne(rng, backends)
    const chosen = pickOne(rng, backends)
    const zone = state.zones.find((z) => z.id === chosen.zoneId)
    const failed = rng() < 0.12
    const fallback = rng() < 0.06
    const failReason = failed ? pickOne(rng, FAIL_REASONS) : null
    decisions.push({
      traceId: uuidFrom(`sched:${String(i)}`),
      tsMs: BASE_MS - i * 30_000,
      namespaceId: chosen.namespaceId,
      crossNamespace: rng() < 0.04,
      requesterServerId: requester.serverId,
      plugin: rng() < 0.7 ? 'Lodestone' : 'PartyCore',
      purpose: pickOne(rng, PURPOSES),
      zoneName: zone?.name ?? 'area-1',
      strategy: 'highest_score',
      source: fallback ? 'local_fallback' : 'control_plane',
      weightsRev: fallback ? null : currentWeights().rev,
      candidateCount: 2 + Math.floor(rng() * 6),
      excludedCount: failed ? 2 : 1,
      chosenServerId: failed ? null : chosen.serverId,
      chosenScore: failed ? -1 : baseScoreOf(chosen),
      failReason,
      durationMs: 1 + Math.floor(rng() * 4),
      excluded: failed
        ? [
            { serverId: pickOne(rng, backends).serverId, reason: 'unhealthy' },
            { serverId: pickOne(rng, backends).serverId, reason: 'draining' },
          ]
        : [{ serverId: pickOne(rng, backends).serverId, reason: 'lost' }],
    })
  }
  return { decisions }
}

const getDecisionState: () => DecisionState = defineScenarioStore(buildDecisions)

function toDecisionItem(row: SchedDecisionDetail): SchedDecisionItem {
  // 列表项不携带逐台排除明细（详情端点才返回）
  const item: SchedDecisionItem & { excluded?: unknown } = { ...row }
  delete item.excluded
  return item
}

/** 生成某服的确定性时序点（step 秒聚合） */
function seriesPoints(serverId: string, fromMs: number, toMs: number, stepSec: number): MetricsSeriesPoint[] {
  const rng = createRng(hashString(`series:${serverId}`))
  const base = 30 + rng() * 30
  const points: MetricsSeriesPoint[] = []
  const stepMs = stepSec * 1000
  const capped = Math.min(Math.floor((toMs - fromMs) / stepMs), 500)
  for (let i = 0; i <= capped; i++) {
    const wave = Math.sin(i / 8) * 8
    const noise = rng() * 6
    const cpu = Math.max(2, Math.min(98, base + wave + noise))
    points.push({
      tsMs: fromMs + i * stepMs,
      cpuPctAvg: Math.round(cpu * 10) / 10,
      cpuPctMax: Math.round((cpu + 4 + noise) * 10) / 10,
      memUsedMbAvg: Math.round(2048 + wave * 40 + noise * 20),
      tpsAvg: Math.round((19.9 - Math.max(0, wave / 6) - noise / 10) * 10) / 10,
      tpsMin: Math.round((19.2 - Math.max(0, wave / 4) - noise / 8) * 10) / 10,
      onlineAvg: Math.max(0, Math.round(60 + wave * 3 + noise)),
      onlineMax: Math.max(0, Math.round(66 + wave * 3 + noise)),
    })
  }
  return points
}

export const metricsHealthHandlers: HttpHandler[] = [
  // 集群聚合概览（/dashboard）
  mockGet('/admin/v2/metrics/summary', () => {
    const state = getClusterState()
    const servers = state.servers
    const items = servers.map((s) => healthItemOf(state, s))
    const backendItems = items.filter((i) => i.kind === 'backend')
    const summary: MetricsSummary = {
      generatedAt: isoOffset(0),
      byKind: {
        proxy: {
          total: servers.filter((s) => s.kind === 'proxy').length,
          online: servers.filter((s) => s.kind === 'proxy' && s.online).length,
        },
        backend: {
          total: servers.filter((s) => s.kind === 'backend').length,
          online: servers.filter((s) => s.kind === 'backend' && s.online).length,
        },
      },
      playersOnline: backendItems.reduce((sum, i) => sum + (i.score > 0 ? (hashString(i.serverId) % 80) + 5 : 0), 0),
      avgTps: backendItems.length === 0 ? 0 : 19.4,
      avgCpuPct: items.length === 0 ? 0 : 42.6,
      levelDistribution: {
        healthy: items.filter((i) => i.level === 'healthy').length,
        degraded: items.filter((i) => i.level === 'degraded').length,
        unhealthy: items.filter((i) => i.level === 'unhealthy').length,
      },
      schedulable: {
        yes: items.filter((i) => i.schedulable).length,
        no: items.filter((i) => !i.schedulable).length,
      },
    }
    return HttpResponse.json(summary)
  }),

  // 单服 / 多服指标时序（serverId 必填，1000+ 子服禁全量扫；includeArchived=true 冷查询强制时间范围，FR-152）
  mockGet('/admin/v2/metrics/series', ({ request }) => {
    const url = new URL(request.url)
    const serverIdRaw = queryStr(url, 'serverId')
    if (serverIdRaw === null) {
      return jsonError(400, 'invalid_param', 'serverId 必填（禁止全量扫描）')
    }
    if (coldRequested(url)) {
      const rangeError = coldRangeError(queryTimeMs(url, 'from'), queryTimeMs(url, 'to'))
      if (rangeError !== null) {
        return rangeError
      }
    }
    const stepSec = queryInt(url, 'step', 60)
    const toMs = queryTimeMs(url, 'to') ?? BASE_MS
    const fromMs = queryTimeMs(url, 'from') ?? toMs - 3_600_000
    const serverIds = serverIdRaw.split(',').filter((s) => s !== '')
    const response: MetricsSeriesResponse = {
      stepSec,
      series: serverIds.map((serverId) => ({ serverId, points: seriesPoints(serverId, fromMs, toMs, stepSec) })),
    }
    return HttpResponse.json(response)
  }),

  // 健康快照回放（serverId 必填；includeArchived=true 冷查询强制时间范围，FR-152）
  mockGet('/admin/v2/health/snapshots', ({ request }) => {
    const url = new URL(request.url)
    const serverId = queryStr(url, 'serverId')
    if (serverId === null) {
      return jsonError(400, 'invalid_param', 'serverId 必填')
    }
    if (coldRequested(url)) {
      const rangeError = coldRangeError(queryTimeMs(url, 'from'), queryTimeMs(url, 'to'))
      if (rangeError !== null) {
        return rangeError
      }
    }
    const state = getClusterState()
    const server = state.servers.find((s) => s.serverId === serverId)
    if (!server) {
      return HttpResponse.json({ items: [] } satisfies HealthSnapshotsResponse)
    }
    const toMs = queryTimeMs(url, 'to') ?? BASE_MS
    const fromMs = queryTimeMs(url, 'from') ?? toMs - 3_600_000
    const config = currentWeights()
    const rng = createRng(hashString(`snapshot:${serverId}`))
    const base = baseScoreOf(server)
    const items: HealthSnapshotPoint[] = []
    const count = Math.min(Math.floor((toMs - fromMs) / 30_000), 240)
    for (let i = 0; i <= count; i++) {
      const score = Math.max(0, Math.min(100, Math.round(base + Math.sin(i / 6) * 6 + rng() * 4 - 2)))
      const level = levelOf(score, config.config)
      items.push({
        tsMs: fromMs + i * 30_000,
        score,
        level,
        schedulable: level !== 'unhealthy' && server.kind === 'backend',
        reasons: level === 'unhealthy' ? ['unhealthy'] : [],
        weightsRev: config.rev,
      })
    }
    return HttpResponse.json({ items } satisfies HealthSnapshotsResponse)
  }),

  // 全部服务器当前健康列表（分页 + 筛选）
  mockGet('/admin/v2/health', ({ request }) => {
    const url = new URL(request.url)
    const state = getClusterState()
    const namespaceId = queryStr(url, 'namespaceId')
    const zone = queryStr(url, 'zone')
    const level = queryStr(url, 'level')
    const schedulable = queryStr(url, 'schedulable')
    const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
    const rows = state.servers
      .map((s) => healthItemOf(state, s))
      .filter((item) => {
        if (namespaceId !== null && String(item.namespaceId) !== namespaceId) {
          return false
        }
        if (zone !== null && item.zoneName !== zone) {
          return false
        }
        if (level !== null && item.level !== level) {
          return false
        }
        if (schedulable !== null && String(item.schedulable) !== schedulable) {
          return false
        }
        if (keyword !== null && !item.serverId.toLowerCase().includes(keyword)) {
          return false
        }
        return true
      })
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies Paged<HealthItem>)
  }),

  // 单服健康详情（因子分解 + 权重版本）
  mockGet('/admin/v2/health/:serverId', (info) => {
    const serverId = pathParam(info, 'serverId')
    const state = getClusterState()
    const server = state.servers.find((s) => s.serverId === serverId)
    if (!server) {
      return jsonError(404, 'server_not_found', `server ${serverId} 不存在`)
    }
    const item = healthItemOf(state, server)
    const detail: HealthDetail = {
      ...item,
      factors: factorsOf(server, item.score),
      weightsRev: currentWeights().rev,
    }
    return HttpResponse.json(detail)
  }),

  // 决策概览（成功率 / 失败原因 Top / 降级占比）
  mockGet('/admin/v2/sched-decisions/summary', ({ request }) => {
    const url = new URL(request.url)
    const window = queryStr(url, 'window') ?? '1h'
    const decisions = getDecisionState().decisions
    const total = decisions.length
    const successCount = decisions.filter((d) => d.failReason === null).length
    const failCounts = new Map<string, number>()
    for (const d of decisions) {
      if (d.failReason !== null) {
        failCounts.set(d.failReason, (failCounts.get(d.failReason) ?? 0) + 1)
      }
    }
    const summary: SchedDecisionSummary = {
      window,
      total,
      successCount,
      successRatePercent: total === 0 ? 100 : Math.round((successCount / total) * 1000) / 10,
      failReasonTop: [...failCounts.entries()]
        .map(([reason, count]) => ({ reason, count }))
        .sort((a, b) => b.count - a.count),
      localFallbackPercent:
        total === 0 ? 0 : Math.round((decisions.filter((d) => d.source === 'local_fallback').length / total) * 1000) / 10,
    }
    return HttpResponse.json(summary)
  }),

  // 决策记录分页查询（热 page/pageSize；includeArchived=true 冷查询改游标分页，FR-152）
  mockGet('/admin/v2/sched-decisions', ({ request }) => {
    const url = new URL(request.url)
    const cold = coldRequested(url)
    if (cold) {
      const rangeError = coldRangeError(queryMsFlexible(url, 'from'), queryMsFlexible(url, 'to'))
      if (rangeError !== null) {
        return rangeError
      }
    }
    const namespaceId = queryStr(url, 'namespaceId')
    const zone = queryStr(url, 'zone')
    const serverId = queryStr(url, 'serverId')
    const result = queryStr(url, 'result')
    const fromMs = queryTimeMs(url, 'from')
    const toMs = queryTimeMs(url, 'to')
    const rows = getDecisionState()
      .decisions.filter((d) => {
        if (namespaceId !== null && String(d.namespaceId) !== namespaceId) {
          return false
        }
        if (zone !== null && d.zoneName !== zone) {
          return false
        }
        if (serverId !== null && d.requesterServerId !== serverId && d.chosenServerId !== serverId) {
          return false
        }
        if (result === 'success' && d.failReason !== null) {
          return false
        }
        if (result === 'failed' && d.failReason === null) {
          return false
        }
        if (fromMs !== null && d.tsMs < fromMs) {
          return false
        }
        if (toMs !== null && d.tsMs > toMs) {
          return false
        }
        return true
      })
      .map(toDecisionItem)
    if (cold) {
      const { items, nextCursor } = cursorPaginate(rows, url)
      return HttpResponse.json({ items, nextCursor, includeArchived: true })
    }
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies Paged<SchedDecisionItem>)
  }),

  // 单条决策详情（候选 / 逐台排除原因 / 选择）
  mockGet('/admin/v2/sched-decisions/:traceId', (info) => {
    const traceId = pathParam(info, 'traceId')
    const row = getDecisionState().decisions.find((d) => d.traceId === traceId)
    if (!row) {
      return jsonError(404, 'decision_not_found', '决策记录不存在')
    }
    return HttpResponse.json(row)
  }),

  // 当前健康权重配置 + 历史 rev
  mockGet('/admin/v2/settings/health-weights', () => {
    const revs = getWeightsState().revs
    return HttpResponse.json({ current: currentWeights(), history: revs } satisfies HealthWeightsResponse)
  }),

  // 全量替换健康权重（热更 + 新 rev）
  mockPut('/admin/v2/settings/health-weights', async ({ request }) => {
    const body = await readBody<HealthWeightsConfig>(request)
    if (!body.weights || !body.normalize || !body.levels) {
      return jsonError(400, 'invalid_param', 'weights / normalize / levels 必填')
    }
    if (Object.values(body.weights).some((w) => typeof w !== 'number' || w < 0)) {
      return jsonError(400, 'invalid_param', '权重必须为非负数')
    }
    const { healthyMin, degradedMin } = body.levels
    if (!(degradedMin > 0 && degradedMin < healthyMin && healthyMin <= 100)) {
      return jsonError(400, 'invalid_param', '阈值须满足 0 < degradedMin < healthyMin ≤ 100')
    }
    const state = getWeightsState()
    const rev: HealthWeightsRev = {
      rev: currentWeights().rev + 1,
      config: { weights: body.weights, normalize: body.normalize, levels: body.levels },
      operator: 'admin',
      createdAt: isoOffset(0),
    }
    state.revs.push(rev)
    return HttpResponse.json({ current: rev, history: state.revs } satisfies HealthWeightsResponse)
  }),
]
