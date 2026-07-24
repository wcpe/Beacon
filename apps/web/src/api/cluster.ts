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
} from '@beacon/contracts'

import { clearAuth, currentToken, notifyUnauthorized } from '../state/auth'

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

/**
 * 全站统一请求封装（FR-179）：注入登录令牌 Authorization + 处理 401 + 非 2xx 抛脱敏 message。
 *
 * 交付 / 可观测 / 系统等域的请求封装均收敛委托到此实现（见 http.ts / request.ts / system.ts），
 * 保证鉴权注入与 401 行为四处一致。有令牌则带 `Authorization: Bearer <令牌>`；
 * 遇 401（令牌缺失 / 失效 / 过期，或登录端点凭据错）先清登录态并触发全局跳登录回调，再抛错供调用方提示。
 */
export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }
  const token = currentToken()
  if (token !== '') {
    headers.Authorization = `Bearer ${token}`
  }
  const response = await fetch(path, {
    method,
    headers: Object.keys(headers).length === 0 ? undefined : headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (response.status === 401) {
    // 令牌失效：清登录态并触发全局跳登录（登录端点凭据错也 401，跳登录为幂等，错误仍抛给调用方提示）。
    clearAuth()
    notifyUnauthorized()
  }
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

/**
 * 冷查询归一列表结果（FR-152）：把「热查询 page/pageSize（有 total、无游标）」与
 * 「冷查询 includeArchived keyset（有 nextCursor、无 total）」两种后端分页形状收敛为统一 shape，
 * 使消费页面按 total===null 判定冷模式而无需区分两种响应类型。
 */
export interface ColdListResult<T> {
  items: T[]
  // 热查询总数；冷查询归并去重后不回总数，为 null。
  total: number | null
  // 冷查询下一页 keyset 游标（null 表示无下一页）；热查询恒 null。
  nextCursor: string | null
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

// 并发身份冲突处置（FR-177）：保留指定实例恢复 active，落败方后续持续 409。
export function resolveConflictIdentity(
  identityId: string,
  keepBootId: string,
  reason: string,
): Promise<AgentIdentityDetail> {
  return request('POST', `/admin/v2/agent-identities/${identityId}/resolve-conflict`, { keepBootId, reason })
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

/** 删除空的 BC 集群（无大区、无已分配代理）。 */
export function deleteBcCluster(id: number): Promise<void> {
  return request('DELETE', `/admin/v2/bc-clusters/${String(id)}`)
}

/** 删除空的大区（无小区）。 */
export function deleteRegion(id: number): Promise<void> {
  return request('DELETE', `/admin/v2/regions/${String(id)}`)
}

/** 删除空的小区（无已分配子服）。 */
export function deleteZone(id: number): Promise<void> {
  return request('DELETE', `/admin/v2/zones/${String(id)}`)
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

// ---- 连接消息域（拓扑异常链路 / 类型汇总）----

export interface MessageEdgeResponse {
  edges: MessageEdgeStat[]
}

/** 按类型聚合（groupBy=type）；广播等不进 edge 时用于数据剖析空态说明 */
export interface MessageTypeStat {
  msgType: string
  total: number
  failed: number
}

export interface MessageTypeStatsResponse {
  types: MessageTypeStat[]
}

export function fetchMessageEdges(): Promise<MessageEdgeResponse> {
  return request('GET', '/admin/v2/messages/stats?groupBy=edge')
}

export function fetchMessageTypeStats(): Promise<MessageTypeStatsResponse> {
  return request('GET', '/admin/v2/messages/stats?groupBy=type')
}
