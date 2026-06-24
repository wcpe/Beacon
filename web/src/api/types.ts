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

// bc（bungee 代理）专属负载指标视图（FR-34，仅展示不参与决策；bukkit 恒为零值）
// backendAvgLatencyMs < 0（约定 -1）表示无可达后端样本（不可用）
export interface ProxyMetricsView {
  onlineConnections: number
  threadCount: number
  uptimeMs: number
  backendUp: number
  backendTotal: number
  backendAvgLatencyMs: number
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
  // agent 自身构建版本（FR-86，见 ADR-0039）：agent 注册自报、旧 agent 为空串；仅展示
  agentVersion: string
  status: string
  capacity: number
  weight: number
  metadata: Record<string, string>
  lastHeartbeat: string
  // 距上次心跳的秒数（控制面渲染时刻算，负值归零；仅展示，FR-81）
  lastHeartbeatAgeSec: number
  // 触发当前状态的原因文案（如「35s 未心跳 > ttl 30s」；online 时空串，FR-81）
  healthReason: string
  appliedMd5: string
  playerCount: number
  tps: number
  // bc（bungee）当前代理的后端子服 serverId 集合（仅 bc 非空、bukkit 恒空，FR-36）；供拓扑连线消费
  backends: string[]
  // 该 bukkit 子服是否被指定为其小区默认入口（FR-48；bungee 恒 false）
  zoneDefaultEntry: boolean
  // bc 专属负载指标（FR-34，仅 bc 非零、bukkit 恒零）；供代理服管理页逐台展示底层参数（FR-52）
  proxy: ProxyMetricsView
  registeredAt: string
}

// 发布影响面预览视图（FR-79）：某条 scope 此刻会落到的在线子服集合
export interface ImpactView {
  namespace: string
  scopeLevel: string
  group: string
  scopeTarget: string
  // 受影响的在线子服 serverId（字典序）
  affected: string[]
  // = affected 长度
  total: number
}

// per-server 有效配置变更时间线条目（FR-80）：该服覆盖链上某 config 项的一次发布（含首发 / 发布 / 回滚）
export interface ConfigTimelineEntry {
  // 所属 config 项 id（同项多版本共享）
  configItemId: number
  dataId: string
  // 该项所在覆盖层：global / group / zone / server
  scopeLevel: string
  // 该层目标键（global/group 为空串；zone=zone编码；server=serverId）
  scopeTarget: string
  version: number
  md5: string
  operator: string
  comment: string
  // 发布时间（UTC ISO 串）
  createdAt: string
}

