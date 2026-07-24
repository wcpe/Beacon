// 文件资产域 mock（/admin/v2/assets*，只读索引 + 预览 / diff / 敏感规则）。
// 契约真源：docs/specs/v2-file-assets.md §5.2；敏感放行 §4.6（无 reason → 403 + sensitive 标记）。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  AssetCompareGroup,
  AssetCompareResponse,
  AssetDiffResponse,
  AssetItem,
  AssetListResponse,
  AssetPreviewResponse,
  AssetRescanResponse,
  AssetScanStatusItem,
  Paged,
} from '@beacon/contracts'
import { jsonError, mockGet, mockPost, mockPut, paginate, queryStr, readBody } from '../http'
import { getClusterState } from '../data/cluster'
import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { BASE_MS, hashString, isoOffset, pseudoSha256, uuidFrom } from '../support'

// 默认敏感路径规则（spec §4.6 默认清单）
const DEFAULT_SENSITIVE_PATTERNS = [
  '**/*secret*',
  '**/*password*',
  '**/*credential*',
  '**/*.pem',
  '**/*.key',
  '**/*.jks',
  '**/*.p12',
  '**/.env',
  '**/.env.*',
  '**/token.*',
  'plugins/Beacon/**',
]

const BINARY_EXTS = new Set(['jar', 'zip', 'png', 'gz', 'p12', 'jks'])

// 各服文件目录模板（相对路径 + 大小量级 + 内容变体数）
const FILE_CATALOG: { path: string; size: number; variants: number }[] = [
  { path: 'server.properties', size: 1240, variants: 2 },
  { path: 'bukkit.yml', size: 980, variants: 1 },
  { path: 'spigot.yml', size: 4200, variants: 2 },
  { path: 'paper-global.yml', size: 5100, variants: 1 },
  { path: 'plugins/Essentials.jar', size: 2_945_120, variants: 2 },
  { path: 'plugins/Essentials/config.yml', size: 8_764, variants: 3 },
  { path: 'plugins/Essentials/messages.yml', size: 15_320, variants: 1 },
  { path: 'plugins/Beacon.jar', size: 1_204_566, variants: 1 },
  { path: 'plugins/Beacon/config.yml', size: 642, variants: 1 },
  { path: 'plugins/Economy.jar', size: 845_210, variants: 2 },
  { path: 'plugins/Economy/config.yml', size: 2_130, variants: 2 },
  { path: 'plugins/Economy/database-password.yml', size: 312, variants: 1 },
  { path: 'plugins/Quests.jar', size: 1_650_400, variants: 1 },
  { path: 'plugins/Quests/config.yml', size: 3_480, variants: 2 },
  { path: 'plugins/Quests/quests/main-story.yml', size: 22_040, variants: 1 },
  { path: 'plugins/Lodestone.jar', size: 512_000, variants: 1 },
  { path: 'plugins/Lodestone/config.yml', size: 1_820, variants: 2 },
  { path: 'plugins/ProtocolLib.jar', size: 3_100_450, variants: 1 },
  { path: 'plugins/PlaceholderAPI.jar', size: 920_330, variants: 1 },
  { path: 'plugins/PlaceholderAPI/config.yml', size: 1_020, variants: 1 },
]

interface AssetState {
  assets: AssetItem[]
  scans: AssetScanStatusItem[]
  patterns: string[]
}

function extOf(path: string): string {
  const name = path.split('/').pop() ?? path
  const dot = name.lastIndexOf('.')
  return dot <= 0 ? '' : name.slice(dot + 1).toLowerCase()
}

