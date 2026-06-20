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

  // 环境
  if (p === '/admin/v1/namespaces' && method === 'GET') {
    return json({ items: mockNamespaces })
  }

  // 审计
  if (p === '/admin/v1/audits' && method === 'GET') {
    const result = getMockAudits(qs as unknown as { namespace?: string; operator?: string; action?: string })
    return json(result)
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

// ---- 实例/分组路由 ----

function handleInstances(p: string, qs: Record<string, string>): Response {
  if (p === '/admin/v1/instances') {
    let items: InstanceView[] = [...mockInstances]
    if (qs.namespace) items = items.filter((i) => i.namespace === qs.namespace)
    if (qs.group) items = items.filter((i) => i.group === qs.group)
    if (qs.zone) items = items.filter((i) => i.zone === qs.zone)
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
