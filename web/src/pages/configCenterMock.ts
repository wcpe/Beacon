// 配置中心新版骨架（批1）的内置 mock。
// 服务器列表走真实 listInstances 接口（与全站 1000 服同源）；本文件只管"配置内容"。
// 统一模型（与"全行级/深合并"对齐）：每个文件都有 全局/大区/小区/单服 四层，每层存"文本"；
//   合并按"叶子键路径"深合并（解析 yaml/json/properties → 拍平成 a.b.c 路径，更具体层按路径覆盖）。
//   这样不论扁平 config.yml 还是深嵌套 yml/json，四视角（生效/层列/补丁栈/对盘）都清晰：一行 = 一个叶子路径。
// 真上线时配置内容走真实接口（批0 行级合并 ADR 后）。

import { load, dump } from 'js-yaml'

export type LayerLevel = 'global' | 'area' | 'zone' | 'server'
export const LAYER_ORDER: LayerLevel[] = ['global', 'area', 'zone', 'server']

export type FileKind = 'config' | 'text' | 'binary'

export interface MockFile {
  path: string
  name: string
  kind: FileKind
  sensitive?: boolean
  nested?: boolean
  // 四层文本（global 为基线，必有；其余可选；server 按 serverId 分别存）
  global: string
  area?: string
  zone?: string
  server?: Record<string, string>
  // serverId -> 磁盘实际文本（对盘用）
  disk?: Record<string, string>
  version?: number
}

export const DEMO_SERVER = 'server-01'
export const DEFAULT_FILE_PATH = 'plugins/Essentials/config.yml'

// 大区 / 小区层针对哪个 group/zone 生效
const AREA_GROUP = 'server-a'
const ZONE_TARGET = 'zone-01'

export const LAYER_LABEL: Record<LayerLevel, { label: string; short: string }> = {
  global: { label: '全局', short: '全局' },
  area: { label: '大区 server-a', short: '大区' },
  zone: { label: '小区 zone-01', short: '小区' },
  server: { label: '单服', short: '单服' },
}

// ===== 文件内容 =====

const ESSENTIALS_GLOBAL = `spawn-protection: 16
pvp: true
motd: "Welcome"
max-players: 100
difficulty: normal`

const ESSENTIALS_AREA = `pvp: false
max-players: 200`

const ESSENTIALS_SERVER01 = `spawn-protection: 0
motd: "Lobby 1"`

const ESSENTIALS_DISK01 = `spawn-protection: 10
pvp: false
motd: "Lobby 1（本地手改）"
max-players: 200
difficulty: normal
debug: true`

const SERVER_PROPERTIES = `# Minecraft server properties
server-port=25565
online-mode=true
white-list=false
max-players=100
view-distance=10
spawn-protection=16
rcon.password=changeme-please
level-seed=
motd=A Beacon-managed server`

const PAPER_GLOBAL = `# Paper 全局配置（嵌套结构示例）
_version: 30
chunk-system:
  gen-parallelism: default
  io-threads: -1
  worker-threads: -1
collisions:
  enable-player-collisions: true
  send-full-pos-for-hard-colliding-entities: true
console:
  enable-brigadier-completions: true
  enable-brigadier-highlighting: true
item-validation:
  display-name: 8192
  book:
    author: 8192
    page: 16384
    title: 8192
messages:
  no-permission: "&cI'm sorry, but you do not have permission."
unsupported-settings:
  allow-headless-pistons: false`

const PAPER_GLOBAL_DISK = `# Paper 全局配置（嵌套结构示例）
_version: 30
chunk-system:
  gen-parallelism: default
  io-threads: 4
  worker-threads: 4
collisions:
  enable-player-collisions: false
  send-full-pos-for-hard-colliding-entities: true
console:
  enable-brigadier-completions: true
  enable-brigadier-highlighting: true
item-validation:
  display-name: 8192
  book:
    author: 8192
    page: 16384
    title: 8192
messages:
  no-permission: "&c抱歉，你没有权限。"
unsupported-settings:
  allow-headless-pistons: true`

const PAPER_AREA = `messages:
  no-permission: "&c本大区禁止该操作。"`

const LUCKPERMS_GLOBAL = `# LuckPerms（嵌套 yml 示例）
storage-method: h2
data:
  address: localhost:3306
  database: minecraft
  username: root
  pool-settings:
    maximum-pool-size: 10
    minimum-idle: 10
    maximum-lifetime: 1800000
    connection-timeout: 5000
sync-minutes: -1
messaging-service: none`

const LUCKPERMS_AREA = `sync-minutes: 30
messaging-service: pluginmsg`

