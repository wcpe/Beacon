// FR-159 共享集群基础数据集：namespace / 信任 / BC 集群 / 大区 / 小区 / server / agent 身份。
// 身份域、namespace 域、区服权威域、指标健康域等都从同一份可变状态取数，
// 保证跨页面（/servers、/zones、/namespaces、/dashboard）看到的是同一个"集群"。
// 实体字段以 docs/specs/v2-zone-authority.md §3 与 v2-agent-identity.md §3/§5 为真源。

import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { isoOffset, uuidFrom } from '../support'

export type ServerKind = 'proxy' | 'backend'

export type IdentityStatus =
  | 'pending'
  | 'active'
  | 'rejected'
  | 'expired'
  | 'disabled'
  | 'conflict'
  | 'unbound'

export type TrustCapability = 'schedule' | 'message' | 'agent_ops'

export interface NamespaceRow {
  id: number
  name: string
  description: string
  createdAt: string
}

export interface TrustRow {
  id: number
  fromNamespaceId: number
  toNamespaceId: number
  capability: TrustCapability
  status: 'active' | 'revoked'
  note: string
  grantedBy: string
  grantedAt: string
  revokedBy: string | null
  revokedAt: string | null
  revokeReason: string | null
  createdAt: string
}

export interface BcClusterRow {
  id: number
  namespaceId: number
  name: string
  description: string
  createdAt: string
}

export interface RegionRow {
  id: number
  bcClusterId: number
  name: string
  description: string
  createdAt: string
}

export interface ZoneRow {
  id: number
  regionId: number
  name: string
  description: string
  createdAt: string
}

export interface ServerRow {
  id: number
  namespaceId: number
  serverId: string
  kind: ServerKind
  /** 仅 proxy：归属 BC 集群，空 = 未分配 */
  bcClusterId: number | null
  /** 仅 backend：归属小区，空 = 未分配 */
  zoneId: number | null
  /** 换区工单预填目标小区（backend） */
  pendingZoneId: number | null
  /** 换集群工单预填目标集群（proxy） */
  pendingBcClusterId: number | null
  isDefaultEntry: boolean
  draining: boolean
  /** 在线摘要（活性来自指标批上报，mock 直接给结论） */
  online: boolean
  createdAt: string
}

export interface ConflictPeer {
  bootId: string
  lastAddr: string
  lastSeenAt: string
}

export interface RezonePrefill {
  targetKind: 'zone' | 'bc_cluster'
  targetId: number
  targetName: string
}

export interface IdentityRow {
  identityId: string
  namespaceId: number
  serverId: string
  kind: ServerKind
  status: IdentityStatus
  bootId: string | null
  lastAddr: string | null
  agentVersion: string | null
  pendingExpiresAt: string | null
  boundAt: string | null
  statusChangedAt: string
  conflictReason: string | null
  /** conflict 状态时的冲突双方明细（详情端点返回） */
  conflictPeers: ConflictPeer[] | null
  /** 换区工单重确认时的预填目标（待确认列表"换区中"提示） */
  rezonePrefill: RezonePrefill | null
}

export interface ClusterState {
  namespaces: NamespaceRow[]
  trusts: TrustRow[]
  bcClusters: BcClusterRow[]
  regions: RegionRow[]
  zones: ZoneRow[]
  servers: ServerRow[]
  identities: IdentityRow[]
  /** 全局自增 id 分配器（mock 各表共享 id 空间即可） */
  nextId: number
}

/** 分配一个新的自增 id */
export function allocId(state: ClusterState): number {
  const id = state.nextId
  state.nextId += 1
  return id
}

const AGENT_VERSION = '2.1.0'
const HOUR = 3_600_000
const DAY = 24 * HOUR

function makeIdentity(
  namespaceId: number,
  serverId: string,
  kind: ServerKind,
  status: IdentityStatus,
  overrides?: Partial<IdentityRow>,
): IdentityRow {
  return {
    identityId: uuidFrom(`identity:${String(namespaceId)}:${serverId}`),
    namespaceId,
    serverId,
    kind,
    status,
    bootId: uuidFrom(`boot:${serverId}`),
    lastAddr: `10.30.${String(namespaceId)}.${String((Math.abs(serverId.length * 7) % 200) + 10)}:25565`,
    agentVersion: AGENT_VERSION,
    pendingExpiresAt: status === 'pending' ? isoOffset(48 * HOUR) : null,
    boundAt: status === 'active' || status === 'disabled' ? isoOffset(-20 * DAY) : null,
    statusChangedAt: isoOffset(-6 * HOUR),
    conflictReason: null,
    conflictPeers: null,
    rezonePrefill: null,
    ...overrides,
  }
}

