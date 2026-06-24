// 管理台 API fetch 封装。
// base 固定为 /admin/v1；开发期由 vite proxy 转发到本地控制面，生产期同源同端口。
// 所有端点严格对齐 docs/API.md 与 internal/handler/*.go，非 2xx 时解析统一错误体并抛出。

import type {
  AgentCommandView,
  AgentLogView,
  ApiKeyCreated,
  ApiKeyView,
  AssignmentView,
  AuditAnalytics,
  AuditPage,
  AlertEventPage,
  ConfigTimelineView,
  ConfigView,
  ConflictDiffView,
  DefaultEntryView,
  DiffView,
  FileRevisionView,
  FileView,
  IgnoreRuleType,
  IgnoreRuleView,
  ImprintConfirmView,
  ImprintDiffView,
  ImprintScope,
  ImpactView,
  InstanceView,
  LoginResult,
  NamespaceView,
  OverrideSetDryRunView,
  OverrideSetRevisionView,
  OverrideSetView,
  PublishResult,
  ResolveDecision,
  ResolveResult,
  ReverseFetchScope,
  ReverseFetchTaskView,
  RevisionView,
  SettingView,
  SystemStatusView,
  ObservabilityView,
  TopologyView,
  ZoneStatView,
} from './types'
import { clearAuth, currentToken } from '../state/auth'

// API 基址：所有管理台接口的公共前缀
const BASE = '/admin/v1'

// 令牌失效（401）时的全局回调；由应用层注册（如跳登录页），避免 client 反向依赖 router。
let unauthorizedHandler: (() => void) | null = null

// 注册 401 处理器：任意 admin 请求遇 401 时触发（清登录态后由处理器跳登录）。
export function setOnUnauthorized(handler: () => void): void {
  unauthorizedHandler = handler
}

// 统一错误体（与 docs/API.md 对齐）：失败时后端返回 { code, message, traceId? }
interface ApiError {
  code?: string
  message?: string
  traceId?: string
}

// 携带业务码的客户端错误：在 Error.message（中文说明，既有调用方按此提示）之外保留后端业务码，
// 供调用方按 code 分支处理（如 FR-71 排空门 409 的 ZONE_SERVER_ONLINE_NONEMPTY 专属提示）。
export class ApiClientError extends Error {
  // 后端业务码（如 ZONE_SERVER_ONLINE_NONEMPTY）；后端未回 code 时为空串
  readonly code: string

  constructor(message: string, code: string) {
    super(message)
    this.name = 'ApiClientError'
    this.code = code
  }
}

// 列表类响应统一包装为 { items: [...] }
interface ItemsResponse<T> {
  items: T[]
}

// 解析非 2xx 响应：优先取后端中文 message，回退到状态码；同时保留业务 code 供按码分支处理。
async function toError(resp: Response): Promise<ApiClientError> {
  let detail = `HTTP ${resp.status}`
  let code = ''
  try {
    const err = (await resp.json()) as ApiError
    if (err.message) detail = err.message
    if (err.code) code = err.code
  } catch {
    // 响应体非 JSON，保留状态码作为提示
  }
  return new ApiClientError(detail, code)
}