const ITEMS_JSON = `{
  "items": [
    {
      "id": "ruby_sword",
      "material": "DIAMOND_SWORD",
      "display": "&c红宝石之剑",
      "lore": ["&7锋利无比", "&7+15 攻击"],
      "enchants": { "sharpness": 5, "unbreaking": 3 },
      "attributes": {
        "damage": 15.5,
        "nbt": { "CustomModelData": 100123, "Unbreakable": true }
      }
    }
  ],
  "settings": { "give-on-join": false, "drop-protection": true }
}`

const ITEMS_JSON_DISK = `{
  "items": [
    {
      "id": "ruby_sword",
      "material": "DIAMOND_SWORD",
      "display": "&c红宝石之剑",
      "lore": ["&7锋利无比", "&7+20 攻击"],
      "enchants": { "sharpness": 6, "unbreaking": 3 },
      "attributes": {
        "damage": 20.0,
        "nbt": { "CustomModelData": 100123, "Unbreakable": true }
      }
    }
  ],
  "settings": { "give-on-join": true, "drop-protection": true }
}`

const BUKKIT_YML = `settings:
  allow-end: true
  warn-on-overload: true
spawn-limits:
  monsters: 70
  animals: 10
  water-animals: 5`

const SPIGOT_YML = `settings:
  bungeecord: true
  restart-on-crash: true
world-settings:
  default:
    view-distance: default
    mob-spawn-range: 6`

const MESSAGES_TXT = `welcome=欢迎来到服务器！
leave=玩家 {player} 离开了
broadcast.restart=服务器将在 {n} 秒后重启`

function f(path: string, name: string, kind: FileKind, global: string, extra?: Partial<MockFile>): MockFile {
  return { path, name, kind, global, version: 1, ...extra }
}

export function initialFiles(): Record<string, MockFile> {
  const list: MockFile[] = [
    f('server.properties', 'server.properties', 'config', SERVER_PROPERTIES, { sensitive: true, server: { [DEMO_SERVER]: 'max-players=80' }, disk: { [DEMO_SERVER]: SERVER_PROPERTIES.replace('white-list=false', 'white-list=true') } }),
    f('bukkit.yml', 'bukkit.yml', 'config', BUKKIT_YML, { nested: true }),
    f('spigot.yml', 'spigot.yml', 'config', SPIGOT_YML, { nested: true }),
    f('paper-global.yml', 'paper-global.yml', 'config', PAPER_GLOBAL, { nested: true, area: PAPER_AREA, disk: { [DEMO_SERVER]: PAPER_GLOBAL_DISK } }),
    f('eula.txt', 'eula.txt', 'text', 'eula=true'),
    f('plugins/Essentials/config.yml', 'config.yml', 'config', ESSENTIALS_GLOBAL, { version: 3, area: ESSENTIALS_AREA, server: { [DEMO_SERVER]: ESSENTIALS_SERVER01 }, disk: { [DEMO_SERVER]: ESSENTIALS_DISK01 } }),
    f('plugins/Essentials/messages.txt', 'messages.txt', 'text', MESSAGES_TXT, { disk: { [DEMO_SERVER]: MESSAGES_TXT + '\nbroadcast.maintenance=维护中' } }),
    f('plugins/Essentials/items.json', 'items.json', 'config', ITEMS_JSON, { nested: true, disk: { [DEMO_SERVER]: ITEMS_JSON_DISK } }),
    f('plugins/LuckPerms/config.yml', 'config.yml', 'config', LUCKPERMS_GLOBAL, { version: 2, nested: true, area: LUCKPERMS_AREA }),
  ]
  const map: Record<string, MockFile> = {}
  for (const x of list) map[x.path] = x
  return map
}

// 服务器磁盘上存在、但尚未纳管的文件（首次「整目录抓取」的对象）。键：serverId → 路径 → 磁盘文本。
// 演示「第一次把整个根目录的所有文件抓取入库」：这些文件在树里以「未纳管」标记，递归抓取时一并纳入受管库。
const WORLDGUARD_CFG = `regions:
  enable: true
  invincibility-removes-mobs: true
  high-frequency-flags: false
  max-region-count-per-player:
    default: 7
protection:
  build-permission-nodes: false`

const WORLDGUARD_REGIONS = `regions:
  spawn:
    type: cuboid
    min: { x: -50, y: 0, z: -50 }
    max: { x: 50, y: 256, z: 50 }
    flags:
      pvp: deny
      greeting: "欢迎来到主城"`

const VAULT_CFG = `# Vault 经济桥接
update-check: true`

