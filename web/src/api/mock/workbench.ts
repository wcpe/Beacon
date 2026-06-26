/**
 * 配置中心「双面板 Xftp 工作台」(FR-111) + 多标签编辑器 (FR-112) 的 mock 数据。
 *
 * 纯前端 style 预览原型用：受管配置树、服务器实时 plugins 树、文件内容、历史修订、同步队列、
 * scope / server 候选；并吸收 FR-113 语义的反向抓取扫描清单、拓印审核 diff、生效预览。
 * 仅 dev 下经 handlers 暴露于 /admin/v1/workbench/*，不对应任何真实后端端点。
 *
 * 层级对齐（改进 6）：受管树与服务器树共用同一套 plugins 目录骨架（同路径对应）——
 * 服务器侧是全量（含未纳管文件），受管侧是其子集（只显已纳管、但路径/文件夹与服务器一致），
 * 两面板同名文件在视觉上对得上。
 */

// 同步状态（左面板行首点，改进 7）：一致(绿) / 有差异待下发(琥珀) / 仅受管未下发(蓝) / 服务器已删(红)
export type SyncStatus = 'synced' | 'drift' | 'managed-only' | 'server-gone'

// 服务器侧标记：与受管一致 / 有差异 / 未纳管（server-only）
export type ServerMark = 'tracked' | 'drift' | 'untracked'

// 覆盖层：全局 / 组 / 实例
export type OverrideScope = 'global' | 'group' | 'server'

// 受管侧节点（树形：文件夹 / 文件）。路径与服务器侧骨架对齐。
export interface ManagedNode {
  // 唯一 key，文件节点用作 /configs/:id 的 id
  key: string
  name: string
  type: 'folder' | 'file'
  // 该受管文件与服务器实况的同步状态（文件夹取其后代聚合态，原型直接给定）
  sync: SyncStatus
  // 文件专属：覆盖层 + 版本号 + 修改时间（人类可读，Xftp 风「修改时间」列）
  scope?: OverrideScope
  version?: number
  modifiedAt?: string
  children?: ManagedNode[]
}

// 服务器侧节点（真实 plugins 目录视角：带大小 / 类型 / 修改时间 + 纳管标记）
export interface ServerNode {
  key: string
  name: string
  type: 'folder' | 'file'
  // 纳管标记（受管侧是否已收录、是否有差异）
  mark: ServerMark
  // 文件专属：大小（人类可读）+ 类型（Xftp 风「类型」列）+ 修改时间（人类可读）
  size?: string
  fileType?: string
  modifiedAt?: string
  children?: ServerNode[]
}

// 同步队列行：抓取(server→managed) / 下发(managed→server)
export interface SyncQueueRow {
  id: string
  // 文件名（mono 展示）
  name: string
  direction: 'fetch' | 'push'
  // 已完成 / 进行中(带百分比) / 待审核(抓取→待 ingest 审核) / 待审核确认(下发→待拓印 diff 确认)
  status: 'done' | 'running' | 'pending-ingest' | 'pending-imprint'
  // 进度百分比（仅 running 用）
  progress?: number
  // 覆盖层·目标（如 组 main / 实例 lobby-1 / 全局；Xftp 风传输面板「覆盖层·目标」列）
  scopeTarget: string
  // 源路径 → 目标路径（Xftp 风「源→目标」列）
  sourcePath: string
  targetPath: string
  // 人类可读时间
  time: string
}

// scope chip 候选（受管侧）
export interface ScopeOption {
  value: string
  label: string
  // 覆盖层类型，用于 chip 着色语义
  scope: OverrideScope
}

// server chip 候选（服务器侧）
export interface ServerOption {
  serverId: string
  label: string
  online: boolean
}

// 编辑器文件内容 + 历史修订（按受管文件 key 索引）
export interface WorkbenchFile {
  // 受管文件 key（= /configs/:id 的 id）
  key: string
  // 面包屑用：环境 / 组 / 文件名
  namespace: string
  group: string
  dataId: string
  scope: OverrideScope
  // 目标实例（生效目标，底部状态行用）
  targetServer: string
  format: string
  content: string
  revisions: WorkbenchRevision[]
}