// 发起请求并解析 JSON；非 2xx 时抛出携带中文说明的错误。
// 单点注入登录令牌（Authorization: Bearer）；遇 401 清登录态并触发全局跳登录。
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = currentToken()
  const resp = await fetch(`${BASE}${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...init?.headers,
    },
  })
  if (resp.status === 401) {
    // 令牌缺失/失效/过期：清登录态并跳登录（登录接口本身凭据错也会 401，统一交由调用方提示）
    clearAuth()
    if (unauthorizedHandler) unauthorizedHandler()
    throw await toError(resp)
  }
  if (!resp.ok) throw await toError(resp)
  // 204 等空体场景返回 undefined（调用方按需忽略）
  if (resp.status === 204) return undefined as T
  return (await resp.json()) as T
}

// 把过滤参数对象拼成 query string，跳过空值（泛型避免要求入参带索引签名）
function qs<T extends object>(params: T): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    sp.set(k, String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

// ===== 登录 / 身份（FR-11 鉴权）=====

// 登录：凭据 → 令牌 + 操作者身份。本端点自身不需令牌（见 docs/API.md）。
export function login(username: string, password: string): Promise<LoginResult> {
  return request<LoginResult>('/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  })
}

// 登出：仅记一条审计（令牌无状态、服务端无会话可吊销），返回 204。
// 本端点需令牌（取认证身份入审计），故走带令牌的 request。
export function logout(): Promise<void> {
  return request<void>('/auth/logout', { method: 'POST' })
}

// ===== 环境（namespace）=====

export function listNamespaces(): Promise<NamespaceView[]> {
  return request<ItemsResponse<NamespaceView>>('/namespaces').then((r) => r.items)
}

export function createNamespace(code: string, name: string): Promise<NamespaceView> {
  return request<NamespaceView>('/namespaces', {
    method: 'POST',
    body: JSON.stringify({ code, name }),
  })
}

// 改环境显示名（code 不可变，仅改 name；FR-53）。
export function updateNamespace(code: string, name: string): Promise<NamespaceView> {
  return request<NamespaceView>(`/namespaces/${encodeURIComponent(code)}`, {
    method: 'PUT',
    body: JSON.stringify({ name }),
  })
}

// 删环境（FR-53）：后端带删除守卫，环境下有实例 / zone / 配置时返 409，错误中文 message 直接提示。
export function deleteNamespace(code: string): Promise<void> {
  return request<void>(`/namespaces/${encodeURIComponent(code)}`, { method: 'DELETE' })
}

// ===== 配置中心 =====

// 配置列表过滤条件
export interface ConfigFilter {
  namespace?: string
  group?: string
  dataId?: string
  scopeLevel?: string
}

export function listConfigs(filter: ConfigFilter): Promise<ConfigView[]> {
  return request<ItemsResponse<ConfigView>>(`/configs${qs(filter)}`).then((r) => r.items)
}

export function getConfig(id: number): Promise<ConfigView> {
  return request<ConfigView>(`/configs/${id}`)
}

// 新建配置参数（三元组 + 覆盖层 + 格式 + 内容 + 备注）
// 操作人由登录令牌身份决定（FR-11，后端取认证身份、忽略请求手填），故不在请求体送 operator。
export interface CreateConfigParams {
  namespace: string
  group: string
  dataId: string
  scopeLevel: string
  scopeTarget: string
  format: string
  content: string
  comment: string
}

export function createConfig(params: CreateConfigParams): Promise<ConfigView> {
  return request<ConfigView>('/configs', {
    method: 'POST',
    body: JSON.stringify(params),
  })
}

export function publishConfig(
  id: number,
  content: string,
  comment: string,
): Promise<PublishResult> {
  return request<PublishResult>(`/configs/${id}`, {
    method: 'PUT',
    body: JSON.stringify({ content, comment }),
  })
}

export function deleteConfig(id: number, comment: string): Promise<void> {
  return request<void>(`/configs/${id}${qs({ comment })}`, { method: 'DELETE' })
}

export function listRevisions(id: number): Promise<RevisionView[]> {
  return request<ItemsResponse<RevisionView>>(`/configs/${id}/revisions`).then((r) => r.items)
}

export function getRevision(id: number, version: number): Promise<RevisionView> {
  return request<RevisionView>(`/configs/${id}/revisions/${version}`)
}

export function rollbackConfig(
  id: number,
  toVersion: number,
  comment: string,
): Promise<PublishResult> {
  return request<PublishResult>(`/configs/${id}/rollback`, {
    method: 'POST',
    body: JSON.stringify({ toVersion, comment }),
  })
}

export function diffConfig(id: number, from: number, to: number): Promise<DiffView> {
  return request<DiffView>(`/configs/${id}/diff${qs({ from, to })}`)
}

// ===== 配置 / 文件批量操作（FR-74）=====

// 批量操作动作：delete（软删）/ disable（置 enabled=false）/ enable（置 enabled=true）。
export type BatchAction = 'delete' | 'disable' | 'enable'

// 批量端点返回体（与后端 {action, count} 对齐）
export interface BatchResult {
  action: BatchAction
  count: number
}

// 批量操作一组配置项（FR-74）：一事务原子完成；空 ids / 非法 action 后端 400；写操作需 full 角色。
export function batchConfigs(action: BatchAction, ids: number[]): Promise<BatchResult> {
  return request<BatchResult>('/configs/batch', {
    method: 'POST',
    body: JSON.stringify({ action, ids }),
  })
}

// ===== 配置有效预览（FR-22）=====

// 有效配置预览参数
export interface EffectiveConfigParams {
  namespace: string
  serverId?: string
  group?: string
  zone?: string
}

// 单条有效配置项（来源链路与被删除的键均按 dataId 维度，对齐后端 effectiveConfigItemView）
export interface EffectiveConfigItem {
  dataId: string
  format: string
  content: string
  md5: string
  sources: Array<{ path: string[]; scope: string }>
  deletions: Array<{ path: string[]; scope: string }>
}

// 有效配置预览返回体（deletions 在各 item 内，无顶层字段）
export interface EffectiveConfigView {
  namespace: string
  serverId?: string
  group?: string
  zone?: string
  md5: string
  items: EffectiveConfigItem[]
}

export function effectiveConfig(params: EffectiveConfigParams): Promise<EffectiveConfigView> {
  return request<EffectiveConfigView>(`/configs/effective${qs(params)}`)
}

// ===== 配置发布影响面预览（FR-79）=====

// 影响面预览参数（按某条 scope 算受影响的在线子服）
export interface ImpactParams {
  namespace: string
  scopeLevel: string
  group?: string
  scopeTarget?: string
}

export function impactPreview(params: ImpactParams): Promise<ImpactView> {
  return request<ImpactView>(`/configs/impact${qs(params)}`)
}

// ===== 文件树有效预览（FR-45）=====

// 有效文件树预览参数（与 configs/effective 同形参）
export interface EffectiveFileParams {
  namespace: string
  serverId?: string
  group?: string
  zone?: string
}

// 单个有效文件（结构化文件按键来源；整文件模式 wholeFile=true、sources 为单条空路径=winner 层）
// 对齐后端 internal/handler/file_handler.go effectiveFileView
export interface EffectiveFileItem {
  path: string
  md5: string
  content: string
  wholeFile: boolean
  sources: Array<{ path: string[]; scope: string }>
  deletions: Array<{ path: string[]; scope: string }>
}

// 有效文件树预览返回体（每文件含合并结果 + 逐键/整文件来源）
export interface EffectiveFileTreeView {
  namespace: string
  serverId?: string
  group?: string
  zone?: string | null
  fileTreeMd5: string
  files: EffectiveFileItem[]
}

export function effectiveFiles(params: EffectiveFileParams): Promise<EffectiveFileTreeView> {
  return request<EffectiveFileTreeView>(`/files/effective${qs(params)}`)
}

// ===== 实例与健康 =====

// 实例列表过滤条件
export interface InstanceFilter {
  namespace?: string
  group?: string
  zone?: string
  role?: string
  status?: string
}

export function listInstances(filter: InstanceFilter): Promise<InstanceView[]> {
  return request<ItemsResponse<InstanceView>>(`/instances${qs(filter)}`).then((r) => r.items)
}

// per-server 有效配置变更时间线（FR-80）：某子服覆盖链涉及 config 项的发布历史，按时间倒序
export interface ConfigTimelineParams {
  serverId: string
  namespace: string
  // 可选 groupHint：未指派时定位 group 层
  group?: string
}

export function serverConfigTimeline(params: ConfigTimelineParams): Promise<ConfigTimelineView> {
  const { serverId, ...query } = params
  return request<ConfigTimelineView>(
    `/instances/${encodeURIComponent(serverId)}/config-timeline${qs(query)}`,
  )
}

// 主动下线标记（FR-49）：已下线实例不在注册表列表出现，前端据此展示「已下线（可取消）」。
export interface OfflineMarker {
  namespace: string
  serverId: string
  reason: string
}

// 列出当前主动下线标记（FR-49）。namespace 可选过滤。
export function listOfflineInstances(namespace?: string): Promise<OfflineMarker[]> {
  return request<ItemsResponse<OfflineMarker>>(`/instances/offline${qs({ namespace })}`).then((r) => r.items)
}

// 主动下线某实例（FR-49）：落 DB 拒绝态 + 移出可用集。namespace 取自该行实例（不再强制先筛环境）。
export function offlineInstance(serverId: string, namespace: string, reason?: string): Promise<void> {
  return request<void>(`/instances/${encodeURIComponent(serverId)}/offline${qs({ namespace })}`, {
    method: 'POST',
    body: JSON.stringify({ reason: reason ?? '' }),
  })
}

// 取消主动下线某实例（FR-49）：清除 DB 拒绝态，使其可重新接入。
export function onlineInstance(serverId: string, namespace: string): Promise<void> {
  return request<void>(`/instances/${encodeURIComponent(serverId)}/offline${qs({ namespace })}`, {
    method: 'DELETE',
  })
}

// 流量调度·排空标记（FR-10，服务器页 drain/undrain 操作；对齐 internal/handler/scheduling_handler.go）。
// drain 仅是落位决策标记（候选排序时降权/剔除），不执行玩家连接（架构红线，见 ADR-0017）。

// 单条 drain 标记视图（对齐 handler.drainView）。
export interface DrainMarker {
  namespace: string
  serverId: string
  reason: string
}

// 列当前 drain 标记（GET /scheduling/drains?namespace=）：后端响应 { items: [] }，取 items。
export function listDrains(namespace?: string): Promise<DrainMarker[]> {
  return request<ItemsResponse<DrainMarker>>(`/scheduling/drains${qs({ namespace })}`).then((r) => r.items)
}

// 标记某实例排空（PUT /scheduling/drains）：namespace/serverId 走请求体（与后端 drainRequest 一致）。
// 写操作需 full 角色（readonly → 403）。
export function drainInstance(serverId: string, namespace: string, reason?: string): Promise<DrainMarker> {
  return request<DrainMarker>('/scheduling/drains', {
    method: 'PUT',
    body: JSON.stringify({ namespace, serverId, reason: reason ?? '' }),
  })
}

// 取消某实例排空（DELETE /scheduling/drains?namespace=&serverId=）：namespace/serverId 走查询参数。
export function undrainInstance(serverId: string, namespace: string): Promise<void> {
  return request<void>(`/scheduling/drains${qs({ namespace, serverId })}`, { method: 'DELETE' })
}

// ===== 集群拓扑（FR-37）=====
// 读控制面内存注册表快照：节点（在线实例）+ bc→bukkit 真实连线 + 大区/zone 分组。namespace 必填。
export function getTopology(namespace: string): Promise<TopologyView> {
  return request<TopologyView>(`/topology${qs({ namespace })}`)
}

// ===== 指标看板（FR-32，见 docs/API.md 指标看板小节）=====
// 只返回负载数字（健康事实），绝不含玩家名单 / 身份。

// 每服人数明细（仅计数，不含名单）。role 供前端按角色分组明细（FR-43）。
export interface ServerPlayers {
  serverId: string
  role: string
  playerCount: number
}

// bc（bungee 代理）维度聚合（与后端 bcSummaryView 对齐，FR-34）。仅负载数字，不含名单。
// avgBackendLatencyMs < 0（约定 -1）表示无可用后端延迟样本。
export interface BCSummary {
  proxyCount: number
  totalConnections: number
  avgThreadCount: number
  backendUp: number
  backendTotal: number
  avgBackendLatencyMs: number
}

// 当前快照聚合视图（与后端 summaryView 对齐）
// avgMemUsed / avgMemMax 单位为字节；avgCpuLoad < 0（约定 -1）表示无可用 CPU 样本。
export interface MetricsSummary {
  totalPlayers: number
  onlineServers: number
  servers: ServerPlayers[]
  avgTps: number
  avgMemUsed: number
  avgMemMax: number
  avgCpuLoad: number
  cpuSampleCount: number
  // bc 代理维度聚合（FR-34）；无 bc 实例时各计数为 0、平均延迟为 -1。
  bc: BCSummary
}

export function metricsSummary(namespace?: string): Promise<MetricsSummary> {
  return request<MetricsSummary>(`/metrics/summary${qs({ namespace })}`)
}

// 趋势时间窗（预设窗口）
export type TrendWindow = '1h' | '6h' | '24h'

// 趋势查询参数（namespace 可选；不传 serverId 返回 namespace 聚合趋势）
export interface TrendParams {
  namespace?: string
  serverId?: string
  window: TrendWindow
}

// 趋势时间序列点（与后端 trendPointView 对齐，avgMemUsed / avgMemMax 单位为字节）
export interface TrendPoint {
  sampledAt: string
  totalPlayers: number
  avgTps: number
  avgMemUsed: number
  avgMemMax: number
  avgCpuLoad: number
}

// 趋势返回体（仅 points，无玩家名单）
export interface MetricsTrend {
  points: TrendPoint[]
}

export function metricsTrend(params: TrendParams): Promise<MetricsTrend> {
  return request<MetricsTrend>(`/metrics/trend${qs(params)}`)
}

// ===== 控制面自身状态（FR-33，见 docs/API.md 控制面自身状态小节）=====
// 控制面进程本身的健康（版本 / 运行时长 / DB 连通 / 在线实例数 / 采样器状态 + Go 运行时资源），
// 区别于 FR-32 的 agent 网络聚合指标。

export function systemStatus(): Promise<SystemStatusView> {
  return request<SystemStatusView>('/system/status')
}

// ===== 控制面自观测（FR-82，见 docs/API.md 控制面自观测小节）=====
// 控制面进程内部运行态（DB 连接池 / 长轮询挂起 / 注册表规模 / 命令队列深度），
// 区别于 FR-33 页眉条与 FR-32 agent 网络负载，只读。

export function systemObservability(): Promise<ObservabilityView> {
  return request<ObservabilityView>('/system/observability')
}

// ===== zone 分配 =====

export function listAssignments(
  namespace?: string,
  group?: string,
  zone?: string,
): Promise<AssignmentView[]> {
  return request<ItemsResponse<AssignmentView>>(`/zones/assignments${qs({ namespace, group, zone })}`).then(
    (r) => r.items,
  )
}

// 新增/改派参数
// 操作人由登录令牌身份决定（FR-11，后端取认证身份、忽略请求手填），故不在请求体送 operator。
export interface AssignParams {
  namespace: string
  serverId: string
  group: string
  zone: string
  note: string
}

export function assignZone(params: AssignParams): Promise<AssignmentView> {
  return request<AssignmentView>('/zones/assignments', {
    method: 'PUT',
    body: JSON.stringify(params),
  })
}

export function unassignZone(namespace: string, serverId: string): Promise<void> {
  return request<void>(`/zones/assignments${qs({ namespace, serverId })}`, {
    method: 'DELETE',
  })
}

export function zoneSummary(namespace?: string, group?: string): Promise<ZoneStatView[]> {
  return request<ItemsResponse<ZoneStatView>>(`/zones${qs({ namespace, group })}`).then((r) => r.items)
}

// ===== 小区默认入口（FR-48）=====
// 只读列出某环境（可选某大区）各小区的默认入口 serverId；供代理服管理页按 BC 所属小区展示默认入口（FR-52）。
export function listDefaultEntries(namespace?: string, group?: string): Promise<DefaultEntryView[]> {
  return request<ItemsResponse<DefaultEntryView>>(`/zones/default-entry${qs({ namespace, group })}`).then(
    (r) => r.items,
  )
}

// ===== 审计 =====

// 审计查询过滤与分页条件
export interface AuditFilter {
  namespace?: string
  // 操作人过滤（后端 GET /admin/v1/audits 的 operator 参数，FR-30）
  operator?: string
  action?: string
  targetType?: string
  targetRef?: string
  // detail 列子串关键字检索（后端 GET /admin/v1/audits 的 detailKeyword 参数，FR-84）
  detailKeyword?: string
  from?: string
  to?: string
  page?: number
  size?: number
}

export function listAudits(filter: AuditFilter): Promise<AuditPage> {
  return request<AuditPage>(`/audits${qs(filter)}`)
}

// 审计导出格式（FR-84）
export type AuditExportFormat = 'csv' | 'json'

// 导出审计（FR-84）：复用 listAudits 同过滤（剔除分页），按 format 流式下载全量命中。
// 端点回非 JSON 的附件流，故不走 request（其按 JSON 解析）；用 fetch 带令牌取 Blob 后触发浏览器下载。
// 文件名优先取响应 Content-Disposition，回退本地生成；遇 401 与 request 同口径清登录态并跳登录。
export async function exportAudits(filter: AuditFilter, format: AuditExportFormat): Promise<void> {
  // 导出全量、不分页：剔除 page/size 再拼参数
  const { page: _page, size: _size, ...rest } = filter
  const token = currentToken()
  const resp = await fetch(`${BASE}/audits/export${qs({ ...rest, format })}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  if (resp.status === 401) {
    clearAuth()
    if (unauthorizedHandler) unauthorizedHandler()
    throw await toError(resp)
  }
  if (!resp.ok) throw await toError(resp)
  const blob = await resp.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filenameFromDisposition(resp.headers.get('Content-Disposition')) ?? `audit-export-${Date.now()}.${format}`
  a.click()
  // 延后释放更稳：避免极少数浏览器在 click 触发下载前就回收 url
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

// ===== 告警历史 / 事件信息流（FR-89，见 docs/API.md 告警事件小节）=====

// 告警事件查询过滤与分页条件（零值字段后端不过滤）
export interface AlertEventFilter {
  // 事件类型（health-transition 等）
  type?: string
  // 严重级别（info / warning / critical）
  level?: string
  namespace?: string
  // RFC3339 时间窗
  from?: string
  to?: string
  page?: number
  size?: number
}

export function listAlertEvents(filter: AlertEventFilter): Promise<AlertEventPage> {
  return request<AlertEventPage>(`/alert-events${qs(filter)}`)
}

// 从 Content-Disposition 头解析附件文件名（filename="..."）；缺失则返回 undefined。
function filenameFromDisposition(header: string | null): string | undefined {
  if (!header) return undefined
  const m = /filename="?([^"]+)"?/.exec(header)
  return m?.[1]
}