const PERMISSIONS_YML = `groups:
  default:
    permissions:
      - essentials.spawn
      - essentials.help
  admin:
    default: false
    permissions:
      - "*"`

export const UNMANAGED_DISK: Record<string, Record<string, string>> = {
  [DEMO_SERVER]: {
    'permissions.yml': PERMISSIONS_YML,
    'plugins/WorldGuard/config.yml': WORLDGUARD_CFG,
    'plugins/WorldGuard/regions.yml': WORLDGUARD_REGIONS,
    'plugins/Vault/config.yml': VAULT_CFG,
  },
}

// 该路径在某服磁盘上的未纳管文本（不存在返回 null）
export function unmanagedText(path: string, serverId: string): string | null {
  return UNMANAGED_DISK[serverId]?.[path] ?? null
}

// 按文件名判语言（不依赖 MockFile，供未纳管文件用）
export function langOfName(name: string): string {
  if (name.endsWith('.json')) return 'json'
  if (name.endsWith('.properties')) return 'properties'
  if (name.endsWith('.yml') || name.endsWith('.yaml')) return 'yaml'
  return 'plaintext'
}

// ===== 文件树 =====
export interface TreeNode {
  type: 'dir' | 'file'
  name: string
  path: string
  children?: TreeNode[]
}

export const TREE: TreeNode[] = [
  { type: 'file', name: 'server.properties', path: 'server.properties' },
  { type: 'file', name: 'bukkit.yml', path: 'bukkit.yml' },
  { type: 'file', name: 'spigot.yml', path: 'spigot.yml' },
  { type: 'file', name: 'paper-global.yml', path: 'paper-global.yml' },
  { type: 'file', name: 'eula.txt', path: 'eula.txt' },
  { type: 'file', name: 'permissions.yml', path: 'permissions.yml' },
  {
    type: 'dir',
    name: 'plugins',
    path: 'plugins',
    children: [
      {
        type: 'dir',
        name: 'Essentials',
        path: 'plugins/Essentials',
        children: [
          { type: 'file', name: 'config.yml', path: 'plugins/Essentials/config.yml' },
          { type: 'file', name: 'messages.txt', path: 'plugins/Essentials/messages.txt' },
          { type: 'file', name: 'items.json', path: 'plugins/Essentials/items.json' },
        ],
      },
      { type: 'dir', name: 'LuckPerms', path: 'plugins/LuckPerms', children: [{ type: 'file', name: 'config.yml', path: 'plugins/LuckPerms/config.yml' }] },
      {
        type: 'dir',
        name: 'WorldGuard',
        path: 'plugins/WorldGuard',
        children: [
          { type: 'file', name: 'config.yml', path: 'plugins/WorldGuard/config.yml' },
          { type: 'file', name: 'regions.yml', path: 'plugins/WorldGuard/regions.yml' },
        ],
      },
      { type: 'dir', name: 'Vault', path: 'plugins/Vault', children: [{ type: 'file', name: 'config.yml', path: 'plugins/Vault/config.yml' }] },
    ],
  },
  {
    type: 'dir',
    name: 'world',
    path: 'world',
    children: [
      { type: 'file', name: 'level.dat', path: 'world/level.dat' },
      { type: 'dir', name: 'region', path: 'world/region', children: [{ type: 'file', name: 'r.0.0.mca', path: 'world/region/r.0.0.mca' }] },
    ],
  },
  { type: 'dir', name: 'logs', path: 'logs', children: [{ type: 'file', name: 'latest.log', path: 'logs/latest.log' }] },
  { type: 'dir', name: 'cache', path: 'cache', children: [{ type: 'file', name: 'FileCache.json', path: 'cache/FileCache.json' }] },
]

// 服务器磁盘上的运行期 / 非配置文件（通常被排除，不纳管）。「磁盘视图」要能看到它们——
// 证明可访问服务器真实磁盘，而不止「受管配置库」这一受管子集。二进制文件内容留空（不可文本预览）。
export const DISK_RUNTIME: Record<string, Record<string, string>> = {
  [DEMO_SERVER]: {
    'logs/latest.log': `[12:00:01] [Server thread/INFO]: Starting minecraft server version 1.20.1
[12:00:03] [Server thread/INFO]: Loading properties
[12:00:08] [Server thread/INFO]: Done (5.213s)! For help, type "help"`,
    'cache/FileCache.json': `{
  "version": 1,
  "entries": []
}`,
    'world/level.dat': '',
    'world/region/r.0.0.mca': '',
  },
}

