// 大厅集群 mock：独立于 zone-tree，按摘要→详情→统一归属迁移契约提供端点。
import { HttpResponse, type HttpHandler } from 'msw'
import type {
  HealthLevel,
  LobbyClusterDetail,
  LobbyClusterListResponse,
  LobbyClusterSummary,
  LobbyMember,
  ServerPlacementTarget,
  ServerPlacementTransferBody,
  ServerPlacementTransferResponse,
} from '@beacon/contracts'

import { jsonError, mockGet, mockPost, pathParam, queryStr, readBody } from '../http'
import { getClusterState, namespaceOfZone, type LobbyClusterRow, type ServerRow } from '../data/cluster'

function playerCountOf(serverId: string): number {
  return serverId === 'lobby-1' ? 32 : 0
}

function lobbyOfServer(row: ServerRow): number | null {
  return row.lobbyClusterNamespaceId ?? null
}

function toMember(row: ServerRow): LobbyMember {
  const playerCount = playerCountOf(row.serverId)
  const schedulable = row.online && !row.draining
  const level: HealthLevel = schedulable ? 'healthy' : row.online ? 'degraded' : 'unhealthy'
  return {
    serverId: row.serverId,
    online: row.online,
    playerCount,
    score: schedulable ? 96 : row.online ? 72 : 0,
    level,
    schedulable,
    reasons: schedulable ? [] : row.draining ? ['draining'] : ['lost'],
    onlineCount: playerCount,
    maxOnline: 100,
    draining: row.draining,
  }
}

function summaryOf(lobby: LobbyClusterRow): LobbyClusterSummary {
  const state = getClusterState()
  const namespace = state.namespaces.find((row) => row.id === lobby.namespaceId)
  const members = state.servers.filter((row) => lobbyOfServer(row) === lobby.namespaceId).map(toMember)
  const schedulableCount = members.filter((member) => member.schedulable).length
  return {
    id: lobby.id,
    namespaceId: lobby.namespaceId,
    namespaceName: namespace?.name ?? '',
    memberCount: members.length,
    schedulableCount,
    ready: schedulableCount > 0,
  }
}

function toDetail(lobby: LobbyClusterRow): LobbyClusterDetail {
  const state = getClusterState()
  const members = state.servers
    .filter((row) => lobbyOfServer(row) === lobby.namespaceId)
    .map(toMember)
  return { ...summaryOf(lobby), members, memberTotal: members.length }
}

function targetIsValid(target: ServerPlacementTarget, namespaceId: number): boolean {
  const state = getClusterState()
  if (target === null) {
    return true
  }
  if (target.kind === 'lobby_cluster') {
    return state.lobbyClusters.some((lobby) => lobby.id === target.id && lobby.namespaceId === namespaceId)
  }
  return namespaceOfZone(state, target.id) === namespaceId
}

function applyTarget(row: ServerRow, target: ServerPlacementTarget): void {
  row.zoneId = null
  row.lobbyClusterNamespaceId = null
  row.isDefaultEntry = false
  if (target?.kind === 'lobby_cluster') {
    const lobby = getClusterState().lobbyClusters.find((item) => item.id === target.id)
    row.lobbyClusterNamespaceId = lobby?.namespaceId ?? null
  }
  if (target?.kind === 'zone') {
    row.zoneId = target.id
  }
}

function placementResponse(row: ServerRow): ServerPlacementTransferResponse {
  const state = getClusterState()
  const lobby = state.lobbyClusters.find((item) => item.namespaceId === lobbyOfServer(row))
  return {
    serverId: row.serverId,
    namespaceId: row.namespaceId,
    placementKind: lobby ? 'lobby_cluster' : row.zoneId === null ? '' : 'zone',
    lobbyClusterId: lobby?.id ?? null,
    zoneId: row.zoneId,
    isDefaultEntry: row.isDefaultEntry,
    draining: row.draining,
  }
}

export const lobbyClusterHandlers: HttpHandler[] = [
  mockGet('/admin/v2/lobby-clusters', ({ request }) => {
    const namespaceId = Number.parseInt(queryStr(new URL(request.url), 'namespaceId') ?? '0', 10)
    const state = getClusterState()
    const items = state.lobbyClusters
      .filter((lobby) => namespaceId === 0 || lobby.namespaceId === namespaceId)
      .map(summaryOf)
    return HttpResponse.json({ items, total: items.length } satisfies LobbyClusterListResponse)
  }),

  mockGet('/admin/v2/lobby-clusters/:id', (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const lobby = getClusterState().lobbyClusters.find((item) => item.id === id)
    if (!lobby) {
      return jsonError(404, 'lobby_cluster_not_found', '大厅集群不存在')
    }
    return HttpResponse.json(toDetail(lobby))
  }),

  mockPost('/admin/v2/server-placement-transfers', async (info) => {
    const body = await readBody<ServerPlacementTransferBody>(info.request)
    if (!body.serverId || !body.reason?.trim() || body.reason.length > 255 || body.target === undefined) {
      return jsonError(400, 'invalid_param', 'serverId、target 与迁移原因必填')
    }
    const state = getClusterState()
    const row = state.servers.find((server) => server.serverId === body.serverId)
    if (!row) {
      return jsonError(404, 'server_not_found', '服务器不存在')
    }
    if (row.kind !== 'backend' || !targetIsValid(body.target, row.namespaceId)) {
      return jsonError(409, 'placement_rejected', '迁移目标不符合服务器归属约束')
    }
    if (
      body.target?.kind === 'lobby_cluster' &&
      !state.identities.some(
        (identity) =>
          identity.namespaceId === row.namespaceId &&
          identity.serverId === row.serverId &&
          identity.kind === 'backend' &&
          (identity.status === 'active' || identity.status === 'disabled'),
      )
    ) {
      return jsonError(409, 'lobby_member_role_invalid', '仅已确认的 backend 服务器可以加入大厅集群')
    }
    if (row.online && playerCountOf(row.serverId) > 0) {
      return jsonError(409, 'drain_required', '在线且有玩家，需先排水')
    }
    applyTarget(row, body.target)
    return HttpResponse.json(placementResponse(row))
  }),
]
