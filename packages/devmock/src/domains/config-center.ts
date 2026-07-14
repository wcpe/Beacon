// 配置中心 V2 域 mock（/admin/v2/config-files*、/admin/v2/config-versions*，全套 17 端点）。
// 契约真源：docs/specs/v2-config-center.md §5；错误码 §5 汇总表；
// 合并语义 §4.1（mock 采用"扁平键"简化实现：`key: value` 行级解析 + 高层覆盖 + null 删键），
// 足以支撑页面演示逐键来源 / diff / 版本链 / 回收站闭环。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  ConfigDeletedKey,
  ConfigDiffResponse,
  ConfigEffectiveResponse,
  ConfigFileDetail,
  ConfigFileItem,
  ConfigFileListResponse,
  ConfigFileRow,
  ConfigFormat,
  ConfigProvenanceEntry,
  ConfigScopeLevel,
  ConfigScopeSummary,
  ConfigValidateResponse,
  ConfigVersionRow,
  RevokeResult,
  SaveVersionResult,
  TrashListResponse,
  VersionListResponse,
} from '@beacon/contracts'
import { jsonError, mockDelete, mockGet, mockPatch, mockPost, paginate, pathParam, queryStr, readBody } from '../http'
import { getClusterState, namespaceOfZone, type ClusterState } from '../data/cluster'
import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { isoOffset, pseudoSha256 } from '../support'

/** 敏感值占位符（读出口统一脱敏） */
export const CONFIG_MASKED = '__BEACON_MASKED__'

interface ConfigState {
  files: ConfigFileRow[]
  versions: ConfigVersionRow[]
  nextFileId: number
  nextVersionId: number
}

const DAY = 86_400_000
const SCOPE_LEVELS: readonly ConfigScopeLevel[] = ['namespace', 'bc_cluster', 'region', 'zone', 'server']

// ---- 扁平内容解析（mock 简化的合并引擎） ----

/** 解析扁平键值内容：yaml/properties 按行解析，json 解析顶层对象；值 "null" 表示删键指令 */
export function parseFlat(format: ConfigFormat, content: string): Map<string, string | null> {
  const map = new Map<string, string | null>()
  if (format === 'json') {
    const parsed: unknown = JSON.parse(content)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      throw new Error('JSON 顶层必须是对象')
    }
    for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
      map.set(key, value === null ? null : typeof value === 'string' ? value : JSON.stringify(value))
    }
    return map
  }
  const separator = format === 'properties' ? '=' : ':'
  const lines = content.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const trimmed = line.trim()
    if (trimmed === '' || trimmed.startsWith('#')) {
      continue
    }
    const sepIndex = trimmed.indexOf(separator)
    if (sepIndex <= 0) {
      throw new Error(`第 ${String(i + 1)} 行语法错误：缺少 "${separator}" 分隔符`)
    }
    const key = trimmed.slice(0, sepIndex).trim()
    const value = trimmed.slice(sepIndex + 1).trim()
    // yaml/json 支持 null 删键；properties 的 null 是普通字符串
    map.set(key, format === 'yaml' && value === 'null' ? null : value)
  }
  return map
}

/** 按固定键序规范序列化（保证 hash 幂等） */
export function serializeFlat(format: ConfigFormat, map: Map<string, string | null>): string {
  const keys = [...map.keys()].sort()
  const separator = format === 'properties' ? '=' : ': '
  return keys
    .map((key) => {
      const value = map.get(key)
      return `${key}${separator}${value ?? 'null'}`
    })
    .join('\n')
}

function contentHashOf(format: ConfigFormat, content: string): string {
  try {
    return pseudoSha256(serializeFlat(format, parseFlat(format, content)))
  } catch {
    return pseudoSha256(content)
  }
}

// ---- 数据集 ----

function seedFile(
  state: ConfigState,
  namespaceId: number,
  name: string,
  format: ConfigFormat,
  description: string,
  sensitivePaths: string[],
  chains: { scopeLevel: ConfigScopeLevel; scopeRefId: number; contents: string[] }[],
  deleted = false,
): void {
  const fileId = state.nextFileId
  state.nextFileId += 1
  state.files.push({
    id: fileId,
    namespaceId,
    name,
    format,
    description,
    schemaJson: null,
    sensitivePaths,
    deletedAt: deleted ? isoOffset(-2 * DAY) : null,
    deletedBy: deleted ? 'ops-chen' : null,
    createdBy: 'admin',
    createdAt: isoOffset(-40 * DAY),
    updatedAt: isoOffset(-3 * DAY),
  })
  for (const chain of chains) {
    let previousId: number | null = null
    chain.contents.forEach((content, index) => {
      const id = state.nextVersionId
      state.nextVersionId += 1
      state.versions.push({
        id,
        configFileId: fileId,
        scopeLevel: chain.scopeLevel,
        scopeRefId: chain.scopeRefId,
        versionNo: index + 1,
        content,
        contentHash: contentHashOf(format, content),
        isRemoval: false,
        basedOnVersionId: previousId,
        remark: index === 0 ? '首次发布' : `第 ${String(index + 1)} 次调整`,
        createdBy: index % 2 === 0 ? 'admin' : 'ops-chen',
        createdAt: isoOffset(-(30 - index * 7) * DAY),
      })
      previousId = id
    })
  }
}