// 该路径在某服磁盘上是否为「非受管」的真实文件（未纳管配置 / 运行期文件）——用于主区是否显示磁盘文件面板
export function isDiskFile(path: string, serverId: string): boolean {
  return unmanagedText(path, serverId) !== null || DISK_RUNTIME[serverId]?.[path] !== undefined
}

// 二进制文件名判定（磁盘视图里这类文件不可文本编辑 / 预览）
export function isBinaryName(name: string): boolean {
  return /\.(dat|mca|gz|jar|zip|png|jpg|db|bin)$/i.test(name)
}

// 某路径在某服磁盘上的实际文本：已纳管→磁盘快照(有漂移)或已部署生效内容；未纳管→其磁盘文本；运行期→样例/二进制占位
export function diskText(path: string, files: Record<string, MockFile>, group: string, zone: string, serverId: string): string {
  const f = files[path]
  if (f) {
    const d = f.disk?.[serverId]
    return d !== undefined ? d : effectiveSource(f, group, zone, serverId)
  }
  const un = unmanagedText(path, serverId)
  if (un !== null) return un
  const rt = DISK_RUNTIME[serverId]?.[path]
  if (rt !== undefined) return rt === '' ? '（二进制文件，不可文本预览）' : rt
  return isBinaryName(path) ? '（二进制文件，不可文本预览）' : '（磁盘上无此文件 / 无快照）'
}

// 裁剪文件树：仅保留满足 keep 的文件，以及其祖先目录（空目录剔除）。受管库视图用 keep = 已纳管。
export function pruneTree(nodes: TreeNode[], keep: (path: string) => boolean): TreeNode[] {
  const out: TreeNode[] = []
  for (const n of nodes) {
    if (n.type === 'file') {
      if (keep(n.path)) out.push(n)
    } else {
      const ch = pruneTree(n.children ?? [], keep)
      if (ch.length) out.push({ ...n, children: ch })
    }
  }
  return out
}

// ===== 排除名单 =====
export interface ExcludeRule {
  id: string
  pattern: string
  scope: string
  scan: boolean
  sync: boolean
  manage: boolean
}

export function initialExcludeRules(): ExcludeRule[] {
  return [
    { id: 'r1', pattern: 'world/**', scope: '全局', scan: true, sync: true, manage: true },
    { id: 'r2', pattern: 'logs/**', scope: '全局', scan: true, sync: true, manage: true },
    { id: 'r3', pattern: 'cache/**', scope: '全局', scan: true, sync: true, manage: true },
    { id: 'r4', pattern: '*.bak', scope: '全局', scan: true, sync: true, manage: false },
    { id: 'r5', pattern: '**/*.tmp', scope: '小区 zone-01', scan: true, sync: false, manage: false },
  ]
}

// ===== 同步队列（三类消息，信息更全） =====
export type QueueKind = 'fetch' | 'publish' | 'audit'

export interface ReviewFile {
  path: string
  lines: number
}

export interface QueueItem {
  id: number
  kind: QueueKind
  title: string
  detail: string
  state: string
  done?: boolean
  // 推送中进度（0-100，存在即显示进度条 + 由定时器推进）
  progress?: number
  operator: string
  target: string
  time: string
  // 待审核项携带可审核清单
  review?: ReviewFile[]
}

export function initialQueue(): QueueItem[] {
  return [
    { id: 1, kind: 'fetch', title: '反向抓取 server-01 / Essentials', detail: '扫描 28 文件 · 3 项待纳管', state: '待审核', operator: 'admin', target: '→ 小区 zone-01', time: '2 分钟前', review: [
      { path: 'plugins/Essentials/config.yml', lines: 3 },
      { path: 'plugins/Essentials/kits.yml', lines: 12 },
      { path: 'plugins/Essentials/spawn.yml', lines: 5 },
    ] },
    { id: 2, kind: 'fetch', title: '反向抓取 srv-0042 / WorldGuard', detail: '扫描 11 文件 · 1 项待纳管', state: '待审核', operator: 'ops-li', target: '→ 大区 server-b', time: '8 分钟前', review: [{ path: 'plugins/WorldGuard/config.yml', lines: 7 }] },
    { id: 3, kind: 'fetch', title: '收编 server-01 / config.yml', detail: '3 处磁盘改动 → 单服补丁', state: '已收编', done: true, operator: 'admin', target: '单服 server-01', time: '20 分钟前' },
    { id: 4, kind: 'publish', title: '发布 config.yml → 大区 server-a', detail: 'v2→v3 · 热推 90 台在线服 · 校验通过', state: '完成', done: true, operator: 'admin', target: '大区 server-a · 90 台', time: '12 分钟前' },
    { id: 5, kind: 'publish', title: '灰度 paper-global.yml → cohort', detail: 'v4→v5 · 先发 12 台 · 待晋升', state: '灰度中', operator: 'ops-li', target: 'cohort 12 台', time: '30 分钟前' },
    { id: 6, kind: 'publish', title: '发布 spigot.yml → 全局', detail: 'v1→v2 · 热推 980 台在线服', state: '完成', done: true, operator: 'admin', target: '全局 · 980 台', time: '1 小时前' },
    { id: 7, kind: 'audit', title: 'admin 改 config.yml 单服 server-01', detail: 'spawn-protection 16→0 · motd 改写', state: '已记录', done: true, operator: 'admin', target: '单服 server-01', time: '1 小时前' },
    { id: 8, kind: 'audit', title: 'ops-li 回滚 LuckPerms/config.yml → v1', detail: '读 v1 内容发为 v3', state: '已记录', done: true, operator: 'ops-li', target: '大区 server-a', time: '2 小时前' },
    { id: 9, kind: 'audit', title: 'admin 新增排除规则 world/**', detail: '作用于 扫描/同步/管理', state: '已记录', done: true, operator: 'admin', target: '全局', time: '昨天' },
  ]
}

