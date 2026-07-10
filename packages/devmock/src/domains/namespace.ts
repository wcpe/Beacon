// namespace 隔离域 mock（/admin/v2/namespaces、/admin/v2/namespace-trusts*）。
// 契约真源：docs/specs/v2-namespace-isolation.md §5（namespace 增查、信任增查/收回）。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  NamespaceCreated,
  NamespaceItem,
  NamespaceListResponse,
  NamespaceTrustItem,
  NamespaceTrustListResponse,
  TrustCapability,
} from '@beacon/contracts'
import { jsonError, mockGet, mockPost, paginate, pathParam, queryStr, readBody } from '../http'
import { getClusterState, isAssigned, type TrustRow } from '../data/cluster'
import { isoOffset, pseudoSha256 } from '../support'

const TRUST_CAPABILITIES: readonly TrustCapability[] = ['schedule', 'message', 'agent_ops']

function namespaceName(id: number): string {
  return getClusterState().namespaces.find((ns) => ns.id === id)?.name ?? `ns-${String(id)}`
}

function toTrustItem(row: TrustRow): NamespaceTrustItem {
  return {
    id: row.id,
    fromNamespaceId: row.fromNamespaceId,
    toNamespaceId: row.toNamespaceId,
    fromNamespaceName: namespaceName(row.fromNamespaceId),
    toNamespaceName: namespaceName(row.toNamespaceId),
    capability: row.capability,
    status: row.status,
    note: row.note,
    grantedBy: row.grantedBy,
    grantedAt: row.grantedAt,
    revokedBy: row.revokedBy,
    revokedAt: row.revokedAt,
    revokeReason: row.revokeReason,
  }
}

interface CreateNamespaceBody {
  name?: string
  description?: string
}

interface CreateTrustBody {
  fromNamespaceId?: number
  toNamespaceId?: number
  capability?: TrustCapability
  note?: string
}

interface RevokeBody {
  reason?: string
}

