/**
 * 配置中心「双面板 Xftp 工作台」(FR-111) 的视图类型（真源）。
 *
 * 这些类型是工作台组件 / hook / 测试共享的展示形状；FR-114 原型期它们随 mock 数据同住
 * 一个 mock 模块，FR-111 接真后端后 mock 数据退场，类型迁来此处独立承载——
 * `useWorkbenchData` 的真链路 hook 把既有真实端点的响应适配成这些形状，组件契约不变。
 */

// 同步状态（左面板行首点）：一致(绿) / 有差异待下发(琥珀) / 仅受管未下发(蓝) / 服务器已删(红)
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
  // 该受管文件与服务器实况的同步状态（文件夹取其后代聚合态）
  sync: SyncStatus
  // 文件专属：覆盖层 + 版本号 + 修改时间（人类可读，Xftp 风「修改时间」列）
  scope?: OverrideScope
  version?: number
  modifiedAt?: string
  // 文件专属：后端文件对象 id（编辑器 / 历史按此拉真内容；无 id 表示纯展示节点）
  fileId?: number
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

// 操作类型（操作日志 / 撤回，FR-114 原型；真撤回属 FR-116）
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

// 反向抓取扫描清单单项：路径 + 大小 + 是否已被忽略规则命中 + 默认是否勾选
export interface IngestScanItem {
  path: string
  size: string
  // 命中忽略规则（如 *.db / userdata/**），默认不纳管
  ignored: boolean
  // 默认是否勾选纳管（已存在/文本配置默认勾，忽略项默认不勾）
  defaultPick: boolean
}

// 拓印审核 diff：期望合并值 ⟷ 服务器现状
export interface ImprintDiff {
  // 期望合并后内容（受管覆盖链合并结果）
  expected: string
  // 服务器当前实况内容
  current: string
}

// 生效预览：覆盖链上某一层的取值（仅列出「该层确有设值」的层）
export interface EffectiveLayer {
  scope: OverrideScope
  value: string
}

// 生效预览单键：键路径 + 完整覆盖链。
// chain 按 global → group → server 顺序排列「确有设值」的层，最后一项即最终生效值。
export interface EffectiveKey {
  key: string
  chain: EffectiveLayer[]
}

// 生效预览单文件：文件名 + 逐键覆盖链
export interface EffectiveFile {
  name: string
  keys: EffectiveKey[]
}

// 发布影响面：受影响的单台服务器
export interface PublishImpactServer {
  serverId: string
  // 是否在线（仅在线服本次热推；离线服按各自覆盖链上线时拉取）
  online: boolean
  // 该服相对待发布值是否有差异（有差异→进拓印审核门）
  changed: boolean
}

// 按覆盖层分组的影响面（如「组 main → 该组 N 台」）
export interface PublishImpactGroup {
  scope: OverrideScope
  label: string
  servers: PublishImpactServer[]
}

// 待发布的单个文件（清单行）
export interface PublishFileEntry {
  name: string
  scope: OverrideScope
  // 当前版本 → 发布后版本（vN → vN+1）
  fromVersion: number
  toVersion: number
}

// 发布影响面响应：发布清单 + 按层分组的受影响服 + 拓印有差异台数
export interface PublishImpact {
  files: PublishFileEntry[]
  groups: PublishImpactGroup[]
  // 在线且与待发布值有差异的台数（拓印审核门据此提示）
  driftCount: number
}
