/**
 * 配置中心 mock 数据 + 进程内可变状态
 *
 * 生成符合后端 API 契约的模拟数据，用于前端独立验证（假后端 / 演示模式）。
 * 所有类型严格对齐 web/src/api/types.ts。
 *
 * 状态约定：
 * - 所有写操作（建 / 改 / 发布 / 回滚 / 删 / 改派 / 审批 / 拓印 / 命令 等）直接修改本模块的可变集合，
 *   后续读请求据此反映最新结果；状态仅存进程内存，**刷新页面即重置**（不做持久化）。
 * - handlers.ts 通过本模块导出的可变集合与 mutator 读写同一份状态，不返回写死常量。
 */

import type {
  AgentCommandView,
  AgentLogView,
  AlertEventPage,
  AlertEventView,
  ApiKeyCreated,
  AssignmentView,
  AuditAnalytics,
  AuditPage,
  AuditView,
  CommandAnalytics,
  CommandMetaView,
  CommandPage,
  ConfigTimelineView,
  ConfigView,
  ConflictDiffView,
  DefaultEntryView,
  DiffView,
  FileRevisionView,
  FileView,
  IgnoreRuleView,
  ImpactView,
  InstanceView,
  NamespaceView,
  ObservabilityView,
  OverrideSetDryRunView,
  OverrideSetRevisionView,
  OverrideSetView,
  PublishResult,
  ReverseFetchTaskView,
  ReversibleOpView,
  RevisionView,
  SettingView,
  SystemStatusView,
  TopologyEdge,
  TopologyGroup,
  TopologyNode,
  TopologyView,
  ZoneStatView,
} from '../types'
import type {
  BCSummary,
  DrainMarker,
  MetricsSummary,
  MetricsTrend,
  OfflineMarker,
  ServerPlayers,
  TrendPoint,
} from '../client'

// ---- 工具函数 ----

// 简单的伪 md5（仅做演示，不追求密码学安全）
function md5(s: string): string {
  let h = 0
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) - h + s.charCodeAt(i)) | 0
  }
  return Math.abs(h).toString(16).padStart(8, '0')
}

function ago(seconds: number): string {
  return new Date(Date.now() - seconds * 1000).toISOString()
}

function now(): string {
  return new Date().toISOString()
}

// ---- 配置项 ----

interface MockConfig {
  item: ConfigView
  revisions: RevisionView[]
}

const YAML_CONTENT_BASE = `# 插件主配置
plugin:
  name: "game_plugin"
  version: "2.3.1"
  debug: false
  enabled: true
`

const YAML_CONTENT_V2 = `# 插件主配置
plugin:
  name: "game_plugin_v2"
  version: "2.3.1"
  features:
    - pvp_arena
  enabled: true
`

const YAML_CONTENT_V3 = `# 插件主配置
plugin:
  name: "game_plugin_v3"
  version: "2.4.0"
  features:
    - pvp_arena
    - skyblock
  debug: true
  enabled: true
`

const PROPERTIES_BASE = `db.host=127.0.0.1
db.port=3306
db.name=game_db
cache.enabled=true
cache.ttl=300`

const PROPERTIES_V2 = `db.host=10.0.0.1
db.port=3306
db.name=game_db
cache.enabled=true
cache.ttl=600
cache.backend=redis`

const JSON_BASE = `{"server":{"host":"0.0.0.0","port":25565},"max-players":100,"online-mode":true}`
const JSON_V2 = `{"server":{"host":"0.0.0.0","port":25565},"max-players":200,"online-mode":true,"view-distance":12}`

/**
 * 生成一条 mock 配置及其 revision 历史
 */
function makeMockConfig(
  id: number,
  namespace: string,
  group: string,
  dataId: string,
  scopeLevel: string,
  scopeTarget: string,
  format: string,
  contents: string[],
  operator: string,
): MockConfig {
  const revisions: RevisionView[] = contents.map((content, idx) => ({
    version: idx + 1,
    md5: md5(content),
    operator,
    comment: idx === 0 ? '初始发布' : `第 ${idx + 1} 次修改`,
    sourceRevision: null,
    createdAt: ago((contents.length - idx) * 3600),
    content,
  }))

  const latestContent = contents[contents.length - 1]
  const item: ConfigView = {
    id,
    namespace,
    group,
    dataId,
    scopeLevel,
    scopeTarget,
    format,
    version: contents.length,
    md5: md5(latestContent),
    enabled: true,
    updatedAt: revisions[revisions.length - 1].createdAt,
    content: latestContent,
  }

  return { item, revisions }
}

// ---- 配置数据集（可变：建 / 改 / 发布 / 回滚 / 删 直接改这份） ----

export const mockConfigs: MockConfig[] = [
  // prod 环境
  makeMockConfig(
    1,
    'prod',
    '__GLOBAL__',
    'game_config.yml',
    'global',
    '',
    'yaml',
    [YAML_CONTENT_BASE, YAML_CONTENT_V2, YAML_CONTENT_V3],
    'admin',
  ),
  makeMockConfig(
    2,
    'prod',
    '__GLOBAL__',
    'db.properties',
    'global',
    '',
    'properties',
    [PROPERTIES_BASE, PROPERTIES_V2],
    'admin',
  ),
  makeMockConfig(
    3,
    'prod',
    'server-a',
    'game_config.yml',
    'group',
    '',
    'yaml',
    [YAML_CONTENT_BASE, YAML_CONTENT_V2],
    'developer',
  ),
  makeMockConfig(
    4,
    'prod',
    'server-a',
    'server.json',
    'zone',
    'zone-01',
    'json',
    [JSON_BASE, JSON_V2],
    'admin',
  ),
  makeMockConfig(
    5,
    'prod',
    'server-b',
    'game_config.yml',
    'group',
    '',
    'yaml',
    [YAML_CONTENT_BASE],
    'developer',
  ),

  // test 环境
  makeMockConfig(
    6,
    'test',
    '__GLOBAL__',
    'game_config.yml',
    'global',
    '',
    'yaml',
    [YAML_CONTENT_BASE, YAML_CONTENT_V2],
    'admin',
  ),
  makeMockConfig(
    7,
    'test',
    '__GLOBAL__',
    'db.properties',
    'global',
    '',
    'properties',
    [PROPERTIES_BASE],
    'developer',
  ),
  makeMockConfig(
    8,
    'test',
    'server-a',
    'game_config.yml',
    'group',
    '',
    'yaml',
    [YAML_CONTENT_BASE],
    'admin',
  ),
]

// 自增序列：配置 id 在现有最大值基础上递增（建配置用）。
let configIdSeq = Math.max(...mockConfigs.map((c) => c.item.id))

// ---- 配置读写便捷函数 ----

/** 获取所有配置项（不含 content，对齐列表接口）；默认隐藏已软删项（enabled=false） */
export function getMockConfigList(includeDisabled = false): ConfigView[] {
  return mockConfigs
    .filter((c) => includeDisabled || c.item.enabled)
    .map(({ item, revisions }) => {
      const latest = revisions[revisions.length - 1]
      return {
        ...item,
        md5: latest.md5,
        updatedAt: latest.createdAt,
        content: undefined, // 列表接口不含 content
      }
    })
}

/** 根据 id 获取配置详情（含 content） */
export function getMockConfigDetail(id: number): ConfigView | null {
  const found = mockConfigs.find((c) => c.item.id === id)
  if (!found) return null
  return { ...found.item, content: found.revisions[found.revisions.length - 1].content }
}

/** 根据 id 获取 revision 列表（v 高→低） */
export function getMockRevisions(id: number): RevisionView[] {
  const found = mockConfigs.find((c) => c.item.id === id)
  if (!found) return []
  return [...found.revisions].reverse()
}

/** 根据 id + from/to 获取 diff */
export function getMockDiff(id: number, fromVersion: number, toVersion: number): DiffView | null {
  const found = mockConfigs.find((c) => c.item.id === id)
  if (!found) return null
  const fromRev = found.revisions.find((r) => r.version === fromVersion)
  const toRev = found.revisions.find((r) => r.version === toVersion)
  if (!fromRev || !toRev) return null
  return {
    fromVersion,
    toVersion,
    fromContent: fromRev.content ?? '',
    toContent: toRev.content ?? '',
  }
}

/** 新建配置：落库 + 首版 revision，返回创建的配置视图 */
export function createMockConfig(body: {
  namespace?: string
  group?: string
  dataId?: string
  scopeLevel?: string
  scopeTarget?: string
  format?: string
  content?: string
  comment?: string
}): ConfigView {
  const id = ++configIdSeq
  const content = body.content ?? ''
  const ts = now()
  const item: ConfigView = {
    id,
    namespace: body.namespace ?? 'prod',
    group: body.group ?? '__GLOBAL__',
    dataId: body.dataId ?? 'new_config.yml',
    scopeLevel: body.scopeLevel ?? 'global',
    scopeTarget: body.scopeTarget ?? '',
    format: body.format ?? 'yaml',
    version: 1,
    md5: md5(content),
    enabled: true,
    updatedAt: ts,
    content,
  }
  const rev: RevisionView = {
    version: 1,
    md5: item.md5,
    operator: 'admin',
    comment: body.comment ?? '新建',
    sourceRevision: null,
    createdAt: ts,
    content,
  }
  mockConfigs.push({ item, revisions: [rev] })
  recordAudit({
    namespace: item.namespace,
    action: 'create',
    targetType: 'config',
    targetRef: item.dataId,
    detail: '新建配置',
  })
  return item
}