export const namespaceHandlers: HttpHandler[] = [
  // namespace 列表（keyword 搜索 + 分页 + 统计摘要）
  mockGet('/admin/v2/namespaces', ({ request }) => {
    const url = new URL(request.url)
    const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
    const state = getClusterState()
    const rows: NamespaceItem[] = state.namespaces
      .filter((ns) => keyword === null || ns.name.toLowerCase().includes(keyword))
      .map((ns) => ({
        id: ns.id,
        name: ns.name,
        description: ns.description,
        serverCount: state.servers.filter((s) => s.namespaceId === ns.id && isAssigned(s)).length,
        bcClusterCount: state.bcClusters.filter((c) => c.namespaceId === ns.id).length,
        activeTrustCount: state.trusts.filter(
          (t) => t.status === 'active' && (t.fromNamespaceId === ns.id || t.toNamespaceId === ns.id),
        ).length,
        createdAt: ns.createdAt,
      }))
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies NamespaceListResponse)
  }),

  // 创建 namespace：name 全局唯一，返回一次性明文 token
  mockPost('/admin/v2/namespaces', async ({ request }) => {
    const body = await readBody<CreateNamespaceBody>(request)
    if (!body.name) {
      return jsonError(400, 'invalid_param', 'name 必填')
    }
    const state = getClusterState()
    if (state.namespaces.some((ns) => ns.name === body.name)) {
      return jsonError(409, 'namespace_duplicate', `namespace ${body.name} 已存在`)
    }
    const id = state.namespaces.reduce((max, ns) => Math.max(max, ns.id), 0) + 1
    const row = {
      id,
      name: body.name,
      description: body.description ?? '',
      createdAt: isoOffset(0),
    }
    state.namespaces.push(row)
    const created: NamespaceCreated = {
      ...row,
      serverCount: 0,
      bcClusterCount: 0,
      activeTrustCount: 0,
      accessToken: `nstk_${pseudoSha256(`token:${body.name}`).slice(0, 40)}`,
    }
    return HttpResponse.json(created, { status: 201 })
  }),

  // 信任行列表（方向 / capability / status 过滤）
  mockGet('/admin/v2/namespace-trusts', ({ request }) => {
    const url = new URL(request.url)
    const fromNs = queryStr(url, 'fromNamespaceId')
    const toNs = queryStr(url, 'toNamespaceId')
    const capability = queryStr(url, 'capability')
    const status = queryStr(url, 'status')
    const rows = getClusterState()
      .trusts.filter((t) => {
        if (fromNs !== null && String(t.fromNamespaceId) !== fromNs) {
          return false
        }
        if (toNs !== null && String(t.toNamespaceId) !== toNs) {
          return false
        }
        if (capability !== null && t.capability !== capability) {
          return false
        }
        if (status !== null && t.status !== status) {
          return false
        }
        return true
      })
      .map(toTrustItem)
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies NamespaceTrustListResponse)
  }),

  // 授予单向信任（原因必填；同三元组已 active → 409；revoked 行复活）
  mockPost('/admin/v2/namespace-trusts', async ({ request }) => {
    const body = await readBody<CreateTrustBody>(request)
    if (
      typeof body.fromNamespaceId !== 'number' ||
      typeof body.toNamespaceId !== 'number' ||
      body.capability === undefined ||
      !TRUST_CAPABILITIES.includes(body.capability)
    ) {
      return jsonError(400, 'invalid_param', 'fromNamespaceId / toNamespaceId / capability 非法')
    }
    if (!body.note) {
      return jsonError(400, 'missing_reason', '建立原因（note）必填')
    }
    if (body.fromNamespaceId === body.toNamespaceId) {
      return jsonError(400, 'invalid_param', '不允许对自身建立信任')
    }
    const state = getClusterState()
    const nsExists = (id: number) => state.namespaces.some((ns) => ns.id === id)
    if (!nsExists(body.fromNamespaceId) || !nsExists(body.toNamespaceId)) {
      return jsonError(404, 'namespace_not_found', '指定 namespace 不存在')
    }
    const existing = state.trusts.find(
      (t) =>
        t.fromNamespaceId === body.fromNamespaceId &&
        t.toNamespaceId === body.toNamespaceId &&
        t.capability === body.capability,
    )
    if (existing?.status === 'active') {
      return jsonError(409, 'trust_duplicate', '同一三元组已存在生效信任')
    }
    if (existing) {
      // 重新授予：复用同一行置回 active
      existing.status = 'active'
      existing.note = body.note
      existing.grantedBy = 'admin'
      existing.grantedAt = isoOffset(0)
      existing.revokedBy = null
      existing.revokedAt = null
      existing.revokeReason = null
      return HttpResponse.json(toTrustItem(existing), { status: 201 })
    }
    const row: TrustRow = {
      id: state.trusts.reduce((max, t) => Math.max(max, t.id), 0) + 1,
      fromNamespaceId: body.fromNamespaceId,
      toNamespaceId: body.toNamespaceId,
      capability: body.capability,
      status: 'active',
      note: body.note,
      grantedBy: 'admin',
      grantedAt: isoOffset(0),
      revokedBy: null,
      revokedAt: null,
      revokeReason: null,
      createdAt: isoOffset(0),
    }
    state.trusts.push(row)
    return HttpResponse.json(toTrustItem(row), { status: 201 })
  }),

  // 收回信任（原因必填，即时生效）
  mockPost('/admin/v2/namespace-trusts/:id/revoke', async (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const row = getClusterState().trusts.find((t) => t.id === id)
    if (!row) {
      return jsonError(404, 'trust_not_found', '信任行不存在')
    }
    if (row.status !== 'active') {
      return jsonError(409, 'illegal_state', '该信任行已收回')
    }
    const body = await readBody<RevokeBody>(info.request)
    if (!body.reason) {
      return jsonError(400, 'missing_reason', '收回原因必填')
    }
    row.status = 'revoked'
    row.revokedBy = 'admin'
    row.revokedAt = isoOffset(0)
    row.revokeReason = body.reason
    return HttpResponse.json(toTrustItem(row))
  }),
]