// ===== 版本快照（回滚 diff，文本） =====
export const REVISION_SNAPSHOTS: Record<string, Record<number, string>> = {
  'plugins/Essentials/config.yml': {
    3: ESSENTIALS_GLOBAL,
    2: `spawn-protection: 0
pvp: false
motd: "Lobby A"
max-players: 150
difficulty: easy`,
    1: `spawn-protection: 16
pvp: true
motd: "Welcome"
max-players: 100
difficulty: normal`,
  },
  'plugins/LuckPerms/config.yml': {
    2: LUCKPERMS_GLOBAL,
    1: LUCKPERMS_GLOBAL.replace('storage-method: h2', 'storage-method: sqlite'),
  },
}

// ===== 纯函数：层文本 / 块解析 / 合并 =====

// 某层对某 (group,zone,server) 生效的文本（不适用返回 null）
export function layerText(file: MockFile, level: LayerLevel, group: string, zone: string, serverId: string): string | null {
  if (level === 'global') return file.global
  if (level === 'area') return group === AREA_GROUP ? (file.area ?? null) : null
  if (level === 'zone') return group === AREA_GROUP && zone === ZONE_TARGET ? (file.zone ?? null) : null
  return file.server?.[serverId] ?? null
}

// 拍平后的一个叶子键路径
export interface Flat {
  path: string
  value: string
}

// 解析一层文本为叶子键路径（yaml/json 深拍平；properties 逐行 k=v）；解析失败返回空
export function parseFlat(text: string, lang: string): Flat[] {
  if (lang === 'properties') {
    const out: Flat[] = []
    for (const raw of text.split('\n')) {
      const line = raw.trim()
      if (!line || line.startsWith('#')) continue
      const i = line.indexOf('=')
      if (i < 0) continue
      out.push({ path: line.slice(0, i).trim(), value: line.slice(i + 1).trim() })
    }
    return out
  }
  let obj: unknown
  try {
    obj = lang === 'json' ? JSON.parse(text) : load(text)
  } catch {
    return []
  }
  const out: Flat[] = []
  flatten(obj, '', out)
  return out
}

function flatten(v: unknown, prefix: string, out: Flat[]): void {
  if (v === null || typeof v !== 'object') {
    out.push({ path: prefix || '(root)', value: fmtVal(v) })
    return
  }
  if (Array.isArray(v)) {
    if (v.length === 0) {
      out.push({ path: prefix, value: '[]' })
      return
    }
    v.forEach((item, i) => flatten(item, prefix ? `${prefix}[${i}]` : `[${i}]`, out))
    return
  }
  const entries = Object.entries(v as Record<string, unknown>)
  if (entries.length === 0) {
    out.push({ path: prefix, value: '{}' })
    return
  }
  for (const [k, val] of entries) flatten(val, prefix ? `${prefix}.${k}` : k, out)
}

function fmtVal(v: unknown): string {
  if (typeof v === 'string') return v
  if (v === null) return 'null'
  return String(v)
}

// 某层对某 (group,zone,server) 生效的叶子路径
function layerFlat(file: MockFile, level: LayerLevel, group: string, zone: string, serverId: string): Flat[] {
  const t = layerText(file, level, group, zone, serverId)
  return t === null ? [] : parseFlat(t, fileLang(file))
}