export interface WorkbenchRevision {
  version: number
  author: string
  time: string
  comment: string
  // 该版本的全量内容（切版本时回灌编辑器）
  content: string
}

// ---- 受管配置树（改进 6：与服务器骨架同路径，仅含已纳管子集）----
// key 用 plugins/... 前缀；同名文件与服务器侧 srv/... 视觉对齐。

export const managedTree: ManagedNode[] = [
  {
    key: 'plugins',
    name: 'plugins',
    type: 'folder',
    sync: 'drift',
    children: [
      {
        key: 'plugins/Essentials',
        name: 'Essentials',
        type: 'folder',
        sync: 'drift',
        children: [
          { key: 'plugins/Essentials/config.yml', name: 'config.yml', type: 'file', sync: 'drift', scope: 'group', version: 7, modifiedAt: '今天 14:32' },
          { key: 'plugins/Essentials/kits.yml', name: 'kits.yml', type: 'file', sync: 'synced', scope: 'global', version: 3, modifiedAt: '昨天 09:10' },
        ],
      },
      {
        key: 'plugins/WorldGuard',
        name: 'WorldGuard',
        type: 'folder',
        sync: 'managed-only',
        children: [
          { key: 'plugins/WorldGuard/config.yml', name: 'config.yml', type: 'file', sync: 'managed-only', scope: 'server', version: 1, modifiedAt: '今天 09:30' },
        ],
      },
      { key: 'plugins/spawn.yml', name: 'spawn.yml', type: 'file', sync: 'drift', scope: 'group', version: 4, modifiedAt: '今天 13:50' },
      { key: 'plugins/motd.yml', name: 'motd.yml', type: 'file', sync: 'synced', scope: 'global', version: 2, modifiedAt: '3 天前 20:00' },
      { key: 'plugins/economy.yml', name: 'economy.yml', type: 'file', sync: 'managed-only', scope: 'server', version: 1, modifiedAt: '今天 10:00' },
      // 服务器已删、受管仍保留（红：server-gone），演示第四态
      { key: 'plugins/legacy.yml', name: 'legacy.yml', type: 'file', sync: 'server-gone', scope: 'group', version: 2, modifiedAt: '上周 三 09:00' },
    ],
  },
]

// ---- 服务器实时 plugins 树（改进 6：全量，含未纳管 untracked）----
// 与受管骨架同名同路径；多出未纳管文件 / 文件夹（待反向抓取纳管）。

export const serverTree: ServerNode[] = [
  {
    key: 'srv/plugins',
    name: 'plugins',
    type: 'folder',
    mark: 'drift',
    children: [
      {
        key: 'srv/plugins/Essentials',
        name: 'Essentials',
        type: 'folder',
        mark: 'drift',
        children: [
          { key: 'srv/plugins/Essentials/config.yml', name: 'config.yml', type: 'file', mark: 'drift', size: '12.4 KB', fileType: 'YAML 文件', modifiedAt: '今天 14:32' },
          { key: 'srv/plugins/Essentials/kits.yml', name: 'kits.yml', type: 'file', mark: 'tracked', size: '3.1 KB', fileType: 'YAML 文件', modifiedAt: '昨天 09:10' },
          // 未纳管：玩家数据目录（不入纳管，演示忽略规则）
          { key: 'srv/plugins/Essentials/userdata', name: 'userdata', type: 'folder', mark: 'untracked', children: [] },
        ],
      },
      {
        key: 'srv/plugins/WorldGuard',
        name: 'WorldGuard',
        type: 'folder',
        mark: 'drift',
        children: [
          { key: 'srv/plugins/WorldGuard/config.yml', name: 'config.yml', type: 'file', mark: 'tracked', size: '6.0 KB', fileType: 'YAML 文件', modifiedAt: '今天 09:30' },
          // 未纳管：大体量区域文件（待反向抓取纳管）
          { key: 'srv/plugins/WorldGuard/regions.yml', name: 'regions.yml', type: 'file', mark: 'untracked', size: '88.0 KB', fileType: 'YAML 文件', modifiedAt: '今天 11:05' },
        ],
      },
      { key: 'srv/plugins/spawn.yml', name: 'spawn.yml', type: 'file', mark: 'drift', size: '1.8 KB', fileType: 'YAML 文件', modifiedAt: '今天 13:50' },
      { key: 'srv/plugins/motd.yml', name: 'motd.yml', type: 'file', mark: 'tracked', size: '0.4 KB', fileType: 'YAML 文件', modifiedAt: '3 天前 20:00' },
      // 未纳管：服务器自带文件
      { key: 'srv/plugins/bukkit.yml', name: 'bukkit.yml', type: 'file', mark: 'untracked', size: '2.2 KB', fileType: 'YAML 文件', modifiedAt: '今天 08:00' },
      { key: 'srv/plugins/server.properties', name: 'server.properties', type: 'file', mark: 'untracked', size: '1.1 KB', fileType: '属性文件', modifiedAt: '今天 08:00' },
    ],
  },
]

