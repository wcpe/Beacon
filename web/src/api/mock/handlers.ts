/**
 * Mock API 路由分发
 *
 * 拦截 /admin/v1/* 请求，按「方法 + 路径」分发到 data.ts 的有状态读写函数。
 * 覆盖 client.ts 的全部端点；写操作真实改内存态，后续读反映结果。
 * 未匹配的端点统一回带特征码的 404（见 UNROUTED_CODE），供契约测试断言「无漏配」。
 */

import {
  assignMockZone,
  batchMockConfigs,
  buildMockTopology,
  cancelMockReverseFetchTask,
  createMockApiKey,
  createMockCommand,
  createMockConfig,
  createMockFile,
  createMockFileSyncTask,
  createMockIgnoreRule,
  createMockReverseFetchTask,
  deleteMockConfig,
  deleteMockFile,
  deleteMockIgnoreRule,
  deleteMockOverrideSet,
  drainMockInstance,
  getMockAgentLogs,
  getMockAlertEvents,
  getMockAssignments,
  getMockAudits,
  getMockAuditAnalytics,
  getMockBrowse,
  getMockCommandAnalytics,
  getMockCommands,
  getMockConfigDetail,
  getMockConfigList,
  getMockConfigTimeline,
  getMockConflictDiff,
  getMockConflicts,
  getMockDefaultEntries,
  getMockDiff,
  getMockDryRun,
  getMockFile,
  getMockFileList,
  getMockFileRevision,
  getMockFileRevisions,
  getMockFileSyncEvents,
  getMockFileSyncTask,
  getMockImpact,
  getMockImprintDiff,
  getMockMetricsSummary,
  getMockObservability,
  getMockOverrideSet,
  getMockOverrideSetList,
  getMockOverrideSetRevisions,
  getMockReverseFetchTask,
  getMockRevisions,
  getMockSystemStatus,
  getMockTrend,
  getMockZoneStats,
  listMockDrains,
  listMockFileSyncTasks,
  listMockIgnoreRules,
  listMockInstances,
  listMockOfflineMarkers,
  listMockReverseFetchTasks,
  listMockReversibleOps,
  listMockSettings,
  makeMockImprintCommand,
  mockApiKeys,
  mockInstances,
  mockNamespaces,
  offlineMockInstance,
  onlineMockInstance,
  publishMockConfig,
  publishMockOverrideSet,
  planMockFileSyncTask,
  resetMockApiKey,
  resolveMockConflicts,
  revokeMockApiKey,
  rollbackMockConfig,
  rollbackMockFile,
  rollbackMockOverrideSet,
  runMockFileSyncAction,
  saveMockFile,
  stripApiKeySecret,
  submitMockReverseFetchTask,
  undoMockReversibleOp,
  undrainMockInstance,
  unassignMockZone,
  updateMockSetting,
} from './data'
import type { LoginResult } from '../types'

// 未匹配端点的特征码：契约测试据此识别「漏配 mock handler」（与普通业务 404 区分）。
export const UNROUTED_CODE = 'MOCK_UNROUTED'

// ---- 辅助 ----

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
  })
}

function noContent(): Response {
  return new Response(null, { status: 204 })
}

function notFound(resource: string): Response {
  return json({ code: 'NOT_FOUND', message: `${resource} 不存在` }, 404)
}

function badRequest(message: string): Response {
  return json({ code: 'INVALID_PARAMS', message }, 400)
}

function parseQS(search: string): Record<string, string> {
  const params: Record<string, string> = {}
  new URLSearchParams(search).forEach((v, k) => {
    params[k] = v
  })
  return params
}

function parseBody(init?: RequestInit): Record<string, unknown> {
  if (!init?.body || typeof init.body !== 'string') return {}
  try {
    return JSON.parse(init.body) as Record<string, unknown>
  } catch {
    return {}
  }
}

function num(v: string | undefined, fallback = 0): number {
  const n = Number(v)
  return Number.isFinite(n) ? n : fallback
}

// ---- 路由分发 ----