// ===== 服务分析 / 平台用量看板（FR-73）=====

// 服务分析聚合查询参数：namespace 可空（聚合全部环境）；from/to 为 RFC3339 时间窗。
export interface AuditAnalyticsParams {
  namespace?: string
  from?: string
  to?: string
}

// 取时间窗内审计活动聚合（总数 / 成功 / 失败 + 按动作分布 + 每日趋势）。
// 窗口上限 92 天（超出后端 400）；与 FR-32 负载看板数据源 / 刷新节奏独立。
export function getAuditAnalytics(params: AuditAnalyticsParams): Promise<AuditAnalytics> {
  return request<AuditAnalytics>(`/audits/analytics${qs(params)}`)
}

// ===== 管理面 API 密钥（FR-42，见 docs/API.md 管理面 API 密钥小节）=====
// 列表 / 元数据绝不含明文；明文仅创建 / 重置时一次性返回。operator 由认证态派生。

export function listApiKeys(): Promise<ApiKeyView[]> {
  return request<ItemsResponse<ApiKeyView>>('/api-keys').then((r) => r.items)
}

// 创建密钥参数：名称 + 角色 + 可选过期时刻（RFC3339；为空表示永不过期）
export interface CreateApiKeyParams {
  name: string
  role: string
  expiresAt?: string
}

