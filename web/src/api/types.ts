// 管理台数据类型：严格对齐后端 REST 契约（docs/API.md 与 internal/handler/*.go）。
// 字段名与后端 JSON 一一对应，不臆造、不增删。

// 覆盖层级（global / group / zone / server）
export type ScopeLevel = 'global' | 'group' | 'zone' | 'server'

// 配置格式
export type ConfigFormat = 'yaml' | 'properties' | 'json'

// 实例健康状态
export type InstanceStatus = 'online' | 'lost' | 'offline'

// 配置项视图（content 仅详情接口返回，列表不含）
export interface ConfigView {
  id: number
  namespace: string
  group: string
  dataId: string
  scopeLevel: string
  scopeTarget: string
  format: string
  version: number
  md5: string
  enabled: boolean
  updatedAt: string
  content?: string
}

// 历史版本视图（content 仅取单版本时返回）
export interface RevisionView {
  version: number
  md5: string
  operator: string
  comment: string
  sourceRevision: number | null
  createdAt: string
  content?: string
}

// 版本 diff 返回体
export interface DiffView {
  fromVersion: number
  toVersion: number
  fromContent: string
  toContent: string
}

// 发布 / 回滚返回体
export interface PublishResult {
  version: number
  md5: string
}

// 实例视图（未分配 zone 时 zone 为 null）
export interface InstanceView {
  namespace: string
  serverId: string
  role: string
  group: string
  zone: string | null
  assigned: boolean
  address: string
  version: string
  status: string
  capacity: number
  weight: number
  metadata: Record<string, string>
  lastHeartbeat: string
  appliedMd5: string
  playerCount: number
  tps: number
  // bc（bungee）当前代理的后端子服 serverId 集合（仅 bc 非空、bukkit 恒空，FR-36）；供拓扑连线消费
  backends: string[]
  registeredAt: string
}

// ===== 集群拓扑（FR-37）=====

// 拓扑节点（一个在线实例；zone 未分配时为 null）
export interface TopologyNode {
  serverId: string
  role: string
  group: string
  zone: string | null
  status: string
  address: string
}

// bc→bukkit 连线（source/target 均为 serverId）
export interface TopologyEdge {
  source: string
  target: string
}

// 大区 / zone 分组（zone 未分配时为 null）
export interface TopologyGroup {
  group: string
  zone: string | null
  members: string[]
}

// 拓扑快照返回体（对齐 internal/handler/topology_handler.go topologyView）
export interface TopologyView {
  namespace: string
  nodes: TopologyNode[]
  edges: TopologyEdge[]
  groups: TopologyGroup[]
}

// zone 指派视图
export interface AssignmentView {
  namespace: string
  serverId: string
  group: string
  zone: string
  note: string
  updatedAt: string
}

// zone 维度汇总视图
export interface ZoneStatView {
  group: string
  zone: string
  serverCount: number
  onlineCount: number
}

// 审计记录视图（对齐 docs/API.md 与 model.AuditLog 字段）
export interface AuditView {
  namespace: string
  operator: string
  action: string
  targetType: string
  targetRef: string
  detail: string
  result: string
  clientIp: string
  createdAt: string
}

// 审计分页返回体
export interface AuditPage {
  total: number
  items: AuditView[]
}

// 环境视图
export interface NamespaceView {
  code: string
  name: string
}

// ===== 登录 / 身份（FR-11 鉴权）=====

// 登录返回体：令牌 + 操作者身份
export interface LoginResult {
  token: string
  operator: string
}

// ===== 控制面自身状态（FR-33）=====

// 数据库连通性（error 仅断开时带）
export interface DbStatusView {
  connected: boolean
  error?: string
}

// Go 运行时资源（heapAlloc / heapSys 单位为字节，由前端格式化）
export interface RuntimeStatsView {
  goroutines: number
  heapAlloc: number
  heapSys: number
}

// 控制面自身状态视图（对齐 internal/handler/system_handler.go systemStatusView）
// 区别于 FR-32 的 agent 网络聚合指标——这里是控制面进程本身的健康。
export interface SystemStatusView {
  version: string
  startedAt: string
  uptimeSeconds: number
  db: DbStatusView
  onlineInstances: number
  samplerEnabled: boolean
  runtime: RuntimeStatsView
  // cpuAvailable=false 表示进程 CPU% 采集失败、不可用；为 true 时 cpuPercent（[0,100]）才有意义
  cpuAvailable: boolean
  cpuPercent: number
}

// ===== 管理面 API 密钥（FR-42，只读角色 + 运行时密钥，见 ADR-0026）=====

// 密钥角色：full（读写）/ readonly（只读）
export type ApiKeyRole = 'full' | 'readonly'

