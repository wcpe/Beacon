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
  registeredAt: string
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
