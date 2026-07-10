// 区服权威域 mock（/admin/v2/zone-tree、bc-clusters、regions、zones、servers、server-assignments、server-rezones）。
// 契约真源：docs/specs/v2-zone-authority.md §5；分配约束 §4.3、换区工单 §4.7。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  AssignmentResponse,
  AssignmentResult,
  ServerItem,
  ServerListResponse,
  ZoneTreeCluster,
  ZoneTreeResponse,
} from '@beacon/contracts'
import {
  jsonError,
  mockGet,
  mockPost,
  mockPut,
  paginate,
  pathParam,
  queryStr,
  readBody,
} from '../http'
import {
  allocId,
  getClusterState,
  isAssigned,
  namespaceOfZone,
  type ClusterState,
  type ServerRow,
} from '../data/cluster'
import { isoOffset } from '../support'

function zoneName(state: ClusterState, zoneId: number | null): string | null {
  if (zoneId === null) {
    return null
  }
  return state.zones.find((z) => z.id === zoneId)?.name ?? null
}

function toServerItem(state: ClusterState, row: ServerRow): ServerItem {
  const zone = row.zoneId === null ? null : (state.zones.find((z) => z.id === row.zoneId) ?? null)
  const region = zone === null ? null : (state.regions.find((r) => r.id === zone.regionId) ?? null)
  return {
    id: row.id,
    namespaceId: row.namespaceId,
    serverId: row.serverId,
    kind: row.kind,
    bcClusterId: row.bcClusterId,
    bcClusterName:
      row.bcClusterId === null
        ? null
        : (state.bcClusters.find((c) => c.id === row.bcClusterId)?.name ?? null),
    zoneId: row.zoneId,
    zoneName: zone?.name ?? null,
    regionName: region?.name ?? null,
    pendingZoneId: row.pendingZoneId,
    pendingZoneName: zoneName(state, row.pendingZoneId),
    isDefaultEntry: row.isDefaultEntry,
    draining: row.draining,
    online: row.online,
    assigned: isAssigned(row),
    createdAt: row.createdAt,
  }
}

interface CreateClusterBody {
  namespaceId?: number
  name?: string
  description?: string
}

interface CreateRegionBody {
  bcClusterId?: number
  name?: string
  description?: string
}

interface CreateZoneBody {
  regionId?: number
  name?: string
  description?: string
}

interface AssignmentBody {
  serverIds?: number[]
  target?: { kind: 'zone' | 'bc_cluster'; id: number } | null
  isDefaultEntry?: boolean
  reason?: string
}

interface RezoneBody {
  serverIds?: number[]
  target?: { kind: 'zone' | 'bc_cluster'; id: number }
  reason?: string
}

interface DrainingBody {
  draining?: boolean
  reason?: string
}