function buildAssets(scenario: MockScenario): AssetState {
  if (scenario === 'empty') {
    return { assets: [], scans: [], patterns: [...DEFAULT_SENSITIVE_PATTERNS] }
  }
  const cluster = getClusterState()
  const servers = cluster.servers.filter((s) => s.kind === 'backend')
  const assets: AssetItem[] = []
  const scans: AssetScanStatusItem[] = []
  for (const server of servers) {
    const serverHash = hashString(`assets:${server.serverId}`)
    let totalSize = 0
    let count = 0
    for (const entry of FILE_CATALOG) {
      // 少量文件在部分服务器上缺失（制造 compare 的 missing 样本）
      if (hashString(`${server.serverId}:${entry.path}:exist`) % 17 === 0) {
        continue
      }
      const variant = hashString(`${server.serverId}:${entry.path}:variant`) % entry.variants
      const ext = extOf(entry.path)
      const size = entry.size + variant * 17
      totalSize += size
      count += 1
      assets.push({
        serverId: server.serverId,
        namespaceId: server.namespaceId,
        path: entry.path,
        ext,
        sha256: pseudoSha256(`content:${entry.path}:v${String(variant)}`),
        size,
        mtimeMs: BASE_MS - ((serverHash + entry.size) % (14 * 86_400_000)),
        isText: !BINARY_EXTS.has(ext),
        scannedAt: isoOffset(-(serverHash % 1800) * 1000),
      })
    }
    scans.push({
      serverId: server.serverId,
      namespaceId: server.namespaceId,
      manifestDigest: pseudoSha256(`digest:${server.serverId}`),
      fileCount: count,
      totalSize,
      truncated: false,
      scannedAt: isoOffset(-(serverHash % 1800) * 1000),
      scanDurationMs: 800 + (serverHash % 4200),
    })
  }
  return { assets, scans, patterns: [...DEFAULT_SENSITIVE_PATTERNS] }
}

const getAssetState: () => AssetState = defineScenarioStore(buildAssets)

/** 简化 glob 匹配：支持 **（跨层）与 *（单层），大小写不敏感 */
export function globMatch(pattern: string, path: string): boolean {
  const escaped = pattern
    .toLowerCase()
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*\*\//g, '§§SLASH§§')
    .replace(/\*\*/g, '§§ANY§§')
    .replace(/\*/g, '[^/]*')
    .replace(/§§SLASH§§/g, '(?:.*/)?')
    .replace(/§§ANY§§/g, '.*')
  return new RegExp(`^${escaped}$`).test(path.toLowerCase())
}

function isSensitive(patterns: readonly string[], path: string): boolean {
  return patterns.some((pattern) => globMatch(pattern, path))
}

/** 生成确定性的文本文件内容（预览 / diff 用） */
function contentFor(serverId: string, path: string, sha256: string): string {
  return [
    `# ${path}（mock 内容，来自 ${serverId} 实时读取）`,
    `# sha256: ${sha256.slice(0, 16)}…`,
    'enabled: true',
    `instance: ${serverId}`,
    `variant-mark: ${sha256.slice(0, 8)}`,
    'options:',
    '  language: zh_CN',
    '  debug: false',
  ].join('\n')
}