/** 发布配置：版本自增 + 生效内容更新，返回发布结果（不存在返回 null） */
export function publishMockConfig(
  id: number,
  content: string,
  comment: string,
): PublishResult | null {
  const found = mockConfigs.find((c) => c.item.id === id)
  if (!found) return null
  const newVer = found.item.version + 1
  const ts = now()
  const rev: RevisionView = {
    version: newVer,
    md5: md5(content),
    operator: 'admin',
    comment: comment ?? '',
    sourceRevision: null,
    createdAt: ts,
    content,
  }
  found.revisions.push(rev)
  found.item.version = newVer
  found.item.content = content
  found.item.md5 = rev.md5
  found.item.updatedAt = ts
  recordAudit({
    namespace: found.item.namespace,
    action: 'publish',
    targetType: 'config',
    targetRef: found.item.dataId,
    detail: `发布版本 v${newVer}`,
  })
  return { version: newVer, md5: rev.md5 }
}

/** 回滚配置：以目标版本内容生成新版本（指针前移），返回发布结果（不存在返回 null/字符串错误） */
export function rollbackMockConfig(
  id: number,
  toVersion: number,
  comment: string,
): PublishResult | 'no-config' | 'no-version' {
  const found = mockConfigs.find((c) => c.item.id === id)
  if (!found) return 'no-config'
  const target = found.revisions.find((r) => r.version === toVersion)
  if (!target) return 'no-version'
  const newVer = found.item.version + 1
  const ts = now()
  const rev: RevisionView = {
    version: newVer,
    md5: md5(target.content ?? ''),
    operator: 'admin',
    comment: comment ?? `回滚到版本 ${toVersion}`,
    sourceRevision: toVersion,
    createdAt: ts,
    content: target.content,
  }
  found.revisions.push(rev)
  found.item.version = newVer
  found.item.content = target.content
  found.item.md5 = rev.md5
  found.item.updatedAt = ts
  recordAudit({
    namespace: found.item.namespace,
    action: 'rollback',
    targetType: 'config',
    targetRef: found.item.dataId,
    detail: `回滚到 v${toVersion}`,
  })
  return { version: newVer, md5: rev.md5 }
}

/** 软删配置（enabled=false），成功返回 true、不存在返回 false */
export function deleteMockConfig(id: number): boolean {
  const found = mockConfigs.find((c) => c.item.id === id)
  if (!found) return false
  found.item.enabled = false
  recordAudit({
    namespace: found.item.namespace,
    action: 'delete',
    targetType: 'config',
    targetRef: found.item.dataId,
    detail: '软删',
  })
  return true
}

/** 批量操作（delete 软删 / disable 停用 / enable 启用）：返回实际命中条数 */
export function batchMockConfigs(action: 'delete' | 'disable' | 'enable', ids: number[]): number {
  let count = 0
  for (const id of ids) {
    const found = mockConfigs.find((c) => c.item.id === id)
    if (!found) continue
    if (action === 'enable') found.item.enabled = true
    else found.item.enabled = false // delete / disable 同效（软删）
    count++
  }
  return count
}

// ---- 实例视图（可变：下线 / 改派 影响派生视图） ----

// bukkit 实例的 proxy 指标恒为零值（仅 bc 非零，FR-34）
const ZERO_PROXY: InstanceView['proxy'] = {
  onlineConnections: 0,
  threadCount: 0,
  uptimeMs: 0,
  backendUp: 0,
  backendTotal: 0,
  backendAvgLatencyMs: -1,
}

export const mockInstances: InstanceView[] = [
  {
    namespace: 'prod',
    serverId: 'server-01',
    role: 'bukkit',
    group: 'server-a',
    zone: 'zone-01',
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.20.4',
    agentVersion: '0.12.0',
    status: 'online',
    capacity: 100,
    weight: 1,
    metadata: {},
    lastHeartbeat: ago(5),
    lastHeartbeatAgeSec: 5,
    healthReason: '',
    appliedMd5: 'abc12345',
    playerCount: 42,
    tps: 19.8,
    backends: [],
    zoneDefaultEntry: true,
    proxy: ZERO_PROXY,
    registeredAt: ago(86400),
  },
  {
    namespace: 'prod',
    serverId: 'server-02',
    role: 'bukkit',
    group: 'server-a',
    zone: 'zone-01',
    assigned: true,
    address: '10.0.0.2:25565',
    version: '1.20.4',
    agentVersion: '0.12.0',
    status: 'online',
    capacity: 100,
    weight: 1,
    metadata: {},
    lastHeartbeat: ago(10),
    lastHeartbeatAgeSec: 10,
    healthReason: '',
    appliedMd5: 'abc12345',
    playerCount: 38,
    tps: 19.5,
    backends: [],
    zoneDefaultEntry: false,
    proxy: ZERO_PROXY,
    registeredAt: ago(86400),
  },
  {
    namespace: 'prod',
    serverId: 'server-03',
    role: 'bungee',
    group: 'server-b',
    zone: 'zone-02',
    assigned: true,
    address: '10.0.0.3:25565',
    version: '1.20.4',
    agentVersion: '0.12.0',
    status: 'online',
    capacity: 200,
    weight: 2,
    metadata: {},
    lastHeartbeat: ago(60),
    lastHeartbeatAgeSec: 60,
    healthReason: '',
    appliedMd5: 'def67890',
    playerCount: 0,
    tps: 0,
    backends: ['server-01', 'server-04'],
    zoneDefaultEntry: false,
    proxy: {
      onlineConnections: 128,
      threadCount: 36,
      uptimeMs: 7_200_000,
      backendUp: 1,
      backendTotal: 2,
      backendAvgLatencyMs: 18,
    },
    registeredAt: ago(172800),
  },
  // 演示集群版本不一致黄标（FR-86）：该服仍跑旧 agent 构建，与本环境多数 0.12.0 不同
  {
    namespace: 'prod',
    serverId: 'server-04',
    role: 'bukkit',
    group: 'server-b',
    zone: 'zone-02',
    assigned: true,
    address: '10.0.0.4:25565',
    version: '1.20.4',
    agentVersion: '0.11.0',
    status: 'online',
    capacity: 100,
    weight: 1,
    metadata: {},
    lastHeartbeat: ago(3),
    lastHeartbeatAgeSec: 3,
    healthReason: '',
    appliedMd5: 'abc12345',
    playerCount: 55,
    tps: 19.2,
    backends: [],
    zoneDefaultEntry: true,
    proxy: ZERO_PROXY,
    registeredAt: ago(43200),
  },
  {
    namespace: 'test',
    serverId: 'test-01',
    role: 'bukkit',
    group: 'server-a',
    zone: 'zone-01',
    assigned: true,
    address: '10.0.1.1:25565',
    version: '1.20.4',
    agentVersion: '0.12.0',
    status: 'online',
    capacity: 50,
    weight: 1,
    metadata: {},
    lastHeartbeat: ago(8),
    lastHeartbeatAgeSec: 8,
    healthReason: '',
    appliedMd5: 'abc12345',
    playerCount: 5,
    tps: 20.0,
    backends: [],
    zoneDefaultEntry: false,
    proxy: ZERO_PROXY,
    registeredAt: ago(86400),
  },
]

export function listMockInstances(filter?: {
  namespace?: string
  group?: string
  zone?: string
  role?: string
  status?: string
}): InstanceView[] {
  let items = [...mockInstances]
  if (filter?.namespace) items = items.filter((i) => i.namespace === filter.namespace)
  if (filter?.group) items = items.filter((i) => i.group === filter.group)
  if (filter?.zone) items = items.filter((i) => i.zone === filter.zone)
  if (filter?.role) items = items.filter((i) => i.role === filter.role)
  if (filter?.status) items = items.filter((i) => i.status === filter.status)
  return items
}

// per-server 有效配置变更时间线（FR-80）：从配置历史派生该服覆盖链涉及项的发布历史。
export function getMockConfigTimeline(
  serverId: string,
  namespace: string,
  group?: string,
): ConfigTimelineView {
  const inst = mockInstances.find((i) => i.serverId === serverId)
  const grp = group ?? inst?.group ?? ''
  const zone = inst?.zone ?? ''
  // 命中该 namespace 下 global / 该 group / 该 zone / 该 server 层的配置项
  const items = mockConfigs
    .filter((c) => c.item.namespace === namespace)
    .filter((c) => {
      const lv = c.item.scopeLevel
      if (lv === 'global') return true
      if (lv === 'group') return c.item.group === grp
      if (lv === 'zone') return c.item.scopeTarget === zone
      if (lv === 'server') return c.item.scopeTarget === serverId
      return false
    })
    .flatMap((c) =>
      c.revisions.map((r) => ({
        configItemId: c.item.id,
        dataId: c.item.dataId,
        scopeLevel: c.item.scopeLevel,
        scopeTarget: c.item.scopeTarget,
        version: r.version,
        md5: r.md5,
        operator: r.operator,
        comment: r.comment,
        createdAt: r.createdAt,
      })),
    )
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  return { namespace, serverId, group: grp, zone, items }
}

// ---- 主动下线标记（FR-49，可变） ----

export const mockOfflineMarkers: OfflineMarker[] = []

export function listMockOfflineMarkers(namespace?: string): OfflineMarker[] {
  return mockOfflineMarkers.filter((m) => !namespace || m.namespace === namespace)
}

