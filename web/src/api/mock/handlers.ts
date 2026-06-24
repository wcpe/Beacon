/**
 * Mock API handlers
 *
 * 拦截 /admin/v1/* 请求，返回 mock 数据。
 * 支持：configs CRUD、revisions、diff、auth/login。
 */

import {
  getMockConfigList,
  getMockConfigDetail,
  getMockRevisions,
  getMockDiff,
  getNextId,
  getNextVersion,
  mockInstances,
  mockZoneStats,
  mockNamespaces,
  getMockAudits,
  getMockFileList,
  getMockFile,
  getMockFileRevisions,
  getMockOverrideSetList,
  getMockOverrideSet,
  getMockOverrideSetRevisions,
  getMockDryRun,
  getMockAssignments,
  getMockMetricsSummary,
  getMockTrend,
  getMockSystemStatus,
  getMockObservability,
  buildMockTopology,
  getMockDefaultEntries,
} from './data'
import type { ConfigView, LoginResult, PublishResult, RevisionView } from '../types'

// ---- 辅助 ----

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
  })
}

function notFound(resource: string): Response {
  return json({ code: 'NOT_FOUND', message: `${resource} 不存在` }, 404)
}

function parseQS(search: string): Record<string, string> {
  const params: Record<string, string> = {}
  const sp = new URLSearchParams(search)
  sp.forEach((v, k) => { params[k] = v })
  return params
}

// ---- 路由分发 ----