function buildConfig(scenario: MockScenario): ConfigState {
  const state: ConfigState = { files: [], versions: [], nextFileId: 3001, nextVersionId: 9001 }
  if (scenario === 'empty') {
    return state
  }
  // 常规数据：贴近真实运维的插件配置文件，覆盖五层贡献 / 敏感值 / 回收站样本
  seedFile(
    state,
    1,
    'plugins/Essentials/config.yml',
    'yaml',
    '基础插件主配置（含传送与经济开关）',
    [],
    [
      {
        scopeLevel: 'namespace',
        scopeRefId: 1,
        contents: [
          'teleport-cooldown: 5\nspawn-on-join: false\neconomy-enabled: true\nmotd: 欢迎来到 Beacon 集群',
          'teleport-cooldown: 3\nspawn-on-join: false\neconomy-enabled: true\nmotd: 欢迎来到 Beacon 集群',
        ],
      },
      {
        scopeLevel: 'zone',
        scopeRefId: 30,
        contents: ['teleport-cooldown: 1\nmotd: 一区主城欢迎你'],
      },
      {
        scopeLevel: 'server',
        scopeRefId: 1002,
        contents: ['spawn-on-join: true'],
      },
    ],
  )
  seedFile(
    state,
    1,
    'plugins/Economy/config.yml',
    'yaml',
    '经济系统配置（含数据库口令，敏感）',
    ['database.password'],
    [
      {
        scopeLevel: 'namespace',
        scopeRefId: 1,
        contents: [
          'database.host: 10.30.0.6\ndatabase.password: prod-secret-233\nstart-balance: 100\ncurrency-name: 金币',
        ],
      },
      {
        scopeLevel: 'zone',
        scopeRefId: 32,
        contents: ['start-balance: 500'],
      },
    ],
  )
  seedFile(
    state,
    1,
    'plugins/Quests/config.json',
    'json',
    '任务系统配置（JSON 格式样例）',
    [],
    [
      {
        scopeLevel: 'namespace',
        scopeRefId: 1,
        contents: ['{"daily-limit": 10, "reward-multiplier": "1.5", "notify": "actionbar"}'],
      },
    ],
  )
  seedFile(
    state,
    1,
    'server.properties',
    'properties',
    '服务器根配置（properties 格式样例）',
    [],
    [
      {
        scopeLevel: 'namespace',
        scopeRefId: 1,
        contents: ['max-players=100\nview-distance=8\npvp=true'],
      },
      {
        scopeLevel: 'zone',
        scopeRefId: 31,
        contents: ['max-players=60\nview-distance=6'],
      },
    ],
  )
  seedFile(
    state,
    2,
    'plugins/Essentials/config.yml',
    'yaml',
    '测试域基础配置',
    [],
    [{ scopeLevel: 'namespace', scopeRefId: 2, contents: ['teleport-cooldown: 0\nmotd: 测试环境'] }],
  )
  // 回收站样本
  seedFile(
    state,
    1,
    'plugins/OldShop/config.yml',
    'yaml',
    '已下线的旧商店插件配置',
    [],
    [{ scopeLevel: 'namespace', scopeRefId: 1, contents: ['shop-enabled: false'] }],
    true,
  )
  if (scenario === 'huge') {
    // 超大量：1500 份文件（各一条 namespace 链），分页真实生效
    for (let i = 1; i <= 1500; i++) {
      seedFile(
        state,
        1,
        `plugins/Addon${String(i).padStart(4, '0')}/config.yml`,
        'yaml',
        `批量生成的插件配置 ${String(i)}`,
        [],
        [{ scopeLevel: 'namespace', scopeRefId: 1, contents: [`enabled: true\nweight: ${String(i % 50)}`] }],
      )
    }
  }
  return state
}

const getConfigState: () => ConfigState = defineScenarioStore(buildConfig)

// ---- 内部查询工具 ----

function findFile(id: number, includeTrashed = false): ConfigFileRow | undefined {
  const row = getConfigState().files.find((f) => f.id === id)
  if (!row) {
    return undefined
  }
  if (row.deletedAt !== null && !includeTrashed) {
    return undefined
  }
  return row
}

function chainOf(fileId: number, scopeLevel: string, scopeRefId: number): ConfigVersionRow[] {
  return getConfigState()
    .versions.filter((v) => v.configFileId === fileId && v.scopeLevel === scopeLevel && v.scopeRefId === scopeRefId)
    .sort((a, b) => a.versionNo - b.versionNo)
}

function headOf(fileId: number, scopeLevel: string, scopeRefId: number): ConfigVersionRow | undefined {
  const chain = chainOf(fileId, scopeLevel, scopeRefId)
  return chain.length === 0 ? undefined : chain[chain.length - 1]
}