/** 主动下线某实例：落标记 + 移出可用集（status=offline） */
export function offlineMockInstance(serverId: string, namespace: string, reason: string): void {
  if (!mockOfflineMarkers.some((m) => m.serverId === serverId && m.namespace === namespace)) {
    mockOfflineMarkers.push({ namespace, serverId, reason })
  }
  const inst = mockInstances.find((i) => i.serverId === serverId && i.namespace === namespace)
  if (inst) {
    inst.status = 'offline'
    inst.healthReason = reason || '已主动下线'
  }
  recordAudit({
    namespace,
    action: 'offline',
    targetType: 'instance',
    targetRef: serverId,
    detail: reason || '主动下线',
  })
}

/** 取消主动下线：清标记 + 复位为在线 */
export function onlineMockInstance(serverId: string, namespace: string): void {
  const idx = mockOfflineMarkers.findIndex(
    (m) => m.serverId === serverId && m.namespace === namespace,
  )
  if (idx >= 0) mockOfflineMarkers.splice(idx, 1)
  const inst = mockInstances.find((i) => i.serverId === serverId && i.namespace === namespace)
  if (inst) {
    inst.status = 'online'
    inst.healthReason = ''
  }
  recordAudit({
    namespace,
    action: 'online',
    targetType: 'instance',
    targetRef: serverId,
    detail: '取消主动下线',
  })
}

// ---- 排空标记（FR-10，可变） ----

export const mockDrains: DrainMarker[] = []

export function listMockDrains(namespace?: string): DrainMarker[] {
  return mockDrains.filter((d) => !namespace || d.namespace === namespace)
}

export function drainMockInstance(
  serverId: string,
  namespace: string,
  reason: string,
): DrainMarker {
  let marker = mockDrains.find((d) => d.serverId === serverId && d.namespace === namespace)
  if (marker) {
    marker.reason = reason
  } else {
    marker = { namespace, serverId, reason }
    mockDrains.push(marker)
  }
  recordAudit({
    namespace,
    action: 'drain',
    targetType: 'instance',
    targetRef: serverId,
    detail: reason || '标记排空',
  })
  return marker
}

export function undrainMockInstance(serverId: string, namespace: string): void {
  const idx = mockDrains.findIndex((d) => d.serverId === serverId && d.namespace === namespace)
  if (idx >= 0) mockDrains.splice(idx, 1)
  recordAudit({
    namespace,
    action: 'undrain',
    targetType: 'instance',
    targetRef: serverId,
    detail: '取消排空',
  })
}

// ---- 分组/Zone 视图 ----

export const mockZoneStats: ZoneStatView[] = [
  { group: 'server-a', zone: 'zone-01', serverCount: 2, onlineCount: 2 },
  { group: 'server-b', zone: 'zone-02', serverCount: 2, onlineCount: 2 },
  { group: 'server-a', zone: 'zone-03', serverCount: 0, onlineCount: 0 },
]

// zone 汇总：namespace 参数对齐 client 形参，演示数据不据此分簇；仅按 group 过滤。
export function getMockZoneStats(_namespace?: string, group?: string): ZoneStatView[] {
  let items = [...mockZoneStats]
  if (group) items = items.filter((z) => z.group === group)
  return items
}

// ---- 环境（可变） ----

export const mockNamespaces: NamespaceView[] = [
  { code: 'prod', name: '生产环境' },
  { code: 'test', name: '测试环境' },
]

// ---- 审计（可变：写操作经 recordAudit 追加） ----

export const mockAudits: AuditView[] = [
  {
    namespace: 'prod',
    operator: 'admin',
    action: 'publish',
    targetType: 'config',
    targetRef: 'game_config.yml',
    detail: '发布版本 v3',
    result: 'success',
    clientIp: '127.0.0.1',
    createdAt: ago(3600),
  },
  {
    namespace: 'prod',
    operator: 'developer',
    action: 'create',
    targetType: 'config',
    targetRef: 'db.properties',
    detail: '新建配置',
    result: 'success',
    clientIp: '127.0.0.1',
    createdAt: ago(7200),
  },
  {
    namespace: 'test',
    operator: 'admin',
    action: 'rollback',
    targetType: 'config',
    targetRef: 'game_config.yml',
    detail: '回滚到 v1',
    result: 'success',
    clientIp: '127.0.0.1',
    createdAt: ago(10800),
  },
  {
    namespace: 'prod',
    operator: 'admin',
    action: 'delete',
    targetType: 'config',
    targetRef: 'old_plugin.yml',
    detail: '软删',
    result: 'success',
    clientIp: '127.0.0.1',
    createdAt: ago(14400),
  },
  {
    namespace: 'prod',
    operator: 'admin',
    action: 'assign',
    targetType: 'zone',
    targetRef: 'server-01 → zone-01',
    detail: '指派 zone',
    result: 'success',
    clientIp: '127.0.0.1',
    createdAt: ago(18000),
  },
]

// 记一条审计（写操作 mutator 内部调用，使审计页 / 服务分析能看到新动作）。
function recordAudit(e: {
  namespace: string
  operator?: string
  action: string
  targetType: string
  targetRef: string
  detail: string
  result?: string
}): void {
  mockAudits.unshift({
    namespace: e.namespace,
    operator: e.operator ?? 'admin',
    action: e.action,
    targetType: e.targetType,
    targetRef: e.targetRef,
    detail: e.detail,
    result: e.result ?? 'success',
    clientIp: '127.0.0.1',
    createdAt: now(),
  })
}

export function getMockAudits(filter?: {
  namespace?: string
  operator?: string
  action?: string
  targetType?: string
  targetRef?: string
  detailKeyword?: string
  page?: number
  size?: number
}): AuditPage {
  let items = [...mockAudits]
  if (filter?.namespace) items = items.filter((a) => a.namespace === filter.namespace)
  if (filter?.operator) items = items.filter((a) => a.operator === filter.operator)
  if (filter?.action) items = items.filter((a) => a.action === filter.action)
  if (filter?.targetType) items = items.filter((a) => a.targetType === filter.targetType)
  if (filter?.targetRef) items = items.filter((a) => a.targetRef.includes(filter.targetRef!))
  if (filter?.detailKeyword) items = items.filter((a) => a.detail.includes(filter.detailKeyword!))
  const total = items.length
  const page = filter?.page ?? 1
  const size = filter?.size ?? (total || 1)
  const start = (page - 1) * size
  return { total, items: items.slice(start, start + size) }
}

// 服务分析聚合（FR-73）：从当前审计数据派生总数 / 成功 / 失败 + 按动作分布 + 每日趋势。
export function getMockAuditAnalytics(filter?: {
  namespace?: string
  from?: string
  to?: string
}): AuditAnalytics {
  let items = [...mockAudits]
  if (filter?.namespace) items = items.filter((a) => a.namespace === filter.namespace)
  const okCount = items.filter((a) => a.result === 'success').length
  const byActionMap = new Map<string, number>()
  for (const a of items) byActionMap.set(a.action, (byActionMap.get(a.action) ?? 0) + 1)
  const byAction = [...byActionMap.entries()]
    .map(([action, count]) => ({ action, count }))
    .sort((a, b) => b.count - a.count)
  const byDayMap = new Map<string, number>()
  for (const a of items) {
    const day = a.createdAt.slice(0, 10)
    byDayMap.set(day, (byDayMap.get(day) ?? 0) + 1)
  }
  const byDay = [...byDayMap.entries()]
    .map(([date, count]) => ({ date, count }))
    .sort((a, b) => a.date.localeCompare(b.date))
  return {
    from: filter?.from ?? ago(7 * 86400),
    to: filter?.to ?? now(),
    total: items.length,
    okCount,
    failCount: items.length - okCount,
    byAction,
    byDay,
  }
}

// ---- 告警事件（FR-89） ----

export const mockAlertEvents: AlertEventView[] = [
  {
    id: 3,
    type: 'health-transition',
    level: 'warning',
    serverId: 'server-04',
    namespace: 'prod',
    message: 'server-04 进入亚健康',
    detail: '35s 未心跳 > ttl 30s',
    createdAt: ago(1800),
  },
  {
    id: 2,
    type: 'health-transition',
    level: 'critical',
    serverId: 'server-02',
    namespace: 'prod',
    message: 'server-02 失联',
    detail: '60s 未心跳',
    createdAt: ago(5400),
  },
  {
    id: 1,
    type: 'health-transition',
    level: 'info',
    serverId: 'server-01',
    namespace: 'prod',
    message: 'server-01 恢复在线',
    detail: '心跳恢复',
    createdAt: ago(9000),
  },
]

export function getMockAlertEvents(filter?: {
  type?: string
  level?: string
  namespace?: string
  from?: string
  to?: string
  page?: number
  size?: number
}): AlertEventPage {
  let items = [...mockAlertEvents]
  if (filter?.type) items = items.filter((e) => e.type === filter.type)
  if (filter?.level) items = items.filter((e) => e.level === filter.level)
  if (filter?.namespace) items = items.filter((e) => e.namespace === filter.namespace)
  const total = items.length
  const page = filter?.page ?? 1
  const size = filter?.size ?? (total || 1)
  const start = (page - 1) * size
  return { total, items: items.slice(start, start + size) }
}

// ---- 文件树托管（可变：建 / 改 / 发布 / 回滚 / 删） ----