// ---- 同步队列（实时演示）----

export const syncQueue: SyncQueueRow[] = [
  {
    id: 'q1',
    name: 'Essentials/config.yml',
    direction: 'fetch',
    status: 'done',
    scopeTarget: '组 main',
    sourcePath: 'lobby-1:/plugins/Essentials/config.yml',
    targetPath: 'prod/main/Essentials/config.yml',
    time: '14:32:10',
  },
  {
    id: 'q2',
    name: 'spawn.yml',
    direction: 'push',
    status: 'running',
    progress: 62,
    scopeTarget: '实例 lobby-1',
    sourcePath: 'prod/main/spawn.yml',
    targetPath: 'lobby-1:/plugins/spawn.yml',
    time: '14:33:01',
  },
  {
    id: 'q3',
    name: 'WorldGuard/regions.yml',
    direction: 'fetch',
    status: 'pending-ingest',
    scopeTarget: '组 main',
    sourcePath: 'lobby-1:/plugins/WorldGuard/regions.yml',
    targetPath: 'prod/main/WorldGuard/regions.yml',
    time: '14:33:20',
  },
  {
    id: 'q4',
    name: 'motd.yml',
    direction: 'push',
    status: 'pending-imprint',
    scopeTarget: '实例 lobby-1',
    sourcePath: 'prod/main/motd.yml',
    targetPath: 'lobby-1:/plugins/motd.yml',
    time: '14:30:55',
  },
]

// ---- 操作日志（撤回 / 回滚 / 详细记录）----
// 每次「大操作」（抓取入队 / 下发 / 发布 / 删除 / 重命名 / 移动 / 新建）留一条带上下文的日志，
// 支持逐条撤回与批量撤回；下发 / 发布类操作的撤回即回滚到操作前版本。

// 操作类型
export type OpAction = 'fetch' | 'push' | 'publish' | 'delete' | 'rename' | 'new' | 'move'

export interface OpLogEntry {
  id: string
  // 人类可读时间（HH:mm:ss）
  time: string
  action: OpAction
  // 操作人（登录身份，FR-11）
  operator: string
  // 受影响文件名列表
  files: string[]
  // 覆盖层·目标 / 落点（如「组 main」「实例 lobby-1」）
  target: string
  // 详细描述（中文，含文件 / 覆盖层 / 版本等上下文）
  detail: string
  // 是否已撤回（已撤回的不可再撤回，置灰）
  undone: boolean
  // 该操作产生的同步队列行 id（撤回时一并移除；运行期填充，历史种子可空）
  queueRowIds?: string[]
}

// 种子日志（历史操作示例，与初始同步队列对应）
export const operationLog: OpLogEntry[] = [
  {
    id: 'log-seed-1',
    time: '14:33:01',
    action: 'push',
    operator: 'admin',
    files: ['spawn.yml'],
    target: '实例 lobby-1',
    detail: '下发 spawn.yml 到 实例 lobby-1（覆盖层：组 main，v4）',
    undone: false,
  },
  {
    id: 'log-seed-2',
    time: '14:32:10',
    action: 'fetch',
    operator: 'admin',
    files: ['Essentials/config.yml'],
    target: '组 main',
    detail: '从 lobby-1 抓取 Essentials/config.yml 纳管到 组 main',
    undone: false,
  },
  {
    id: 'log-seed-3',
    time: '14:30:55',
    action: 'publish',
    operator: 'ops',
    files: ['motd.yml'],
    target: '全局',
    detail: '发布 motd.yml（全局 v2），热推到受影响在线服 2 台',
    undone: false,
  },
  {
    id: 'log-seed-4',
    time: '14:20:08',
    action: 'rename',
    operator: 'ops',
    files: ['economy.yml'],
    target: '受管配置',
    detail: '重命名 econ.yml → economy.yml',
    undone: true,
  },
]