// 生效：按叶子路径深合并（更具体层按路径覆盖，保留首见顺序）
export interface FlatLine {
  path: string
  value: string
  source: LayerLevel
}

export function flatEffective(file: MockFile, group: string, zone: string, serverId: string): FlatLine[] {
  const order: string[] = []
  const map = new Map<string, { value: string; source: LayerLevel }>()
  for (const lvl of LAYER_ORDER) {
    for (const { path, value } of layerFlat(file, lvl, group, zone, serverId)) {
      if (!map.has(path)) order.push(path)
      map.set(path, { value, source: lvl })
    }
  }
  return order.map((p) => ({ path: p, value: map.get(p)!.value, source: map.get(p)!.source }))
}

// 层列：每个叶子路径在各层的值 + 胜出层
export interface ColumnRow {
  path: string
  cells: Record<LayerLevel, string | null>
  winner: LayerLevel
}

export function columnRows(file: MockFile, group: string, zone: string, serverId: string): ColumnRow[] {
  const order: string[] = []
  const perLayer: Record<LayerLevel, Map<string, string>> = { global: new Map(), area: new Map(), zone: new Map(), server: new Map() }
  for (const lvl of LAYER_ORDER) {
    for (const { path, value } of layerFlat(file, lvl, group, zone, serverId)) {
      if (!order.includes(path)) order.push(path)
      perLayer[lvl].set(path, value)
    }
  }
  const winner = new Map(flatEffective(file, group, zone, serverId).map((e) => [e.path, e.source]))
  return order.map((path) => {
    const cells = {} as Record<LayerLevel, string | null>
    for (const lvl of LAYER_ORDER) cells[lvl] = perLayer[lvl].has(path) ? perLayer[lvl].get(path)! : null
    return { path, cells, winner: winner.get(path) ?? 'global' }
  })
}

// 对盘：生效叶子路径 vs 磁盘叶子路径
export type DiffKind = 'same' | 'changed' | 'added' | 'removed'
export interface DriftRow {
  path: string
  effective: string | null
  disk: string | null
  kind: DiffKind
}

export function driftRows(file: MockFile, group: string, zone: string, serverId: string): DriftRow[] {
  const eff = new Map(flatEffective(file, group, zone, serverId).map((e) => [e.path, e.value]))
  const diskText = file.disk?.[serverId]
  const disk = new Map((diskText !== undefined ? parseFlat(diskText, fileLang(file)) : []).map((x) => [x.path, x.value]))
  const order: string[] = []
  for (const k of eff.keys()) order.push(k)
  for (const k of disk.keys()) if (!order.includes(k)) order.push(k)
  return order.map((path) => {
    const e = eff.has(path) ? eff.get(path)! : null
    const d = disk.has(path) ? disk.get(path)! : null
    let kind: DiffKind = 'same'
    if (e !== null && d === null) kind = 'removed'
    else if (e === null && d !== null) kind = 'added'
    else if (e !== d) kind = 'changed'
    return { path, effective: e, disk: d, kind }
  })
}

export function driftCount(file: MockFile, group: string, zone: string, serverId: string): number {
  if (file.disk?.[serverId] === undefined) return 0
  return driftRows(file, group, zone, serverId).filter((d) => d.kind !== 'same').length
}

// ===== 目录级（递归）=====

// 某目录下（含所有子目录）的全部文件路径。dirPath 传 '' 表示整个服务器根目录。
export function descendantFiles(dirPath: string): string[] {
  const out: string[] = []
  function walk(ns: TreeNode[]): void {
    for (const n of ns) {
      if (n.type === 'file') {
        if (dirPath === '' || n.path === dirPath || n.path.startsWith(`${dirPath}/`)) out.push(n.path)
      } else walk(n.children ?? [])
    }
  }
  walk(TREE)
  return out
}

// 目录递归视图里每个文件的状态（对盘）
export interface DirFileRow {
  path: string
  name: string
  lang: string
  managed: boolean // 已纳管（在受管库）
  unmanaged: boolean // 未纳管但磁盘上存在（首次抓取的对象）
  excluded: boolean // 命中排除名单
  hasDisk: boolean // 有磁盘快照可对盘
  drift: number // 已纳管：对盘漂移数；未纳管：将纳管的有效行数
}