export const mockFiles: FileView[] = [
  {
    id: 1,
    namespace: 'prod',
    group: '__GLOBAL__',
    path: 'plugins/game/config.yml',
    scopeLevel: 'global',
    scopeTarget: '',
    version: 3,
    md5: 'abc12345',
    enabled: true,
    updatedAt: ago(3600),
    content: 'enabled: true\ncooldown: 30\n',
  },
  {
    id: 2,
    namespace: 'prod',
    group: '__GLOBAL__',
    path: 'plugins/game/db.properties',
    scopeLevel: 'global',
    scopeTarget: '',
    version: 2,
    md5: 'def67890',
    enabled: true,
    updatedAt: ago(7200),
    content: 'url=jdbc:mysql://localhost/game\npool=10\n',
  },
  {
    id: 3,
    namespace: 'prod',
    group: 'server-a',
    path: 'plugins/game/config.yml',
    scopeLevel: 'group',
    scopeTarget: '',
    version: 1,
    md5: 'aaa11111',
    enabled: true,
    updatedAt: ago(10800),
    content: 'enabled: true\ncooldown: 15\n',
  },
  // ===== test 环境受管配置：与 mock test-01 服务器同环境，使演示模式在 test 环境的配置中心开箱即有内容 =====
  // path 约定为「相对 plugins/ 根」（与真后端一致，受管树固定以 plugins 为根，不带 plugins/ 前缀避免双层）。
  {
    id: 4,
    namespace: 'test',
    group: '__GLOBAL__',
    path: 'Essentials/config.yml',
    scopeLevel: 'global',
    scopeTarget: '',
    version: 2,
    md5: 'e5e5e5e5',
    enabled: true,
    updatedAt: ago(1800),
    content: 'spawn-on-join: true\nmotd: 欢迎来到测试服\n',
  },
  {
    id: 5,
    namespace: 'test',
    group: '__GLOBAL__',
    path: 'WorldGuard/config.yml',
    scopeLevel: 'global',
    scopeTarget: '',
    version: 1,
    md5: 'f6f6f6f6',
    enabled: true,
    updatedAt: ago(5400),
    content: 'build-permission: false\nuse-region-flags: true\n',
  },
  {
    id: 6,
    namespace: 'test',
    group: '__GLOBAL__',
    path: 'Essentials/spawn.yml',
    scopeLevel: 'global',
    scopeTarget: '',
    version: 1,
    md5: 'a7a7a7a7',
    enabled: true,
    updatedAt: ago(3600),
    content: 'spawns:\n  default:\n    world: world\n    x: 0\n    y: 64\n    z: 0\n',
  },
]

// 文件版本历史（可变：发布 / 回滚追加版本）；按 fileId 索引。
const mockFileRevisions: Record<number, FileRevisionView[]> = {
  1: [
    {
      version: 3,
      md5: 'abc12345',
      operator: 'developer',
      comment: '更新',
      sourceRevision: null,
      createdAt: ago(3600),
      content: 'enabled: true\ncooldown: 30\n',
    },
    {
      version: 2,
      md5: 'bbb22222',
      operator: 'developer',
      comment: '调冷却',
      sourceRevision: null,
      createdAt: ago(7200),
      content: 'enabled: true\ncooldown: 20\n',
    },
    {
      version: 1,
      md5: 'aaa11111',
      operator: 'admin',
      comment: '初始',
      sourceRevision: null,
      createdAt: ago(10800),
      content: 'enabled: true\n',
    },
  ],
  2: [
    {
      version: 2,
      md5: 'def67890',
      operator: 'admin',
      comment: '更新',
      sourceRevision: null,
      createdAt: ago(7200),
      content: 'url=jdbc:mysql://localhost/game\npool=10\n',
    },
    {
      version: 1,
      md5: 'ccc33333',
      operator: 'admin',
      comment: '初始',
      sourceRevision: null,
      createdAt: ago(14400),
      content: 'url=jdbc:mysql://localhost/game\npool=5\n',
    },
  ],
  3: [
    {
      version: 1,
      md5: 'aaa11111',
      operator: 'admin',
      comment: '初始',
      sourceRevision: null,
      createdAt: ago(10800),
      content: 'enabled: true\ncooldown: 15\n',
    },
  ],
}

let fileIdSeq = Math.max(...mockFiles.map((f) => f.id))

export function getMockFileList(filter?: {
  namespace?: string
  group?: string
  path?: string
  scopeLevel?: string
}): FileView[] {
  let items = mockFiles.filter((f) => f.enabled)
  if (filter?.namespace) items = items.filter((f) => f.namespace === filter.namespace)
  if (filter?.group) items = items.filter((f) => f.group === filter.group)
  if (filter?.path) items = items.filter((f) => f.path === filter.path)
  if (filter?.scopeLevel) items = items.filter((f) => f.scopeLevel === filter.scopeLevel)
  return items.map((f) => ({ ...f, content: undefined }))
}

export function getMockFile(id: number): FileView | null {
  return mockFiles.find((f) => f.id === id) ?? null
}

export function getMockFileRevisions(id: number): FileRevisionView[] {
  return mockFileRevisions[id] ? [...mockFileRevisions[id]] : []
}

export function getMockFileRevision(id: number, version: number): FileRevisionView | null {
  return mockFileRevisions[id]?.find((r) => r.version === version) ?? null
}

/** 新建受管文件对象（首版），返回创建的文件视图 */
export function createMockFile(body: {
  namespace?: string
  group?: string
  path?: string
  scopeLevel?: string
  scopeTarget?: string
  content?: string
  comment?: string
}): FileView {
  const id = ++fileIdSeq
  const content = body.content ?? ''
  const ts = now()
  const file: FileView = {
    id,
    namespace: body.namespace ?? 'prod',
    group: body.group ?? '__GLOBAL__',
    path: body.path ?? 'plugins/new/config.yml',
    scopeLevel: body.scopeLevel ?? 'global',
    scopeTarget: body.scopeTarget ?? '',
    version: 1,
    md5: md5(content),
    enabled: true,
    updatedAt: ts,
    content,
  }
  mockFiles.push(file)
  mockFileRevisions[id] = [
    {
      version: 1,
      md5: file.md5,
      operator: 'admin',
      comment: body.comment ?? '新建',
      sourceRevision: null,
      createdAt: ts,
      content,
    },
  ]
  recordAudit({
    namespace: file.namespace,
    action: 'create',
    targetType: 'file',
    targetRef: file.path,
    detail: '新建文件',
  })
  return file
}

/** 保存受管文件（发布新版本）：内容写回 + 版本自增，返回发布结果（不存在返回 null） */
export function saveMockFile(id: number, content: string, comment = ''): PublishResult | null {
  const file = mockFiles.find((f) => f.id === id)
  if (!file) return null
  const ts = now()
  file.content = content
  file.version += 1
  file.md5 = md5(content)
  file.updatedAt = ts
  const revs = (mockFileRevisions[id] ??= [])
  revs.unshift({
    version: file.version,
    md5: file.md5,
    operator: 'admin',
    comment,
    sourceRevision: null,
    createdAt: ts,
    content,
  })
  recordAudit({
    namespace: file.namespace,
    action: 'publish',
    targetType: 'file',
    targetRef: file.path,
    detail: `发布版本 v${file.version}`,
  })
  return { version: file.version, md5: file.md5 }
}

/** 回滚受管文件到目标版本（指针前移），返回发布结果（不存在返回 null/字符串错误） */
export function rollbackMockFile(
  id: number,
  toVersion: number,
  comment: string,
): PublishResult | 'no-file' | 'no-version' {
  const file = mockFiles.find((f) => f.id === id)
  if (!file) return 'no-file'
  const target = mockFileRevisions[id]?.find((r) => r.version === toVersion)
  if (!target) return 'no-version'
  const ts = now()
  file.content = target.content
  file.version += 1
  file.md5 = md5(target.content ?? '')
  file.updatedAt = ts
  mockFileRevisions[id].unshift({
    version: file.version,
    md5: file.md5,
    operator: 'admin',
    comment: comment || `回滚到版本 ${toVersion}`,
    sourceRevision: toVersion,
    createdAt: ts,
    content: target.content,
  })
  recordAudit({
    namespace: file.namespace,
    action: 'rollback',
    targetType: 'file',
    targetRef: file.path,
    detail: `回滚到 v${toVersion}`,
  })
  return { version: file.version, md5: file.md5 }
}

/** 软删受管文件，成功返回 true、不存在返回 false */
export function deleteMockFile(id: number): boolean {
  const file = mockFiles.find((f) => f.id === id)
  if (!file) return false
  file.enabled = false
  recordAudit({
    namespace: file.namespace,
    action: 'delete',
    targetType: 'file',
    targetRef: file.path,
    detail: '软删',
  })
  return true
}

// ---- 覆盖集（FR-15，可变） ----

export const mockOverrideSets: OverrideSetView[] = [
  {
    id: 1,
    namespace: 'prod',
    group: '__GLOBAL__',
    name: '三方插件覆盖',
    scopeLevel: 'global',
    scopeTarget: '',
    targetRoot: '/plugins/third-party',
    reloadCommand: '/reload',
    mode: 'merge',
    version: 1,
    enabled: true,
    updatedAt: ago(3600),
  },
]

const mockOverrideSetRevisions: Record<number, OverrideSetRevisionView[]> = {
  1: [
    {
      version: 1,
      targetRoot: '/plugins/third-party',
      reloadCommand: '/reload',
      operator: 'admin',
      comment: '初始',
      sourceRevision: null,
      createdAt: ago(3600),
    },
  ],
}