export async function handleMockRequest(path: string, init?: RequestInit): Promise<Response> {
  const url = new URL(path, 'http://localhost')
  const p = url.pathname
  const qs = parseQS(url.search)
  const method = (init?.method ?? 'GET').toUpperCase()

  // auth
  if (p === '/admin/v1/auth/login' && method === 'POST') {
    const body = init?.body ? JSON.parse(init.body as string) : {}
    if (!body.username || !body.password) {
      return json({ code: 'INVALID_CREDENTIALS', message: '账号或口令为空' }, 401)
    }
    const result: LoginResult = { token: 'mock-token-' + Date.now(), operator: body.username }
    return json(result)
  }
  if (p === '/admin/v1/auth/logout' && method === 'POST') {
    // 登出仅记审计，后端返回 204；mock 直接回空体
    return new Response(null, { status: 204 })
  }

  // 实例列表 GET
  if (p === '/admin/v1/instances' && method === 'GET') {
    return handleInstances(p, qs)
  }

  // 分组/Zone 汇总 GET
  if (p === '/admin/v1/zones' && method === 'GET') {
    return handleZones(p, qs)
  }

  // 小区默认入口 GET（FR-48）：供代理服管理页按 BC 所属 group+zone 索引默认入口（FR-52）
  if (p === '/admin/v1/zones/default-entry' && method === 'GET') {
    return json({ items: getMockDefaultEntries(qs) })
  }

  // 可观测看板·当前快照聚合 GET（FR-32 / FR-34）
  if (p === '/admin/v1/metrics/summary' && method === 'GET') {
    return json(getMockMetricsSummary(qs.namespace))
  }

  // 可观测看板·历史趋势 GET（FR-32）
  if (p === '/admin/v1/metrics/trend' && method === 'GET') {
    return json(getMockTrend(qs.namespace, qs.window ?? '1h'))
  }

  // 控制面自身状态 GET（FR-33）：页眉状态条消费
  if (p === '/admin/v1/system/status' && method === 'GET') {
    return json(getMockSystemStatus())
  }

  // 控制面自观测 GET（FR-82）：控制面健康页消费
  if (p === '/admin/v1/system/observability' && method === 'GET') {
    return json(getMockObservability())
  }

  // 集群拓扑 GET（FR-37）：namespace 必填（与后端一致，缺失返 400）
  if (p === '/admin/v1/topology' && method === 'GET') {
    if (!qs.namespace) {
      return json({ code: 'INVALID_PARAMS', message: 'namespace 为必填' }, 400)
    }
    return json(buildMockTopology(qs.namespace))
  }

  // 环境
  if (p === '/admin/v1/namespaces' && method === 'GET') {
    return json({ items: mockNamespaces })
  }

  // 新建环境（FR-7）：编码必填、重复编码返 409（与后端一致）
  if (p === '/admin/v1/namespaces' && method === 'POST') {
    const body = init?.body ? JSON.parse(init.body as string) : {}
    if (!body.code) {
      return json({ code: 'INVALID_PARAMS', message: '环境编码为必填' }, 400)
    }
    if (mockNamespaces.some((n) => n.code === body.code)) {
      return json({ code: 'CONFLICT', message: `环境 ${body.code} 已存在` }, 409)
    }
    const ns = { code: body.code, name: body.name ?? '' }
    mockNamespaces.push(ns)
    return json(ns, 201)
  }

  // 环境改名 / 删除（FR-53）
  const namespaceMatch = p.match(/^\/admin\/v1\/namespaces\/([^/]+)$/)
  if (namespaceMatch && method === 'PUT') {
    const code = decodeURIComponent(namespaceMatch[1])
    const ns = mockNamespaces.find((n) => n.code === code)
    if (!ns) return notFound(`环境 ${code}`)
    const body = init?.body ? JSON.parse(init.body as string) : {}
    // code 不可变，仅改显示名
    ns.name = body.name ?? ns.name
    return json({ code: ns.code, name: ns.name })
  }
  if (namespaceMatch && method === 'DELETE') {
    const code = decodeURIComponent(namespaceMatch[1])
    const idx = mockNamespaces.findIndex((n) => n.code === code)
    if (idx === -1) return notFound(`环境 ${code}`)
    // prod / test 内置环境含在用数据，演示删除守卫（与后端 409 一致）
    if (code === 'prod' || code === 'test') {
      return json({ code: 'CONFLICT', message: `环境 ${code} 下仍有实例 / zone / 配置，禁止删除` }, 409)
    }
    mockNamespaces.splice(idx, 1)
    return new Response(null, { status: 204 })
  }

  // 审计
  if (p === '/admin/v1/audits' && method === 'GET') {
    const result = getMockAudits(qs as unknown as { namespace?: string; operator?: string; action?: string })
    return json(result)
  }

  // 管理面 API 密钥（FR-42）
  if (p === '/admin/v1/api-keys' && method === 'GET') {
    return json({ items: mockApiKeys.map(stripSecret) })
  }
  if (p === '/admin/v1/api-keys' && method === 'POST') {
    const body = init?.body ? JSON.parse(init.body as string) : {}
    const created = newMockApiKey(body.name ?? 'mock-key', body.role ?? 'readonly', body.expiresAt ?? null)
    mockApiKeys.unshift(created)
    return json(created, 201)
  }
  const apiKeyResetMatch = p.match(/^\/admin\/v1\/api-keys\/(\d+)\/reset$/)
  if (apiKeyResetMatch && method === 'POST') {
    const id = Number(apiKeyResetMatch[1])
    const k = mockApiKeys.find((x) => x.id === id)
    if (!k || k.status === 'revoked') return notFound(`密钥 #${id}`)
    k.key = 'bk_' + Math.random().toString(36).slice(2, 14)
    k.keyPrefix = k.key.slice(0, 9)
    k.lastUsedAt = null
    return json(k)
  }
  const apiKeyMatch = p.match(/^\/admin\/v1\/api-keys\/(\d+)$/)
  if (apiKeyMatch && method === 'DELETE') {
    const id = Number(apiKeyMatch[1])
    const k = mockApiKeys.find((x) => x.id === id)
    if (!k || k.status === 'revoked') return notFound(`密钥 #${id}`)
    k.status = 'revoked'
    return json({ ok: true })
  }

  // 文件树托管
  if (p === '/admin/v1/files' && method === 'GET') {
    return json({ items: getMockFileList(qs as unknown as { namespace?: string; group?: string; path?: string }) })
  }

  const fileDetailMatch = p.match(/^\/admin\/v1\/files\/(\d+)$/)
  if (fileDetailMatch && method === 'GET') {
    const id = Number(fileDetailMatch[1])
    const file = getMockFile(id)
    if (!file) return notFound(`文件 #${id}`)
    return json(file)
  }

  const fileRevMatch = p.match(/^\/admin\/v1\/files\/(\d+)\/revisions$/)
  if (fileRevMatch && method === 'GET') {
    const id = Number(fileRevMatch[1])
    return json({ items: getMockFileRevisions(id) })
  }

  // 覆盖集
  if (p === '/admin/v1/override-sets' && method === 'GET') {
    return json({ items: getMockOverrideSetList(qs as unknown as { namespace?: string; group?: string }) })
  }

  const osDetailMatch = p.match(/^\/admin\/v1\/override-sets\/(\d+)$/)
  if (osDetailMatch && method === 'GET') {
    const id = Number(osDetailMatch[1])
    const os = getMockOverrideSet(id)
    if (!os) return notFound(`覆盖集 #${id}`)
    return json(os)
  }

  const osRevMatch = p.match(/^\/admin\/v1\/override-sets\/(\d+)\/revisions$/)
  if (osRevMatch && method === 'GET') {
    return json({ items: getMockOverrideSetRevisions(Number(osRevMatch[1])) })
  }

  const osDryRunMatch = p.match(/^\/admin\/v1\/override-sets\/(\d+)\/dry-run$/)
  if (osDryRunMatch && method === 'GET') {
    return json(getMockDryRun(Number(osDryRunMatch[1])))
  }

  // Zone 指派
  if (p === '/admin/v1/zones/assignments' && method === 'GET') {
    return json({ items: getMockAssignments(qs as unknown as { namespace?: string; group?: string; zone?: string }) })
  }

  // 实例下线
  const instOfflineMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/offline$/)
  if (instOfflineMatch && method === 'POST') {
    return new Response(null, { status: 204 })
  }

  // 反向抓取触发（FR-39）：命令某在线实例读 plugins 回传并 ingest，返回创建的 pending 命令
  const reverseFetchMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/reverse-fetch$/)
  if (reverseFetchMatch && method === 'POST') {
    const serverId = decodeURIComponent(reverseFetchMatch[1])
    const body = init?.body ? JSON.parse(init.body as string) : {}
    if (!body.group) {
      return json({ code: 'INVALID_PARAMS', message: '目标组为必填' }, 400)
    }
    if (body.scope === 'server' && !body.target) {
      return json({ code: 'INVALID_PARAMS', message: 'server 层需指定目标实例' }, 400)
    }
    // namespace 由源实例的 ?namespace= 带上（与真实端点一致），回退到该实例的归属。
    const ns = new URL(path, 'http://localhost').searchParams.get('namespace')
    const inst = mockInstances.find((i) => i.serverId === serverId)
    const command = {
      id: Date.now(),
      namespace: ns ?? inst?.namespace ?? 'prod',
      serverId,
      type: 'ingest-plugins',
      status: 'pending',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    }
    // 真实端点建命令后返回 202 Accepted（命令异步执行），mock 对齐。
    return json(command, 202)
  }

  // 按需拓印触发（FR-46）：命令某在线实例读 plugins 回传、控制面取目标文件转存待审，返回 pending 命令。
  // mock 直接返回 ready 态命令（省去 agent 回传往返），便于审核台在 dev 下走通 diff/confirm。
  const imprintMatch = p.match(/^\/admin\/v1\/instances\/([^/]+)\/imprint$/)
  if (imprintMatch && method === 'POST') {
    const serverId = decodeURIComponent(imprintMatch[1])
    const body = init?.body ? JSON.parse(init.body as string) : {}
    if (!body.path) {
      return json({ code: 'INVALID_PARAM', message: '目标文件 path 为必填' }, 400)
    }
    const ns = new URL(path, 'http://localhost').searchParams.get('namespace')
    const inst = mockInstances.find((i) => i.serverId === serverId)
    return json(
      {
        id: Date.now(),
        namespace: ns ?? inst?.namespace ?? 'prod',
        serverId,
        type: 'ingest-plugins',
        // mock 直接 ready：真实链路为 pending→agent 回传→ready，前端审核台以轮询到 ready 为准
        status: 'ready',
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
      },
      202,
    )
  }

  // 拓印命令状态轮询（FR-46）：mock 直接回 ready（dev 下省去 agent 回传等待）。
  const imprintStatusMatch = p.match(/^\/admin\/v1\/imprints\/(\d+)$/)
  if (imprintStatusMatch && method === 'GET') {
    return json({
      id: Number(imprintStatusMatch[1]),
      namespace: 'prod',
      serverId: 'lobby-1',
      type: 'ingest-plugins',
      status: 'ready',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    })
  }

  // 拓印 diff（FR-46）：返回本地实际值 ⟷ 期望合并值（mock 构造一处差异 + 逐键来源徽标）。
  const imprintDiffMatch = p.match(/^\/admin\/v1\/imprints\/(\d+)\/diff$/)
  if (imprintDiffMatch && method === 'GET') {
    return json({
      path: 'AllinCore/config.yml',
      actualContent: 'enabled: true\nmax-players: 200\nmotd: 本机实际值\n',
      actualMd5: 'aaaaaaaa1111',
      expectedContent: 'enabled: true\nmax-players: 100\nmotd: 期望合并值\n',
      expectedMd5: 'bbbbbbbb2222',
      expectedWholeFile: false,
      expectedSources: [
        { path: ['enabled'], scope: 'global' },
        { path: ['max-players'], scope: 'group' },
        { path: ['motd'], scope: 'zone' },
      ],
      expectedDeletions: [],
      differs: true,
    })
  }

  // 拓印确认落库（FR-46）：mock 简化为成功落 server 层首版。
  const imprintConfirmMatch = p.match(/^\/admin\/v1\/imprints\/(\d+)\/confirm$/)
  if (imprintConfirmMatch && method === 'POST') {
    const body = init?.body ? JSON.parse(init.body as string) : {}
    return json({
      fileId: 1,
      scopeLevel: body.scope ?? 'server',
      group: body.group ?? '',
      target: body.target ?? '',
      version: 1,
      md5: body.reviewedMd5 ?? 'aaaaaaaa1111',
    })
  }

  // 文件发布/回滚/删除（简化返回）
  if (fileDetailMatch && method === 'PUT') {
    return json({ version: 2, md5: 'mock-md5' })
  }
  if (fileDetailMatch && method === 'DELETE') {
    return new Response(null, { status: 204 })
  }

  // 覆盖集发布/回滚/删除（简化返回）
  if (osDetailMatch && method === 'PUT') {
    return json({ version: 2, targetRoot: '/plugins/third-party' })
  }
  if (osDetailMatch && method === 'DELETE') {
    return new Response(null, { status: 204 })
  }
  if (osRevMatch && method === 'POST') {
    return json({ version: 2, targetRoot: '/plugins/third-party' })
  }
  if (fileRevMatch && method === 'POST') {
    return json({ version: 2, md5: 'mock-md5' })
  }

  // 配置列表 GET
  if (p === '/admin/v1/configs' && method === 'GET') {
    let items = getMockConfigList()
    if (qs.namespace) items = items.filter((c) => c.namespace === qs.namespace)
    if (qs.group) items = items.filter((c) => c.group === qs.group)
    if (qs.dataId) items = items.filter((c) => c.dataId === qs.dataId)
    if (qs.scopeLevel) items = items.filter((c) => c.scopeLevel === qs.scopeLevel)
    return json({ items })
  }

  // 配置详情 GET
  const detailMatch = p.match(/^\/admin\/v1\/configs\/(\d+)$/)
  if (detailMatch && method === 'GET') {
    const id = Number(detailMatch[1])
    const detail = getMockConfigDetail(id)
    if (!detail) return notFound(`配置 #${id}`)
    return json(detail)
  }

  // 新建配置 POST
  if (p === '/admin/v1/configs' && method === 'POST') {
    const body = init?.body ? JSON.parse(init.body as string) : {}
    const id = getNextId()
    const content = body.content ?? ''
    const newConfig: ConfigView = {
      id,
      namespace: body.namespace ?? 'prod',
      group: body.group ?? '__GLOBAL__',
      dataId: body.dataId ?? 'new_config.yml',
      scopeLevel: body.scopeLevel ?? 'global',
      scopeTarget: body.scopeTarget ?? '',
      format: body.format ?? 'yaml',
      version: 1,
      md5: 'mock-md5-' + id,
      enabled: true,
      updatedAt: new Date().toISOString(),
      content,
    }
    const newRev: RevisionView = {
      version: 1,
      md5: 'mock-md5-' + id,
      operator: 'admin',
      comment: body.comment ?? '新建',
      sourceRevision: null,
      createdAt: new Date().toISOString(),
      content,
    }
    // 存入 mock 数据（内存级别，仅当前会话有效）
    mockStore.push({ item: newConfig, revisions: [newRev] })
    return json(newConfig, 201)
  }

  // 发布 PUT
  if (detailMatch && method === 'PUT') {
    const id = Number(detailMatch[1])
    const body = init?.body ? JSON.parse(init.body as string) : {}
    const existing = mockStore.find((c) => c.item.id === id)
    if (!existing) return notFound(`配置 #${id}`)
    const newVer = getNextVersion(id)
    const content = body.content ?? existing.item.content ?? ''
    const rev: RevisionView = {
      version: newVer,
      md5: 'mock-md5-v' + newVer,
      operator: 'admin',
      comment: body.comment ?? '',
      sourceRevision: null,
      createdAt: new Date().toISOString(),
      content,
    }
    existing.revisions.push(rev)
    existing.item.version = newVer
    existing.item.content = content
    existing.item.md5 = rev.md5
    existing.item.updatedAt = rev.createdAt
    const result: PublishResult = { version: newVer, md5: rev.md5 }
    return json(result)
  }

  // 软删 DELETE
  if (detailMatch && method === 'DELETE') {
    const id = Number(detailMatch[1])
    const idx = mockStore.findIndex((c) => c.item.id === id)
    if (idx === -1) return notFound(`配置 #${id}`)
    mockStore[idx].item.enabled = false
    return new Response(null, { status: 204 })
  }

  // 版本列表 GET
  const revMatch = p.match(/^\/admin\/v1\/configs\/(\d+)\/revisions$/)
  if (revMatch && method === 'GET') {
    const id = Number(revMatch[1])
    const revisions = getMockRevisions(id)
    if (revisions.length === 0) {
      const existing = mockStore.find((c) => c.item.id === id)
      if (!existing) return notFound(`配置 #${id}`)
    }
    return json({ items: revisions })
  }

  // 单版本 GET
  const revDetailMatch = p.match(/^\/admin\/v1\/configs\/(\d+)\/revisions\/(\d+)$/)
  if (revDetailMatch && method === 'GET') {
    const id = Number(revDetailMatch[1])
    const version = Number(revDetailMatch[2])
    const revisions = getMockRevisions(id)
    const rev = revisions.find((r) => r.version === version)
    if (!rev) return notFound(`版本 v${version}`)
    return json(rev)
  }

  // 回滚 POST
  const rollbackMatch = p.match(/^\/admin\/v1\/configs\/(\d+)\/rollback$/)
  if (rollbackMatch && method === 'POST') {
    const id = Number(rollbackMatch[1])
    const body = init?.body ? JSON.parse(init.body as string) : {}
    const existing = mockStore.find((c) => c.item.id === id)
    if (!existing) return notFound(`配置 #${id}`)
    const targetVersion = body.toVersion ?? 1
    const targetRev = existing.revisions.find((r) => r.version === targetVersion)
    if (!targetRev) return notFound(`版本 v${targetVersion}`)
    const newVer = getNextVersion(id)
    const rev: RevisionView = {
      version: newVer,
      md5: 'mock-md5-v' + newVer,
      operator: 'admin',
      comment: body.comment ?? `回滚到版本 ${targetVersion}`,
      sourceRevision: targetVersion,
      createdAt: new Date().toISOString(),
      content: targetRev.content,
    }
    existing.revisions.push(rev)
    existing.item.version = newVer
    existing.item.content = targetRev.content
    existing.item.md5 = rev.md5
    existing.item.updatedAt = rev.createdAt
    const result: PublishResult = { version: newVer, md5: rev.md5 }
    return json(result)
  }

  // Diff GET
  const diffMatch = p.match(/^\/admin\/v1\/configs\/(\d+)\/diff$/)
  if (diffMatch && method === 'GET') {
    const id = Number(diffMatch[1])
    const from = Number(qs.from ?? '0')
    const to = Number(qs.to ?? '0')
    if (!from || !to) {
      return json({ code: 'INVALID_PARAMS', message: 'from 和 to 均为必填' }, 400)
    }
    const diff = getMockDiff(id, from, to)
    if (!diff) return notFound(`配置 #${id} 的版本对比 v${from} → v${to}`)
    return json(diff)
  }

  // 兜底
  return json({ code: 'NOT_FOUND', message: `未注册的 mock 端点: ${method} ${p}` }, 404)
}