// ---- scope / server 候选 ----

export const scopeOptions: ScopeOption[] = [
  { value: 'global', label: '全局', scope: 'global' },
  { value: 'group:main', label: '组 main', scope: 'group' },
  { value: 'group:pvp', label: '组 pvp', scope: 'group' },
  { value: 'server:lobby-1', label: '实例 lobby-1', scope: 'server' },
]

export const serverOptions: ServerOption[] = [
  { serverId: 'lobby-1', label: 'lobby-1', online: true },
  { serverId: 'lobby-2', label: 'lobby-2', online: true },
  { serverId: 'pvp-1', label: 'pvp-1', online: false },
]

// ---- 反向抓取扫描清单（改进 8：右→左抓取的「待审核 ingest」浮层数据，仿 FR-58~60）----
// 命令某台在线实例扫描其 plugins/ 后回传的全量清单，逐项可勾选纳管 + 忽略规则。

// 扫描清单单项：路径 + 大小 + 是否已被忽略规则命中 + 默认是否勾选
export interface IngestScanItem {
  path: string
  size: string
  // 命中忽略规则（如 *.db / userdata/**），默认不纳管
  ignored: boolean
  // 默认是否勾选纳管（已存在/文本配置默认勾，忽略项默认不勾）
  defaultPick: boolean
}

// 内置忽略规则（演示用，浮层底部展示）
export const ingestIgnoreRules: string[] = ['userdata/**', '*.db', '*.lock', 'logs/**']

export const ingestScanList: IngestScanItem[] = [
  { path: 'Essentials/config.yml', size: '12.4 KB', ignored: false, defaultPick: true },
  { path: 'Essentials/kits.yml', size: '3.1 KB', ignored: false, defaultPick: true },
  { path: 'Essentials/userdata/Notch.yml', size: '0.6 KB', ignored: true, defaultPick: false },
  { path: 'WorldGuard/config.yml', size: '6.0 KB', ignored: false, defaultPick: true },
  { path: 'WorldGuard/regions.yml', size: '88.0 KB', ignored: false, defaultPick: true },
  { path: 'bukkit.yml', size: '2.2 KB', ignored: false, defaultPick: true },
  { path: 'spawn.yml', size: '1.8 KB', ignored: false, defaultPick: true },
  { path: 'cache.db', size: '4.0 MB', ignored: true, defaultPick: false },
]

// ---- 拓印审核 diff（改进 8：左→右下发的「待审核确认」浮层数据，仿 FR-46）----
// 期望合并值 ⟷ 服务器现状，单人自审「看过才能确认」才真下发。

export interface ImprintDiff {
  // 期望合并后内容（受管覆盖链合并结果）
  expected: string
  // 服务器当前实况内容
  current: string
}

// 按文件名索引拓印 diff（原型只给少量演示）
export const imprintDiffs: Record<string, ImprintDiff> = {
  'motd.yml': {
    expected: `# 服务器 MOTD（受管·全局 期望值）
motd: '&b&l欢迎来到大厅 &7| &f输入 /help 查看帮助'
max-players: 200
`,
    current: `# 服务器 MOTD（服务器现状）
motd: '&b欢迎'
max-players: 100
`,
  },
  'spawn.yml': {
    expected: `# 出生点配置（受管·组 main 期望值）
spawns:
  default: { world: world, x: 128.5, y: 64.0, z: -64.5 }
  pvp: { world: pvp, x: 0.0, y: 72.0, z: 0.0 }
respawn-at-spawn: true
`,
    current: `# 出生点配置（服务器现状）
spawns:
  default: { world: world, x: 120.0, y: 64.0, z: -64.5 }
respawn-at-spawn: false
`,
  },
}

// ---- 生效预览（改进 8：受管面板「生效预览」视图，仿 FR-45/68）----
// 某服合并后有效树 + 逐键「完整覆盖链」（全局 → 组 → 实例 逐层取值）。