export function getMockOverrideSetList(filter?: {
  namespace?: string
  group?: string
  scopeLevel?: string
}): OverrideSetView[] {
  let items = mockOverrideSets.filter((s) => s.enabled)
  if (filter?.namespace) items = items.filter((s) => s.namespace === filter.namespace)
  if (filter?.group) items = items.filter((s) => s.group === filter.group)
  if (filter?.scopeLevel) items = items.filter((s) => s.scopeLevel === filter.scopeLevel)
  return items
}

export function getMockOverrideSet(id: number): OverrideSetView | null {
  return mockOverrideSets.find((s) => s.id === id) ?? null
}

export function getMockOverrideSetRevisions(id: number): OverrideSetRevisionView[] {
  return mockOverrideSetRevisions[id] ? [...mockOverrideSetRevisions[id]] : []
}

/** 发布覆盖集新版本：targetRoot / reloadCommand 更新 + 版本自增，返回 {version,targetRoot}（不存在返回 null） */
export function publishMockOverrideSet(
  id: number,
  targetRoot: string,
  reloadCommand: string,
  comment: string,
): { version: number; targetRoot: string } | null {
  const os = mockOverrideSets.find((s) => s.id === id)
  if (!os) return null
  os.version += 1
  os.targetRoot = targetRoot
  os.reloadCommand = reloadCommand
  os.updatedAt = now()
  ;(mockOverrideSetRevisions[id] ??= []).unshift({
    version: os.version,
    targetRoot,
    reloadCommand,
    operator: 'admin',
    comment,
    sourceRevision: null,
    createdAt: os.updatedAt,
  })
  recordAudit({
    namespace: os.namespace,
    action: 'publish',
    targetType: 'override-set',
    targetRef: os.name,
    detail: `发布版本 v${os.version}`,
  })
  return { version: os.version, targetRoot }
}

/** 回滚覆盖集到目标版本，返回 {version,targetRoot}（不存在返回 null/字符串错误） */
export function rollbackMockOverrideSet(
  id: number,
  toVersion: number,
  comment: string,
): { version: number; targetRoot: string } | 'no-set' | 'no-version' {
  const os = mockOverrideSets.find((s) => s.id === id)
  if (!os) return 'no-set'
  const target = mockOverrideSetRevisions[id]?.find((r) => r.version === toVersion)
  if (!target) return 'no-version'
  os.version += 1
  os.targetRoot = target.targetRoot
  os.reloadCommand = target.reloadCommand
  os.updatedAt = now()
  mockOverrideSetRevisions[id].unshift({
    version: os.version,
    targetRoot: target.targetRoot,
    reloadCommand: target.reloadCommand,
    operator: 'admin',
    comment: comment || `回滚到版本 ${toVersion}`,
    sourceRevision: toVersion,
    createdAt: os.updatedAt,
  })
  recordAudit({
    namespace: os.namespace,
    action: 'rollback',
    targetType: 'override-set',
    targetRef: os.name,
    detail: `回滚到 v${toVersion}`,
  })
  return { version: os.version, targetRoot: os.targetRoot }
}

/** 软删覆盖集，成功返回 true、不存在返回 false */
export function deleteMockOverrideSet(id: number): boolean {
  const os = mockOverrideSets.find((s) => s.id === id)
  if (!os) return false
  os.enabled = false
  recordAudit({
    namespace: os.namespace,
    action: 'delete',
    targetType: 'override-set',
    targetRef: os.name,
    detail: '软删',
  })
  return true
}