export function createApiKey(params: CreateApiKeyParams): Promise<ApiKeyCreated> {
  return request<ApiKeyCreated>('/api-keys', {
    method: 'POST',
    body: JSON.stringify(params),
  })
}

// 吊销密钥（软删，不可逆；旧明文立即失效）
export function revokeApiKey(id: number): Promise<void> {
  return request<void>(`/api-keys/${id}`, { method: 'DELETE' })
}

// 重置密钥：轮换明文，旧明文立即失效，返回的新明文仅此一次可见
export function resetApiKey(id: number): Promise<ApiKeyCreated> {
  return request<ApiKeyCreated>(`/api-keys/${id}/reset`, { method: 'POST' })
}

// ===== 文件树托管（通道B，FR-14）=====

// 文件列表过滤条件
export interface FileFilter {
  namespace?: string
  group?: string
  path?: string
  scopeLevel?: string
}

export function listFiles(filter: FileFilter): Promise<FileView[]> {
  return request<ItemsResponse<FileView>>(`/files${qs(filter)}`).then((r) => r.items)
}

export function getFile(id: number): Promise<FileView> {
  return request<FileView>(`/files/${id}`)
}

// 新建文件对象参数（首次发布）
// 操作人由登录令牌身份决定（FR-11，后端取认证身份、忽略请求手填），故不在请求体送 operator。
export interface CreateFileParams {
  namespace: string
  group: string
  path: string
  scopeLevel: string
  scopeTarget: string
  content: string
  comment: string
}