function chainsOf(fileId: number): { scopeLevel: ConfigScopeLevel; scopeRefId: number }[] {
  const seen = new Set<string>()
  const result: { scopeLevel: ConfigScopeLevel; scopeRefId: number }[] = []
  for (const version of getConfigState().versions) {
    if (version.configFileId !== fileId) {
      continue
    }
    const key = `${version.scopeLevel}:${String(version.scopeRefId)}`
    if (!seen.has(key)) {
      seen.add(key)
      result.push({ scopeLevel: version.scopeLevel, scopeRefId: version.scopeRefId })
    }
  }
  return result
}

function scopeName(state: ClusterState, level: ConfigScopeLevel, refId: number): string {
  switch (level) {
    case 'namespace':
      return state.namespaces.find((n) => n.id === refId)?.name ?? `ns-${String(refId)}`
    case 'bc_cluster':
      return state.bcClusters.find((c) => c.id === refId)?.name ?? `cluster-${String(refId)}`
    case 'region':
      return state.regions.find((r) => r.id === refId)?.name ?? `region-${String(refId)}`
    case 'zone':
      return state.zones.find((z) => z.id === refId)?.name ?? `zone-${String(refId)}`
    case 'server':
      return state.servers.find((s) => s.id === refId)?.serverId ?? `server-${String(refId)}`
  }
}

/** 校验 scope 实体存在且归属文件 namespace（CONFIG_SCOPE_MISMATCH 判定） */
function scopeBelongs(state: ClusterState, file: ConfigFileRow, level: ConfigScopeLevel, refId: number): boolean {
  switch (level) {
    case 'namespace':
      return refId === file.namespaceId
    case 'bc_cluster':
      return state.bcClusters.some((c) => c.id === refId && c.namespaceId === file.namespaceId)
    case 'region': {
      const region = state.regions.find((r) => r.id === refId)
      if (!region) {
        return false
      }
      return state.bcClusters.some((c) => c.id === region.bcClusterId && c.namespaceId === file.namespaceId)
    }
    case 'zone':
      return namespaceOfZone(state, refId) === file.namespaceId
    case 'server':
      return state.servers.some((s) => s.id === refId && s.namespaceId === file.namespaceId)
  }
}

/** 敏感键脱敏 */
function maskContent(file: ConfigFileRow, content: string): string {
  if (file.sensitivePaths.length === 0 || content === '') {
    return content
  }
  try {
    const map = parseFlat(file.format, content)
    for (const path of file.sensitivePaths) {
      if (map.has(path) && map.get(path) !== null) {
        map.set(path, CONFIG_MASKED)
      }
    }
    return serializeFlat(file.format, map)
  } catch {
    return content
  }
}

interface ResolvedTarget {
  layers: { scopeLevel: ConfigScopeLevel; scopeRefId: number | null }[]
}

/** 解析目标的五层链（未分配 server 只有 namespace + server 两层） */
function resolveTarget(state: ClusterState, file: ConfigFileRow, url: URL): ResolvedTarget | Response {
  const serverIdRaw = queryStr(url, 'serverId')
  const zoneIdRaw = queryStr(url, 'zoneId')
  const regionIdRaw = queryStr(url, 'regionId')
  const clusterIdRaw = queryStr(url, 'bcClusterId')
  const layers: { scopeLevel: ConfigScopeLevel; scopeRefId: number | null }[] = [
    { scopeLevel: 'namespace', scopeRefId: file.namespaceId },
  ]
  const pushChainFromZone = (zoneId: number): boolean => {
    const zone = state.zones.find((z) => z.id === zoneId)
    if (!zone) {
      return false
    }
    const region = state.regions.find((r) => r.id === zone.regionId)
    layers.push({ scopeLevel: 'bc_cluster', scopeRefId: region?.bcClusterId ?? null })
    layers.push({ scopeLevel: 'region', scopeRefId: region?.id ?? null })
    layers.push({ scopeLevel: 'zone', scopeRefId: zone.id })
    return true
  }
  if (serverIdRaw !== null) {
    const server = state.servers.find((s) => String(s.id) === serverIdRaw || s.serverId === serverIdRaw)
    if (server?.namespaceId !== file.namespaceId) {
      return jsonError(400, 'CONFIG_SCOPE_MISMATCH', '目标 server 不存在或不属于文件 namespace')
    }
    if (server.zoneId !== null) {
      pushChainFromZone(server.zoneId)
    }
    layers.push({ scopeLevel: 'server', scopeRefId: server.id })
    return { layers }
  }
  if (zoneIdRaw !== null) {
    if (!pushChainFromZone(Number.parseInt(zoneIdRaw, 10))) {
      return jsonError(400, 'CONFIG_SCOPE_MISMATCH', '目标 zone 不存在')
    }
    return { layers }
  }
  if (regionIdRaw !== null) {
    const region = state.regions.find((r) => r.id === Number.parseInt(regionIdRaw, 10))
    if (!region) {
      return jsonError(400, 'CONFIG_SCOPE_MISMATCH', '目标 region 不存在')
    }
    layers.push({ scopeLevel: 'bc_cluster', scopeRefId: region.bcClusterId })
    layers.push({ scopeLevel: 'region', scopeRefId: region.id })
    return { layers }
  }
  if (clusterIdRaw !== null) {
    layers.push({ scopeLevel: 'bc_cluster', scopeRefId: Number.parseInt(clusterIdRaw, 10) })
    return { layers }
  }
  return { layers }
}