export async function handleMockRequest(path: string, init?: RequestInit): Promise<Response> {
  const url = new URL(path, 'http://localhost')
  const p = url.pathname
  const qs = parseQS(url.search)
  const method = (init?.method ?? 'GET').toUpperCase()

  // 顺序：先精确路径 + 方法，再正则匹配带 id 的子资源。

  // ===== 登录 / 身份 =====
  if (p === '/admin/v1/auth/login' && method === 'POST') {
    const body = parseBody(init)
    if (!body.username || !body.password) {
      return json({ code: 'INVALID_CREDENTIALS', message: '账号或口令为空' }, 401)
    }
    const result: LoginResult = {
      token: 'mock-token-' + Date.now(),
      operator: String(body.username),
    }
    return json(result)
  }
  if (p === '/admin/v1/auth/logout' && method === 'POST') return noContent()

  // ===== 环境（namespace）=====
  if (p === '/admin/v1/namespaces' && method === 'GET') {
    return json({ items: mockNamespaces })
  }
  if (p === '/admin/v1/namespaces' && method === 'POST') {
    const body = parseBody(init)
    if (!body.code) return badRequest('环境编码为必填')
    if (mockNamespaces.some((n) => n.code === body.code)) {
      return json({ code: 'CONFLICT', message: `环境 ${String(body.code)} 已存在` }, 409)
    }
    const ns = { code: String(body.code), name: String(body.name ?? '') }
    mockNamespaces.push(ns)
    return json(ns, 201)
  }
  const namespaceMatch = p.match(/^\/admin\/v1\/namespaces\/([^/]+)$/)
  if (namespaceMatch && method === 'PUT') {
    const code = decodeURIComponent(namespaceMatch[1])
    const ns = mockNamespaces.find((n) => n.code === code)
    if (!ns) return notFound(`环境 ${code}`)
    const body = parseBody(init)
    ns.name = String(body.name ?? ns.name)
    return json({ code: ns.code, name: ns.name })
  }
  if (namespaceMatch && method === 'DELETE') {
    const code = decodeURIComponent(namespaceMatch[1])
    const idx = mockNamespaces.findIndex((n) => n.code === code)
    if (idx === -1) return notFound(`环境 ${code}`)
    // prod / test 内置环境含在用数据，演示删除守卫（与后端 409 一致）
    if (code === 'prod' || code === 'test') {
      return json(
        { code: 'CONFLICT', message: `环境 ${code} 下仍有实例 / zone / 配置，禁止删除` },
        409,
      )
    }
    mockNamespaces.splice(idx, 1)
    return noContent()
  }

  // ===== 配置中心 =====
  if (p === '/admin/v1/configs' && method === 'GET') {
    let items = getMockConfigList()
    if (qs.namespace) items = items.filter((c) => c.namespace === qs.namespace)
    if (qs.group) items = items.filter((c) => c.group === qs.group)
    if (qs.dataId) items = items.filter((c) => c.dataId === qs.dataId)
    if (qs.scopeLevel) items = items.filter((c) => c.scopeLevel === qs.scopeLevel)
    return json({ items })
  }
  if (p === '/admin/v1/configs' && method === 'POST') {
    return json(createMockConfig(parseBody(init)), 201)
  }
  if (p === '/admin/v1/configs/batch' && method === 'POST') {
    const body = parseBody(init)
    const action = String(body.action ?? '')
    const ids = Array.isArray(body.ids) ? (body.ids as number[]) : []
    if (!['delete', 'disable', 'enable'].includes(action)) return badRequest('非法批量动作')
    if (ids.length === 0) return badRequest('ids 为空')
    const count = batchMockConfigs(action as 'delete' | 'disable' | 'enable', ids)
    return json({ action, count })
  }
  if (p === '/admin/v1/configs/effective' && method === 'GET') {
    return json(getMockEffectiveConfig(qs))
  }
  if (p === '/admin/v1/configs/impact' && method === 'GET') {
    if (!qs.namespace || !qs.scopeLevel) return badRequest('namespace 与 scopeLevel 为必填')
    return json(
      getMockImpact({
        namespace: qs.namespace,
        scopeLevel: qs.scopeLevel,
        group: qs.group,
        scopeTarget: qs.scopeTarget,
      }),
    )
  }

  const configDiffMatch = p.match(/^\/admin\/v1\/configs\/(\d+)\/diff$/)
  if (configDiffMatch && method === 'GET') {
    const id = Number(configDiffMatch[1])
    const from = num(qs.from)
    const to = num(qs.to)
    if (!from || !to) return badRequest('from 和 to 均为必填')
    const diff = getMockDiff(id, from, to)
    if (!diff) return notFound(`配置 #${id} 的版本对比 v${from} → v${to}`)
    return json(diff)
  }
  const configRollbackMatch = p.match(/^\/admin\/v1\/configs\/(\d+)\/rollback$/)
  if (configRollbackMatch && method === 'POST') {
    const id = Number(configRollbackMatch[1])
    const body = parseBody(init)
    const r = rollbackMockConfig(id, num(String(body.toVersion), 1), String(body.comment ?? ''))
    if (r === 'no-config') return notFound(`配置 #${id}`)
    if (r === 'no-version') return notFound(`版本 v${String(body.toVersion)}`)
    return json(r)
  }
  const configRevDetailMatch = p.match(/^\/admin\/v1\/configs\/(\d+)\/revisions\/(\d+)$/)
  if (configRevDetailMatch && method === 'GET') {
    const id = Number(configRevDetailMatch[1])
    const version = Number(configRevDetailMatch[2])
    const rev = getMockRevisions(id).find((r) => r.version === version)
    if (!rev) return notFound(`版本 v${version}`)
    return json(rev)
  }
  const configRevMatch = p.match(/^\/admin\/v1\/configs\/(\d+)\/revisions$/)
  if (configRevMatch && method === 'GET') {
    const id = Number(configRevMatch[1])
    const revisions = getMockRevisions(id)
    if (revisions.length === 0 && !getMockConfigDetail(id)) return notFound(`配置 #${id}`)
    return json({ items: revisions })
  }
  const configDetailMatch = p.match(/^\/admin\/v1\/configs\/(\d+)$/)
  if (configDetailMatch && method === 'GET') {
    const id = Number(configDetailMatch[1])
    const detail = getMockConfigDetail(id)
    if (!detail) return notFound(`配置 #${id}`)
    return json(detail)
  }
  if (configDetailMatch && method === 'PUT') {
    const id = Number(configDetailMatch[1])
    const body = parseBody(init)
    const r = publishMockConfig(id, String(body.content ?? ''), String(body.comment ?? ''))
    if (!r) return notFound(`配置 #${id}`)
    return json(r)
  }
  if (configDetailMatch && method === 'DELETE') {
    const id = Number(configDetailMatch[1])
    if (!deleteMockConfig(id)) return notFound(`配置 #${id}`)
    return noContent()
  }

  // ===== 文件树有效预览（FR-45）=====
  if (p === '/admin/v1/files/effective' && method === 'GET') {
    return json(getMockEffectiveFiles(qs))
  }

  // ===== 实例与健康 =====
  if (p === '/admin/v1/instances' && method === 'GET') {
    return json({ items: listMockInstances(qs) })
  }
  if (p === '/admin/v1/instances/offline' && method === 'GET') {
    return json({ items: listMockOfflineMarkers(qs.namespace) })
  }

  const instTimelineMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/config-timeline$/)
  if (instTimelineMatch && method === 'GET') {
    const serverId = decodeURIComponent(instTimelineMatch[1])
    if (!qs.namespace) return badRequest('namespace 为必填')
    return json(getMockConfigTimeline(serverId, qs.namespace, qs.group))
  }
  const instOfflineMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/offline$/)
  if (instOfflineMatch && method === 'POST') {
    const serverId = decodeURIComponent(instOfflineMatch[1])
    const ns = qs.namespace ?? ''
    const body = parseBody(init)
    offlineMockInstance(serverId, ns, String(body.reason ?? ''))
    return noContent()
  }
  if (instOfflineMatch && method === 'DELETE') {
    const serverId = decodeURIComponent(instOfflineMatch[1])
    onlineMockInstance(serverId, qs.namespace ?? '')
    return noContent()
  }
  const instBrowseMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/browse$/)
  if (instBrowseMatch && method === 'GET') {
    return json(getMockBrowse(qs.op ?? 'tree', qs.path ?? ''))
  }
  const instReverseFetchMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/reverse-fetch$/)
  if (instReverseFetchMatch && method === 'POST') {
    const serverId = decodeURIComponent(instReverseFetchMatch[1])
    const body = parseBody(init)
    if (!body.group) return badRequest('目标组为必填')
    if (body.scope === 'server' && !body.target) return badRequest('server 层需指定目标实例')
    const ns = qs.namespace ?? 'prod'
    // FR-58 受管任务：触发即建任务并返回任务视图（client.createScanTask / triggerReverseFetch 共用此端点）
    return json(
      createMockReverseFetchTask(serverId, ns, {
        scope: String(body.scope ?? 'group'),
        group: String(body.group),
        target: body.target ? String(body.target) : undefined,
      }),
      202,
    )
  }
  const instImprintMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/imprint$/)
  if (instImprintMatch && method === 'POST') {
    const serverId = decodeURIComponent(instImprintMatch[1])
    const body = parseBody(init)
    if (!body.path) return badRequest('目标文件 path 为必填')
    return json(makeMockImprintCommand(serverId, qs.namespace ?? 'prod'), 202)
  }
  const instLogsMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/logs$/)
  if (instLogsMatch && method === 'POST') {
    const serverId = decodeURIComponent(instLogsMatch[1])
    const cmd = createMockCommand(serverId, qs.namespace ?? 'prod', 'tail-logs', 'pending')
    return json({ commandId: cmd.id, status: 'pending', lines: [] }, 202)
  }
  if (instLogsMatch && method === 'GET') {
    const serverId = decodeURIComponent(instLogsMatch[1])
    return json(getMockAgentLogs(serverId))
  }
  const instResyncMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/resync$/)
  if (instResyncMatch && method === 'POST') {
    const serverId = decodeURIComponent(instResyncMatch[1])
    return json(
      createMockCommand(serverId, qs.namespace ?? 'prod', 'resync-config', 'pending'),
      202,
    )
  }

  // ===== 流量调度·排空（FR-10）=====
  if (p === '/admin/v1/scheduling/drains' && method === 'GET') {
    return json({ items: listMockDrains(qs.namespace) })
  }
  if (p === '/admin/v1/scheduling/drains' && method === 'PUT') {
    const body = parseBody(init)
    if (!body.namespace || !body.serverId) return badRequest('namespace 与 serverId 为必填')
    return json(
      drainMockInstance(String(body.serverId), String(body.namespace), String(body.reason ?? '')),
    )
  }
  if (p === '/admin/v1/scheduling/drains' && method === 'DELETE') {
    if (!qs.namespace || !qs.serverId) return badRequest('namespace 与 serverId 为必填')
    undrainMockInstance(qs.serverId, qs.namespace)
    return noContent()
  }

  // ===== 集群拓扑（FR-37）=====
  if (p === '/admin/v1/topology' && method === 'GET') {
    if (!qs.namespace) return badRequest('namespace 为必填')
    return json(buildMockTopology(qs.namespace))
  }

  // ===== 指标看板（FR-32 / FR-34）=====
  if (p === '/admin/v1/metrics/summary' && method === 'GET') {
    return json(getMockMetricsSummary(qs.namespace))
  }
  if (p === '/admin/v1/metrics/trend' && method === 'GET') {
    return json(getMockTrend(qs.namespace, qs.window ?? '1h'))
  }

  // ===== 控制面自身状态 / 自观测 / 在线更新 =====
  if (p === '/admin/v1/system/status' && method === 'GET') return json(getMockSystemStatus())
  if (p === '/admin/v1/system/observability' && method === 'GET') {
    return json(getMockObservability())
  }
  if (p === '/admin/v1/system/update-check' && method === 'GET') {
    return json(getMockUpdateCheck())
  }
  if (p === '/admin/v1/system/update' && method === 'GET') return json(getMockUpdateProgress())
  if (p === '/admin/v1/system/update' && method === 'POST') {
    updateState.phase = 'downloading'
    updateState.targetVersion = 'v0.99.0'
    return json({ accepted: true }, 202)
  }
  if (p === '/admin/v1/system/update/cancel' && method === 'POST') {
    const hadActive = updateState.phase !== 'idle'
    updateState.phase = 'idle'
    updateState.percent = 0
    return json({ cancelled: hadActive }, hadActive ? 202 : 200)
  }
  if (p === '/admin/v1/system/proxy-test' && method === 'GET') {
    return json({ ok: true })
  }
  if (p === '/admin/v1/system/rollback' && method === 'POST') {
    return json({ accepted: true }, 202)
  }

  // ===== zone 分配 =====
  if (p === '/admin/v1/zones/assignments' && method === 'GET') {
    return json({ items: getMockAssignments(qs) })
  }
  if (p === '/admin/v1/zones/assignments' && method === 'PUT') {
    const body = parseBody(init)
    if (!body.namespace || !body.serverId || !body.zone) {
      return badRequest('namespace / serverId / zone 为必填')
    }
    return json(
      assignMockZone({
        namespace: String(body.namespace),
        serverId: String(body.serverId),
        group: String(body.group ?? ''),
        zone: String(body.zone),
        note: String(body.note ?? ''),
      }),
    )
  }
  if (p === '/admin/v1/zones/assignments' && method === 'DELETE') {
    if (!qs.namespace || !qs.serverId) return badRequest('namespace 与 serverId 为必填')
    unassignMockZone(qs.namespace, qs.serverId)
    return noContent()
  }
  if (p === '/admin/v1/zones/default-entry' && method === 'GET') {
    return json({ items: getMockDefaultEntries(qs) })
  }
  if (p === '/admin/v1/zones' && method === 'GET') {
    return json({ items: getMockZoneStats(qs.namespace, qs.group) })
  }

  // ===== 审计 / 服务分析 =====
  if (p === '/admin/v1/audits' && method === 'GET') {
    return json(
      getMockAudits({
        ...qs,
        page: qs.page ? num(qs.page) : undefined,
        size: qs.size ? num(qs.size) : undefined,
      }),
    )
  }
  if (p === '/admin/v1/audits/analytics' && method === 'GET') {
    return json(getMockAuditAnalytics(qs))
  }

  // ===== 告警事件（FR-89）=====
  if (p === '/admin/v1/alert-events' && method === 'GET') {
    return json(
      getMockAlertEvents({
        ...qs,
        page: qs.page ? num(qs.page) : undefined,
        size: qs.size ? num(qs.size) : undefined,
      }),
    )
  }

  // ===== 命令观测（FR-104）=====
  if (p === '/admin/v1/commands' && method === 'GET') {
    return json(
      getMockCommands({
        ...qs,
        page: qs.page ? num(qs.page) : undefined,
        size: qs.size ? num(qs.size) : undefined,
      }),
    )
  }
  if (p === '/admin/v1/commands/analytics' && method === 'GET') {
    return json(getMockCommandAnalytics(qs))
  }

  // ===== API 密钥（FR-42）=====
  if (p === '/admin/v1/api-keys' && method === 'GET') {
    return json({ items: mockApiKeys.map(stripApiKeySecret) })
  }
  if (p === '/admin/v1/api-keys' && method === 'POST') {
    const body = parseBody(init)
    const created = createMockApiKey(
      String(body.name ?? 'mock-key'),
      String(body.role ?? 'readonly'),
      body.expiresAt ? String(body.expiresAt) : null,
    )
    return json(created, 201)
  }
  const apiKeyResetMatch = p.match(/^\/admin\/v1\/api-keys\/(\d+)\/reset$/)
  if (apiKeyResetMatch && method === 'POST') {
    const k = resetMockApiKey(Number(apiKeyResetMatch[1]))
    if (!k) return notFound(`密钥 #${apiKeyResetMatch[1]}`)
    return json(k)
  }
  const apiKeyMatch = p.match(/^\/admin\/v1\/api-keys\/(\d+)$/)
  if (apiKeyMatch && method === 'DELETE') {
    if (!revokeMockApiKey(Number(apiKeyMatch[1]))) return notFound(`密钥 #${apiKeyMatch[1]}`)
    return noContent()
  }

  // ===== 文件树托管（FR-14）=====
  if (p === '/admin/v1/files' && method === 'GET') {
    return json({ items: getMockFileList(qs) })
  }
  if (p === '/admin/v1/files' && method === 'POST') {
    return json(createMockFile(parseBody(init)), 201)
  }
  if (p === '/admin/v1/files/import' && method === 'POST') {
    // 导入走 multipart：mock 无法解析 FormData 内容，返回示意计数（让上传对话框走通）。
    return json({ files: 3, created: 2, updated: 1 })
  }
  const fileRollbackMatch = p.match(/^\/admin\/v1\/files\/(\d+)\/rollback$/)
  if (fileRollbackMatch && method === 'POST') {
    const id = Number(fileRollbackMatch[1])
    const body = parseBody(init)
    const r = rollbackMockFile(id, num(String(body.toVersion), 1), String(body.comment ?? ''))
    if (r === 'no-file') return notFound(`文件 #${id}`)
    if (r === 'no-version') return notFound(`版本 v${String(body.toVersion)}`)
    return json(r)
  }
  const fileRevDetailMatch = p.match(/^\/admin\/v1\/files\/(\d+)\/revisions\/(\d+)$/)
  if (fileRevDetailMatch && method === 'GET') {
    const id = Number(fileRevDetailMatch[1])
    const version = Number(fileRevDetailMatch[2])
    const rev = getMockFileRevision(id, version)
    if (!rev) return notFound(`文件 #${id} 版本 v${version}`)
    return json(rev)
  }
  const fileRevMatch = p.match(/^\/admin\/v1\/files\/(\d+)\/revisions$/)
  if (fileRevMatch && method === 'GET') {
    return json({ items: getMockFileRevisions(Number(fileRevMatch[1])) })
  }
  const fileDetailMatch = p.match(/^\/admin\/v1\/files\/(\d+)$/)
  if (fileDetailMatch && method === 'GET') {
    const id = Number(fileDetailMatch[1])
    const file = getMockFile(id)
    if (!file) return notFound(`文件 #${id}`)
    return json(file)
  }
  if (fileDetailMatch && method === 'PUT') {
    const id = Number(fileDetailMatch[1])
    const body = parseBody(init)
    const r = saveMockFile(id, String(body.content ?? ''), String(body.comment ?? ''))
    if (!r) return notFound(`文件 #${id}`)
    return json(r)
  }
  if (fileDetailMatch && method === 'DELETE') {
    const id = Number(fileDetailMatch[1])
    if (!deleteMockFile(id)) return notFound(`文件 #${id}`)
    return noContent()
  }

  // ===== 拓印审核台（FR-46）=====
  const imprintDiffMatch = p.match(/^\/admin\/v1\/imprints\/(\d+)\/diff$/)
  if (imprintDiffMatch && method === 'GET') {
    return json(getMockImprintDiff())
  }
  const imprintConfirmMatch = p.match(/^\/admin\/v1\/imprints\/(\d+)\/confirm$/)
  if (imprintConfirmMatch && method === 'POST') {
    const body = parseBody(init)
    return json({
      fileId: 1,
      scopeLevel: String(body.scope ?? 'server'),
      group: String(body.group ?? ''),
      target: String(body.target ?? ''),
      version: 1,
      md5: String(body.reviewedMd5 ?? 'aaaaaaaa1111'),
    })
  }
  const imprintStatusMatch = p.match(/^\/admin\/v1\/imprints\/(\d+)$/)
  if (imprintStatusMatch && method === 'GET') {
    return json({
      id: Number(imprintStatusMatch[1]),
      namespace: 'prod',
      serverId: 'server-01',
      type: 'ingest-plugins',
      status: 'ready',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    })
  }

  // ===== 覆盖集（FR-15）=====
  if (p === '/admin/v1/override-sets' && method === 'GET') {
    return json({ items: getMockOverrideSetList(qs) })
  }
  const osRollbackMatch = p.match(/^\/admin\/v1\/override-sets\/(\d+)\/rollback$/)
  if (osRollbackMatch && method === 'POST') {
    const id = Number(osRollbackMatch[1])
    const body = parseBody(init)
    const r = rollbackMockOverrideSet(
      id,
      num(String(body.toVersion), 1),
      String(body.comment ?? ''),
    )
    if (r === 'no-set') return notFound(`覆盖集 #${id}`)
    if (r === 'no-version') return notFound(`版本 v${String(body.toVersion)}`)
    return json(r)
  }
  const osDryRunMatch = p.match(/^\/admin\/v1\/override-sets\/(\d+)\/dry-run$/)
  if (osDryRunMatch && method === 'GET') {
    const dry = getMockDryRun(Number(osDryRunMatch[1]))
    if (!dry) return notFound(`覆盖集 #${osDryRunMatch[1]}`)
    return json(dry)
  }
  const osRevMatch = p.match(/^\/admin\/v1\/override-sets\/(\d+)\/revisions$/)
  if (osRevMatch && method === 'GET') {
    return json({ items: getMockOverrideSetRevisions(Number(osRevMatch[1])) })
  }
  const osDetailMatch = p.match(/^\/admin\/v1\/override-sets\/(\d+)$/)
  if (osDetailMatch && method === 'GET') {
    const os = getMockOverrideSet(Number(osDetailMatch[1]))
    if (!os) return notFound(`覆盖集 #${osDetailMatch[1]}`)
    return json(os)
  }
  if (osDetailMatch && method === 'PUT') {
    const id = Number(osDetailMatch[1])
    const body = parseBody(init)
    const r = publishMockOverrideSet(
      id,
      String(body.targetRoot ?? ''),
      String(body.reloadCommand ?? ''),
      String(body.comment ?? ''),
    )
    if (!r) return notFound(`覆盖集 #${id}`)
    return json(r)
  }
  if (osDetailMatch && method === 'DELETE') {
    const id = Number(osDetailMatch[1])
    if (!deleteMockOverrideSet(id)) return notFound(`覆盖集 #${id}`)
    return noContent()
  }

  // ===== 反向抓取受管任务 + 冲突 + 忽略规则（FR-58/59）=====
  if (p === '/admin/v1/reverse-fetch/tasks' && method === 'GET') {
    return json({ items: listMockReverseFetchTasks(qs) })
  }
  if (p === '/admin/v1/reverse-fetch/ignore-rules' && method === 'GET') {
    return json({ items: listMockIgnoreRules(qs) })
  }
  if (p === '/admin/v1/reverse-fetch/ignore-rules' && method === 'POST') {
    const body = parseBody(init)
    return json(
      createMockIgnoreRule({
        namespace: String(body.namespace ?? ''),
        scope: String(body.scope ?? 'group'),
        group: String(body.group ?? ''),
        target: body.target ? String(body.target) : undefined,
        ruleType: String(body.ruleType ?? 'exact'),
        pattern: String(body.pattern ?? ''),
        comment: body.comment ? String(body.comment) : undefined,
      }),
      201,
    )
  }
  const ignoreRuleMatch = p.match(/^\/admin\/v1\/reverse-fetch\/ignore-rules\/(\d+)$/)
  if (ignoreRuleMatch && method === 'DELETE') {
    if (!deleteMockIgnoreRule(Number(ignoreRuleMatch[1]))) {
      return notFound(`忽略规则 #${ignoreRuleMatch[1]}`)
    }
    return noContent()
  }
  const taskSubmitMatch = p.match(/^\/admin\/v1\/reverse-fetch\/tasks\/(\d+)\/submit$/)
  if (taskSubmitMatch && method === 'POST') {
    const id = Number(taskSubmitMatch[1])
    const body = parseBody(init)
    const paths = Array.isArray(body.selectedPaths) ? (body.selectedPaths as string[]) : []
    const t = submitMockReverseFetchTask(id, paths)
    if (!t) return notFound(`任务 #${id}`)
    return json(t)
  }
  const taskCancelMatch = p.match(/^\/admin\/v1\/reverse-fetch\/tasks\/(\d+)\/cancel$/)
  if (taskCancelMatch && method === 'POST') {
    const t = cancelMockReverseFetchTask(Number(taskCancelMatch[1]))
    if (!t) return notFound(`任务 #${taskCancelMatch[1]}`)
    return json(t)
  }
  const taskConflictDiffMatch = p.match(
    /^\/admin\/v1\/reverse-fetch\/tasks\/(\d+)\/conflicts\/diff$/,
  )
  if (taskConflictDiffMatch && method === 'GET') {
    return json(getMockConflictDiff(Number(taskConflictDiffMatch[1]), qs.path ?? ''))
  }
  const taskConflictsMatch = p.match(/^\/admin\/v1\/reverse-fetch\/tasks\/(\d+)\/conflicts$/)
  if (taskConflictsMatch && method === 'GET') {
    return json({ conflicts: getMockConflicts(Number(taskConflictsMatch[1])) })
  }
  const taskResolveMatch = p.match(/^\/admin\/v1\/reverse-fetch\/tasks\/(\d+)\/resolve$/)
  if (taskResolveMatch && method === 'POST') {
    const id = Number(taskResolveMatch[1])
    const body = parseBody(init)
    const decisions = Array.isArray(body.decisions)
      ? (body.decisions as Array<{ path: string; action: string }>)
      : []
    const r = resolveMockConflicts(id, decisions)
    if (!r) return notFound(`任务 #${id}`)
    return json(r)
  }
  const taskDetailMatch = p.match(/^\/admin\/v1\/reverse-fetch\/tasks\/(\d+)$/)
  if (taskDetailMatch && method === 'GET') {
    const t = getMockReverseFetchTask(Number(taskDetailMatch[1]))
    if (!t) return notFound(`任务 #${taskDetailMatch[1]}`)
    return json(t)
  }

  // ===== 多级灰度配置同步中心（file-sync）=====
  if (p === '/admin/v1/file-sync/tasks' && method === 'GET') {
    return json({ items: listMockFileSyncTasks(qs) })
  }
  if (p === '/admin/v1/file-sync/tasks' && method === 'POST') {
    const body = parseBody(init)
    if (!body.sourceServerId) return badRequest('源服务器为必填')
    if (!body.directory) return badRequest('同步目录为必填')
    return json(createMockFileSyncTask(body), 201)
  }
  const fileSyncEventsMatch = p.match(/^\/admin\/v1\/file-sync\/tasks\/([^/]+)\/events$/)
  if (fileSyncEventsMatch && method === 'GET') {
    const id = decodeURIComponent(fileSyncEventsMatch[1])
    const events = getMockFileSyncEvents(id, Number(qs.afterLogId ?? 0))
    const body = events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join('')
    return new Response(body, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream; charset=utf-8' },
    })
  }
  const fileSyncPlanMatch = p.match(/^\/admin\/v1\/file-sync\/tasks\/([^/]+)\/plan$/)
  if (fileSyncPlanMatch && method === 'POST') {
    const id = decodeURIComponent(fileSyncPlanMatch[1])
    const body = parseBody(init)
    const targets = Array.isArray(body.targetServerIds) ? (body.targetServerIds as string[]) : []
    const task = planMockFileSyncTask(id, targets)
    if (!task) return notFound(`文件同步任务 ${id}`)
    return json(task)
  }
  const fileSyncActionMatch = p.match(
    /^\/admin\/v1\/file-sync\/tasks\/([^/]+)\/(start|pause|resume|terminate)$/,
  )
  if (fileSyncActionMatch && method === 'POST') {
    const id = decodeURIComponent(fileSyncActionMatch[1])
    const action = fileSyncActionMatch[2]
    const task = runMockFileSyncAction(id, action)
    if (!task) return notFound(`文件同步任务 ${id}`)
    return json(task)
  }
  const fileSyncTaskMatch = p.match(/^\/admin\/v1\/file-sync\/tasks\/([^/]+)$/)
  if (fileSyncTaskMatch && method === 'GET') {
    const id = decodeURIComponent(fileSyncTaskMatch[1])
    const task = getMockFileSyncTask(id)
    if (!task) return notFound(`文件同步任务 ${id}`)
    return json(task)
  }

  // ===== 运维设置（FR-61/62）=====
  if (p === '/admin/v1/settings' && method === 'GET') {
    return json({ items: listMockSettings() })
  }
  const settingMatch = p.match(/^\/admin\/v1\/settings\/([^/]+)$/)
  if (settingMatch && method === 'PUT') {
    const key = decodeURIComponent(settingMatch[1])
    const body = parseBody(init)
    if (!updateMockSetting(key, String(body.value ?? ''))) {
      return badRequest(`未知设置项 ${key}`)
    }
    return noContent()
  }

  // ===== 配置操作级撤回（FR-116）=====
  if (p === '/admin/v1/reversible-operations' && method === 'GET') {
    return json({
      items: listMockReversibleOps({ ...qs, limit: qs.limit ? num(qs.limit) : undefined }),
    })
  }
  const undoMatch = p.match(/^\/admin\/v1\/reversible-operations\/(\d+)\/undo$/)
  if (undoMatch && method === 'POST') {
    const op = undoMockReversibleOp(Number(undoMatch[1]))
    if (!op) return notFound(`可逆操作 #${undoMatch[1]}`)
    return json(op)
  }

  // ===== 审计导出（FR-84）=====：返回附件流（CSV / JSON），让浏览器下载走通。
  if (p === '/admin/v1/audits/export' && method === 'GET') {
    const format = qs.format === 'json' ? 'json' : 'csv'
    const page = getMockAudits(qs)
    const body =
      format === 'json'
        ? JSON.stringify(page.items)
        : 'namespace,operator,action,targetRef,detail,createdAt\n' +
          page.items
            .map(
              (a) =>
                `${a.namespace},${a.operator},${a.action},${a.targetRef},${a.detail},${a.createdAt}`,
            )
            .join('\n')
    return new Response(body, {
      status: 200,
      headers: {
        'Content-Type': format === 'json' ? 'application/json' : 'text/csv',
        'Content-Disposition': `attachment; filename="audit-export.${format}"`,
      },
    })
  }

  // ===== 兜底：未注册端点回带特征码 404，供契约测试识别漏配 =====
  return json({ code: UNROUTED_CODE, message: `未注册的 mock 端点: ${method} ${p}` }, 404)
}