// 计算某目录（递归）下每个文件的对盘状态
export function dirFileRows(dir: string, files: Record<string, MockFile>, rules: ExcludeRule[], group: string, zone: string, serverId: string): DirFileRow[] {
  return descendantFiles(dir).map((path) => {
    const excluded = isExcluded(path, rules)
    const f = files[path]
    if (f) {
      const hasDisk = f.disk?.[serverId] !== undefined
      return { path, name: f.name, lang: fileLang(f), managed: true, unmanaged: false, excluded, hasDisk, drift: hasDisk ? driftCount(f, group, zone, serverId) : 0 }
    }
    const disk = unmanagedText(path, serverId)
    const name = path.split('/').pop() ?? path
    const lines = disk ? disk.split('\n').filter((l) => l.trim() && !l.trim().startsWith('#')).length : 0
    return { path, name, lang: langOfName(name), managed: false, unmanaged: disk !== null, excluded, hasDisk: disk !== null, drift: lines }
  })
}

// 文本逐行 diff（回滚用，原文本）
export interface DiffLine {
  left: string | null
  right: string | null
  changed: boolean
}

export function textDiff(a: string, b: string): DiffLine[] {
  const la = a.split('\n')
  const lb = b.split('\n')
  const max = Math.max(la.length, lb.length)
  const out: DiffLine[] = []
  for (let i = 0; i < max; i++) {
    const l = i < la.length ? la[i] : null
    const r = i < lb.length ? lb[i] : null
    out.push({ left: l, right: r, changed: l !== r })
  }
  return out
}

// ===== 写回：把某叶子路径的新值写进一层文本 =====

// 把 a.b[0].c 拆成 ['a','b',0,'c']
function parsePath(path: string): (string | number)[] {
  const out: (string | number)[] = []
  for (const t of path.match(/[^.[\]]+|\[\d+\]/g) ?? []) {
    if (t.startsWith('[')) out.push(Number(t.slice(1, -1)))
    else out.push(t)
  }
  return out
}

// 字符串值还原为合适类型（数字/布尔/null/去引号字符串）
function coerce(v: string): unknown {
  if (v === 'true') return true
  if (v === 'false') return false
  if (v === 'null') return null
  if (/^-?\d+(\.\d+)?$/.test(v)) return Number(v)
  if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) return v.slice(1, -1)
  return v
}

function deepSet(obj: Record<string, unknown>, segs: (string | number)[], value: unknown): void {
  let cur: unknown = obj
  for (let i = 0; i < segs.length - 1; i++) {
    const k = segs[i]
    const nextIsIdx = typeof segs[i + 1] === 'number'
    const c = cur as Record<string | number, unknown>
    if (c[k] === null || typeof c[k] !== 'object') c[k] = nextIsIdx ? [] : {}
    cur = c[k]
  }
  ;(cur as Record<string | number, unknown>)[segs[segs.length - 1]] = value
}

// 把 path=value 写进一层文本（properties 行级；yaml/json 解析→深设→序列化，注释会丢，mock 可接受）
export function deepSetText(text: string, path: string, value: string, lang: string): string {
  if (lang === 'properties') {
    const lines = text.split('\n')
    const idx = lines.findIndex((l) => l.trim().startsWith(path + '='))
    if (idx >= 0) lines[idx] = `${path}=${value}`
    else lines.push(`${path}=${value}`)
    return lines.join('\n')
  }
  let obj: Record<string, unknown> = {}
  try {
    const parsed = lang === 'json' ? JSON.parse(text) : load(text)
    if (parsed && typeof parsed === 'object') obj = parsed as Record<string, unknown>
  } catch {
    obj = {}
  }
  deepSet(obj, parsePath(path), coerce(value))
  return lang === 'json' ? JSON.stringify(obj, null, 2) : dump(obj, { lineWidth: -1 }).trimEnd()
}

export function fileLang(file: MockFile): string {
  if (file.name.endsWith('.json')) return 'json'
  if (file.name.endsWith('.properties')) return 'properties'
  if (file.name.endsWith('.yml') || file.name.endsWith('.yaml')) return 'yaml'
  return 'plaintext'
}

// ===== 生效"真源文件"（深合并后序列化成真实 yaml/json/properties，供编辑器编辑） =====

function deepMergeObj(base: Record<string, unknown>, over: Record<string, unknown>): Record<string, unknown> {
  for (const [k, v] of Object.entries(over)) {
    const b = base[k]
    if (v && typeof v === 'object' && !Array.isArray(v) && b && typeof b === 'object' && !Array.isArray(b)) {
      base[k] = deepMergeObj({ ...(b as Record<string, unknown>) }, v as Record<string, unknown>)
    } else base[k] = v
  }
  return base
}

// plaintext / 非结构化：取最具体的有内容层（整文件覆盖）
export function topSourceLayer(file: MockFile, group: string, zone: string, serverId: string): LayerLevel {
  for (const lvl of [...LAYER_ORDER].reverse()) {
    const t = layerText(file, lvl, group, zone, serverId)
    if (t !== null && t.trim() !== '') return lvl
  }
  return 'global'
}

