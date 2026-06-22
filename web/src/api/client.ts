// 管理台 API fetch 封装。
// base 固定为 /admin/v1；开发期由 vite proxy 转发到本地控制面，生产期同源同端口。
// 所有端点严格对齐 docs/API.md 与 internal/handler/*.go，非 2xx 时解析统一错误体并抛出。

import type {
  AgentCommandView,
  ApiKeyCreated,
  ApiKeyView,
  AssignmentView,
  AuditPage,
  ConfigView,
  DiffView,
  FileRevisionView,
  FileView,
  ImprintConfirmView,
  ImprintDiffView,
  ImprintScope,
  InstanceView,
  LoginResult,
  NamespaceView,
  OverrideSetDryRunView,
  OverrideSetRevisionView,
  OverrideSetView,
  PublishResult,
  ReverseFetchScope,
  RevisionView,
  SystemStatusView,
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

// 列表类响应统一包装为 { items: [...] }
interface ItemsResponse<T> {
  items: T[]
}

// 解析非 2xx 响应：优先取后端中文 message，回退到状态码
async function toError(resp: Response): Promise<Error> {
  let detail = `HTTP ${resp.status}`
  try {
    const err = (await resp.json()) as ApiError
    if (err.message) detail = err.message
  } catch {
    // 响应体非 JSON，保留状态码作为提示
  }
  return new Error(detail)
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

// ===== 审计 =====

// 审计查询过滤与分页条件
export interface AuditFilter {
  namespace?: string
  // 操作人过滤（后端 GET /admin/v1/audits 的 operator 参数，FR-30）
  operator?: string
  action?: string
  targetType?: string
  targetRef?: string
  from?: string
  to?: string
  page?: number
  size?: number
}

export function listAudits(filter: AuditFilter): Promise<AuditPage> {
  return request<AuditPage>(`/audits${qs(filter)}`)
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