export const zoneAuthorityHandlers: HttpHandler[] = [
  // 区服结构树：BC 集群 → 大区 → 小区，各节点带服务器计数与默认入口计数
  mockGet('/admin/v2/zone-tree', ({ request }) => {
    const url = new URL(request.url)
    const state = getClusterState()
    const namespaceId = Number.parseInt(queryStr(url, 'namespaceId') ?? '0', 10)
    const clusters: ZoneTreeCluster[] = state.bcClusters
      .filter((c) => namespaceId === 0 || c.namespaceId === namespaceId)
      .map((cluster) => ({
        id: cluster.id,
        name: cluster.name,
        description: cluster.description,
        proxyCount: state.servers.filter((s) => s.kind === 'proxy' && s.bcClusterId === cluster.id).length,
        regions: state.regions
          .filter((r) => r.bcClusterId === cluster.id)
          .map((region) => ({
            id: region.id,
            name: region.name,
            description: region.description,
            zones: state.zones
              .filter((z) => z.regionId === region.id)
              .map((zone) => ({
                id: zone.id,
                name: zone.name,
                description: zone.description,
                serverCount: state.servers.filter((s) => s.zoneId === zone.id).length,
                defaultEntryCount: state.servers.filter((s) => s.zoneId === zone.id && s.isDefaultEntry).length,
              })),
          })),
      }))
    const unassignedCount = state.servers.filter(
      (s) => (namespaceId === 0 || s.namespaceId === namespaceId) && !isAssigned(s),
    ).length
    return HttpResponse.json({ namespaceId, clusters, unassignedCount } satisfies ZoneTreeResponse)
  }),

  // 新建 BC 集群（namespace 内重名 409）
  mockPost('/admin/v2/bc-clusters', async ({ request }) => {
    const body = await readBody<CreateClusterBody>(request)
    if (typeof body.namespaceId !== 'number' || !body.name) {
      return jsonError(400, 'invalid_param', 'namespaceId / name 必填')
    }
    const state = getClusterState()
    if (!state.namespaces.some((ns) => ns.id === body.namespaceId)) {
      return jsonError(404, 'namespace_not_found', 'namespace 不存在')
    }
    if (state.bcClusters.some((c) => c.namespaceId === body.namespaceId && c.name === body.name)) {
      return jsonError(409, 'name_duplicate', `集群 ${body.name} 在该 namespace 内已存在`)
    }
    const row = {
      id: allocId(state),
      namespaceId: body.namespaceId,
      name: body.name,
      description: body.description ?? '',
      createdAt: isoOffset(0),
    }
    state.bcClusters.push(row)
    return HttpResponse.json(row, { status: 201 })
  }),

  // 新建大区（父集群必须存在，重名 409）
  mockPost('/admin/v2/regions', async ({ request }) => {
    const body = await readBody<CreateRegionBody>(request)
    if (typeof body.bcClusterId !== 'number' || !body.name) {
      return jsonError(400, 'invalid_param', 'bcClusterId / name 必填')
    }
    const state = getClusterState()
    if (!state.bcClusters.some((c) => c.id === body.bcClusterId)) {
      return jsonError(404, 'bc_cluster_not_found', 'BC 集群不存在')
    }
    if (state.regions.some((r) => r.bcClusterId === body.bcClusterId && r.name === body.name)) {
      return jsonError(409, 'name_duplicate', `大区 ${body.name} 在该集群内已存在`)
    }
    const row = {
      id: allocId(state),
      bcClusterId: body.bcClusterId,
      name: body.name,
      description: body.description ?? '',
      createdAt: isoOffset(0),
    }
    state.regions.push(row)
    return HttpResponse.json(row, { status: 201 })
  }),

  // 新建小区（父大区必须存在；zone 名在 namespace 内唯一）
  mockPost('/admin/v2/zones', async ({ request }) => {
    const body = await readBody<CreateZoneBody>(request)
    if (typeof body.regionId !== 'number' || !body.name) {
      return jsonError(400, 'invalid_param', 'regionId / name 必填')
    }
    const state = getClusterState()
    const region = state.regions.find((r) => r.id === body.regionId)
    if (!region) {
      return jsonError(404, 'region_not_found', '大区不存在')
    }
    const cluster = state.bcClusters.find((c) => c.id === region.bcClusterId)
    const namespaceId = cluster?.namespaceId ?? 0
    const duplicated = state.zones.some(
      (z) => z.name === body.name && namespaceOfZone(state, z.id) === namespaceId,
    )
    if (duplicated) {
      return jsonError(409, 'name_duplicate', `小区名 ${body.name} 在该 namespace 内已存在`)
    }
    const row = {
      id: allocId(state),
      regionId: body.regionId,
      name: body.name,
      description: body.description ?? '',
      createdAt: isoOffset(0),
    }
    state.zones.push(row)
    return HttpResponse.json(row, { status: 201 })
  }),

  // server 分页列表：namespaceId / kind / assigned / zoneId / bcClusterId / keyword 组合筛选
  mockGet('/admin/v2/servers', ({ request }) => {
    const url = new URL(request.url)
    const state = getClusterState()
    const namespaceId = queryStr(url, 'namespaceId')
    const kind = queryStr(url, 'kind')
    const assigned = queryStr(url, 'assigned')
    const zoneId = queryStr(url, 'zoneId')
    const bcClusterId = queryStr(url, 'bcClusterId')
    const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
    const rows = state.servers
      .filter((row) => {
        if (namespaceId !== null && String(row.namespaceId) !== namespaceId) {
          return false
        }
        if (kind !== null && row.kind !== kind) {
          return false
        }
        if (assigned !== null && String(isAssigned(row)) !== assigned) {
          return false
        }
        if (zoneId !== null && String(row.zoneId ?? '') !== zoneId) {
          return false
        }
        if (bcClusterId !== null && String(row.bcClusterId ?? '') !== bcClusterId) {
          return false
        }
        if (keyword !== null && !row.serverId.toLowerCase().includes(keyword)) {
          return false
        }
        return true
      })
      .map((row) => toServerItem(state, row))
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies ServerListResponse)
  }),

  // 批量首次分配 / 解除分配：仅未分配 server 可首次分配；已分配传目标 → 409 rezone_required
  mockPost('/admin/v2/server-assignments', async ({ request }) => {
    const body = await readBody<AssignmentBody>(request)
    if (!Array.isArray(body.serverIds) || body.serverIds.length === 0) {
      return jsonError(400, 'invalid_param', 'serverIds 必填')
    }
    if (body.target === undefined) {
      return jsonError(400, 'invalid_param', 'target 必填（解除分配传 null）')
    }
    const state = getClusterState()
    const rows: ServerRow[] = []
    for (const id of body.serverIds) {
      const row = state.servers.find((s) => s.id === id)
      if (!row) {
        return jsonError(404, 'server_not_found', `server ${String(id)} 不存在`)
      }
      rows.push(row)
    }
    const kinds = new Set(rows.map((r) => r.kind))
    const namespaces = new Set(rows.map((r) => r.namespaceId))
    if (kinds.size > 1 || namespaces.size > 1) {
      return jsonError(400, 'mixed_selection', '批量分配必须同 namespace、同 kind')
    }
    const target = body.target
    if (target === null) {
      // 解除分配：原因必填，归属清空并自动清默认入口
      if (!body.reason) {
        return jsonError(400, 'missing_reason', '解除分配原因必填')
      }
      const results: AssignmentResult[] = rows.map((row) => {
        row.zoneId = null
        row.bcClusterId = null
        row.isDefaultEntry = false
        return { id: row.id, serverId: row.serverId, ok: true }
      })
      return HttpResponse.json({ results } satisfies AssignmentResponse)
    }
    // 校验目标存在与 namespace 归属链一致
    const targetNamespace =
      target.kind === 'zone'
        ? namespaceOfZone(state, target.id)
        : (state.bcClusters.find((c) => c.id === target.id)?.namespaceId ?? null)
    if (targetNamespace === null) {
      return jsonError(404, 'target_not_found', '分配目标不存在')
    }
    const failed: AssignmentResult[] = []
    for (const row of rows) {
      if (isAssigned(row)) {
        failed.push({ id: row.id, serverId: row.serverId, ok: false, code: 'rezone_required' })
      } else if (row.namespaceId !== targetNamespace) {
        failed.push({ id: row.id, serverId: row.serverId, ok: false, code: 'namespace_mismatch' })
      } else if ((target.kind === 'zone') !== (row.kind === 'backend')) {
        failed.push({ id: row.id, serverId: row.serverId, ok: false, code: 'kind_mismatch' })
      }
    }
    if (failed.length > 0) {
      // 整批事务原子：任一失败整批回滚并逐台列原因
      return HttpResponse.json(
        { code: 'assignment_rejected', message: '存在不满足首次分配条件的 server，整批回滚', traceId: 'trace-devmock', results: failed },
        { status: 409 },
      )
    }
    const results: AssignmentResult[] = rows.map((row) => {
      if (target.kind === 'zone') {
        row.zoneId = target.id
        row.isDefaultEntry = body.isDefaultEntry === true
      } else {
        row.bcClusterId = target.id
      }
      row.pendingZoneId = null
      row.pendingBcClusterId = null
      return { id: row.id, serverId: row.serverId, ok: true }
    })
    return HttpResponse.json({ results } satisfies AssignmentResponse)
  }),

  // 批量发起换区工单：解绑 + 清归属 + 记预填目标（身份重入 pending）
  mockPost('/admin/v2/server-rezones', async ({ request }) => {
    const body = await readBody<RezoneBody>(request)
    if (!Array.isArray(body.serverIds) || body.serverIds.length === 0 || body.target === undefined) {
      return jsonError(400, 'invalid_param', 'serverIds / target 必填')
    }
    if (!body.reason) {
      return jsonError(400, 'missing_reason', '换区原因必填')
    }
    const state = getClusterState()
    const target = body.target
    const targetZone = target.kind === 'zone' ? state.zones.find((z) => z.id === target.id) : undefined
    const targetCluster =
      target.kind === 'bc_cluster' ? state.bcClusters.find((c) => c.id === target.id) : undefined
    if (!targetZone && !targetCluster) {
      return jsonError(404, 'target_not_found', '换区目标不存在')
    }
    const results: AssignmentResult[] = []
    for (const id of body.serverIds) {
      const row = state.servers.find((s) => s.id === id)
      if (!row) {
        return jsonError(404, 'server_not_found', `server ${String(id)} 不存在`)
      }
      if (!isAssigned(row)) {
        return jsonError(400, 'not_assigned', `server ${row.serverId} 未分配，应走首次分配`)
      }
      // 解除归属 + 记预填目标
      row.zoneId = null
      row.bcClusterId = null
      row.isDefaultEntry = false
      if (target.kind === 'zone') {
        row.pendingZoneId = target.id
      } else {
        row.pendingBcClusterId = target.id
      }
      // 身份走 T10 解绑 → T11 重入 pending（mock 直接落 pending + 预填）
      const identity = state.identities.find(
        (row2) => row2.namespaceId === row.namespaceId && row2.serverId === row.serverId,
      )
      if (identity) {
        identity.status = 'pending'
        identity.statusChangedAt = isoOffset(0)
        identity.pendingExpiresAt = isoOffset(72 * 3_600_000)
        identity.rezonePrefill = {
          targetKind: target.kind,
          targetId: target.id,
          targetName: targetZone?.name ?? targetCluster?.name ?? '',
        }
      }
      results.push({ id: row.id, serverId: row.serverId, ok: true })
    }
    return HttpResponse.json({ results } satisfies AssignmentResponse)
  }),

  // 更新默认入口标记（未分配 → 409）
  mockPut('/admin/v2/servers/:id/default-entry', async (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const state = getClusterState()
    const row = state.servers.find((s) => s.id === id)
    if (!row) {
      return jsonError(404, 'server_not_found', 'server 不存在')
    }
    if (row.zoneId === null) {
      return jsonError(409, 'not_assigned', '未分配小区的 server 不能设为默认入口')
    }
    const body = await readBody<{ value?: boolean }>(info.request)
    row.isDefaultEntry = body.value === true
    return HttpResponse.json(toServerItem(state, row))
  }),

  // 切换排空标记（消费方为调度 schedulable 判定）
  mockPut('/admin/v2/servers/:serverId/draining', async (info) => {
    const serverId = pathParam(info, 'serverId')
    const state = getClusterState()
    const row = state.servers.find((s) => s.serverId === serverId)
    if (!row) {
      return jsonError(404, 'server_not_found', 'server 不存在')
    }
    const body = await readBody<DrainingBody>(info.request)
    if (typeof body.draining !== 'boolean') {
      return jsonError(400, 'invalid_param', 'draining 必填')
    }
    row.draining = body.draining
    return HttpResponse.json(toServerItem(state, row))
  }),
]