interface ServerSeed {
  serverId: string
  kind: ServerKind
  zoneId?: number | null
  bcClusterId?: number | null
  isDefaultEntry?: boolean
  draining?: boolean
  online?: boolean
  pendingZoneId?: number | null
}

function makeServer(state: ClusterState, namespaceId: number, seed: ServerSeed): ServerRow {
  const row: ServerRow = {
    id: allocId(state),
    namespaceId,
    serverId: seed.serverId,
    kind: seed.kind,
    bcClusterId: seed.bcClusterId ?? null,
    zoneId: seed.zoneId ?? null,
    pendingZoneId: seed.pendingZoneId ?? null,
    pendingBcClusterId: null,
    isDefaultEntry: seed.isDefaultEntry ?? false,
    draining: seed.draining ?? false,
    online: seed.online ?? true,
    createdAt: isoOffset(-25 * DAY),
  }
  state.servers.push(row)
  return row
}

function emptyState(): ClusterState {
  return {
    namespaces: [],
    trusts: [],
    bcClusters: [],
    regions: [],
    zones: [],
    servers: [],
    identities: [],
    nextId: 1,
  }
}

/** 常规态：两个 namespace、真实感中小规模集群，覆盖身份状态机全部枚举值 */
function buildNormal(): ClusterState {
  const state = emptyState()
  state.nextId = 1000

  state.namespaces.push(
    { id: 1, name: 'prod', description: '生产环境（主力集群）', createdAt: isoOffset(-90 * DAY) },
    { id: 2, name: 'test', description: '测试环境（预发验证）', createdAt: isoOffset(-60 * DAY) },
  )

  state.trusts.push(
    {
      id: 1,
      fromNamespaceId: 2,
      toNamespaceId: 1,
      capability: 'schedule',
      status: 'active',
      note: '压测期间允许 test 域借用 prod 富余算力',
      grantedBy: 'admin',
      grantedAt: isoOffset(-10 * DAY),
      revokedBy: null,
      revokedAt: null,
      revokeReason: null,
      createdAt: isoOffset(-10 * DAY),
    },
    {
      id: 2,
      fromNamespaceId: 2,
      toNamespaceId: 1,
      capability: 'message',
      status: 'revoked',
      note: '联调期跨域消息通道',
      grantedBy: 'admin',
      grantedAt: isoOffset(-30 * DAY),
      revokedBy: 'ops-chen',
      revokedAt: isoOffset(-5 * DAY),
      revokeReason: '联调结束，按最小授权收回',
      createdAt: isoOffset(-30 * DAY),
    },
  )

  state.bcClusters.push(
    { id: 10, namespaceId: 1, name: 'bc-main', description: '主线 BC 集群', createdAt: isoOffset(-80 * DAY) },
    { id: 11, namespaceId: 2, name: 'bc-test', description: '测试 BC 集群', createdAt: isoOffset(-55 * DAY) },
  )
  state.regions.push(
    { id: 20, bcClusterId: 10, name: '华东大区', description: '华东节点', createdAt: isoOffset(-80 * DAY) },
    { id: 21, bcClusterId: 10, name: '华南大区', description: '华南节点', createdAt: isoOffset(-70 * DAY) },
    { id: 22, bcClusterId: 11, name: '测试大区', description: '预发与回归', createdAt: isoOffset(-55 * DAY) },
  )
  state.zones.push(
    { id: 30, regionId: 20, name: 'area-1', description: '一区（主城）', createdAt: isoOffset(-80 * DAY) },
    { id: 31, regionId: 20, name: 'area-2', description: '二区（生存）', createdAt: isoOffset(-75 * DAY) },
    { id: 32, regionId: 21, name: 'area-3', description: '三区（商业街）', createdAt: isoOffset(-70 * DAY) },
    { id: 33, regionId: 21, name: 'survival-1', description: '生存专区', createdAt: isoOffset(-65 * DAY) },
    { id: 34, regionId: 22, name: 'test-area-1', description: '测试一区', createdAt: isoOffset(-55 * DAY) },
  )

  // prod 域：代理 + 各小区子服（多数在线健康，穿插禁用 / 排空 / 失联样本）
  makeServer(state, 1, { serverId: 'proxy-1', kind: 'proxy', bcClusterId: 10 })
  makeServer(state, 1, { serverId: 'proxy-2', kind: 'proxy', bcClusterId: 10 })
  makeServer(state, 1, { serverId: 'lobby-1', kind: 'backend', zoneId: 30, isDefaultEntry: true })
  makeServer(state, 1, { serverId: 'lobby-2', kind: 'backend', zoneId: 30 })
  makeServer(state, 1, { serverId: 'game-1', kind: 'backend', zoneId: 30 })
  makeServer(state, 1, { serverId: 'game-2', kind: 'backend', zoneId: 30 })
  makeServer(state, 1, { serverId: 'game-3', kind: 'backend', zoneId: 31 })
  makeServer(state, 1, { serverId: 'game-4', kind: 'backend', zoneId: 31, online: false })
  makeServer(state, 1, { serverId: 'game-5', kind: 'backend', zoneId: 31 })
  makeServer(state, 1, { serverId: 'game-6', kind: 'backend', zoneId: 31 })
  makeServer(state, 1, { serverId: 'mall-1', kind: 'backend', zoneId: 32 })
  makeServer(state, 1, { serverId: 'pvp-1', kind: 'backend', zoneId: 32 })
  makeServer(state, 1, { serverId: 'survival-1', kind: 'backend', zoneId: 33, draining: true })
  // 未分配篮：已确认但尚未落区
  makeServer(state, 1, { serverId: 'build-1', kind: 'backend', zoneId: null })
  makeServer(state, 1, { serverId: 'event-1', kind: 'backend', zoneId: null, online: false })
  // 换区中：原归属已清、预填目标 area-2
  makeServer(state, 1, { serverId: 'survival-2', kind: 'backend', zoneId: null, pendingZoneId: 31 })

  // test 域
  makeServer(state, 2, { serverId: 'test-proxy-1', kind: 'proxy', bcClusterId: 11 })
  makeServer(state, 2, { serverId: 'test-lobby-1', kind: 'backend', zoneId: 34, isDefaultEntry: true })
  makeServer(state, 2, { serverId: 'test-game-1', kind: 'backend', zoneId: 34 })

  // 已有 server 行的服务器 → active 身份（mall-1 为 disabled、game-6 为 conflict 样本）
  for (const server of state.servers) {
    if (server.serverId === 'mall-1') {
      state.identities.push(
        makeIdentity(server.namespaceId, server.serverId, server.kind, 'disabled', {
          statusChangedAt: isoOffset(-2 * DAY),
        }),
      )
      continue
    }
    if (server.serverId === 'game-6') {
      state.identities.push(
        makeIdentity(server.namespaceId, server.serverId, server.kind, 'conflict', {
          conflictReason: 'duplicate-boot-id',
          conflictPeers: [
            { bootId: uuidFrom('boot:game-6:a'), lastAddr: '10.30.1.61:25565', lastSeenAt: isoOffset(-8 * 60_000) },
            { bootId: uuidFrom('boot:game-6:b'), lastAddr: '10.30.1.62:25565', lastSeenAt: isoOffset(-3 * 60_000) },
          ],
        }),
      )
      continue
    }
    if (server.serverId === 'survival-2') {
      // 换区中：身份重入 pending，带预填目标
      state.identities.push(
        makeIdentity(server.namespaceId, server.serverId, server.kind, 'pending', {
          rezonePrefill: { targetKind: 'zone', targetId: 31, targetName: 'area-2' },
          statusChangedAt: isoOffset(-30 * 60_000),
        }),
      )
      continue
    }
    state.identities.push(makeIdentity(server.namespaceId, server.serverId, server.kind, 'active'))
  }

  // 无 server 行的身份样本：待确认 / 占用冲突 / 拒绝 / 过期 / 解绑
  state.identities.push(
    makeIdentity(1, 'game-new-1', 'backend', 'pending', { statusChangedAt: isoOffset(-2 * HOUR) }),
    makeIdentity(1, 'lobby-2', 'backend', 'pending', {
      identityId: uuidFrom('identity:1:lobby-2:dup'),
      conflictReason: 'server-id-occupied',
      statusChangedAt: isoOffset(-1 * HOUR),
    }),
    makeIdentity(1, 'game-x', 'backend', 'rejected', { statusChangedAt: isoOffset(-3 * DAY) }),
    makeIdentity(1, 'game-old-1', 'backend', 'expired', { statusChangedAt: isoOffset(-4 * DAY) }),
    makeIdentity(1, 'retired-1', 'backend', 'unbound', { statusChangedAt: isoOffset(-15 * DAY) }),
    makeIdentity(2, 'test-game-new', 'backend', 'pending', { statusChangedAt: isoOffset(-40 * 60_000) }),
  )

  return state
}