export function createFile(params: CreateFileParams): Promise<FileView> {
  return request<FileView>('/files', {
    method: 'POST',
    body: JSON.stringify(params),
  })
}

export function publishFile(
  id: number,
  content: string,
  comment: string,
): Promise<PublishResult> {
  return request<PublishResult>(`/files/${id}`, {
    method: 'PUT',
    body: JSON.stringify({ content, comment }),
  })
}

export function deleteFile(id: number, comment: string): Promise<void> {
  return request<void>(`/files/${id}${qs({ comment })}`, { method: 'DELETE' })
}

export function listFileRevisions(id: number): Promise<FileRevisionView[]> {
  return request<ItemsResponse<FileRevisionView>>(`/files/${id}/revisions`).then((r) => r.items)
}

export function getFileRevision(id: number, version: number): Promise<FileRevisionView> {
  return request<FileRevisionView>(`/files/${id}/revisions/${version}`)
}

export function rollbackFile(
  id: number,
  toVersion: number,
  comment: string,
): Promise<PublishResult> {
  return request<PublishResult>(`/files/${id}/rollback`, {
    method: 'POST',
    body: JSON.stringify({ toVersion, comment }),
  })
}

// 批量操作一组文件对象（FR-74）：一事务原子完成；空 ids / 非法 action 后端 400；写操作需 full 角色。
export function batchFiles(action: BatchAction, ids: number[]): Promise<BatchResult> {
  return request<BatchResult>('/files/batch', {
    method: 'POST',
    body: JSON.stringify({ action, ids }),
  })
}

