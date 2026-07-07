// Agent 身份域 mock（/admin/v2/agent-identities*）。
// 契约真源：docs/specs/v2-agent-identity.md §5.2；状态机 §4.3（非法迁移一律 409 illegal_state）。

import { HttpResponse, type HttpHandler } from 'msw'
import {
  jsonError,
  mockGet,
  mockPost,
  paginate,
  pathParam,
  queryStr,
  readBody,
  type Paged,
} from '../http'
import {
  allocId,
  getClusterState,
  type ConflictPeer,
  type IdentityRow,
  type RezonePrefill,
  type ServerKind,
} from '../data/cluster'
import { isoOffset, uuidFrom } from '../support'

/** 身份列表项（GET /admin/v2/agent-identities 的 items 元素） */
export interface AgentIdentityItem {
  identityId: string
  namespaceId: number
  serverId: string
  kind: ServerKind
  status: string
  bootId: string | null
  lastAddr: string | null
  agentVersion: string | null
  pendingExpiresAt: string | null
  boundAt: string | null
  statusChangedAt: string
  conflictReason: string | null
}

/** 身份详情（额外携带冲突双方与换区预填目标） */
export interface AgentIdentityDetail extends AgentIdentityItem {
  conflictPeers: ConflictPeer[] | null
  rezonePrefill: RezonePrefill | null
}

export type AgentIdentityListResponse = Paged<AgentIdentityItem>

function toItem(row: IdentityRow): AgentIdentityItem {
  return {
    identityId: row.identityId,
    namespaceId: row.namespaceId,
    serverId: row.serverId,
    kind: row.kind,
    status: row.status,
    bootId: row.bootId,
    lastAddr: row.lastAddr,
    agentVersion: row.agentVersion,
    pendingExpiresAt: row.pendingExpiresAt,
    boundAt: row.boundAt,
    statusChangedAt: row.statusChangedAt,
    conflictReason: row.conflictReason,
  }
}

function toDetail(row: IdentityRow): AgentIdentityDetail {
  return { ...toItem(row), conflictPeers: row.conflictPeers, rezonePrefill: row.rezonePrefill }
}

function findIdentity(identityId: string): IdentityRow | undefined {
  return getClusterState().identities.find((row) => row.identityId === identityId)
}

function notFound(): Response {
  return jsonError(404, 'identity_not_found', '身份不存在')
}

function illegalState(current: string): Response {
  return jsonError(409, 'illegal_state', `当前状态 ${current} 不允许该操作`)
}

function missingReason(): Response {
  return jsonError(400, 'missing_reason', '原因必填')
}

interface ApproveBody {
  forceUnbindOccupier?: boolean
  target?: { kind: 'zone' | 'bc_cluster'; id: number } | null
}

interface ReasonBody {
  reason?: string
}

interface ResolveConflictBody {
  keepBootId?: string
  reason?: string
}

