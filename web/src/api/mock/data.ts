/**
 * 配置中心 mock 数据工厂
 *
 * 生成符合后端 API 契约的模拟数据，用于前端独立验证。
 * 所有类型严格对齐 web/src/api/types.ts。
 */

import type {
  AuditView, AuditPage, ConfigView, DiffView, FileRevisionView, FileView,
  InstanceView, NamespaceView, OverrideSetDryRunView, OverrideSetRevisionView,
  OverrideSetView, RevisionView, ZoneStatView, AssignmentView,
} from '../types'

// ---- 工具函数 ----

function md5(s: string): string {
  // 简单的伪 md5（仅做演示，不追求密码学安全）
  let h = 0
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) - h + s.charCodeAt(i)) | 0
  }
  return Math.abs(h).toString(16).padStart(8, '0')
}

function ago(seconds: number): string {
  return new Date(Date.now() - seconds * 1000).toISOString()
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

// ---- 数据集 ----

export const mockConfigs: MockConfig[] = [
  // prod 环境
  makeMockConfig(1, 'prod', '__GLOBAL__', 'game_config.yml', 'global', '', 'yaml', [YAML_CONTENT_BASE, YAML_CONTENT_V2, YAML_CONTENT_V3], 'admin'),
  makeMockConfig(2, 'prod', '__GLOBAL__', 'db.properties', 'global', '', 'properties', [PROPERTIES_BASE, PROPERTIES_V2], 'admin'),
  makeMockConfig(3, 'prod', 'server-a', 'game_config.yml', 'group', '', 'yaml', [YAML_CONTENT_BASE, YAML_CONTENT_V2], 'developer'),
  makeMockConfig(4, 'prod', 'server-a', 'server.json', 'zone', 'zone-01', 'json', [JSON_BASE, JSON_V2], 'admin'),
  makeMockConfig(5, 'prod', 'server-b', 'game_config.yml', 'group', '', 'yaml', [YAML_CONTENT_BASE], 'developer'),

  // test 环境
  makeMockConfig(6, 'test', '__GLOBAL__', 'game_config.yml', 'global', '', 'yaml', [YAML_CONTENT_BASE, YAML_CONTENT_V2], 'admin'),
  makeMockConfig(7, 'test', '__GLOBAL__', 'db.properties', 'global', '', 'properties', [PROPERTIES_BASE], 'developer'),
  makeMockConfig(8, 'test', 'server-a', 'game_config.yml', 'group', '', 'yaml', [YAML_CONTENT_BASE], 'admin'),
]

// ---- 导出便捷函数 ----

/** 获取所有配置项（不含 content，对齐列表接口） */
export function getMockConfigList(): ConfigView[] {
  return mockConfigs.map(({ item, revisions }) => {
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

/** 根据 id 获取 revision 列表 */
export function getMockRevisions(id: number): RevisionView[] {
  const found = mockConfigs.find((c) => c.item.id === id)
  if (!found) return []
  return [...found.revisions].reverse() // v 高→低
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

/** 获取下一个可用 id（新建用） */
export function getNextId(): number {
  return Math.max(...mockConfigs.map((c) => c.item.id)) + 1
}

/** 获取最新版本的下一个 version */
export function getNextVersion(id: number): number {
  const found = mockConfigs.find((c) => c.item.id === id)
  if (!found) return 1
  return found.item.version + 1
}

// ---- 实例视图 ----

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
  { namespace: 'prod', serverId: 'server-01', role: 'bukkit', group: 'server-a', zone: 'zone-01', assigned: true, address: '10.0.0.1:25565', version: '1.20.4', status: 'online', capacity: 100, weight: 1, metadata: {}, lastHeartbeat: ago(5), appliedMd5: 'abc12345', playerCount: 42, tps: 19.8, backends: [], zoneDefaultEntry: true, proxy: ZERO_PROXY, registeredAt: ago(86400) },
  { namespace: 'prod', serverId: 'server-02', role: 'bukkit', group: 'server-a', zone: 'zone-01', assigned: true, address: '10.0.0.2:25565', version: '1.20.4', status: 'online', capacity: 100, weight: 1, metadata: {}, lastHeartbeat: ago(10), appliedMd5: 'abc12345', playerCount: 38, tps: 19.5, backends: [], zoneDefaultEntry: false, proxy: ZERO_PROXY, registeredAt: ago(86400) },
  { namespace: 'prod', serverId: 'server-03', role: 'bungee', group: 'server-b', zone: 'zone-02', assigned: true, address: '10.0.0.3:25565', version: '1.20.4', status: 'lost', capacity: 200, weight: 2, metadata: {}, lastHeartbeat: ago(60), appliedMd5: 'def67890', playerCount: 0, tps: 0, backends: ['server-01', 'server-04'], zoneDefaultEntry: false, proxy: { onlineConnections: 128, threadCount: 36, uptimeMs: 7_200_000, backendUp: 1, backendTotal: 2, backendAvgLatencyMs: 18 }, registeredAt: ago(172800) },
  { namespace: 'prod', serverId: 'server-04', role: 'bukkit', group: 'server-b', zone: 'zone-02', assigned: true, address: '10.0.0.4:25565', version: '1.20.4', status: 'online', capacity: 100, weight: 1, metadata: {}, lastHeartbeat: ago(3), appliedMd5: 'abc12345', playerCount: 55, tps: 19.2, backends: [], zoneDefaultEntry: false, proxy: ZERO_PROXY, registeredAt: ago(43200) },
  { namespace: 'test', serverId: 'test-01', role: 'bukkit', group: 'server-a', zone: 'zone-01', assigned: true, address: '10.0.1.1:25565', version: '1.20.4', status: 'online', capacity: 50, weight: 1, metadata: {}, lastHeartbeat: ago(8), appliedMd5: 'abc12345', playerCount: 5, tps: 20.0, backends: [], zoneDefaultEntry: false, proxy: ZERO_PROXY, registeredAt: ago(86400) },
]

// ---- 分组/Zone 视图 ----

export const mockZoneStats: ZoneStatView[] = [
  { group: 'server-a', zone: 'zone-01', serverCount: 2, onlineCount: 2 },
  { group: 'server-b', zone: 'zone-02', serverCount: 2, onlineCount: 1 },
  { group: 'server-a', zone: 'zone-03', serverCount: 0, onlineCount: 0 },
]

// ---- 环境 ----

export const mockNamespaces: NamespaceView[] = [
  { code: 'prod', name: '生产环境' },
  { code: 'test', name: '测试环境' },
]

// ---- 审计 ----

export function getMockAudits(filter?: { namespace?: string; operator?: string; action?: string; targetType?: string; page?: number; size?: number }): AuditPage {
  const all: AuditView[] = [
    { namespace: 'prod', operator: 'admin', action: 'publish', targetType: 'config', targetRef: 'game_config.yml', detail: '发布版本 v3', result: 'success', clientIp: '127.0.0.1', createdAt: ago(3600) },
    { namespace: 'prod', operator: 'developer', action: 'create', targetType: 'config', targetRef: 'db.properties', detail: '新建配置', result: 'success', clientIp: '127.0.0.1', createdAt: ago(7200) },
    { namespace: 'test', operator: 'admin', action: 'rollback', targetType: 'config', targetRef: 'game_config.yml', detail: '回滚到 v1', result: 'success', clientIp: '127.0.0.1', createdAt: ago(10800) },
    { namespace: 'prod', operator: 'admin', action: 'delete', targetType: 'config', targetRef: 'old_plugin.yml', detail: '软删', result: 'success', clientIp: '127.0.0.1', createdAt: ago(14400) },
    { namespace: 'prod', operator: 'admin', action: 'assign', targetType: 'zone', targetRef: 'server-01 → zone-01', detail: '指派 zone', result: 'success', clientIp: '127.0.0.1', createdAt: ago(18000) },
  ]
  let items = all
  if (filter?.namespace) items = items.filter(a => a.namespace === filter.namespace)
  if (filter?.operator) items = items.filter(a => a.operator === filter.operator)
  if (filter?.action) items = items.filter(a => a.action === filter.action)
  return { total: items.length, items }
}

// ---- 文件树托管 ----

export const mockFiles: FileView[] = [
  { id: 1, namespace: 'prod', group: '__GLOBAL__', path: 'plugins/game/config.yml', scopeLevel: 'global', scopeTarget: '', version: 3, md5: 'abc12345', enabled: true, updatedAt: ago(3600) },
  { id: 2, namespace: 'prod', group: '__GLOBAL__', path: 'plugins/game/db.properties', scopeLevel: 'global', scopeTarget: '', version: 2, md5: 'def67890', enabled: true, updatedAt: ago(7200) },
  { id: 3, namespace: 'prod', group: 'server-a', path: 'plugins/game/config.yml', scopeLevel: 'group', scopeTarget: '', version: 1, md5: 'aaa11111', enabled: true, updatedAt: ago(10800) },
]

export function getMockFileList(filter?: { namespace?: string; group?: string; path?: string; scopeLevel?: string }): FileView[] {
  let items = [...mockFiles]
  if (filter?.namespace) items = items.filter(f => f.namespace === filter.namespace)
  if (filter?.group) items = items.filter(f => f.group === filter.group)
  if (filter?.path) items = items.filter(f => f.path === filter.path)
  return items
}

export function getMockFile(id: number): FileView | null {
  return mockFiles.find(f => f.id === id) ?? null
}

export function getMockFileRevisions(_id: number): FileRevisionView[] {
  return [
    { version: 1, md5: 'aaa11111', operator: 'admin', comment: '初始', sourceRevision: null, createdAt: ago(7200) },
    { version: 2, md5: 'bbb22222', operator: 'developer', comment: '更新', sourceRevision: null, createdAt: ago(3600) },
  ]
}

// ---- 覆盖集 ----

export const mockOverrideSets: OverrideSetView[] = [
  { id: 1, namespace: 'prod', group: '__GLOBAL__', name: '三方插件覆盖', scopeLevel: 'global', scopeTarget: '', targetRoot: '/plugins/third-party', reloadCommand: '/reload', mode: 'merge', version: 1, enabled: true, updatedAt: ago(3600) },
]

export function getMockOverrideSetList(filter?: { namespace?: string; group?: string }): OverrideSetView[] {
  let items = [...mockOverrideSets]
  if (filter?.namespace) items = items.filter(s => s.namespace === filter.namespace)
  return items
}

export function getMockOverrideSet(id: number): OverrideSetView | null {
  return mockOverrideSets.find(s => s.id === id) ?? null
}

export function getMockOverrideSetRevisions(_id: number): OverrideSetRevisionView[] {
  return [
    { version: 1, targetRoot: '/plugins/third-party', reloadCommand: '/reload', operator: 'admin', comment: '初始', sourceRevision: null, createdAt: ago(3600) },
  ]
}

export function getMockDryRun(_id: number): OverrideSetDryRunView {
  return {
    targetRoot: '/plugins/third-party',
    reloadCommand: '/reload',
    commandFirstToken: 'reload',
    memberPaths: ['plugins/third-party/lib.jar', 'plugins/third-party/config.yml'],
  }
}

// ---- Zone 指派 ----

export const mockAssignments: AssignmentView[] = [
  { namespace: 'prod', serverId: 'server-01', group: 'server-a', zone: 'zone-01', note: '主力服', updatedAt: ago(3600) },
  { namespace: 'prod', serverId: 'server-02', group: 'server-a', zone: 'zone-01', note: '备用', updatedAt: ago(3600) },
  { namespace: 'prod', serverId: 'server-03', group: 'server-b', zone: 'zone-02', note: 'Bungee 入口', updatedAt: ago(3600) },
]

export function getMockAssignments(filter?: { namespace?: string; group?: string; zone?: string }): AssignmentView[] {
  let items = [...mockAssignments]
  if (filter?.namespace) items = items.filter(a => a.namespace === filter.namespace)
  if (filter?.group) items = items.filter(a => a.group === filter.group)
  if (filter?.zone) items = items.filter(a => a.zone === filter.zone)
  return items
}