// 覆盖链上某一层的取值（仅列出「该层确有设值」的层；未设层不出现在链里）
export interface EffectiveLayer {
  scope: OverrideScope
  value: string
}

// 生效预览单键：键路径 + 完整覆盖链。
// chain 按 global → group → server 顺序排列「确有设值」的层，最后一项即最终生效值；
// 之前各层为「被覆盖（遮蔽）」层。链长 > 1（或最终生效层非 global）即视为被定制。
export interface EffectiveKey {
  key: string
  chain: EffectiveLayer[]
}

// 生效预览单文件：文件名 + 逐键覆盖链
export interface EffectiveFile {
  name: string
  keys: EffectiveKey[]
}

// 按目标实例索引（原型仅给 lobby-1 一份示意）。
// 演示三类链：纯全局基线（未定制）、组覆盖全局、实例覆盖全局（组未设而跳层）。
export const effectivePreview: Record<string, EffectiveFile[]> = {
  'lobby-1': [
    {
      name: 'Essentials/config.yml',
      keys: [
        // 仅全局设值 → 基线，未被定制
        { key: 'ops-name-color', chain: [{ scope: 'global', value: "'c'" }] },
        // 全局 '+' 被组 '~' 覆盖
        { key: 'nickname-prefix', chain: [{ scope: 'global', value: "'+'" }, { scope: 'group', value: "'~'" }] },
        // 全局 '3' 被组 '0' 覆盖
        { key: 'teleport-cooldown', chain: [{ scope: 'global', value: '3' }, { scope: 'group', value: '0' }] },
        // 全局 false 被实例 true 覆盖（组未设、跳层）
        { key: 'spawn-on-join', chain: [{ scope: 'global', value: 'false' }, { scope: 'server', value: 'true' }] },
      ],
    },
    {
      name: 'spawn.yml',
      keys: [
        { key: 'spawns.default.world', chain: [{ scope: 'global', value: 'world' }] },
        { key: 'spawns.default.x', chain: [{ scope: 'global', value: '120.0' }, { scope: 'group', value: '128.5' }] },
        { key: 'respawn-at-spawn', chain: [{ scope: 'global', value: 'false' }, { scope: 'group', value: 'true' }] },
      ],
    },
    {
      name: 'motd.yml',
      keys: [
        { key: 'motd', chain: [{ scope: 'global', value: "'&b&l欢迎来到大厅 …'" }] },
        { key: 'max-players', chain: [{ scope: 'global', value: '100' }, { scope: 'group', value: '200' }] },
      ],
    },
  ],
}

// ---- 发布影响面（改进 1，仿 FR-2 热推语义）----
// 受管层文件改一处影响多台服：发布不是逐台拖，而是「按覆盖层发布 → 热推到所有受影响在线服」。
// 按选中文件 + 覆盖层解析受影响服务器清单，按层分组（全局→全部 / 组→该组 / 实例→定向）。

// 受影响的单台服务器
export interface PublishImpactServer {
  serverId: string
  // 是否在线（仅在线服本次热推；离线服按各自覆盖链上线时拉取）
  online: boolean
  // 该服相对待发布值是否有差异（有差异→进拓印审核门）
  changed: boolean
}

// 按覆盖层分组的影响面（如「组 main → 该组 N 台」）
export interface PublishImpactGroup {
  // 覆盖层类型（决定该组解析口径）
  scope: OverrideScope
  // 分组标签（如「全局」「组 main」「实例 lobby-1」）
  label: string
  servers: PublishImpactServer[]
}

// 待发布的单个文件（清单行）
export interface PublishFileEntry {
  // 文件名（含路径，如 spawn.yml）
  name: string
  scope: OverrideScope
  // 当前版本 → 发布后版本（vN → vN+1）
  fromVersion: number
  toVersion: number
}

// 发布影响面响应：将发布清单 + 按层分组的受影响服 + 拓印有差异台数
export interface PublishImpact {
  files: PublishFileEntry[]
  groups: PublishImpactGroup[]
  // 在线且与待发布值有差异的台数（拓印审核门据此提示）
  driftCount: number
}