export const identityHandlers: HttpHandler[] = [
  // 身份分页列表：status / namespaceId / keyword 筛选
  mockGet('/admin/v2/agent-identities', ({ request }) => {
    const url = new URL(request.url)
    const status = queryStr(url, 'status')
    const namespaceId = queryStr(url, 'namespaceId')
    const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
    const filtered = getClusterState()
      .identities.filter((row) => {
        if (status !== null && row.status !== status) {
          return false
        }
        if (namespaceId !== null && String(row.namespaceId) !== namespaceId) {
          return false
        }
        if (
          keyword !== null &&
          !row.serverId.toLowerCase().includes(keyword) &&
          !row.identityId.toLowerCase().includes(keyword)
        ) {
          return false
        }
        return true
      })
      .map(toItem)
    const { items, total } = paginate(filtered, url)
    return HttpResponse.json({ items, total } satisfies AgentIdentityListResponse)
  }),

  // 单条详情（conflict 时附冲突双方明细）
  mockGet('/admin/v2/agent-identities/:identityId', (info) => {
    const row = findIdentity(pathParam(info, 'identityId'))
    if (!row) {
      return notFound()
    }
    return HttpResponse.json(toDetail(row))
  }),

  // 确认接入：Q3 占用冲突须显式强制解绑；换区重确认按预填/传入目标落区
  mockPost('/admin/v2/agent-identities/:identityId/approve', async (info) => {
    const row = findIdentity(pathParam(info, 'identityId'))
    if (!row) {
      return notFound()
    }
    if (row.status !== 'pending') {
      return illegalState(row.status)
    }
    const body = await readBody<ApproveBody>(info.request)
    if (body.target !== undefined && body.target !== null && row.rezonePrefill === null) {
      return jsonError(400, 'target_not_allowed', '仅换区重确认允许指定落区目标')
    }
    const state = getClusterState()
    if (row.conflictReason === 'server-id-occupied') {
      if (body.forceUnbindOccupier !== true) {
        return jsonError(409, 'server_id_occupied', `serverId ${row.serverId} 已被其他身份绑定，须显式勾选强制解绑`)
      }
      const occupier = state.identities.find(
        (other) =>
          other.identityId !== row.identityId &&
          other.namespaceId === row.namespaceId &&
          other.serverId === row.serverId &&
          (other.status === 'active' || other.status === 'disabled'),
      )
      if (occupier) {
        occupier.status = 'unbound'
        occupier.statusChangedAt = isoOffset(0)
      }
    }
    // 确认落绑定：server 行不存在则创建（未分配）
    let server = state.servers.find(
      (candidate) => candidate.namespaceId === row.namespaceId && candidate.serverId === row.serverId,
    )
    if (!server) {
      server = {
        id: allocId(state),
        namespaceId: row.namespaceId,
        serverId: row.serverId,
        kind: row.kind,
        bcClusterId: null,
        zoneId: null,
        pendingZoneId: null,
        pendingBcClusterId: null,
        isDefaultEntry: false,
        draining: false,
        online: true,
        createdAt: isoOffset(0),
      }
      state.servers.push(server)
    }
    // 换区重确认：缺省取预填目标，显式 target:null 表示暂不分配
    if (row.rezonePrefill !== null) {
      const target = body.target === undefined ? row.rezonePrefill : body.target
      if (target !== null) {
        const targetKind = 'targetKind' in target ? target.targetKind : target.kind
        const targetId = 'targetId' in target ? target.targetId : target.id
        if (targetKind === 'zone') {
          server.zoneId = targetId
        } else {
          server.bcClusterId = targetId
        }
      }
      server.pendingZoneId = null
      server.pendingBcClusterId = null
      row.rezonePrefill = null
    }
    row.status = 'active'
    row.boundAt = isoOffset(0)
    row.statusChangedAt = isoOffset(0)
    row.pendingExpiresAt = null
    row.conflictReason = null
    return HttpResponse.json(toDetail(row))
  }),

  // 拒绝接入（原因必填）
  mockPost('/admin/v2/agent-identities/:identityId/reject', async (info) => {
    const row = findIdentity(pathParam(info, 'identityId'))
    if (!row) {
      return notFound()
    }
    if (row.status !== 'pending') {
      return illegalState(row.status)
    }
    const body = await readBody<ReasonBody>(info.request)
    if (!body.reason) {
      return missingReason()
    }
    row.status = 'rejected'
    row.statusChangedAt = isoOffset(0)
    row.pendingExpiresAt = null
    return HttpResponse.json(toDetail(row))
  }),

  // 允许被拒身份重新申请（转 expired，agent 重注册回 pending）
  mockPost('/admin/v2/agent-identities/:identityId/allow-reapply', async (info) => {
    const row = findIdentity(pathParam(info, 'identityId'))
    if (!row) {
      return notFound()
    }
    if (row.status !== 'rejected') {
      return illegalState(row.status)
    }
    const body = await readBody<ReasonBody>(info.request)
    if (!body.reason) {
      return missingReason()
    }
    row.status = 'expired'
    row.statusChangedAt = isoOffset(0)
    return HttpResponse.json(toDetail(row))
  }),

  // 禁用（摘除调度与指令下发，原因必填）
  mockPost('/admin/v2/agent-identities/:identityId/disable', async (info) => {
    const row = findIdentity(pathParam(info, 'identityId'))
    if (!row) {
      return notFound()
    }
    if (row.status !== 'active') {
      return illegalState(row.status)
    }
    const body = await readBody<ReasonBody>(info.request)
    if (!body.reason) {
      return missingReason()
    }
    row.status = 'disabled'
    row.statusChangedAt = isoOffset(0)
    return HttpResponse.json(toDetail(row))
  }),

  // 恢复禁用身份
  mockPost('/admin/v2/agent-identities/:identityId/enable', (info) => {
    const row = findIdentity(pathParam(info, 'identityId'))
    if (!row) {
      return notFound()
    }
    if (row.status !== 'disabled') {
      return illegalState(row.status)
    }
    row.status = 'active'
    row.statusChangedAt = isoOffset(0)
    return HttpResponse.json(toDetail(row))
  }),

  // 解绑（换 serverId / namespace 的前置，原因必填）
  mockPost('/admin/v2/agent-identities/:identityId/unbind', async (info) => {
    const row = findIdentity(pathParam(info, 'identityId'))
    if (!row) {
      return notFound()
    }
    if (row.status !== 'active' && row.status !== 'disabled' && row.status !== 'conflict') {
      return illegalState(row.status)
    }
    const body = await readBody<ReasonBody>(info.request)
    if (!body.reason) {
      return missingReason()
    }
    row.status = 'unbound'
    row.statusChangedAt = isoOffset(0)
    row.conflictReason = null
    row.conflictPeers = null
    return HttpResponse.json(toDetail(row))
  }),

  // 冲突处置：保留指定实例
  mockPost('/admin/v2/agent-identities/:identityId/resolve-conflict', async (info) => {
    const row = findIdentity(pathParam(info, 'identityId'))
    if (!row) {
      return notFound()
    }
    if (row.status !== 'conflict') {
      return illegalState(row.status)
    }
    const body = await readBody<ResolveConflictBody>(info.request)
    if (!body.reason) {
      return missingReason()
    }
    const keep = row.conflictPeers?.find((peer) => peer.bootId === body.keepBootId)
    if (!keep) {
      return jsonError(400, 'boot_id_not_in_conflict', 'keepBootId 不在冲突双方之内')
    }
    row.status = 'active'
    row.bootId = keep.bootId
    row.lastAddr = keep.lastAddr
    row.statusChangedAt = isoOffset(0)
    row.conflictReason = null
    row.conflictPeers = null
    return HttpResponse.json(toDetail(row))
  }),
]

/** 供测试构造新身份时复用（保持 identityId 生成一致） */
export function mockIdentityId(namespaceId: number, serverId: string): string {
  return uuidFrom(`identity:${String(namespaceId)}:${serverId}`)
}