interface MergedResult {
  merged: Map<string, string | null>
  provenance: ConfigProvenanceEntry[]
  deletedKeys: ConfigDeletedKey[]
  layerSummaries: ConfigEffectiveResponse['layers']
}

/** 低 → 高逐层合并并记录逐键来源与删键 */
function mergeLayers(state: ClusterState, file: ConfigFileRow, target: ResolvedTarget): MergedResult {
  const merged = new Map<string, string | null>()
  const origin = new Map<string, ConfigProvenanceEntry>()
  const deletedKeys: ConfigDeletedKey[] = []
  const layerSummaries: ConfigEffectiveResponse['layers'] = []
  for (const layer of target.layers) {
    if (layer.scopeRefId === null) {
      layerSummaries.push({
        scopeLevel: layer.scopeLevel,
        scopeRefId: null,
        scopeName: null,
        contributing: false,
        headVersionNo: null,
        headHash: null,
      })
      continue
    }
    const head = headOf(file.id, layer.scopeLevel, layer.scopeRefId)
    const contributing = head !== undefined && !head.isRemoval
    layerSummaries.push({
      scopeLevel: layer.scopeLevel,
      scopeRefId: layer.scopeRefId,
      scopeName: scopeName(state, layer.scopeLevel, layer.scopeRefId),
      contributing,
      headVersionNo: head?.versionNo ?? null,
      headHash: head?.contentHash ?? null,
    })
    if (!head || !contributing) {
      continue
    }
    let map: Map<string, string | null>
    try {
      map = parseFlat(file.format, head.content)
    } catch {
      continue
    }
    for (const [key, value] of map) {
      if (value === null) {
        if (merged.has(key)) {
          merged.delete(key)
          origin.delete(key)
          deletedKeys.push({
            path: key,
            scopeLevel: layer.scopeLevel,
            scopeRefId: layer.scopeRefId,
            versionNo: head.versionNo,
          })
        }
        continue
      }
      merged.set(key, value)
      origin.set(key, {
        path: key,
        scopeLevel: layer.scopeLevel,
        scopeRefId: layer.scopeRefId,
        scopeName: scopeName(state, layer.scopeLevel, layer.scopeRefId),
        versionNo: head.versionNo,
      })
    }
  }
  return {
    merged,
    provenance: [...origin.values()].sort((a, b) => (a.path < b.path ? -1 : 1)),
    deletedKeys,
    layerSummaries,
  }
}

function toFileItem(row: ConfigFileRow): ConfigFileItem {
  const chains = chainsOf(row.id)
  const contributing = chains.filter((c) => {
    const head = headOf(row.id, c.scopeLevel, c.scopeRefId)
    return head !== undefined && !head.isRemoval
  }).length
  return {
    id: row.id,
    namespaceId: row.namespaceId,
    name: row.name,
    format: row.format,
    description: row.description,
    contributingLayerCount: contributing,
    updatedAt: row.updatedAt,
  }
}

function fileNotFound(): Response {
  return jsonError(404, 'CONFIG_FILE_NOT_FOUND', '配置文件不存在')
}

/** 组装文件详情（元数据 + 各层覆盖概览），GET 详情 / PATCH / restore 共用 */
function fileDetailOf(file: ConfigFileRow): ConfigFileDetail {
  const cluster = getClusterState()
  const scopes = chainsOf(file.id)
    .map((chain) => {
      const head = headOf(file.id, chain.scopeLevel, chain.scopeRefId)
      return head
        ? {
            scopeLevel: chain.scopeLevel,
            scopeRefId: chain.scopeRefId,
            scopeName: scopeName(cluster, chain.scopeLevel, chain.scopeRefId),
            headVersionNo: head.versionNo,
            isRemoval: head.isRemoval,
          }
        : null
    })
    .filter((s) => s !== null)
  return { ...file, scopes }
}

/** 解析 diff 端点的一侧描述：version:<id> / scope:<level>:<refId> / effective:<targetType>:<targetId> */
function resolveDiffSide(state: ClusterState, file: ConfigFileRow, spec: string): Map<string, string | null> | Response {
  const parts = spec.split(':')
  if (parts[0] === 'version') {
    const version = getConfigState().versions.find((v) => v.id === Number.parseInt(parts[1], 10))
    if (version?.configFileId !== file.id) {
      return jsonError(404, 'CONFIG_VERSION_NOT_FOUND', 'diff 引用的版本不存在')
    }
    return parseFlat(file.format, version.content)
  }
  if (parts[0] === 'scope') {
    const level = parts[1] as ConfigScopeLevel
    const head = headOf(file.id, level, Number.parseInt(parts[2], 10))
    if (!head || head.isRemoval) {
      return new Map<string, string | null>()
    }
    return parseFlat(file.format, head.content)
  }
  if (parts[0] === 'effective') {
    const targetType = parts[1]
    const targetId = parts[2]
    const url = new URL('http://mock.local/')
    const paramName =
      targetType === 'server' ? 'serverId' : targetType === 'zone' ? 'zoneId' : targetType === 'region' ? 'regionId' : targetType === 'bc_cluster' ? 'bcClusterId' : null
    if (paramName !== null) {
      url.searchParams.set(paramName, targetId)
    }
    const target = resolveTarget(state, file, url)
    if (target instanceof Response) {
      return target
    }
    return mergeLayers(state, file, target).merged
  }
  return jsonError(400, 'invalid_param', `无法识别的 diff 侧描述：${spec}`)
}