/** 超大量态：1200+ 子服、3 集群 × 12 大区 × 48 小区，分页在 handler 内真实生效 */
function buildHuge(): ClusterState {
  const state = emptyState()
  state.nextId = 100000

  state.namespaces.push(
    { id: 1, name: 'prod', description: '生产环境（全网 1200+ 子服）', createdAt: isoOffset(-365 * DAY) },
    { id: 2, name: 'test', description: '测试环境', createdAt: isoOffset(-200 * DAY) },
  )
  state.trusts.push({
    id: 1,
    fromNamespaceId: 2,
    toNamespaceId: 1,
    capability: 'schedule',
    status: 'active',
    note: '常态化压测借用',
    grantedBy: 'admin',
    grantedAt: isoOffset(-100 * DAY),
    revokedBy: null,
    revokedAt: null,
    revokeReason: null,
    createdAt: isoOffset(-100 * DAY),
  })

  const clusterNames = ['bc-main', 'bc-backup', 'bc-event']
  const zoneIds: number[] = []
  for (let c = 0; c < 3; c++) {
    const clusterId = 10 + c
    state.bcClusters.push({
      id: clusterId,
      namespaceId: 1,
      name: clusterNames[c],
      description: `${clusterNames[c]} 集群`,
      createdAt: isoOffset(-300 * DAY),
    })
    for (let r = 0; r < 4; r++) {
      const regionId = 100 + c * 4 + r
      state.regions.push({
        id: regionId,
        bcClusterId: clusterId,
        name: `大区-${String(c + 1)}-${String(r + 1)}`,
        description: `第 ${String(c + 1)} 集群第 ${String(r + 1)} 大区`,
        createdAt: isoOffset(-280 * DAY),
      })
      for (let z = 0; z < 4; z++) {
        const zoneId = 1000 + (c * 4 + r) * 4 + z
        zoneIds.push(zoneId)
        state.zones.push({
          id: zoneId,
          regionId,
          name: `area-${String(c + 1)}-${String(r + 1)}-${String(z + 1)}`,
          description: `小区 ${String(zoneId)}`,
          createdAt: isoOffset(-260 * DAY),
        })
      }
    }
  }

  // 12 台代理
  for (let p = 1; p <= 12; p++) {
    makeServer(state, 1, {
      serverId: `proxy-${String(p).padStart(2, '0')}`,
      kind: 'proxy',
      bcClusterId: 10 + ((p - 1) % 3),
    })
  }
  // 1200 台已分配子服：round-robin 落 48 个小区，穿插禁用 / 失联 / 排空
  for (let n = 1; n <= 1200; n++) {
    const serverId = `game-${String(n).padStart(4, '0')}`
    makeServer(state, 1, {
      serverId,
      kind: 'backend',
      zoneId: zoneIds[(n - 1) % zoneIds.length],
      isDefaultEntry: n % 25 === 1,
      draining: n % 211 === 0,
      online: n % 97 !== 0,
    })
  }
  // 40 台未分配 + 25 台待确认
  for (let n = 1; n <= 40; n++) {
    makeServer(state, 1, { serverId: `pool-${String(n).padStart(3, '0')}`, kind: 'backend', zoneId: null })
  }

  for (const server of state.servers) {
    const disabled = server.kind === 'backend' && server.serverId.startsWith('game-') && server.id % 131 === 0
    state.identities.push(
      makeIdentity(server.namespaceId, server.serverId, server.kind, disabled ? 'disabled' : 'active'),
    )
  }
  for (let n = 1; n <= 25; n++) {
    state.identities.push(
      makeIdentity(1, `newbox-${String(n).padStart(3, '0')}`, 'backend', 'pending', {
        statusChangedAt: isoOffset(-n * HOUR),
      }),
    )
  }

  return state
}

function buildCluster(scenario: MockScenario): ClusterState {
  switch (scenario) {
    case 'normal':
      return buildNormal()
    case 'huge':
      return buildHuge()
    default:
      // empty（error 场景不会读到数据集）
      return emptyState()
  }
}

/** 取当前场景的集群基础数据集（可变，写操作直接修改） */
export const getClusterState: () => ClusterState = defineScenarioStore(buildCluster)

/** 沿 zone → region → bc_cluster 链解析 namespace 归属 */
export function namespaceOfZone(state: ClusterState, zoneId: number): number | null {
  const zone = state.zones.find((z) => z.id === zoneId)
  if (!zone) {
    return null
  }
  const region = state.regions.find((r) => r.id === zone.regionId)
  if (!region) {
    return null
  }
  const cluster = state.bcClusters.find((c) => c.id === region.bcClusterId)
  return cluster ? cluster.namespaceId : null
}

/** server 是否已分配（backend 看 zoneId，proxy 看 bcClusterId） */
export function isAssigned(server: ServerRow): boolean {
  return server.kind === 'backend' ? server.zoneId !== null : server.bcClusterId !== null
}