// 选中文件名（末段）→ 覆盖层 + 当前版本（用于解析待发布清单）
const PUBLISH_FILE_META: Record<string, { scope: OverrideScope; version: number }> = {
  'config.yml': { scope: 'group', version: 7 },
  'kits.yml': { scope: 'global', version: 3 },
  'spawn.yml': { scope: 'group', version: 4 },
  'motd.yml': { scope: 'global', version: 2 },
  'economy.yml': { scope: 'server', version: 1 },
  'legacy.yml': { scope: 'group', version: 2 },
}

// 各覆盖层解析口径（演示）：全局→全部在线、组 main→该组在线、实例→定向单服
const PUBLISH_SCOPE_GROUPS: Record<OverrideScope, PublishImpactGroup> = {
  global: {
    scope: 'global',
    label: '全局',
    servers: [
      { serverId: 'lobby-1', online: true, changed: true },
      { serverId: 'lobby-2', online: true, changed: false },
      { serverId: 'pvp-1', online: false, changed: true },
    ],
  },
  group: {
    scope: 'group',
    label: '组 main',
    servers: [
      { serverId: 'lobby-1', online: true, changed: true },
      { serverId: 'lobby-2', online: true, changed: true },
    ],
  },
  server: {
    scope: 'server',
    label: '实例 lobby-1',
    servers: [{ serverId: 'lobby-1', online: true, changed: true }],
  },
}

// 按选中文件名解析发布影响面（纯前端 mock）：取各文件覆盖层、合并去重涉及的分组、统计在线有差异台数。
export function resolvePublishImpact(names: string[]): PublishImpact {
  const files: PublishFileEntry[] = names.map((raw) => {
    const base = raw.split('/').pop() ?? raw
    const meta = PUBLISH_FILE_META[base] ?? { scope: 'group' as OverrideScope, version: 1 }
    return { name: raw, scope: meta.scope, fromVersion: meta.version, toVersion: meta.version + 1 }
  })
  // 涉及的覆盖层（按出现顺序去重）
  const scopes: OverrideScope[] = []
  for (const f of files) if (!scopes.includes(f.scope)) scopes.push(f.scope)
  const groups = scopes.map((s) => PUBLISH_SCOPE_GROUPS[s])
  // 在线且有差异的台数（按 serverId 去重，避免跨组重复计数）
  const driftIds = new Set<string>()
  for (const g of groups) for (const s of g.servers) if (s.online && s.changed) driftIds.add(s.serverId)
  return { files, groups, driftCount: driftIds.size }
}

// ---- 文件内容 + 历史修订 ----

const ESS_CONFIG = `# Essentials 主配置（受管·组 main 覆盖）
ops-name-color: 'c'
nickname-prefix: '~'
teleport-cooldown: 0
teleport-delay: 0
spawn-on-join: true
`

const SPAWN_YML = `# 出生点配置
spawns:
  default:
    world: world
    x: 128.5
    y: 64.0
    z: -64.5
  pvp:
    world: pvp
    x: 0.0
    y: 72.0
    z: 0.0
respawn-at-spawn: true
`

const MOTD_YML = `# 服务器 MOTD
motd: '&b&l欢迎来到大厅 &7| &f输入 /help 查看帮助'
max-players: 200
`

const ECONOMY_YML = `# 经济配置（受管·实例 lobby-1 覆盖，仅受管·待下发）
starting-balance: 1000
currency-symbol: '$'
max-money: 10000000000
`

const KITS_YML = `# 礼包配置
kits:
  starter:
    delay: 0
    items:
      - bread 16
      - wooden_sword 1
`

const WG_CONFIG = `# WorldGuard 主配置（仅受管·待下发）
build-permission-nodes:
  enable: false
regions:
  enable: true
  high-frequency-flags: false
`

const LEGACY_YML = `# 旧配置（服务器侧已删，受管仍保留）
deprecated: true
note: 服务器实况已无此文件
`