// ---- 内存存储（用于新建/修改/回滚等写操作） ----

import { mockConfigs } from './data'
import type { InstanceView } from '../types'

interface MockStoreEntry {
  item: ConfigView
  revisions: RevisionView[]
}

const mockStore: MockStoreEntry[] = mockConfigs.map((c) => ({ ...c }))

// ---- API 密钥 mock（内存级别，FR-42）----

import type { ApiKeyCreated } from '../types'

let apiKeySeq = 1
const mockApiKeys: ApiKeyCreated[] = [
  {
    id: apiKeySeq++,
    name: '业务管理后端',
    role: 'readonly',
    keyPrefix: 'bk_demo01',
    status: 'active',
    createdAt: new Date(Date.now() - 86400000).toISOString(),
    expiresAt: null,
    lastUsedAt: new Date(Date.now() - 3600000).toISOString(),
    key: '',
  },
]

// 列表 / 元数据剥离明文 key（后端绝不返回明文）
function stripSecret(k: ApiKeyCreated): Omit<ApiKeyCreated, 'key'> {
  const { key: _key, ...rest } = k
  return rest
}

// 生成一把新 mock 密钥（明文仅此返回）
function newMockApiKey(name: string, role: string, expiresAt: string | null): ApiKeyCreated {
  const key = 'bk_' + Math.random().toString(36).slice(2, 14)
  return {
    id: apiKeySeq++,
    name,
    role,
    keyPrefix: key.slice(0, 9),
    status: 'active',
    createdAt: new Date().toISOString(),
    expiresAt,
    lastUsedAt: null,
    key,
  }
}

// ---- 实例/分组路由 ----

function handleInstances(p: string, qs: Record<string, string>): Response {
  if (p === '/admin/v1/instances') {
    let items: InstanceView[] = [...mockInstances]
    if (qs.namespace) items = items.filter((i) => i.namespace === qs.namespace)
    if (qs.group) items = items.filter((i) => i.group === qs.group)
    if (qs.zone) items = items.filter((i) => i.zone === qs.zone)
    if (qs.role) items = items.filter((i) => i.role === qs.role)
    if (qs.status) items = items.filter((i) => i.status === qs.status)
    return json({ items })
  }
  return json({ code: 'NOT_FOUND', message: '未注册的 mock 端点' }, 404)
}

function handleZones(p: string, _qs: Record<string, string>): Response {
  if (p === '/admin/v1/zones') {
    return json({ items: [...mockZoneStats] })
  }
  return json({ code: 'NOT_FOUND', message: '未注册的 mock 端点' }, 404)
}