// ---- 局部派生（有效配置 / 文件预览 / 更新态：纯展示派生，留在 handler 层） ----

import type { EffectiveConfigView, EffectiveFileTreeView } from '../client'
import type { UpdateCheckView, UpdateProgressView } from '../types'

// 有效配置预览（FR-22）：从配置项按覆盖层 best-effort 派生（演示级，不做真合并）。
function getMockEffectiveConfig(qs: Record<string, string>): EffectiveConfigView {
  const namespace = qs.namespace ?? 'prod'
  const items = getMockConfigList()
    .filter((c) => c.namespace === namespace)
    .filter((c) => (qs.group ? c.group === qs.group || c.scopeLevel === 'global' : true))
    .map((c) => ({
      dataId: c.dataId,
      format: c.format,
      content: '',
      md5: c.md5,
      sources: [{ path: [] as string[], scope: c.scopeLevel }],
      deletions: [] as Array<{ path: string[]; scope: string }>,
    }))
  // 同 dataId 去重（取首个，演示级）
  const seen = new Set<string>()
  const deduped = items.filter((i) => (seen.has(i.dataId) ? false : (seen.add(i.dataId), true)))
  return {
    namespace,
    serverId: qs.serverId,
    group: qs.group,
    zone: qs.zone,
    md5: 'mock-effective-md5',
    items: deduped,
  }
}