// ===== 配置导入（上传通道，FR-38）=====

// 单个待导入文件（相对 path + 浏览器 File 对象）
export interface ImportFileEntry {
  path: string
  file: File
}

// 导入结果（与后端 import 端点响应对齐）
export interface ImportResult {
  files: number
  created: number
  updated: number
}

// 把一份目录批量上传到某组（scope=group，整文件覆盖语义）。
// 走 multipart/form-data：不手动设 Content-Type（由浏览器带 boundary）；files 与 paths 按顺序一一对应。
// 单独实现而非复用 request()，因后者会对有 body 的请求强加 application/json。
export async function importFiles(
  namespace: string,
  group: string,
  entries: ImportFileEntry[],
  comment = '',
): Promise<ImportResult> {
  const form = new FormData()
  form.append('namespace', namespace)
  form.append('group', group)
  if (comment) form.append('comment', comment)
  for (const e of entries) {
    form.append('paths', e.path)
    form.append('files', e.file, e.path)
  }
  const token = currentToken()
  const resp = await fetch(`${BASE}/files/import`, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: form,
  })
  if (resp.status === 401) {
    clearAuth()
    if (unauthorizedHandler) unauthorizedHandler()
    throw await toError(resp)
  }
  if (!resp.ok) throw await toError(resp)
  return (await resp.json()) as ImportResult
}

// ===== 配置导入·在线实例反向抓取（FR-39）=====

// 反向抓取触发参数：目标层 + 目标组；server 层另需目标 serverId。
// operator 由登录令牌身份决定（后端取认证身份），故不在请求体送 operator。
export interface ReverseFetchParams {
  // 目标层：group（落组级覆盖）/ server（落实例级覆盖）
  scope: ReverseFetchScope
  // 入库到哪个组的文件树
  group: string
  // 仅 server 层需要：覆盖落到哪个 serverId（组层留空）
  target?: string
}

// 触发对某在线实例的反向抓取：命令该 agent 读其真实 plugins/ 文本配置回传并 ingest。
// namespace 走查询参数（与 offlineInstance 等实例端点一致，实例列表跨 namespace、须随源实例带上）。
// 写操作需 full 角色（readonly → 403）；返回创建的 pending 命令，结果经命令状态 / 审计 / 文件树体现。
export function triggerReverseFetch(
  serverId: string,
  namespace: string,
  params: ReverseFetchParams,
): Promise<AgentCommandView> {
  return request<AgentCommandView>(
    `/instances/${encodeURIComponent(serverId)}/reverse-fetch${qs({ namespace })}`,
    {
      method: 'POST',
      body: JSON.stringify(params),
    },
  )
}

// ===== 按需拓印回写 + 审核台（FR-46）=====

// 触发对某在线实例某文件的按需拓印：命令该 agent 读其真实 plugins/ 树回传，
// 控制面取该 path 的磁盘当前内容转存待审（不落库），返回创建的 pending 命令。
// namespace 走查询参数（同 triggerReverseFetch）；写操作需 full 角色（readonly → 403）。
export function triggerImprint(
  serverId: string,
  namespace: string,
  params: { path: string },
): Promise<AgentCommandView> {
  return request<AgentCommandView>(
    `/instances/${encodeURIComponent(serverId)}/imprint${qs({ namespace })}`,
    {
      method: 'POST',
      body: JSON.stringify(params),
    },
  )
}

// 拉拓印命令状态（供前端轮询至 ready）：仅命令状态，不含瞬态磁盘内容。
export function imprintStatus(commandId: number): Promise<AgentCommandView> {
  return request<AgentCommandView>(`/imprints/${commandId}`)
}

// 拓印 diff 查询参数：并入层选择（解期望合并值的视角）。
export interface ImprintDiffParams {
  scope: ImprintScope
  group?: string
  zone?: string
  target?: string
}

// 拉拓印 diff：命令须 ready；返回本地实际值 ⟷ 按并入层视角解出的期望合并值（含逐键来源）。
export function imprintDiff(commandId: number, params: ImprintDiffParams): Promise<ImprintDiffView> {
  return request<ImprintDiffView>(`/imprints/${commandId}/diff${qs(params)}`)
}