export const workbenchFiles: Record<string, WorkbenchFile> = {
  'plugins/Essentials/config.yml': {
    key: 'plugins/Essentials/config.yml',
    namespace: 'prod',
    group: 'main',
    dataId: 'Essentials/config.yml',
    scope: 'group',
    targetServer: 'lobby-1',
    format: 'yaml',
    content: ESS_CONFIG,
    revisions: [
      { version: 7, author: 'admin', time: '今天 14:32', comment: '调整传送冷却为 0', content: ESS_CONFIG },
      { version: 6, author: 'admin', time: '昨天 18:01', comment: '加入 spawn-on-join', content: ESS_CONFIG.replace('spawn-on-join: true\n', '') },
      { version: 5, author: 'ops', time: '3 天前 10:22', comment: '昵称前缀改为 ~', content: ESS_CONFIG.replace("'~'", "'+'") },
      { version: 4, author: 'ops', time: '上周 二 09:00', comment: '初版导入', content: '# Essentials 主配置（初版）\nteleport-cooldown: 3\n' },
    ],
  },
  'plugins/spawn.yml': {
    key: 'plugins/spawn.yml',
    namespace: 'prod',
    group: 'main',
    dataId: 'spawn.yml',
    scope: 'group',
    targetServer: 'lobby-1',
    format: 'yaml',
    content: SPAWN_YML,
    revisions: [
      { version: 4, author: 'admin', time: '今天 13:50', comment: '新增 pvp 出生点', content: SPAWN_YML },
      { version: 3, author: 'admin', time: '昨天 15:30', comment: '微调默认坐标', content: SPAWN_YML.replace('128.5', '120.0') },
      { version: 2, author: 'ops', time: '上周 五 12:00', comment: '开启回生点', content: '# 出生点配置\nrespawn-at-spawn: true\n' },
      { version: 1, author: 'ops', time: '上周 一 08:00', comment: '初版', content: '# 出生点配置\n' },
    ],
  },
  'plugins/motd.yml': {
    key: 'plugins/motd.yml',
    namespace: 'prod',
    group: 'main',
    dataId: 'motd.yml',
    scope: 'global',
    targetServer: '全局',
    format: 'yaml',
    content: MOTD_YML,
    revisions: [
      { version: 2, author: 'admin', time: '3 天前 20:00', comment: '上限提到 200', content: MOTD_YML },
      { version: 1, author: 'admin', time: '上周 三 09:00', comment: '初版', content: '# 服务器 MOTD\nmotd: \'欢迎\'\nmax-players: 100\n' },
    ],
  },
  'plugins/economy.yml': {
    key: 'plugins/economy.yml',
    namespace: 'prod',
    group: 'main',
    dataId: 'economy.yml',
    scope: 'server',
    targetServer: 'lobby-1',
    format: 'yaml',
    content: ECONOMY_YML,
    revisions: [
      { version: 1, author: 'admin', time: '今天 10:00', comment: '新建（仅受管·待下发）', content: ECONOMY_YML },
    ],
  },
  'plugins/Essentials/kits.yml': {
    key: 'plugins/Essentials/kits.yml',
    namespace: 'prod',
    group: 'main',
    dataId: 'Essentials/kits.yml',
    scope: 'global',
    targetServer: '全局',
    format: 'yaml',
    content: KITS_YML,
    revisions: [
      { version: 3, author: 'ops', time: '昨天 09:10', comment: '加入新手礼包', content: KITS_YML },
      { version: 2, author: 'ops', time: '上周 四 14:00', comment: '初版', content: '# 礼包配置\nkits: {}\n' },
    ],
  },
  'plugins/WorldGuard/config.yml': {
    key: 'plugins/WorldGuard/config.yml',
    namespace: 'prod',
    group: 'main',
    dataId: 'WorldGuard/config.yml',
    scope: 'server',
    targetServer: 'lobby-1',
    format: 'yaml',
    content: WG_CONFIG,
    revisions: [
      { version: 1, author: 'admin', time: '今天 09:30', comment: '新建（仅受管·待下发）', content: WG_CONFIG },
    ],
  },
  'plugins/legacy.yml': {
    key: 'plugins/legacy.yml',
    namespace: 'prod',
    group: 'main',
    dataId: 'legacy.yml',
    scope: 'group',
    targetServer: 'lobby-1',
    format: 'yaml',
    content: LEGACY_YML,
    revisions: [
      { version: 2, author: 'ops', time: '上周 三 09:00', comment: '标记弃用', content: LEGACY_YML },
    ],
  },
}

// 受管文件 key 列表（编辑器路由校验未知 id 用）
export function workbenchFileKeys(): string[] {
  return Object.keys(workbenchFiles)
}
