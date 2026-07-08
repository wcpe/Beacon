// 系统大域数据获取（/settings /system /system/version /api-keys /namespaces）：
// 统一走 mock handlers 端点，读端点用于 useQuery、写端点用于 useMutation；
// 错误按脱敏后的 message 抛出（ADR-0057）。复用集群域已导出的 ApiClientError，避免重复定义。

import type {
  ApiKeyItem,
  ArchiveJobDetail,
  ArchiveJobListResponse,
  ArchiveOverview,
  HealthWeightsConfig,
  HealthWeightsResponse,
  NamespaceCreated,
  NamespaceListResponse,
  NamespaceTrustItem,
  NamespaceTrustListResponse,
  SettingItem,
  SystemObservability,
  SystemStatus,
  UpdateCheck,
  UpdateProgress,
} from '@beacon/devmock'

import { ApiClientError, parseApiJson } from './cluster'

export { ApiClientError }

interface ErrorBodyShape {
  code?: unknown
  message?: unknown
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
    const message =
      typeof shape.message === 'string' ? shape.message : `请求失败（HTTP ${String(response.status)}）`
    throw new ApiClientError(response.status, code, message)
  }
  return parsed as T
}

/** 拼装 query 串：跳过 null / undefined / 空串。 */
function buildQuery(params: Record<string, string | number | boolean | null | undefined>): string {
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

// ---- /system 控制面健康（Legacy 形状）----

export function fetchSystemStatus(): Promise<SystemStatus> {
  return request('GET', '/admin/v1/system/status')
}

export function fetchSystemObservability(): Promise<SystemObservability> {
  return request('GET', '/admin/v1/system/observability')
}

// ---- /system/version 版本与更新（Legacy 形状）----

export function fetchUpdateCheck(): Promise<UpdateCheck> {
  return request('GET', '/admin/v1/system/update-check')
}

export function fetchUpdateProgress(): Promise<UpdateProgress> {
  return request('GET', '/admin/v1/system/update')
}

export function triggerUpdate(): Promise<{ accepted: boolean }> {
  return request('POST', '/admin/v1/system/update')
}

export function cancelUpdate(): Promise<{ cancelled: boolean }> {
  return request('POST', '/admin/v1/system/update/cancel')
}

export function rollbackUpdate(): Promise<{ accepted: boolean }> {
  return request('POST', '/admin/v1/system/rollback')
}

export interface ProxyTestResult {
  ok: boolean
  error?: string
}

export function testProxy(): Promise<ProxyTestResult> {
  return request('GET', '/admin/v1/system/proxy-test')
}

// ---- /api-keys 密钥（Legacy 形状）----

export function fetchApiKeys(): Promise<{ items: ApiKeyItem[] }> {
  return request('GET', '/admin/v1/api-keys')
}

export interface CreateApiKeyBody {
  name: string
  role: 'full' | 'readonly'
  expiresAt?: string
}

/** 创建密钥：响应含一次性明文 key（仅此一次可见）。 */
export function createApiKey(body: CreateApiKeyBody): Promise<ApiKeyItem & { key: string }> {
  return request('POST', '/admin/v1/api-keys', body)
}

export function revokeApiKey(id: number): Promise<{ ok: boolean }> {
  return request('DELETE', `/admin/v1/api-keys/${String(id)}`)
}

/** 重置密钥：轮换明文，旧明文立即失效，响应含一次性新明文。 */
export function resetApiKey(id: number): Promise<ApiKeyItem & { key: string }> {
  return request('POST', `/admin/v1/api-keys/${String(id)}/reset`)
}

// ---- /settings 运维设置（Legacy 热改项 + v2 健康权重）----

export function fetchSettings(): Promise<{ items: SettingItem[] }> {
  return request('GET', '/admin/v1/settings')
}

export function updateSetting(key: string, value: string): Promise<{ ok: boolean }> {
  return request('PUT', `/admin/v1/settings/${key}`, { value })
}

export function fetchHealthWeights(): Promise<HealthWeightsResponse> {
  return request('GET', '/admin/v2/settings/health-weights')
}

export function putHealthWeights(config: HealthWeightsConfig): Promise<HealthWeightsResponse> {
  return request('PUT', '/admin/v2/settings/health-weights', config)
}

// ---- /settings 归档与清理（v2）----

export function fetchArchiveOverview(): Promise<ArchiveOverview> {
  return request('GET', '/admin/v2/archive/overview')
}

export interface ArchiveJobQuery {
  status?: string
  mode?: string
  trigger?: string
  page?: number
  pageSize?: number
}

export function fetchArchiveJobs(query: ArchiveJobQuery): Promise<ArchiveJobListResponse> {
  return request('GET', `/admin/v2/archive/jobs${buildQuery({ ...query })}`)
}

export function fetchArchiveJobDetail(id: number): Promise<ArchiveJobDetail> {
  return request('GET', `/admin/v2/archive/jobs/${String(id)}`)
}

export interface CreateArchiveJobBody {
  mode: 'dry_run' | 'execute'
  domains?: string[]
}

export function createArchiveJob(body: CreateArchiveJobBody): Promise<ArchiveJobDetail> {
  return request('POST', '/admin/v2/archive/jobs', body)
}

export function retryArchiveJob(id: number): Promise<ArchiveJobDetail> {
  return request('POST', `/admin/v2/archive/jobs/${String(id)}/retry`)
}

export function cancelArchiveJob(id: number): Promise<ArchiveJobDetail> {
  return request('POST', `/admin/v2/archive/jobs/${String(id)}/cancel`)
}

// ---- /namespaces namespace 与互通信任（v2）----

export interface NamespaceQuery {
  keyword?: string
  page?: number
  pageSize?: number
}

export function fetchNamespaceList(query: NamespaceQuery): Promise<NamespaceListResponse> {
  return request('GET', `/admin/v2/namespaces${buildQuery({ ...query })}`)
}

export interface CreateNamespaceBody {
  name: string
  description?: string
}

/** 创建 namespace：响应含一次性明文接入 token（仅此一次可见）。 */
export function createNamespace(body: CreateNamespaceBody): Promise<NamespaceCreated> {
  return request('POST', '/admin/v2/namespaces', body)
}

export interface TrustQuery {
  fromNamespaceId?: number
  toNamespaceId?: number
  capability?: string
  status?: string
  page?: number
  pageSize?: number
}

export function fetchTrusts(query: TrustQuery): Promise<NamespaceTrustListResponse> {
  return request('GET', `/admin/v2/namespace-trusts${buildQuery({ ...query })}`)
}

export interface GrantTrustBody {
  fromNamespaceId: number
  toNamespaceId: number
  capability: 'schedule' | 'message' | 'agent_ops'
  note: string
}

export function grantTrust(body: GrantTrustBody): Promise<NamespaceTrustItem> {
  return request('POST', '/admin/v2/namespace-trusts', body)
}

export function revokeTrust(id: number, reason: string): Promise<NamespaceTrustItem> {
  return request('POST', `/admin/v2/namespace-trusts/${String(id)}/revoke`, { reason })
}