export const fileAssetsHandlers: HttpHandler[] = [
  // 每服扫描概要（先于 /assets 注册无冲突，路径不同）
  mockGet('/admin/v2/assets/scan-status', ({ request }) => {
    const url = new URL(request.url)
    const namespaceId = queryStr(url, 'namespaceId')
    if (namespaceId === null) {
      return jsonError(400, 'invalid_param', 'namespaceId 必填')
    }
    const serverId = queryStr(url, 'serverId')
    const rows = getAssetState().scans.filter((row) => {
      if (String(row.namespaceId) !== namespaceId) {
        return false
      }
      if (serverId !== null && row.serverId !== serverId) {
        return false
      }
      return true
    })
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies Paged<AssetScanStatusItem>)
  }),

  // 跨服同路径哈希分组比对 + 缺失服列表
  mockGet('/admin/v2/assets/compare', ({ request }) => {
    const url = new URL(request.url)
    const namespaceId = queryStr(url, 'namespaceId')
    const path = queryStr(url, 'path')
    if (namespaceId === null || path === null) {
      return jsonError(400, 'invalid_param', 'namespaceId / path 必填')
    }
    const cluster = getClusterState()
    const serverIds = queryStr(url, 'serverIds')
    const zoneId = queryStr(url, 'zoneId')
    const scope = cluster.servers.filter((s) => {
      if (s.kind !== 'backend' || String(s.namespaceId) !== namespaceId) {
        return false
      }
      if (serverIds !== null && !serverIds.split(',').includes(s.serverId)) {
        return false
      }
      if (zoneId !== null && String(s.zoneId ?? '') !== zoneId) {
        return false
      }
      return true
    })
    const rows = getAssetState().assets.filter(
      (a) => a.path === path && scope.some((s) => s.serverId === a.serverId),
    )
    const groups = new Map<string, AssetCompareGroup>()
    for (const row of rows) {
      let group = groups.get(row.sha256)
      if (!group) {
        group = { sha256: row.sha256, size: row.size, servers: [] }
        groups.set(row.sha256, group)
      }
      group.servers.push({ serverId: row.serverId, mtimeMs: row.mtimeMs, scannedAt: row.scannedAt })
    }
    const covered = new Set(rows.map((r) => r.serverId))
    const missing = scope.filter((s) => !covered.has(s.serverId)).map((s) => s.serverId)
    const response: AssetCompareResponse = {
      path,
      groups: [...groups.values()].sort((a, b) => b.servers.length - a.servers.length),
      missing,
    }
    return HttpResponse.json(response)
  }),

  // 敏感路径规则清单
  mockGet('/admin/v2/assets/sensitive-rules', () => {
    return HttpResponse.json({ patterns: getAssetState().patterns })
  }),

  // 整体替换敏感路径规则
  mockPut('/admin/v2/assets/sensitive-rules', async ({ request }) => {
    const body = await readBody<{ patterns?: string[] }>(request)
    if (!Array.isArray(body.patterns)) {
      return jsonError(400, 'invalid_param', 'patterns 必填（整体替换语义）')
    }
    const state = getAssetState()
    state.patterns = body.patterns
    return HttpResponse.json({ patterns: state.patterns })
  }),

  // 批量下发重扫命令（离线服标记入响应不阻断整批）
  mockPost('/admin/v2/assets/rescan', async ({ request }) => {
    const body = await readBody<{ namespaceId?: number; serverIds?: string[]; force?: boolean }>(request)
    if (!Array.isArray(body.serverIds) || body.serverIds.length === 0) {
      return jsonError(400, 'invalid_param', 'serverIds 必填')
    }
    if (body.serverIds.length > 100) {
      return jsonError(400, 'invalid_param', '单批重扫上限 100 台')
    }
    const cluster = getClusterState()
    const results = body.serverIds.map((serverId) => {
      const server = cluster.servers.find((s) => s.serverId === serverId)
      const offline = !server?.online
      return {
        serverId,
        commandId: offline ? null : uuidFrom(`rescan:${serverId}`),
        offline,
      }
    })
    return HttpResponse.json({ results } satisfies AssetRescanResponse)
  }),

  // 文本文件安全预览（敏感命中须填原因；二进制只回元数据）
  mockPost('/admin/v2/assets/preview', async ({ request }) => {
    const body = await readBody<{ serverId?: string; path?: string; reason?: string }>(request)
    if (!body.serverId || !body.path) {
      return jsonError(400, 'invalid_param', 'serverId / path 必填')
    }
    const state = getAssetState()
    const row = state.assets.find((a) => a.serverId === body.serverId && a.path === body.path)
    if (!row) {
      return jsonError(404, 'asset_not_found', '该服清单中不存在此文件')
    }
    const server = getClusterState().servers.find((s) => s.serverId === body.serverId)
    if (!server?.online) {
      return jsonError(504, 'asset_agent_offline', 'agent 离线，无法实时读取文件内容')
    }
    const sensitive = isSensitive(state.patterns, row.path)
    if (sensitive && !body.reason) {
      return jsonError(403, 'asset_sensitive_path', '命中敏感路径规则，查看内容必须填写原因', { sensitive: true })
    }
    const binary = BINARY_EXTS.has(row.ext)
    const truncated = !binary && row.size > 512 * 1024
    const response: AssetPreviewResponse = {
      content: binary ? null : contentFor(row.serverId, row.path, row.sha256),
      truncated,
      binary,
      sha256: row.sha256,
      size: row.size,
      sensitive,
    }
    return HttpResponse.json(response)
  }),

  // 两侧文件内容 diff（哈希相同短路；二进制 / 超限拒绝）
  mockPost('/admin/v2/assets/diff', async ({ request }) => {
    const body = await readBody<{
      left?: { serverId: string; path: string }
      right?: { serverId: string; path: string }
      reason?: string
    }>(request)
    if (!body.left || !body.right) {
      return jsonError(400, 'invalid_param', 'left / right 必填')
    }
    const state = getAssetState()
    const leftRef = body.left
    const rightRef = body.right
    const left = state.assets.find((a) => a.serverId === leftRef.serverId && a.path === leftRef.path)
    const right = state.assets.find((a) => a.serverId === rightRef.serverId && a.path === rightRef.path)
    if (!left || !right) {
      return jsonError(404, 'asset_not_found', 'diff 目标文件不存在于清单')
    }
    if (BINARY_EXTS.has(left.ext) || BINARY_EXTS.has(right.ext) || left.size > 512 * 1024 || right.size > 512 * 1024) {
      return jsonError(400, 'asset_diff_unsupported', '二进制或超过 512 KiB 的文件不支持 diff，请改用哈希比对')
    }
    if ((isSensitive(state.patterns, left.path) || isSensitive(state.patterns, right.path)) && !body.reason) {
      return jsonError(403, 'asset_sensitive_path', '命中敏感路径规则，diff 必须填写原因', { sensitive: true })
    }
    if (left.sha256 === right.sha256) {
      return HttpResponse.json({ identical: true } satisfies AssetDiffResponse)
    }
    const response: AssetDiffResponse = {
      identical: false,
      left: { serverId: left.serverId, path: left.path, content: contentFor(left.serverId, left.path, left.sha256), sha256: left.sha256 },
      right: { serverId: right.serverId, path: right.path, content: contentFor(right.serverId, right.path, right.sha256), sha256: right.sha256 },
    }
    return HttpResponse.json(response)
  }),

  // 资产搜索分页（namespaceId 必填 + 组合条件；仅 name 单条件拒绝）
  mockGet('/admin/v2/assets', ({ request }) => {
    const url = new URL(request.url)
    const namespaceId = queryStr(url, 'namespaceId')
    if (namespaceId === null) {
      return jsonError(400, 'invalid_param', 'namespaceId 必填（禁止无 namespace 全表扫描）')
    }
    const serverId = queryStr(url, 'serverId')
    const pathPrefix = queryStr(url, 'pathPrefix')
    const name = queryStr(url, 'name')
    const ext = queryStr(url, 'ext')
    const sha256 = queryStr(url, 'sha256')
    if (name !== null && serverId === null && pathPrefix === null && ext === null && sha256 === null) {
      return jsonError(400, 'invalid_param', 'name 是兜底条件，须与至少一个索引条件组合使用')
    }
    const rows = getAssetState().assets.filter((row) => {
      if (String(row.namespaceId) !== namespaceId) {
        return false
      }
      if (serverId !== null && row.serverId !== serverId) {
        return false
      }
      if (pathPrefix !== null && !row.path.startsWith(pathPrefix)) {
        return false
      }
      if (name !== null && !row.path.split('/').slice(-1)[0].includes(name)) {
        return false
      }
      if (ext !== null && row.ext !== ext) {
        return false
      }
      if (sha256 !== null && row.sha256 !== sha256) {
        return false
      }
      return true
    })
    const { items, total } = paginate(rows, url, { defaultSize: 50 })
    return HttpResponse.json({ items, total } satisfies AssetListResponse)
  }),
]