// 拓印确认落库参数：并入层 + 目标键 + 自审 md5（须等于 diff 返回的 actualMd5）。
export interface ConfirmImprintParams {
  scope: ImprintScope
  group?: string
  zone?: string
  target?: string
  reviewedMd5: string
}

// 确认拓印落库：单人自审门（reviewedMd5 不匹配 → 412）；通过后落该层文件覆盖、命令转 done。
// 写操作需 full 角色（readonly → 403）。
export function confirmImprint(
  commandId: number,
  params: ConfirmImprintParams,
): Promise<ImprintConfirmView> {
  return request<ImprintConfirmView>(`/imprints/${commandId}/confirm`, {
    method: 'POST',
    body: JSON.stringify(params),
  })
}

// ===== 在线日志/诊断查看器（FR-88，见 ADR-0040）=====

// 触发取某在线实例的自身脱敏日志：建 pending tail-logs 命令并唤醒 agent，返回命令视图（202）。
// namespace 走查询参数；写操作需 full 角色（readonly → 403）；该实例已有进行中取日志命令 → 409 AGENT_LOG_ACTIVE。
export function requestAgentLogs(serverId: string, namespace: string): Promise<AgentLogView> {
  return request<AgentLogView>(`/instances/${encodeURIComponent(serverId)}/logs${qs({ namespace })}`, {
    method: 'POST',
  })
}

// 查询某实例最近一次取日志结果：done 则附脱敏日志行；从无取日志命令 → 204（返回 undefined，调用方按需处理）。
export function getAgentLogs(
  serverId: string,
  namespace: string,
): Promise<AgentLogView | undefined> {
  return request<AgentLogView | undefined>(
    `/instances/${encodeURIComponent(serverId)}/logs${qs({ namespace })}`,
  )
}

// ===== 强制重同步（FR-91）=====

// 触发某在线实例强制重同步：建 pending resync-config 命令并唤醒 agent，令其重拉有效配置/文件树/覆盖集并 apply。
// namespace 走查询参数；写操作需 full 角色（readonly → 403）；返回命令 id（端点返回命令视图，取其 id）。
export function triggerResync(serverId: string, namespace: string): Promise<{ commandId: number }> {
  return request<AgentCommandView>(
    `/instances/${encodeURIComponent(serverId)}/resync${qs({ namespace })}`,
    { method: 'POST' },
  ).then((cmd) => ({ commandId: cmd.id }))
}

// ===== 三方文件覆盖兼容（override-set，FR-15）=====
// 写操作 operator 以登录令牌身份为准（后端忽略请求手填），故不在请求体送 operator。

// 覆盖集列表过滤条件
export interface OverrideSetFilter {
  namespace?: string
  group?: string
  scopeLevel?: string
}

export function listOverrideSets(filter: OverrideSetFilter): Promise<OverrideSetView[]> {
  return request<ItemsResponse<OverrideSetView>>(`/override-sets${qs(filter)}`).then((r) => r.items)
}

export function getOverrideSet(id: number): Promise<OverrideSetView> {
  return request<OverrideSetView>(`/override-sets/${id}`)
}

export function publishOverrideSet(
  id: number,
  targetRoot: string,
  reloadCommand: string,
  comment: string,
): Promise<{ version: number; targetRoot: string }> {
  return request<{ version: number; targetRoot: string }>(`/override-sets/${id}`, {
    method: 'PUT',
    body: JSON.stringify({ targetRoot, reloadCommand, comment }),
  })
}

export function deleteOverrideSet(id: number, comment: string): Promise<void> {
  return request<void>(`/override-sets/${id}${qs({ comment })}`, { method: 'DELETE' })
}

export function listOverrideSetRevisions(id: number): Promise<OverrideSetRevisionView[]> {
  return request<ItemsResponse<OverrideSetRevisionView>>(`/override-sets/${id}/revisions`).then(
    (r) => r.items,
  )
}

export function rollbackOverrideSet(
  id: number,
  toVersion: number,
  comment: string,
): Promise<{ version: number; targetRoot: string }> {
  return request<{ version: number; targetRoot: string }>(`/override-sets/${id}/rollback`, {
    method: 'POST',
    body: JSON.stringify({ toVersion, comment }),
  })
}

export function dryRunOverrideSet(id: number): Promise<OverrideSetDryRunView> {
  return request<OverrideSetDryRunView>(`/override-sets/${id}/dry-run`)
}

// ===== 反向抓取受管任务 + 审核台 + 冲突 diff（FR-58/59/60）=====

// 建扫描任务参数：目标层 + 目标组；server 层另需目标 serverId。
// operator 由登录令牌身份决定（后端取认证身份），故不在请求体送 operator。
export interface CreateScanTaskParams {
  // 目标层：group（落组级覆盖）/ server（落实例级覆盖）
  scope: ReverseFetchScope
  // 入库到哪个组的文件树
  group: string
  // 仅 server 层需要：覆盖落到哪个 serverId（组层留空）
  target?: string
}

