// 管理台 API fetch 封装。
// base 固定为 /admin/v1；开发期由 vite proxy 转发到本地控制面，生产期同源同端口。
// 所有端点严格对齐 docs/API.md 与 internal/handler/*.go，非 2xx 时解析统一错误体并抛出。

import type {
  AssignmentView,
  AuditPage,
  ConfigView,
  DiffView,
  InstanceView,
  NamespaceView,
  PublishResult,
  RevisionView,
  ZoneStatView,
} from './types'

// API 基址：所有管理台接口的公共前缀
const BASE = '/admin/v1'

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
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(`${BASE}${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
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

// 新建配置参数（三元组 + 覆盖层 + 格式 + 内容 + 操作人 + 备注）
export interface CreateConfigParams {
  namespace: string
  group: string
  dataId: string
  scopeLevel: string
  scopeTarget: string
  format: string
  content: string
  operator: string
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
  operator: string,
  comment: string,
): Promise<PublishResult> {
  return request<PublishResult>(`/configs/${id}`, {
    method: 'PUT',
    body: JSON.stringify({ content, operator, comment }),
  })
}

export function deleteConfig(id: number, operator: string, comment: string): Promise<void> {
  return request<void>(`/configs/${id}${qs({ operator, comment })}`, { method: 'DELETE' })
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
  operator: string,
  comment: string,
): Promise<PublishResult> {
  return request<PublishResult>(`/configs/${id}/rollback`, {
    method: 'POST',
    body: JSON.stringify({ toVersion, operator, comment }),
  })
}

export function diffConfig(id: number, from: number, to: number): Promise<DiffView> {
  return request<DiffView>(`/configs/${id}/diff${qs({ from, to })}`)
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

export function offlineInstance(
  serverId: string,
  namespace: string,
  operator: string,
): Promise<void> {
  return request<void>(`/instances/${encodeURIComponent(serverId)}/offline${qs({ namespace, operator })}`, {
    method: 'POST',
  })
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
export interface AssignParams {
  namespace: string
  serverId: string
  group: string
  zone: string
  operator: string
  note: string
}

export function assignZone(params: AssignParams): Promise<AssignmentView> {
  return request<AssignmentView>('/zones/assignments', {
    method: 'PUT',
    body: JSON.stringify(params),
  })
}

export function unassignZone(
  namespace: string,
  serverId: string,
  operator: string,
): Promise<void> {
  return request<void>(`/zones/assignments${qs({ namespace, serverId, operator })}`, {
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