// 有效文件树预览（FR-45）：从在线实例所在环境的受管文件派生（演示级）。
function getMockEffectiveFiles(qs: Record<string, string>): EffectiveFileTreeView {
  const namespace = qs.namespace ?? 'prod'
  const inst = qs.serverId ? mockInstances.find((i) => i.serverId === qs.serverId) : undefined
  return {
    namespace,
    serverId: qs.serverId,
    group: qs.group ?? inst?.group,
    zone: qs.zone ?? inst?.zone ?? null,
    fileTreeMd5: 'mock-file-tree-md5',
    files: [
      {
        path: 'plugins/game/config.yml',
        md5: 'abc12345',
        content: 'enabled: true\ncooldown: 30\n',
        wholeFile: false,
        sources: [{ path: ['enabled'], scope: 'global' }],
        deletions: [],
      },
    ],
  }
}

// 更新检查（FR-99）：mock 固定为「已是最新」（无可用更新），避免误叠红点。
function getMockUpdateCheck(): UpdateCheckView {
  return {
    status: 'ok',
    currentVersion: 'dev-mock',
    channel: 'stable',
    hasUpdate: false,
    isDevBuild: true,
    latestVersion: '',
    releaseNotes: '',
    releaseUrl: '',
    publishedAt: '',
    checkedAt: new Date().toISOString(),
    cacheExpiresAt: new Date(Date.now() + 600000).toISOString(),
  }
}

// 更新进度（FR-100）：进程内瞬态，触发更新 / 取消会改写 updateState。
const updateState: { phase: UpdateProgressView['phase']; percent: number; targetVersion: string } =
  {
    phase: 'idle',
    percent: 0,
    targetVersion: '',
  }

function getMockUpdateProgress(): UpdateProgressView {
  return {
    phase: updateState.phase,
    percent: updateState.percent,
    targetVersion: updateState.targetVersion,
    error: '',
    rollbackAvailable: false,
  }
}