// 建扫描任务（FR-58）：命令在线源 agent 扫描其 plugins/ 树回传清单（两段式第一段）。
// namespace 走查询参数（实例列表跨 namespace、须随源实例带上）；写操作需 full 角色（readonly → 403）。
export function createScanTask(
  serverId: string,
  namespace: string,
  params: CreateScanTaskParams,
): Promise<ReverseFetchTaskView> {
  return request<ReverseFetchTaskView>(
    `/instances/${encodeURIComponent(serverId)}/reverse-fetch${qs({ namespace })}`,
    {
      method: 'POST',
      body: JSON.stringify(params),
    },
  )
}

// 任务历史列表过滤条件（任务台筛选）
export interface ReverseFetchTaskFilter {
  namespace?: string
  serverId?: string
  status?: string
}

// 列任务历史（FR-58）：后端响应 { items: [] }，取 items。
export function listReverseFetchTasks(
  filter: ReverseFetchTaskFilter,
): Promise<ReverseFetchTaskView[]> {
  return request<ItemsResponse<ReverseFetchTaskView>>(`/reverse-fetch/tasks${qs(filter)}`).then(
    (r) => r.items,
  )
}

// 取单任务详情（含清单 / 计数 / ignoredByRule 标记；供轮询与审核台）。
export function getReverseFetchTask(id: number): Promise<ReverseFetchTaskView> {
  return request<ReverseFetchTaskView>(`/reverse-fetch/tasks/${id}`)
}

// 提交选定集（FR-58）：任务须 pending-review；选定 path 数组 + 是否确认纳入超阈值文件。
// 选定集含超阈值文件时须 confirmOverThreshold=true，否则后端 400。写操作需 full 角色。
export function submitReverseFetchTask(
  id: number,
  body: { selectedPaths: string[]; confirmOverThreshold: boolean },
): Promise<ReverseFetchTaskView> {
  return request<ReverseFetchTaskView>(`/reverse-fetch/tasks/${id}/submit`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

// 取消任务（FR-58）：非终态 → cancelled。
export function cancelReverseFetchTask(id: number): Promise<ReverseFetchTaskView> {
  return request<ReverseFetchTaskView>(`/reverse-fetch/tasks/${id}/cancel`, { method: 'POST' })
}

// 列冲突 path（FR-59）：任务须 conflict-review。
export function listConflicts(id: number): Promise<string[]> {
  return request<{ conflicts: string[] }>(`/reverse-fetch/tasks/${id}/conflicts`).then(
    (r) => r.conflicts,
  )
}

// 取单冲突文件 diff（FR-59）：抓取值 ⟷ 目标已有版本（供前端逐文件审）。
export function conflictDiff(id: number, path: string): Promise<ConflictDiffView> {
  return request<ConflictDiffView>(`/reverse-fetch/tasks/${id}/conflicts/diff${qs({ path })}`)
}

// 冲突审核落库（FR-59）：逐冲突文件 overwrite（须自审 reviewedMd5）/ keep；写操作需 full 角色。
export function resolveConflicts(
  id: number,
  decisions: ResolveDecision[],
): Promise<ResolveResult> {
  return request<ResolveResult>(`/reverse-fetch/tasks/${id}/resolve`, {
    method: 'POST',
    body: JSON.stringify({ decisions }),
  })
}

// 持久忽略规则列表过滤条件
export interface IgnoreRuleFilter {
  namespace: string
  scope?: string
  group?: string
  target?: string
}

// 列活跃忽略规则（FR-59）。
export function listIgnoreRules(filter: IgnoreRuleFilter): Promise<IgnoreRuleView[]> {
  return request<ItemsResponse<IgnoreRuleView>>(
    `/reverse-fetch/ignore-rules${qs(filter)}`,
  ).then((r) => r.items)
}

// 建忽略规则参数（FR-59）：ruleType 取值 exact（单文件）/ prefix（目录前缀）。
export interface CreateIgnoreRuleParams {
  namespace: string
  scope: ReverseFetchScope
  group: string
  target?: string
  ruleType: IgnoreRuleType
  pattern: string
  comment?: string
}

// 建一条持久忽略规则（FR-59）；写操作需 full 角色。
export function createIgnoreRule(params: CreateIgnoreRuleParams): Promise<IgnoreRuleView> {
  return request<IgnoreRuleView>('/reverse-fetch/ignore-rules', {
    method: 'POST',
    body: JSON.stringify(params),
  })
}

// 删一条持久忽略规则（FR-59，软删）；写操作需 full 角色。
export function deleteIgnoreRule(id: number): Promise<void> {
  return request<void>(`/reverse-fetch/ignore-rules/${id}`, { method: 'DELETE' })
}

// ===== 运维设置（FR-62，消费 FR-61 设置端点）=====

// 列热改设置项（FR-61）：后端响应 { items: [] }，取 items。白名单皆热改项，启动 / 安全项不在其中。
export function listSettings(): Promise<SettingView[]> {
  return request<ItemsResponse<SettingView>>('/settings').then((r) => r.items)
}

// 改单个设置项（FR-61）：值统一以字符串送（后端按白名单 valueType 解析校验）。
// 非法值 / 白名单外 key → 400（标准错误体），由调用方按中文 message 提示；写操作需 full 角色。
export function updateSetting(key: string, value: string): Promise<void> {
  return request<void>(`/settings/${encodeURIComponent(key)}`, {
    method: 'PUT',
    body: JSON.stringify({ value }),
  })
}