export function getMockDryRun(id: number): OverrideSetDryRunView | null {
  const os = mockOverrideSets.find((s) => s.id === id)
  if (!os) return null
  const firstToken = os.reloadCommand.replace(/^\//, '').split(/\s+/)[0] ?? ''
  return {
    targetRoot: os.targetRoot,
    reloadCommand: os.reloadCommand,
    commandFirstToken: firstToken,
    memberPaths: [`${os.targetRoot}/lib.jar`, `${os.targetRoot}/config.yml`].map((p) =>
      p.replace(/^\//, ''),
    ),
  }
}

// ---- Zone 指派（FR-13，可变） ----

export const mockAssignments: AssignmentView[] = [
  {
    namespace: 'prod',
    serverId: 'server-01',
    group: 'server-a',
    zone: 'zone-01',
    note: '主力服',
    updatedAt: ago(3600),
  },
  {
    namespace: 'prod',
    serverId: 'server-02',
    group: 'server-a',
    zone: 'zone-01',
    note: '备用',
    updatedAt: ago(3600),
  },
  {
    namespace: 'prod',
    serverId: 'server-03',
    group: 'server-b',
    zone: 'zone-02',
    note: 'Bungee 入口',
    updatedAt: ago(3600),
  },
]

export function getMockAssignments(filter?: {
  namespace?: string
  group?: string
  zone?: string
}): AssignmentView[] {
  let items = [...mockAssignments]
  if (filter?.namespace) items = items.filter((a) => a.namespace === filter.namespace)
  if (filter?.group) items = items.filter((a) => a.group === filter.group)
  if (filter?.zone) items = items.filter((a) => a.zone === filter.zone)
  return items
}

/** 新增 / 改派 zone：落 / 改指派，并同步实例的 zone 派生字段，返回最新指派视图 */
export function assignMockZone(body: {
  namespace: string
  serverId: string
  group: string
  zone: string
  note: string
}): AssignmentView {
  const ts = now()
  let a = mockAssignments.find(
    (x) => x.namespace === body.namespace && x.serverId === body.serverId,
  )
  if (a) {
    a.group = body.group
    a.zone = body.zone
    a.note = body.note
    a.updatedAt = ts
  } else {
    a = { ...body, updatedAt: ts }
    mockAssignments.push(a)
  }
  // 同步实例派生 zone / assigned（让服务器页与拓扑反映改派）
  const inst = mockInstances.find(
    (i) => i.namespace === body.namespace && i.serverId === body.serverId,
  )
  if (inst) {
    inst.group = body.group
    inst.zone = body.zone
    inst.assigned = true
  }
  recordAudit({
    namespace: body.namespace,
    action: 'assign',
    targetType: 'zone',
    targetRef: `${body.serverId} → ${body.zone}`,
    detail: '指派 zone',
  })
  return a
}

/** 取消 zone 指派：移除指派 + 复位实例 zone */
export function unassignMockZone(namespace: string, serverId: string): void {
  const idx = mockAssignments.findIndex((a) => a.namespace === namespace && a.serverId === serverId)
  if (idx >= 0) mockAssignments.splice(idx, 1)
  const inst = mockInstances.find((i) => i.namespace === namespace && i.serverId === serverId)
  if (inst) {
    inst.zone = null
    inst.assigned = false
  }
  recordAudit({
    namespace,
    action: 'unassign',
    targetType: 'zone',
    targetRef: serverId,
    detail: '取消指派',
  })
}

// ---- 可观测看板（FR-32 / FR-34 / FR-43）----

// bungee 角色编码：bc 代理不计入「真实玩家」与平均口径，与后端 metric_aggregate 同口径（FR-43）。
const ROLE_BUNGEE = 'bungee'
// 平均内存 / CPU 不在实例视图字段内，dev mock 用代表性常量呈现（仅可视化用，不追求真实采样）。
const MOCK_AVG_MEM_USED = 2 * 1024 * 1024 * 1024 // 2 GiB
const MOCK_AVG_MEM_MAX = 4 * 1024 * 1024 * 1024 // 4 GiB
const MOCK_AVG_CPU_LOAD = 0.35 // 35%

// bc（bungee）维度聚合：仅对在线代理的 proxy 指标求和 / 取均值（FR-34）。
function summarizeMockBC(online: InstanceView[]): BCSummary {
  const proxies = online.filter((i) => i.role === ROLE_BUNGEE)
  if (proxies.length === 0) {
    return {
      proxyCount: 0,
      totalConnections: 0,
      avgThreadCount: 0,
      backendUp: 0,
      backendTotal: 0,
      avgBackendLatencyMs: -1,
    }
  }
  const totalConnections = proxies.reduce((s, p) => s + p.proxy.onlineConnections, 0)
  const avgThreadCount = proxies.reduce((s, p) => s + p.proxy.threadCount, 0) / proxies.length
  const backendUp = proxies.reduce((s, p) => s + p.proxy.backendUp, 0)
  const backendTotal = proxies.reduce((s, p) => s + p.proxy.backendTotal, 0)
  const sampled = proxies.filter((p) => p.proxy.backendAvgLatencyMs >= 0)
  const avgBackendLatencyMs =
    sampled.length > 0
      ? sampled.reduce((s, p) => s + p.proxy.backendAvgLatencyMs, 0) / sampled.length
      : -1
  return {
    proxyCount: proxies.length,
    totalConnections,
    avgThreadCount,
    backendUp,
    backendTotal,
    avgBackendLatencyMs,
  }
}

// 当前快照聚合：从在线实例派生（namespace 为空时聚合全部环境），与后端 Summarize 同口径。
export function getMockMetricsSummary(namespace?: string): MetricsSummary {
  const online = mockInstances.filter(
    (i) => i.status === 'online' && (!namespace || i.namespace === namespace),
  )
  const servers: ServerPlayers[] = online
    .map((i) => ({
      serverId: i.serverId,
      role: i.role,
      playerCount: i.role === ROLE_BUNGEE ? i.proxy.onlineConnections : i.playerCount,
    }))
    .sort((a, b) => a.serverId.localeCompare(b.serverId))

  const bukkits = online.filter((i) => i.role !== ROLE_BUNGEE)
  const bukkitCount = bukkits.length
  const totalPlayers = bukkits.reduce((s, i) => s + i.playerCount, 0)
  const avgTps = bukkitCount > 0 ? bukkits.reduce((s, i) => s + i.tps, 0) / bukkitCount : 0

  return {
    totalPlayers,
    onlineServers: online.length,
    servers,
    avgTps,
    avgMemUsed: bukkitCount > 0 ? MOCK_AVG_MEM_USED : 0,
    avgMemMax: bukkitCount > 0 ? MOCK_AVG_MEM_MAX : 0,
    avgCpuLoad: bukkitCount > 0 ? MOCK_AVG_CPU_LOAD : -1,
    cpuSampleCount: bukkitCount,
    bc: summarizeMockBC(online),
  }
}

const TREND_WINDOW_SECONDS: Record<string, number> = { '1h': 3600, '6h': 21600, '24h': 86400 }
const TREND_POINTS = 24

// 历史趋势：围绕当前快照做平滑正弦波动（确定性，避免每次刷新跳变），仅返回负载数字。
export function getMockTrend(namespace: string | undefined, window: string): MetricsTrend {
  const span = TREND_WINDOW_SECONDS[window] ?? TREND_WINDOW_SECONDS['1h']
  const step = span / (TREND_POINTS - 1)
  const base = getMockMetricsSummary(namespace)
  const cpuAvailable = base.avgCpuLoad >= 0
  const points: TrendPoint[] = []
  for (let i = 0; i < TREND_POINTS; i++) {
    const wave = Math.sin((i / TREND_POINTS) * Math.PI * 2)
    points.push({
      sampledAt: ago((TREND_POINTS - 1 - i) * step),
      totalPlayers: Math.max(0, Math.round(base.totalPlayers * (1 + wave * 0.15))),
      avgTps:
        base.avgTps > 0
          ? Number(Math.min(20, Math.max(0, base.avgTps + wave * 0.3)).toFixed(2))
          : 0,
      avgMemUsed: Math.max(0, Math.round(base.avgMemUsed * (1 + wave * 0.1))),
      avgMemMax: base.avgMemMax,
      avgCpuLoad: cpuAvailable
        ? Number(Math.min(1, Math.max(0, base.avgCpuLoad + wave * 0.1)).toFixed(3))
        : -1,
    })
  }
  return { points }
}

// 发布影响面预览（FR-79）：按某条 scope 算此刻会落到的在线子服集合。
export function getMockImpact(params: {
  namespace: string
  scopeLevel: string
  group?: string
  scopeTarget?: string
}): ImpactView {
  const online = mockInstances.filter(
    (i) => i.namespace === params.namespace && i.status === 'online',
  )
  let affected: string[]
  if (params.scopeLevel === 'global') {
    affected = online.map((i) => i.serverId)
  } else if (params.scopeLevel === 'group') {
    affected = online.filter((i) => i.group === params.group).map((i) => i.serverId)
  } else if (params.scopeLevel === 'zone') {
    affected = online.filter((i) => i.zone === params.scopeTarget).map((i) => i.serverId)
  } else {
    affected = online.filter((i) => i.serverId === params.scopeTarget).map((i) => i.serverId)
  }
  affected = affected.sort((a, b) => a.localeCompare(b))
  return {
    namespace: params.namespace,
    scopeLevel: params.scopeLevel,
    group: params.group ?? '',
    scopeTarget: params.scopeTarget ?? '',
    affected,
    total: affected.length,
  }
}

// ---- 控制面自身状态（FR-33）----

export function getMockSystemStatus(): SystemStatusView {
  const onlineInstances = mockInstances.filter((i) => i.status === 'online').length
  const uptimeSeconds = 18000 // 5 小时
  return {
    version: 'dev-mock',
    startedAt: ago(uptimeSeconds),
    uptimeSeconds,
    db: { connected: true },
    onlineInstances,
    samplerEnabled: true,
    runtime: { goroutines: 48, heapAlloc: 32 * 1024 * 1024, heapSys: 64 * 1024 * 1024 },
    cpuAvailable: true,
    cpuPercent: 12.5,
  }
}

// ---- 控制面自观测（FR-82）----

export function getMockObservability(): ObservabilityView {
  const registryByStatus: Record<string, number> = {}
  for (const i of mockInstances) {
    registryByStatus[i.status] = (registryByStatus[i.status] ?? 0) + 1
  }
  const commandByStatus: Record<string, number> = {}
  for (const c of mockCommands) {
    commandByStatus[c.status] = (commandByStatus[c.status] ?? 0) + 1
  }
  return {
    dbPool: {
      maxOpenConnections: 20,
      openConnections: 5,
      inUse: 2,
      idle: 3,
      waitCount: 0,
      waitDurationMs: 0,
    },
    longpoll: { config: 3, file: 1, topology: 0, command: 2, total: 6 },
    registryByStatus,
    registryTotal: mockInstances.length,
    commandByStatus,
  }
}

// ---- 集群拓扑（FR-37）----

const TOPOLOGY_AVAILABLE = new Set(['online', 'degraded'])

export function buildMockTopology(namespace: string): TopologyView {
  const avail = mockInstances.filter(
    (i) => i.namespace === namespace && TOPOLOGY_AVAILABLE.has(i.status),
  )
  const index = new Set(avail.map((i) => i.serverId))

  const nodes: TopologyNode[] = avail
    .map((i) => ({
      serverId: i.serverId,
      role: i.role,
      group: i.group,
      zone: i.zone,
      status: i.status,
      address: i.address,
    }))
    .sort((a, b) => a.serverId.localeCompare(b.serverId))

  const edges: TopologyEdge[] = []
  for (const i of avail) {
    if (i.role !== ROLE_BUNGEE) continue
    for (const backend of i.backends) {
      if (index.has(backend)) edges.push({ source: i.serverId, target: backend })
    }
  }
  edges.sort((a, b) =>
    a.source === b.source ? a.target.localeCompare(b.target) : a.source.localeCompare(b.source),
  )

  const bucket = new Map<string, TopologyGroup>()
  for (const i of avail) {
    const key = `${i.group} ${i.zone ?? ''}`
    let g = bucket.get(key)
    if (!g) {
      g = { group: i.group, zone: i.zone, members: [] }
      bucket.set(key, g)
    }
    g.members.push(i.serverId)
  }
  const groups: TopologyGroup[] = [...bucket.values()]
    .map((g) => ({ ...g, members: [...g.members].sort((a, b) => a.localeCompare(b)) }))
    .sort((a, b) =>
      a.group === b.group
        ? (a.zone ?? '').localeCompare(b.zone ?? '')
        : a.group.localeCompare(b.group),
    )

  return { namespace, nodes, edges, groups }
}

// ---- 小区默认入口（FR-48）----

export const mockDefaultEntries: DefaultEntryView[] = [
  {
    namespace: 'prod',
    group: 'server-a',
    zone: 'zone-01',
    defaultServerId: 'server-01',
    updatedAt: ago(3600),
  },
  {
    namespace: 'prod',
    group: 'server-b',
    zone: 'zone-02',
    defaultServerId: 'server-04',
    updatedAt: ago(3600),
  },
]

export function getMockDefaultEntries(filter?: {
  namespace?: string
  group?: string
}): DefaultEntryView[] {
  let items = [...mockDefaultEntries]
  if (filter?.namespace) items = items.filter((e) => e.namespace === filter.namespace)
  if (filter?.group) items = items.filter((e) => e.group === filter.group)
  return items
}

// ---- API 密钥（FR-42，可变） ----

let apiKeySeq = 1

export const mockApiKeys: ApiKeyCreated[] = [
  {
    id: apiKeySeq++,
    name: '业务管理后端',
    role: 'readonly',
    keyPrefix: 'bk_demo01',
    status: 'active',
    createdAt: ago(86400),
    expiresAt: null,
    lastUsedAt: ago(3600),
    key: '',
  },
]

// 列表 / 元数据剥离明文 key（后端绝不返回明文）
export function stripApiKeySecret(k: ApiKeyCreated): Omit<ApiKeyCreated, 'key'> {
  const { key: _key, ...rest } = k
  return rest
}

export function createMockApiKey(
  name: string,
  role: string,
  expiresAt: string | null,
): ApiKeyCreated {
  const key = 'bk_' + Math.random().toString(36).slice(2, 14)
  const created: ApiKeyCreated = {
    id: apiKeySeq++,
    name,
    role,
    keyPrefix: key.slice(0, 9),
    status: 'active',
    createdAt: now(),
    expiresAt,
    lastUsedAt: null,
    key,
  }
  mockApiKeys.unshift(created)
  return created
}

export function resetMockApiKey(id: number): ApiKeyCreated | null {
  const k = mockApiKeys.find((x) => x.id === id)
  if (!k || k.status === 'revoked') return null
  k.key = 'bk_' + Math.random().toString(36).slice(2, 14)
  k.keyPrefix = k.key.slice(0, 9)
  k.lastUsedAt = null
  return k
}

export function revokeMockApiKey(id: number): boolean {
  const k = mockApiKeys.find((x) => x.id === id)
  if (!k || k.status === 'revoked') return false
  k.status = 'revoked'
  return true
}

// ---- agent 命令（FR-104，可变：反向抓取 / 拓印 / 取日志 / 重同步 触发即建命令） ----

let commandSeq = 1000

export const mockCommands: CommandMetaView[] = [
  {
    commandId: commandSeq++,
    namespace: 'prod',
    serverId: 'server-01',
    type: 'resync-config',
    status: 'done',
    resultDetail: '已重同步',
    operator: 'admin',
    createdAt: ago(3600),
    updatedAt: ago(3590),
    ageSeconds: 3600,
  },
  {
    commandId: commandSeq++,
    namespace: 'prod',
    serverId: 'server-04',
    type: 'ingest-plugins',
    status: 'failed',
    resultDetail: 'agent 回传超时',
    operator: 'admin',
    createdAt: ago(7200),
    updatedAt: ago(7100),
    ageSeconds: 7200,
  },
  {
    commandId: commandSeq++,
    namespace: 'prod',
    serverId: 'server-02',
    type: 'tail-logs',
    status: 'pending',
    resultDetail: '',
    operator: 'admin',
    createdAt: ago(30),
    updatedAt: ago(30),
    ageSeconds: 30,
  },
]

/** 建一条 agent 命令（反向抓取 / 拓印 / 取日志 / 重同步 共用），返回 AgentCommandView */
export function createMockCommand(
  serverId: string,
  namespace: string,
  type: string,
  status: string,
): AgentCommandView {
  const id = commandSeq++
  const ts = now()
  mockCommands.unshift({
    commandId: id,
    namespace,
    serverId,
    type,
    status,
    resultDetail: '',
    operator: 'admin',
    createdAt: ts,
    updatedAt: ts,
    ageSeconds: 0,
  })
  return { id, namespace, serverId, type, status, createdAt: ts, updatedAt: ts }
}

export function getMockCommands(filter?: {
  namespace?: string
  serverId?: string
  type?: string
  status?: string
  page?: number
  size?: number
}): CommandPage {
  let items = [...mockCommands]
  if (filter?.namespace) items = items.filter((c) => c.namespace === filter.namespace)
  if (filter?.serverId) items = items.filter((c) => c.serverId === filter.serverId)
  if (filter?.type) items = items.filter((c) => c.type === filter.type)
  if (filter?.status) items = items.filter((c) => c.status === filter.status)
  const total = items.length
  const page = filter?.page ?? 1
  const size = filter?.size ?? (total || 1)
  const start = (page - 1) * size
  return { total, items: items.slice(start, start + size) }
}

export function getMockCommandAnalytics(filter?: {
  namespace?: string
  from?: string
  to?: string
}): CommandAnalytics {
  let items = [...mockCommands]
  if (filter?.namespace) items = items.filter((c) => c.namespace === filter.namespace)
  const byStatusMap = new Map<string, number>()
  const byTypeMap = new Map<string, number>()
  const byServerMap = new Map<string, number>()
  for (const c of items) {
    byStatusMap.set(c.status, (byStatusMap.get(c.status) ?? 0) + 1)
    byTypeMap.set(c.type, (byTypeMap.get(c.type) ?? 0) + 1)
    byServerMap.set(c.serverId, (byServerMap.get(c.serverId) ?? 0) + 1)
  }
  const byDayMap = new Map<string, { issued: number; done: number; failed: number }>()
  for (const c of items) {
    const day = c.createdAt.slice(0, 10)
    const e = byDayMap.get(day) ?? { issued: 0, done: 0, failed: 0 }
    e.issued++
    if (c.status === 'done') e.done++
    if (c.status === 'failed') e.failed++
    byDayMap.set(day, e)
  }
  return {
    from: filter?.from ?? ago(7 * 86400),
    to: filter?.to ?? now(),
    total: items.length,
    byStatus: [...byStatusMap.entries()].map(([status, count]) => ({ status, count })),
    byType: [...byTypeMap.entries()].map(([type, count]) => ({ type, count })),
    byServer: [...byServerMap.entries()]
      .map(([serverId, count]) => ({ serverId, count }))
      .sort((a, b) => b.count - a.count),
    byDay: [...byDayMap.entries()]
      .map(([date, e]) => ({ date, ...e }))
      .sort((a, b) => a.date.localeCompare(b.date)),
  }
}

// ---- agent 取日志（FR-88）----

export function getMockAgentLogs(serverId: string): AgentLogView {
  return {
    commandId: commandSeq,
    status: 'done',
    lines: [
      { level: 'INFO', text: `[${serverId}] 服务器启动完成` },
      { level: 'INFO', text: '加载配置：plugins/game/config.yml' },
      { level: 'WARN', text: '检测到旧版数据格式，已自动迁移' },
      { level: 'ERROR', text: '连接 ***（已脱敏）失败，重试中' },
    ],
  }
}

// ---- 反向抓取受管任务 + 冲突 + 忽略规则（FR-58/59，可变） ----

let taskSeq = 5000

export const mockReverseFetchTasks: ReverseFetchTaskView[] = [
  {
    id: taskSeq++,
    namespace: 'prod',
    serverId: 'server-01',
    scope: 'group',
    group: 'server-a',
    target: '',
    status: 'pending-review',
    scanCommandId: 0,
    submitCommandId: 0,
    totalFiles: 4,
    selectedCount: 0,
    overThresholdCount: 1,
    skippedCount: 0,
    files: [
      {
        path: 'Essentials/config.yml',
        size: 12400,
        isText: true,
        overThreshold: false,
        ignoredByRule: false,
      },
      {
        path: 'Essentials/kits.yml',
        size: 3100,
        isText: true,
        overThreshold: false,
        ignoredByRule: false,
      },
      {
        path: 'WorldGuard/regions.yml',
        size: 88000,
        isText: true,
        overThreshold: true,
        ignoredByRule: false,
      },
      { path: 'spawn.yml', size: 1800, isText: true, overThreshold: false, ignoredByRule: false },
    ],
    selectedPaths: [],
    operator: 'admin',
    note: '',
    lastError: '',
    createdAt: ago(600),
    updatedAt: ago(600),
    elapsedSec: 600,
  },
]

export function listMockReverseFetchTasks(filter?: {
  namespace?: string
  serverId?: string
  status?: string
}): ReverseFetchTaskView[] {
  let items = [...mockReverseFetchTasks]
  if (filter?.namespace) items = items.filter((t) => t.namespace === filter.namespace)
  if (filter?.serverId) items = items.filter((t) => t.serverId === filter.serverId)
  if (filter?.status) items = items.filter((t) => t.status === filter.status)
  return items.sort((a, b) => b.id - a.id)
}

export function getMockReverseFetchTask(id: number): ReverseFetchTaskView | null {
  return mockReverseFetchTasks.find((t) => t.id === id) ?? null
}

/** 建扫描任务（FR-58）：mock 直接落 pending-review 态（省去 agent 回传往返），返回任务视图 */
export function createMockReverseFetchTask(
  serverId: string,
  namespace: string,
  body: { scope?: string; group?: string; target?: string },
): ReverseFetchTaskView {
  const id = taskSeq++
  const ts = now()
  const task: ReverseFetchTaskView = {
    id,
    namespace,
    serverId,
    scope: body.scope ?? 'group',
    group: body.group ?? '',
    target: body.target ?? '',
    status: 'pending-review',
    scanCommandId: createMockCommand(serverId, namespace, 'ingest-plugins', 'fetched').id,
    submitCommandId: 0,
    totalFiles: 2,
    selectedCount: 0,
    overThresholdCount: 0,
    skippedCount: 0,
    files: [
      {
        path: 'Essentials/config.yml',
        size: 12400,
        isText: true,
        overThreshold: false,
        ignoredByRule: false,
      },
      { path: 'spawn.yml', size: 1800, isText: true, overThreshold: false, ignoredByRule: false },
    ],
    selectedPaths: [],
    operator: 'admin',
    note: '',
    lastError: '',
    createdAt: ts,
    updatedAt: ts,
    elapsedSec: 0,
  }
  mockReverseFetchTasks.unshift(task)
  recordAudit({
    namespace,
    action: 'reverse-fetch',
    targetType: 'instance',
    targetRef: serverId,
    detail: '发起反向抓取扫描',
  })
  return task
}

/** 提交选定集（FR-58）：mock 流转到 done（无冲突路径简化），返回任务视图 */
export function submitMockReverseFetchTask(
  id: number,
  selectedPaths: string[],
): ReverseFetchTaskView | null {
  const task = mockReverseFetchTasks.find((t) => t.id === id)
  if (!task) return null
  task.selectedPaths = selectedPaths
  task.selectedCount = selectedPaths.length
  task.status = 'done'
  task.updatedAt = now()
  recordAudit({
    namespace: task.namespace,
    action: 'reverse-fetch',
    targetType: 'instance',
    targetRef: task.serverId,
    detail: `提交选定 ${selectedPaths.length} 个文件`,
  })
  return task
}

export function cancelMockReverseFetchTask(id: number): ReverseFetchTaskView | null {
  const task = mockReverseFetchTasks.find((t) => t.id === id)
  if (!task) return null
  task.status = 'cancelled'
  task.updatedAt = now()
  return task
}

// 列某任务的冲突 path（FR-59）：mock 对处于 conflict-review 的任务给两个示意冲突文件，否则空。
export function getMockConflicts(id: number): string[] {
  const task = mockReverseFetchTasks.find((t) => t.id === id)
  if (!task || task.status !== 'conflict-review') return []
  return ['Essentials/config.yml', 'spawn.yml']
}

// 冲突审核落库（FR-59）：mock 统计 overwrite 数为 updated，流转任务到 done。
export function resolveMockConflicts(
  id: number,
  decisions: Array<{ path: string; action: string }>,
): { created: number; updated: number } | null {
  const task = mockReverseFetchTasks.find((t) => t.id === id)
  if (!task) return null
  const updated = decisions.filter((d) => d.action === 'overwrite').length
  task.status = 'done'
  task.updatedAt = now()
  recordAudit({
    namespace: task.namespace,
    action: 'reverse-fetch',
    targetType: 'instance',
    targetRef: task.serverId,
    detail: `冲突审核落库（覆盖 ${updated}）`,
  })
  return { created: 0, updated }
}

// 冲突 diff（FR-59）：mock 构造单文件抓取值 ⟷ 已有版本差异（按 taskId+path 派生确定性内容）。
export function getMockConflictDiff(taskId: number, path: string): ConflictDiffView {
  return {
    path,
    fetchedContent: `# ${path}（抓取值·任务 ${taskId}）\nmax-players: 200\n`,
    fetchedMd5: md5(`fetched:${taskId}:${path}`),
    existingContent: `# ${path}（目标已有）\nmax-players: 100\n`,
    existingMd5: md5(`existing:${taskId}:${path}`),
    version: 1,
  }
}

// 持久忽略规则（FR-59，可变）
let ignoreRuleSeq = 7000

export const mockIgnoreRules: IgnoreRuleView[] = [
  {
    id: ignoreRuleSeq++,
    namespace: 'prod',
    scope: 'group',
    group: 'server-a',
    target: '',
    ruleType: 'prefix',
    pattern: 'WorldGuard/',
    comment: '区域数据不入库',
    operator: 'admin',
    createdAt: ago(86400),
  },
]

export function listMockIgnoreRules(filter?: {
  namespace?: string
  scope?: string
  group?: string
  target?: string
}): IgnoreRuleView[] {
  let items = [...mockIgnoreRules]
  if (filter?.namespace) items = items.filter((r) => r.namespace === filter.namespace)
  if (filter?.scope) items = items.filter((r) => r.scope === filter.scope)
  if (filter?.group) items = items.filter((r) => r.group === filter.group)
  if (filter?.target) items = items.filter((r) => r.target === filter.target)
  return items
}

export function createMockIgnoreRule(body: {
  namespace: string
  scope: string
  group: string
  target?: string
  ruleType: string
  pattern: string
  comment?: string
}): IgnoreRuleView {
  const rule: IgnoreRuleView = {
    id: ignoreRuleSeq++,
    namespace: body.namespace,
    scope: body.scope,
    group: body.group,
    target: body.target ?? '',
    ruleType: body.ruleType,
    pattern: body.pattern,
    comment: body.comment ?? '',
    operator: 'admin',
    createdAt: now(),
  }
  mockIgnoreRules.unshift(rule)
  return rule
}

export function deleteMockIgnoreRule(id: number): boolean {
  const idx = mockIgnoreRules.findIndex((r) => r.id === id)
  if (idx === -1) return false
  mockIgnoreRules.splice(idx, 1)
  return true
}

// ---- 运维设置（FR-61/62，可变） ----

export const mockSettings: SettingView[] = [
  {
    key: 'health.scan-interval-sec',
    value: '5',
    valueType: 'int',
    default: '5',
    desc: '健康扫描周期（秒）',
    isStartup: false,
  },
  {
    key: 'health.ttl-sec',
    value: '30',
    valueType: 'int',
    default: '30',
    desc: '心跳失联判定阈值（秒）',
    isStartup: false,
  },
  {
    key: 'longpoll.timeout-sec',
    value: '30',
    valueType: 'int',
    default: '30',
    desc: '配置长轮询挂起超时（秒）',
    isStartup: false,
  },
  {
    key: 'metrics.sampler-enabled',
    value: 'true',
    valueType: 'bool',
    default: 'true',
    desc: '是否启用指标采样器',
    isStartup: false,
  },
]

export function listMockSettings(): SettingView[] {
  return [...mockSettings]
}

/** 改单个设置项（FR-61）：白名单内才改、值写回，返回是否命中白名单 */
export function updateMockSetting(key: string, value: string): boolean {
  const s = mockSettings.find((x) => x.key === key)
  if (!s) return false
  s.value = value
  recordAudit({
    namespace: '',
    action: 'update-setting',
    targetType: 'setting',
    targetRef: key,
    detail: `改设置 ${key}=${value}`,
  })
  return true
}

// ---- 配置操作级撤回（FR-116，可变） ----
// 撤回账目由控制面写操作派生，mock 仅给两条种子供工作台操作日志演示（无新增 mutator，故用字面 id）。

export const mockReversibleOps: ReversibleOpView[] = [
  {
    id: 9000,
    namespace: 'prod',
    opType: 'publish',
    scope: 'global',
    scopeTarget: '',
    status: 'reversible',
    summary: '发布 game_config.yml 至 v3',
    operator: 'admin',
    reversedBy: '',
    createdAt: ago(1800),
    reversible: true,
  },
  {
    id: 9001,
    namespace: 'prod',
    opType: 'fetch',
    scope: 'group',
    scopeTarget: 'server-a',
    status: 'reversible',
    summary: '反向抓取纳管 4 个文件',
    operator: 'admin',
    reversedBy: '',
    createdAt: ago(3600),
    reversible: true,
  },
]

export function listMockReversibleOps(filter?: {
  namespace?: string
  opType?: string
  status?: string
  limit?: number
}): ReversibleOpView[] {
  let items = [...mockReversibleOps]
  if (filter?.namespace) items = items.filter((o) => o.namespace === filter.namespace)
  if (filter?.opType) items = items.filter((o) => o.opType === filter.opType)
  if (filter?.status) items = items.filter((o) => o.status === filter.status)
  items = items.sort((a, b) => b.id - a.id)
  if (filter?.limit) items = items.slice(0, filter.limit)
  return items
}

/** 撤回一条可逆操作（幂等）：置 reversed，返回最新账目（不存在返回 null） */
export function undoMockReversibleOp(id: number): ReversibleOpView | null {
  const op = mockReversibleOps.find((o) => o.id === id)
  if (!op) return null
  op.status = 'reversed'
  op.reversible = false
  op.reversedBy = 'admin'
  recordAudit({
    namespace: op.namespace,
    action: 'undo',
    targetType: 'reversible-op',
    targetRef: String(id),
    detail: '撤回操作',
  })
  return op
}

// ---- agent 只读文件浏览（FR-110）----

const mockBrowseTree = {
  path: '',
  dir: true,
  size: 4096,
  children: [
    {
      name: 'Essentials',
      dir: true,
      size: 4096,
      isText: false,
      overThreshold: false,
      children: [
        { name: 'config.yml', dir: false, size: 12400, isText: true, overThreshold: false },
        { name: 'kits.yml', dir: false, size: 3100, isText: true, overThreshold: false },
        {
          name: 'userdata',
          dir: true,
          size: 4096,
          isText: false,
          overThreshold: false,
          children: [],
        },
      ],
    },
    {
      name: 'WorldGuard',
      dir: true,
      size: 4096,
      isText: false,
      overThreshold: false,
      children: [
        { name: 'config.yml', dir: false, size: 6000, isText: true, overThreshold: false },
        { name: 'regions.yml', dir: false, size: 88000, isText: true, overThreshold: false },
      ],
    },
    { name: 'spawn.yml', dir: false, size: 1800, isText: true, overThreshold: false },
    { name: 'motd.yml', dir: false, size: 400, isText: true, overThreshold: false },
    { name: 'bukkit.yml', dir: false, size: 2200, isText: true, overThreshold: false },
    { name: 'server.properties', dir: false, size: 1100, isText: true, overThreshold: false },
  ],
}

const mockBrowseFiles: Record<string, string> = {
  'spawn.yml':
    '# 出生点配置（服务器现状）\nspawns:\n  default: { world: world, x: 120.0, y: 64.0, z: -64.5 }\nrespawn-at-spawn: false\n',
  'motd.yml': "# 服务器 MOTD（服务器现状）\nmotd: '&b欢迎'\nmax-players: 100\n",
}

export function getMockBrowse(op: string, path: string): unknown {
  if (op === 'file') {
    const base = path.split('/').pop() ?? path
    return {
      path,
      content: mockBrowseFiles[base] ?? `# ${path}（服务器现状·示意）\nkey: value\n`,
      truncated: false,
    }
  }
  if (op === 'list') {
    const entries = mockBrowseTree.children.map(({ children: _children, ...rest }) => rest)
    return {
      path,
      entries,
      offset: 0,
      limit: entries.length,
      total: entries.length,
      hasMore: false,
    }
  }
  return mockBrowseTree
}

// ---- 拓印（FR-46）：mock 直接 ready，便于审核台走通 diff/confirm ----

export function makeMockImprintCommand(serverId: string, namespace: string): AgentCommandView {
  const cmd = createMockCommand(serverId, namespace, 'ingest-plugins', 'ready')
  return cmd
}

export function getMockImprintDiff(): {
  path: string
  actualContent: string
  actualMd5: string
  expectedContent: string
  expectedMd5: string
  expectedWholeFile: boolean
  expectedSources: Array<{ path: string[]; scope: string }>
  expectedDeletions: Array<{ path: string[]; scope: string }>
  differs: boolean
} {
  return {
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
  }
}