function maskMap(file: ConfigFileRow, map: Map<string, string | null>): Map<string, string | null> {
  const masked = new Map(map)
  for (const path of file.sensitivePaths) {
    if (masked.has(path) && masked.get(path) !== null) {
      masked.set(path, CONFIG_MASKED)
    }
  }
  return masked
}

interface SaveVersionBody {
  scopeLevel?: ConfigScopeLevel
  scopeRefId?: number
  content?: string
  remark?: string
  basedOnVersionId?: number | null
}

export const configCenterHandlers: HttpHandler[] = [
  // 回收站分页列表（必须先于 :id 注册）
  mockGet('/admin/v2/config-files/trash', ({ request }) => {
    const url = new URL(request.url)
    const namespaceId = queryStr(url, 'namespaceId')
    const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
    const rows = getConfigState()
      .files.filter((f) => {
        if (f.deletedAt === null) {
          return false
        }
        if (namespaceId !== null && String(f.namespaceId) !== namespaceId) {
          return false
        }
        if (keyword !== null && !f.name.toLowerCase().includes(keyword)) {
          return false
        }
        return true
      })
      .map((f) => ({
        id: f.id,
        namespaceId: f.namespaceId,
        name: f.name,
        format: f.format,
        deletedBy: f.deletedBy,
        deletedAt: f.deletedAt,
      }))
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies TrashListResponse)
  }),

  // 配置文件分页列表（不含回收站）
  mockGet('/admin/v2/config-files', ({ request }) => {
    const url = new URL(request.url)
    const namespaceId = queryStr(url, 'namespaceId')
    if (namespaceId === null) {
      return jsonError(400, 'invalid_param', 'namespaceId 必填')
    }
    const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
    const serverId = queryStr(url, 'serverId')
    const state = getClusterState()
    const rows = getConfigState()
      .files.filter((f) => {
        if (f.deletedAt !== null || String(f.namespaceId) !== namespaceId) {
          return false
        }
        if (keyword !== null && !f.name.toLowerCase().includes(keyword)) {
          return false
        }
        return true
      })
      .map((f) => {
        const item = toFileItem(f)
        if (serverId !== null) {
          const url2 = new URL('http://mock.local/')
          url2.searchParams.set('serverId', serverId)
          const target = resolveTarget(state, f, url2)
          if (!(target instanceof Response)) {
            const merged = mergeLayers(state, f, target)
            item.effectiveHash = pseudoSha256(serializeFlat(f.format, merged.merged))
          }
        }
        return item
      })
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies ConfigFileListResponse)
  }),

  // 创建配置文件（重名 409 CONFIG_FILE_DUPLICATE）
  mockPost('/admin/v2/config-files', async ({ request }) => {
    const body = await readBody<{
      namespaceId?: number
      name?: string
      format?: ConfigFormat
      description?: string
      schemaJson?: string
      sensitivePaths?: string[]
    }>(request)
    if (typeof body.namespaceId !== 'number' || !body.name || !body.format) {
      return jsonError(400, 'invalid_param', 'namespaceId / name / format 必填')
    }
    const state = getConfigState()
    if (state.files.some((f) => f.namespaceId === body.namespaceId && f.name === body.name && f.deletedAt === null)) {
      return jsonError(409, 'CONFIG_FILE_DUPLICATE', `文件 ${body.name} 已存在`)
    }
    const row: ConfigFileRow = {
      id: state.nextFileId,
      namespaceId: body.namespaceId,
      name: body.name,
      format: body.format,
      description: body.description ?? '',
      schemaJson: body.schemaJson ?? null,
      sensitivePaths: body.sensitivePaths ?? [],
      deletedAt: null,
      deletedBy: null,
      createdBy: 'admin',
      createdAt: isoOffset(0),
      updatedAt: isoOffset(0),
    }
    state.nextFileId += 1
    state.files.push(row)
    return HttpResponse.json(row satisfies ConfigFileRow, { status: 201 })
  }),

  // 各层贡献链概览
  mockGet('/admin/v2/config-files/:id/scopes', (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    const cluster = getClusterState()
    const scopes: ConfigScopeSummary[] = chainsOf(file.id)
      .map((chain) => {
        const head = headOf(file.id, chain.scopeLevel, chain.scopeRefId)
        if (!head) {
          return null
        }
        return {
          scopeLevel: chain.scopeLevel,
          scopeRefId: chain.scopeRefId,
          scopeName: scopeName(cluster, chain.scopeLevel, chain.scopeRefId),
          headVersionNo: head.versionNo,
          headHash: head.contentHash,
          isRemoval: head.isRemoval,
          updatedBy: head.createdBy,
          updatedAt: head.createdAt,
        }
      })
      .filter((s): s is ConfigScopeSummary => s !== null)
      .sort((a, b) => SCOPE_LEVELS.indexOf(a.scopeLevel) - SCOPE_LEVELS.indexOf(b.scopeLevel))
    return HttpResponse.json({ scopes } satisfies { scopes: ConfigScopeSummary[] })
  }),

  // 某链版本列表
  mockGet('/admin/v2/config-files/:id/versions', (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    const url = new URL(info.request.url)
    const scopeLevel = queryStr(url, 'scopeLevel')
    const scopeRefId = queryStr(url, 'scopeRefId')
    if (scopeLevel === null || scopeRefId === null) {
      return jsonError(400, 'invalid_param', 'scopeLevel / scopeRefId 必填')
    }
    const chain = chainOf(file.id, scopeLevel, Number.parseInt(scopeRefId, 10))
      .map((v) => ({
        versionId: v.id,
        versionNo: v.versionNo,
        contentHash: v.contentHash,
        isRemoval: v.isRemoval,
        basedOnVersionId: v.basedOnVersionId,
        remark: v.remark,
        createdBy: v.createdBy,
        createdAt: v.createdAt,
      }))
      .reverse()
    const { items, total } = paginate(chain, url)
    return HttpResponse.json({ items, total } satisfies VersionListResponse)
  }),

  // 保存新版本（语法校验 + 乐观并发 + 无变化拒绝 + 敏感占位符回填）
  mockPost('/admin/v2/config-files/:id/versions', async (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    const body = await readBody<SaveVersionBody>(info.request)
    if (!body.scopeLevel || typeof body.scopeRefId !== 'number' || typeof body.content !== 'string') {
      return jsonError(400, 'invalid_param', 'scopeLevel / scopeRefId / content 必填')
    }
    const cluster = getClusterState()
    if (!scopeBelongs(cluster, file, body.scopeLevel, body.scopeRefId)) {
      return jsonError(400, 'CONFIG_SCOPE_MISMATCH', 'scope 实体不存在或不属于文件 namespace')
    }
    if (body.content.length > 1024 * 1024) {
      return jsonError(422, 'CONFIG_CONTENT_TOO_LARGE', '单版本内容超过 1 MiB 上限')
    }
    let map: Map<string, string | null>
    try {
      map = parseFlat(file.format, body.content)
    } catch (error) {
      return jsonError(400, 'CONFIG_SYNTAX_INVALID', error instanceof Error ? error.message : '语法解析失败')
    }
    const head = headOf(file.id, body.scopeLevel, body.scopeRefId)
    const basedOn = body.basedOnVersionId ?? null
    if ((head?.id ?? null) !== basedOn) {
      return jsonError(409, 'CONFIG_VERSION_CONFLICT', '基线版本已过期，请重新加载后合并')
    }
    // 敏感占位符回填：等于占位符的敏感键取上一版本明文
    for (const path of file.sensitivePaths) {
      if (map.get(path) === CONFIG_MASKED) {
        if (!head || head.isRemoval) {
          return jsonError(400, 'CONFIG_SENSITIVE_PLACEHOLDER_INVALID', `敏感键 ${path} 无上一版本可回填`)
        }
        const previous = parseFlat(file.format, head.content).get(path)
        if (previous === undefined || previous === null) {
          return jsonError(400, 'CONFIG_SENSITIVE_PLACEHOLDER_INVALID', `敏感键 ${path} 无上一版本可回填`)
        }
        map.set(path, previous)
      }
    }
    const normalized = serializeFlat(file.format, map)
    const contentHash = pseudoSha256(normalized)
    if (head && !head.isRemoval && head.contentHash === contentHash) {
      return jsonError(400, 'CONFIG_NO_CHANGE', '内容与当前 head 相同，不产生空版本')
    }
    const state = getConfigState()
    const version: ConfigVersionRow = {
      id: state.nextVersionId,
      configFileId: file.id,
      scopeLevel: body.scopeLevel,
      scopeRefId: body.scopeRefId,
      versionNo: (head?.versionNo ?? 0) + 1,
      content: normalized,
      contentHash,
      isRemoval: false,
      basedOnVersionId: head?.id ?? null,
      remark: body.remark ?? '',
      createdBy: 'admin',
      createdAt: isoOffset(0),
    }
    state.nextVersionId += 1
    state.versions.push(version)
    file.updatedAt = isoOffset(0)
    return HttpResponse.json(
      { versionId: version.id, versionNo: version.versionNo, contentHash } satisfies SaveVersionResult,
      { status: 201 },
    )
  }),

  // 只读校验（不落库不审计）
  mockPost('/admin/v2/config-files/:id/validate', async (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    const body = await readBody<{ scopeLevel?: string; content?: string }>(info.request)
    if (typeof body.content !== 'string') {
      return jsonError(400, 'invalid_param', 'content 必填')
    }
    try {
      parseFlat(file.format, body.content)
      return HttpResponse.json({ valid: true, errors: [] } satisfies ConfigValidateResponse)
    } catch (error) {
      return HttpResponse.json({
        valid: false,
        errors: [{ path: '(root)', message: error instanceof Error ? error.message : '语法解析失败' }],
      } satisfies ConfigValidateResponse)
    }
  }),

  // 有效配置预览（合并 + 逐键来源 + 删键列表）
  mockGet('/admin/v2/config-files/:id/effective', (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    const cluster = getClusterState()
    const target = resolveTarget(cluster, file, new URL(info.request.url))
    if (target instanceof Response) {
      return target
    }
    const merged = mergeLayers(cluster, file, target)
    const effectiveHash = pseudoSha256(serializeFlat(file.format, merged.merged))
    const response: ConfigEffectiveResponse = {
      effectiveContent: serializeFlat(file.format, maskMap(file, merged.merged)),
      effectiveHash,
      provenance: merged.provenance,
      deletedKeys: merged.deletedKeys,
      layers: merged.layerSummaries,
    }
    return HttpResponse.json(response)
  }),

  // 版本间 / 层间 / 目标间键级 diff（脱敏后输出）
  mockGet('/admin/v2/config-files/:id/diff', (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    const url = new URL(info.request.url)
    const left = queryStr(url, 'left')
    const right = queryStr(url, 'right')
    if (left === null || right === null) {
      return jsonError(400, 'invalid_param', 'left / right 必填')
    }
    const cluster = getClusterState()
    const leftMap = resolveDiffSide(cluster, file, left)
    if (leftMap instanceof Response) {
      return leftMap
    }
    const rightMap = resolveDiffSide(cluster, file, right)
    if (rightMap instanceof Response) {
      return rightMap
    }
    const maskedLeft = maskMap(file, leftMap)
    const maskedRight = maskMap(file, rightMap)
    const added: ConfigDiffResponse['added'] = []
    const removed: ConfigDiffResponse['removed'] = []
    const changed: ConfigDiffResponse['changed'] = []
    for (const [key, value] of maskedRight) {
      if (!maskedLeft.has(key)) {
        added.push({ path: key, right: value ?? 'null' })
      } else if (maskedLeft.get(key) !== value) {
        changed.push({ path: key, left: maskedLeft.get(key) ?? 'null', right: value ?? 'null' })
      }
    }
    for (const [key, value] of maskedLeft) {
      if (!maskedRight.has(key)) {
        removed.push({ path: key, left: value ?? 'null' })
      }
    }
    const unifiedDiff = [
      `--- ${left}`,
      `+++ ${right}`,
      ...removed.map((r) => `- ${r.path}: ${r.left}`),
      ...changed.flatMap((c) => [`- ${c.path}: ${c.left}`, `+ ${c.path}: ${c.right}`]),
      ...added.map((a) => `+ ${a.path}: ${a.right}`),
    ].join('\n')
    return HttpResponse.json({ added, removed, changed, unifiedDiff } satisfies ConfigDiffResponse)
  }),

  // 从回收站恢复（名称被未删除文件占用 → 409）
  mockPost('/admin/v2/config-files/:id/restore', (info) => {
    const state = getConfigState()
    const file = state.files.find((f) => f.id === Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    if (file.deletedAt === null) {
      return fileNotFound()
    }
    if (state.files.some((f) => f.id !== file.id && f.namespaceId === file.namespaceId && f.name === file.name && f.deletedAt === null)) {
      return jsonError(409, 'CONFIG_FILE_DUPLICATE', `名称 ${file.name} 已被未删除文件占用`)
    }
    file.deletedAt = null
    file.deletedBy = null
    return HttpResponse.json(fileDetailOf(file) satisfies ConfigFileDetail)
  }),

  // 彻底删除（仅回收站内文件；原因必填；连带版本链）
  mockPost('/admin/v2/config-files/:id/purge', async (info) => {
    const state = getConfigState()
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const file = state.files.find((f) => f.id === id)
    if (!file) {
      return fileNotFound()
    }
    if (file.deletedAt === null) {
      return jsonError(400, 'CONFIG_FILE_NOT_TRASHED', '仅回收站内文件可彻底删除')
    }
    const body = await readBody<{ reason?: string }>(info.request)
    if (!body.reason) {
      return jsonError(400, 'missing_reason', '彻底删除原因必填')
    }
    state.files = state.files.filter((f) => f.id !== id)
    state.versions = state.versions.filter((v) => v.configFileId !== id)
    return new HttpResponse(null, { status: 204 })
  }),

  // 撤销某层贡献（生成 removal 版本）
  mockDelete('/admin/v2/config-files/:id/scopes/:scopeLevel/:scopeRefId', async (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    const body = await readBody<{ reason?: string }>(info.request)
    if (!body.reason) {
      return jsonError(400, 'missing_reason', '撤销原因必填')
    }
    const scopeLevel = pathParam(info, 'scopeLevel') as ConfigScopeLevel
    const scopeRefId = Number.parseInt(pathParam(info, 'scopeRefId'), 10)
    const head = headOf(file.id, scopeLevel, scopeRefId)
    if (!head || head.isRemoval) {
      return jsonError(400, 'invalid_param', '该层无可撤销的贡献（head 已是撤销版本或链不存在）')
    }
    const state = getConfigState()
    const version: ConfigVersionRow = {
      id: state.nextVersionId,
      configFileId: file.id,
      scopeLevel,
      scopeRefId,
      versionNo: head.versionNo + 1,
      content: '',
      contentHash: pseudoSha256(''),
      isRemoval: true,
      basedOnVersionId: head.id,
      remark: `撤销层贡献：${body.reason}`,
      createdBy: 'admin',
      createdAt: isoOffset(0),
    }
    state.nextVersionId += 1
    state.versions.push(version)
    return HttpResponse.json(
      { versionId: version.id, versionNo: version.versionNo, isRemoval: true } satisfies RevokeResult,
      { status: 201 },
    )
  }),

  // 文件元数据 + 各层覆盖概览
  mockGet('/admin/v2/config-files/:id', (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    return HttpResponse.json(fileDetailOf(file) satisfies ConfigFileDetail)
  }),

  // 更新描述 / schema / 敏感路径（改敏感路径需 reason）
  mockPatch('/admin/v2/config-files/:id', async (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    const body = await readBody<{
      description?: string
      schemaJson?: string
      sensitivePaths?: string[]
      reason?: string
    }>(info.request)
    if (body.sensitivePaths !== undefined && !body.reason) {
      return jsonError(400, 'missing_reason', '修改敏感路径必须填写原因')
    }
    if (body.description !== undefined) {
      file.description = body.description
    }
    if (body.schemaJson !== undefined) {
      try {
        JSON.parse(body.schemaJson)
      } catch {
        return jsonError(400, 'invalid_param', 'schemaJson 不是合法 JSON')
      }
      file.schemaJson = body.schemaJson
    }
    if (body.sensitivePaths !== undefined) {
      file.sensitivePaths = body.sensitivePaths
    }
    file.updatedAt = isoOffset(0)
    return HttpResponse.json(fileDetailOf(file) satisfies ConfigFileDetail)
  }),

  // 移入回收站（软删除，版本链保留）
  mockDelete('/admin/v2/config-files/:id', (info) => {
    const file = findFile(Number.parseInt(pathParam(info, 'id'), 10))
    if (!file) {
      return fileNotFound()
    }
    file.deletedAt = isoOffset(0)
    file.deletedBy = 'admin'
    return new HttpResponse(null, { status: 204 })
  }),

  // 版本详情（内容脱敏）
  mockGet('/admin/v2/config-versions/:versionId', (info) => {
    const version = getConfigState().versions.find(
      (v) => v.id === Number.parseInt(pathParam(info, 'versionId'), 10),
    )
    if (!version) {
      return jsonError(404, 'CONFIG_VERSION_NOT_FOUND', '版本不存在')
    }
    const file = findFile(version.configFileId)
    if (!file) {
      return fileNotFound()
    }
    return HttpResponse.json({ ...version, content: maskContent(file, version.content) } satisfies ConfigVersionRow)
  }),

  // 回退：基于历史版本生成新版本
  mockPost('/admin/v2/config-versions/:versionId/rollback', async (info) => {
    const state = getConfigState()
    const source = state.versions.find((v) => v.id === Number.parseInt(pathParam(info, 'versionId'), 10))
    if (!source) {
      return jsonError(404, 'CONFIG_VERSION_NOT_FOUND', '版本不存在')
    }
    const file = findFile(source.configFileId)
    if (!file) {
      return fileNotFound()
    }
    const head = headOf(file.id, source.scopeLevel, source.scopeRefId)
    if (head && !head.isRemoval && head.contentHash === source.contentHash) {
      return jsonError(400, 'CONFIG_NO_CHANGE', '目标版本与当前 head 内容相同')
    }
    const body = await readBody<{ remark?: string }>(info.request)
    const version: ConfigVersionRow = {
      id: state.nextVersionId,
      configFileId: file.id,
      scopeLevel: source.scopeLevel,
      scopeRefId: source.scopeRefId,
      versionNo: (head?.versionNo ?? 0) + 1,
      content: source.content,
      contentHash: source.contentHash,
      isRemoval: false,
      basedOnVersionId: source.id,
      remark: body.remark ?? `回退自 v${String(source.versionNo)}`,
      createdBy: 'admin',
      createdAt: isoOffset(0),
    }
    state.nextVersionId += 1
    state.versions.push(version)
    return HttpResponse.json(
      { versionId: version.id, versionNo: version.versionNo, contentHash: version.contentHash } satisfies SaveVersionResult,
      { status: 201 },
    )
  }),
]