// 密钥状态：active（生效）/ expired（已过期）/ revoked（已吊销）
export type ApiKeyStatus = 'active' | 'expired' | 'revoked'

// 密钥视图（列表 / 元数据）：**绝不含明文与哈希**，仅非机密前缀片段供识别
export interface ApiKeyView {
  id: number
  name: string
  role: string
  keyPrefix: string
  status: string
  createdAt: string
  expiresAt: string | null
  lastUsedAt: string | null
}

// 创建 / 重置返回体：在元数据之外**一次性**附带明文 key（之后不可再得，丢失只能重置）
export interface ApiKeyCreated extends ApiKeyView {
  key: string
}

// ===== 文件树托管（通道B，FR-14）=====

// 文件对象视图（content 仅详情接口返回，列表不含；对齐 internal/handler/file_handler.go fileView）
export interface FileView {
  id: number
  namespace: string
  group: string
  path: string
  scopeLevel: string
  scopeTarget: string
  version: number
  md5: string
  enabled: boolean
  updatedAt: string
  content?: string
}

// 文件历史版本视图（content 仅取单版本时返回）
export interface FileRevisionView {
  version: number
  md5: string
  operator: string
  comment: string
  sourceRevision: number | null
  createdAt: string
  content?: string
}

// ===== 配置导入·在线实例反向抓取（FR-39）=====

// 反向抓取目标层：group（组级覆盖）/ server（实例级覆盖）
export type ReverseFetchScope = 'group' | 'server'

// 反向抓取 / 拓印命令状态（命令生命周期，真源落库；对齐 model.AgentCommand）
// pending（已建待拉）/ fetched（agent 已拉取）/ ready（FR-46 拓印已抓取待确认）/ done（完成）/ failed（失败）/ expired（超时）
export type AgentCommandStatus = 'pending' | 'fetched' | 'ready' | 'done' | 'failed' | 'expired'

// 反向抓取命令视图（POST /admin/v1/instances/{serverId}/reverse-fetch 返回的已创建命令）
// 触发即返回 pending 命令，后续状态经命令查询 / 审计 / 文件树体现，不在触发响应里同步等待结果。
export interface AgentCommandView {
  id: number
  namespace: string
  serverId: string
  type: string
  status: string
  createdAt: string
  updatedAt: string
}

// ===== 按需拓印回写 + 审核台（FR-46）=====

// 拓印并入层：与文件覆盖四层一致（global/group/zone/server）。
export type ImprintScope = 'global' | 'group' | 'zone' | 'server'

// 拓印 diff 视图（GET /admin/v1/imprints/{commandId}/diff，对齐 handler.imprintDiffView）：
// 本地实际值（agent 回传、命令转存的磁盘原文）⟷ 期望合并值（按并入层视角解出的覆盖链合并结果，复用 FR-45）。
export interface ImprintDiffView {
  path: string
  // 本地实际值：拓印源磁盘当前内容 + md5（md5 确认时回带作自审凭据）
  actualContent: string
  actualMd5: string
  // 期望合并值：覆盖链合并结果 + md5（期望侧无该文件时为空串）
  expectedContent: string
  expectedMd5: string
  // 期望合并值是否整文件覆盖模式（结构化深合并为 false）
  expectedWholeFile: boolean
  // 期望合并值逐键 / 整文件来源（复用 FR-45 provenance，来源徽标）
  expectedSources: Array<{ path: string[]; scope: string }>
  // 期望侧被减量删除的键（结构化）
  expectedDeletions: Array<{ path: string[]; scope: string }>
  // 本地实际值与期望合并值是否有差异
  differs: boolean
}

// 拓印确认落库结果视图（POST /admin/v1/imprints/{commandId}/confirm，对齐 handler.imprintConfirmView）。
export interface ImprintConfirmView {
  fileId: number
  scopeLevel: string
  group: string
  target: string
  version: number
  md5: string
}

// ===== 三方文件覆盖兼容（override-set，FR-15）=====

// 覆盖集视图（对齐 internal/handler/override_set_handler.go overrideSetView）
export interface OverrideSetView {
  id: number
  namespace: string
  group: string
  name: string
  scopeLevel: string
  scopeTarget: string
  targetRoot: string
  reloadCommand: string
  mode: string
  version: number
  enabled: boolean
  updatedAt: string
}

// 覆盖集历史版本视图
export interface OverrideSetRevisionView {
  version: number
  targetRoot: string
  reloadCommand: string
  operator: string
  comment: string
  sourceRevision: number | null
  createdAt: string
}

// 覆盖集发布前 dry-run 只读预览：将覆盖哪些成员文件 + 将执行什么命令
export interface OverrideSetDryRunView {
  targetRoot: string
  reloadCommand: string
  commandFirstToken: string
  memberPaths: string[]
}
