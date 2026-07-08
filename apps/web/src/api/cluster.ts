// 集群域数据获取（/servers /zones /topology 共用）：统一走 mock handlers 的 /admin/v2 端点。
// 读取端点用于 useQuery，写端点用于 useMutation；错误按脱敏后的 message 抛出（ADR-0057）。

import type {
  AgentIdentityDetail,
  AgentIdentityListResponse,
  AssignmentResponse,
  HealthDetail,
  MessageEdgeStat,
  NamespaceListResponse,
  ServerItem,
  ServerListResponse,
  ZoneTreeResponse,
} from '@beacon/devmock'

/** 携带后端错误码的 API 错误（message 已脱敏，可直接展示） */
export class ApiClientError extends Error {
  readonly code: string
  readonly status: number

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiClientError'
    this.status = status
    this.code = code
  }
}

interface ErrorBodyShape {
  code?: unknown
  message?: unknown
}

/**
 * 安全解析响应体为 JSON：空体返回 null；若拿到的是 HTML（演示模式下 mock 尚未接管
 * 或端点未实现时请求会 bypass 到 SPA 返回 index.html），抛出清晰的 ApiClientError，
 * 而非让 JSON.parse 抛出难懂的「Unexpected token '<'」。各请求封装统一调用。
 */
export function parseApiJson(text: string, status: number): unknown {
  if (text === '') {
    return null
  }
  const head = text.trimStart().slice(0, 1)
  if (head === '<') {
    throw new ApiClientError(status, 'mock_not_ready', '演示数据未就绪（请刷新页面重试）')
  }
  return JSON.parse(text)
}

/** 统一请求封装：非 2xx 抛 ApiClientError（取脱敏 message），204 返回 undefined。 */
async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await response.text()
  const parsed: unknown = parseApiJson(text, response.status)
  if (!response.ok) {
    const shape = (parsed ?? {}) as ErrorBodyShape
    const code = typeof shape.code === 'string' ? shape.code : 'unknown'
    const message = typeof shape.message === 'string' ? shape.message : `请求失败（HTTP ${String(response.status)}）`
    throw new ApiClientError(response.status, code, message)
  }
  return parsed as T
}

/** 拼装 query 串：跳过 null / undefined / 空串。 */
export function buildQuery(params: Record<string, string | number | boolean | null | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === null || value === undefined || value === '') {
      continue
    }
    search.set(key, String(value))
  }
  const query = search.toString()
  return query === '' ? '' : `?${query}`
}

// ---- namespace（页面顶部作用域选择）----

export function fetchNamespaces(): Promise<NamespaceListResponse> {
  return request('GET', '/admin/v2/namespaces?pageSize=100')
}

// ---- 身份域（注册待确认 / 绑定状态机）----

export interface IdentityQuery {
  status?: string
  namespaceId?: number
  keyword?: string
  page?: number
  pageSize?: number
}

export function fetchIdentities(query: IdentityQuery): Promise<AgentIdentityListResponse> {
  return request('GET', `/admin/v2/agent-identities${buildQuery({ ...query })}`)
}

export function fetchIdentityDetail(identityId: string): Promise<AgentIdentityDetail> {
  return request('GET', `/admin/v2/agent-identities/${identityId}`)
}

export interface ApproveBody {
  forceUnbindOccupier?: boolean
  target?: { kind: 'zone' | 'bc_cluster'; id: number } | null
}

export function approveIdentity(identityId: string, body: ApproveBody): Promise<AgentIdentityDetail> {
  return request('POST', `/admin/v2/agent-identities/${identityId}/approve`, body)
}

export function rejectIdentity(identityId: string, reason: string): Promise<AgentIdentityDetail> {
  return request('POST', `/admin/v2/agent-identities/${identityId}/reject`, { reason })
}

export function disableIdentity(identityId: string, reason: string): Promise<AgentIdentityDetail> {
  return request('POST', `/admin/v2/agent-identities/${identityId}/disable`, { reason })
}

export function enableIdentity(identityId: string): Promise<AgentIdentityDetail> {
  return request('POST', `/admin/v2/agent-identities/${identityId}/enable`)
}

export function unbindIdentity(identityId: string, reason: string): Promise<AgentIdentityDetail> {
  return request('POST', `/admin/v2/agent-identities/${identityId}/unbind`, { reason })
}

// ---- 区服权威域（结构树 / server 资产 / 分配 / 换区）----

export interface ServerQuery {
  namespaceId?: number
  kind?: string
  assigned?: boolean
  zoneId?: number
  bcClusterId?: number
  keyword?: string
  page?: number
  pageSize?: number
}

export function fetchServers(query: ServerQuery): Promise<ServerListResponse> {
  return request('GET', `/admin/v2/servers${buildQuery({ ...query })}`)
}

export function fetchZoneTree(namespaceId: number): Promise<ZoneTreeResponse> {
  return request('GET', `/admin/v2/zone-tree${buildQuery({ namespaceId })}`)
}

export interface CreateClusterBody {
  namespaceId: number
  name: string
  description?: string
}

export function createBcCluster(body: CreateClusterBody): Promise<unknown> {
  return request('POST', '/admin/v2/bc-clusters', body)
}

export interface CreateRegionBody {
  bcClusterId: number
  name: string
  description?: string
}

export function createRegion(body: CreateRegionBody): Promise<unknown> {
  return request('POST', '/admin/v2/regions', body)
}

export interface CreateZoneBody {
  regionId: number
  name: string
  description?: string
}

export function createZone(body: CreateZoneBody): Promise<unknown> {
  return request('POST', '/admin/v2/zones', body)
}

export interface AssignmentBody {
  serverIds: number[]
  target: { kind: 'zone' | 'bc_cluster'; id: number } | null
  isDefaultEntry?: boolean
  reason?: string
}

export function assignServers(body: AssignmentBody): Promise<AssignmentResponse> {
  return request('POST', '/admin/v2/server-assignments', body)
}

export interface RezoneBody {
  serverIds: number[]
  target: { kind: 'zone' | 'bc_cluster'; id: number }
  reason: string
}

export function rezoneServers(body: RezoneBody): Promise<AssignmentResponse> {
  return request('POST', '/admin/v2/server-rezones', body)
}

export function setDefaultEntry(serverRowId: number, value: boolean): Promise<ServerItem> {
  return request('PUT', `/admin/v2/servers/${String(serverRowId)}/default-entry`, { value })
}

export function setDraining(serverId: string, draining: boolean, reason: string): Promise<ServerItem> {
  return request('PUT', `/admin/v2/servers/${serverId}/draining`, { draining, reason })
}

// ---- 指标健康域（健康详情）----

export function fetchHealthDetail(serverId: string): Promise<HealthDetail> {
  return request('GET', `/admin/v2/health/${serverId}`)
}

// ---- 连接消息域（拓扑异常链路）----

export interface MessageEdgeResponse {
  edges: MessageEdgeStat[]
}

export function fetchMessageEdges(): Promise<MessageEdgeResponse> {
  return request('GET', '/admin/v2/messages/stats?groupBy=edge')
}