// 合并后的真实源文本
export function effectiveSource(file: MockFile, group: string, zone: string, serverId: string): string {
  const lang = fileLang(file)
  if (lang === 'plaintext') return layerText(file, topSourceLayer(file, group, zone, serverId), group, zone, serverId) ?? file.global
  if (lang === 'properties') {
    const m = new Map<string, string>()
    for (const lvl of LAYER_ORDER) for (const { path, value } of layerFlat(file, lvl, group, zone, serverId)) m.set(path, value)
    return [...m].map(([k, v]) => `${k}=${v}`).join('\n')
  }
  let obj: Record<string, unknown> = {}
  for (const lvl of LAYER_ORDER) {
    const t = layerText(file, lvl, group, zone, serverId)
    if (t === null) continue
    try {
      const o = lang === 'json' ? JSON.parse(t) : load(t)
      if (o && typeof o === 'object') obj = deepMergeObj(obj, o as Record<string, unknown>)
    } catch {
      /* 解析失败的层跳过 */
    }
  }
  return lang === 'json' ? JSON.stringify(obj, null, 2) : dump(obj, { lineWidth: -1 }).trimEnd()
}

// 叶子路径来源扩展成"每段前缀的统一来源"（去数组下标）：某前缀下所有叶子同源才记，供逐行着色继承
export function provenanceMap(file: MockFile, group: string, zone: string, serverId: string): Map<string, LayerLevel> {
  const sets = new Map<string, Set<LayerLevel>>()
  for (const { path, source } of flatEffective(file, group, zone, serverId)) {
    const cleaned = path.replace(/\[\d+\]/g, '')
    let acc = ''
    for (const seg of cleaned.split('.')) {
      acc = acc ? `${acc}.${seg}` : seg
      if (!sets.has(acc)) sets.set(acc, new Set())
      sets.get(acc)!.add(source)
    }
  }
  const out = new Map<string, LayerLevel>()
  for (const [k, s] of sets) if (s.size === 1) out.set(k, [...s][0])
  return out
}

// 真实源文本逐行 → 来源层：按缩进键栈定位前缀路径查 prov；查不到继承父块来源，再不行用兜底（保证每行都有标签）
export function lineSources(text: string, prov: Map<string, LayerLevel>, lang: string, fallback: LayerLevel): (LayerLevel | null)[] {
  const out: (LayerLevel | null)[] = []
  const stack: { indent: number; key: string; source: LayerLevel }[] = []
  for (const raw of text.split('\n')) {
    const trimmed = raw.trim()
    if (!trimmed || trimmed.startsWith('#')) {
      out.push(null)
      continue
    }
    const indent = raw.length - raw.trimStart().length
    while (stack.length && stack[stack.length - 1].indent >= indent) stack.pop()
    const parent = stack.length ? stack[stack.length - 1].source : fallback
    if (/^[{}[\],]+$/.test(trimmed) || trimmed.startsWith('- ')) {
      out.push(parent)
      continue
    }
    const sep = lang === 'properties' ? trimmed.indexOf('=') : trimmed.indexOf(':')
    if (sep < 0) {
      out.push(parent)
      continue
    }
    const key = trimmed.slice(0, sep).trim().replace(/^["']|["']$/g, '')
    const rest = trimmed.slice(sep + 1).trim().replace(/,$/, '')
    const path = lang === 'properties' ? key : [...stack.map((s) => s.key), key].join('.')
    const src = prov.get(path) ?? parent
    if (lang !== 'properties' && (rest === '' || rest === '{' || rest === '[')) {
      stack.push({ indent, key, source: src })
      out.push(src)
    } else out.push(src)
  }
  return out
}

// 简易 glob
export function globToRegExp(pattern: string): RegExp {
  let re = ''
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i]
    if (c === '*') {
      if (pattern[i + 1] === '*') {
        re += '.*'
        i++
      } else re += '[^/]*'
    } else if (c === '?') re += '[^/]'
    else re += c.replace(/[.+^${}()|[\]\\]/g, '\\$&')
  }
  return new RegExp('^' + re + '$')
}

export function isExcluded(path: string, rules: ExcludeRule[]): boolean {
  return rules.some((r) => {
    const p = r.pattern.endsWith('/') ? r.pattern + '**' : r.pattern
    return globToRegExp(p).test(path) || path.startsWith(r.pattern.replace(/\/?\**$/, '') + '/')
  })
}