// per-server 有效配置变更时间线视图（FR-80）：某子服覆盖链涉及 config 项的发布历史，按时间倒序
export interface ConfigTimelineView {
  namespace: string
  serverId: string
  group: string
  zone: string
  items: ConfigTimelineEntry[]
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

// 小区默认入口视图（FR-48）：每小区唯一指定一个在线 bukkit serverId 作 BC 默认/fallback 服
export interface DefaultEntryView {
  namespace: string
  group: string
  zone: string
  defaultServerId: string
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

// ===== 控制面自观测（FR-82）=====

// 数据库连接池统计（取自 sql.DBStats，非方言；累计字段为进程起算累计值）
export interface DbPoolView {
  maxOpenConnections: number
  openConnections: number
  inUse: number
  idle: number
  waitCount: number
  waitDurationMs: number
}

// 长轮询四通道当前挂起 waiter 数（config/file/topology/command + 合计）
export interface LongpollView {
  config: number
  file: number
  topology: number
  command: number
  total: number
}

// 控制面自观测快照视图（对齐 internal/handler/observability_handler.go observabilityView）
// 控制面进程内部运行态——区别于 FR-33 页眉条与 FR-32 agent 网络负载，只读。
export interface ObservabilityView {
  dbPool: DbPoolView
  longpoll: LongpollView
  // 注册表按健康状态计数（online/degraded/lost/offline，无某状态则该键缺省）
  registryByStatus: Record<string, number>
  registryTotal: number
  // 命令队列按状态计数（pending/fetched/ready/...，无某状态则该键缺省）
  commandByStatus: Record<string, number>
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

// ===== 在线日志/诊断查看器（FR-88，见 ADR-0040）=====

// agent 自身日志的一行（级别 + 已脱敏文本；脱敏在 agent 侧落环形缓冲那一刻完成）。
export interface AgentLogLine {
  level: string
  text: string
}

// 取 agent 日志视图（POST/GET /admin/v1/instances/{serverId}/logs）：
// 命令 id + 状态 + 若 done 则附脱敏日志行（进行中 / 失败时 lines 为空）。
export interface AgentLogView {
  commandId: number
  status: AgentCommandStatus
  lines: AgentLogLine[]
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

// ===== 反向抓取受管任务 + 审核台 + 冲突 diff（FR-58/59/60）=====

// 反向抓取受管任务状态机（对齐 internal/model/enums.go）：
// scanning（下发 scan 待 agent 回清单）/ pending-review（清单已到待审核选定）/
// fetching（已提交选定待 agent 回内容）/ conflict-review（选定内容与目标已有版本冲突待人工 diff）/
// ingesting（落库中）/ done / failed / cancelled / expired（终态）。
export type ReverseFetchTaskStatus =
  | 'scanning'
  | 'pending-review'
  | 'fetching'
  | 'conflict-review'
  | 'ingesting'
  | 'done'
  | 'failed'
  | 'cancelled'
  | 'expired'

// 任务目标层：group（落组级覆盖）/ server（落实例级覆盖）。复用 FR-39 的 ReverseFetchScope。

// 扫描清单单文件视图（无内容，对齐 handler.reverseFetchScanFileView）。
// overThreshold=true 表示超大小阈值（须人工勾确认才纳入）；ignoredByRule=true 表示命中活跃持久忽略规则（默认排除）。
export interface ReverseFetchScanFileView {
  path: string
  size: number
  isText: boolean
  overThreshold: boolean
  ignoredByRule: boolean
}

// 反向抓取受管任务视图（对齐 handler.reverseFetchTaskView）。
// 清单与选定经后端 best-effort 解析后展开为 files / selectedPaths 数组。
export interface ReverseFetchTaskView {
  id: number
  namespace: string
  serverId: string
  scope: string
  group: string
  target: string
  status: string
  scanCommandId: number
  submitCommandId: number
  totalFiles: number
  selectedCount: number
  overThresholdCount: number
  skippedCount: number
  files: ReverseFetchScanFileView[]
  selectedPaths: string[]
  operator: string
  note: string
  // 失败原因明细（FR-87）：agent 回传 scan/submit 错误或控制面入库失败时非空，供 failed 任务展示
  lastError: string
  createdAt: string
  updatedAt: string
  // 已用时长（FR-87）：控制面渲染时刻距 updatedAt 的秒数（≥0），供时长展示 + 非终态卡死警示
  elapsedSec: number
}

// 单个冲突文件 diff 视图（对齐 handler.conflictDiffView）：抓取值 ⟷ 目标已有版本。
export interface ConflictDiffView {
  path: string
  fetchedContent: string
  fetchedMd5: string
  existingContent: string
  existingMd5: string
  version: number
}

// 单个冲突文件处置：overwrite（取抓取值，须带自审 reviewedMd5=fetchedMd5）/ keep（保留已有）。
export interface ResolveDecision {
  path: string
  action: 'overwrite' | 'keep'
  reviewedMd5?: string
}

// 冲突审核落库结果（对齐 handler.Resolve 响应）。
export interface ResolveResult {
  created: number
  updated: number
}

// 持久忽略规则视图（对齐 handler.ignoreRuleView）。ruleType 取值 exact（单文件精确）/ prefix（目录前缀）。
export interface IgnoreRuleView {
  id: number
  namespace: string
  scope: string
  group: string
  target: string
  ruleType: string
  pattern: string
  comment: string
  operator: string
  createdAt: string
}

// 忽略规则类型：exact（单文件精确匹配）/ prefix（目录前缀匹配）。
export type IgnoreRuleType = 'exact' | 'prefix'

// ===== 服务分析 / 平台用量看板（FR-73）=====

// 按动作分布单条（降序按 count，action 为审计枚举原值、前端 i18n 映射中文）。
export interface AuditActionCount {
  action: string
  count: number
}

// 每日趋势单条（升序按 date，date 为 UTC 日 YYYY-MM-DD）。
export interface AuditDayCount {
  date: string
  count: number
}

// 服务分析聚合视图（对齐 docs/specs/service-analysis.md §3.2 契约）：
// 时间窗内审计活动的总数 / 成功 / 失败 + 按动作分布 + 每日趋势。
export interface AuditAnalytics {
  from: string
  to: string
  total: number
  okCount: number
  failCount: number
  byAction: AuditActionCount[]
  byDay: AuditDayCount[]
}

// ===== 运维设置（FR-62，消费 FR-61 设置端点）=====

// 设置项值类型（对齐后端 GET /admin/v1/settings 的 valueType）。
export type SettingValueType = 'int' | 'bool' | 'string'

// 单条运维设置项视图（对齐 GET /admin/v1/settings 的 items 元素）。
// 白名单皆热改项，isStartup 恒 false；启动 / 安全项不进白名单、此处不可见。
export interface SettingView {
  key: string
  // 当前生效值（字符串形态；按 valueType 解释展示与编辑）
  value: string
  valueType: SettingValueType
  // 默认值（字符串形态）
  default: string
  // 中文说明
  desc: string
  // 是否启动项（白名单皆热改项，恒 false）
  isStartup: boolean
}
