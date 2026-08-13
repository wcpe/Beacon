// 系统大域数据获取（/settings /system /system/version /api-keys /namespaces）：
// 统一走 mock handlers 端点，读端点用于 useQuery、写端点用于 useMutation；
// 错误按脱敏后的 message 抛出（ADR-0057）。复用集群域已导出的 ApiClientError，避免重复定义。

import type {
  ApiKeyItem,
  ArchiveJobDetail,
  ArchiveJobListResponse,
  ArchiveOverview,
  EnvItem,
  EnvListResponse,
  HealthWeightsConfig,
  HealthWeightsResponse,
  NamespaceCreated,
  NamespaceListResponse,
  NamespaceTrustItem,
  NamespaceTrustListResponse,
  NamespaceLifecycleImpact,
  SettingItem,
  SystemObservability,
  SystemStatus,
  UpdateCheck,
  UpdateProgress,
} from '@beacon/contracts'

import type { ApprovalTicket } from './cluster'

// 收敛到集群域的全站统一请求封装（含鉴权注入与 401 处理，FR-179）；ApiClientError 再导出供调用方 instanceof。
import { ApiClientError, buildQuery, request } from './cluster'

export { ApiClientError }

// ---- /system 控制面健康（Legacy 形状）----

export function fetchSystemStatus(): Promise<SystemStatus> {
  return request('GET', '/admin/v1/system/status')
}

export function fetchSystemObservability(): Promise<SystemObservability> {
  return request('GET', '/admin/v1/system/observability')
}

// ---- /system/version 版本与更新（Legacy 形状）----

export function fetchUpdateCheck(force = false): Promise<UpdateCheck> {
  return request('GET', `/admin/v1/system/update-check${force ? '?force=true' : ''}`)
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
  reason: string
}

export interface ApiKeyApprovalTicket {
  approvalRequestId: string
  status: string
  operationKey: string
}

/** 创建密钥只提交审批申请；明文只能在审批成功后由原申请人一次性领取。 */
export function createApiKey(body: CreateApiKeyBody, idempotencyKey: string): Promise<ApiKeyApprovalTicket> {
  return request('POST', '/admin/v1/api-keys', body, { headers: { 'Idempotency-Key': idempotencyKey } })
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
  lifecycleStatus?: 'active' | 'archived' | 'tombstoned' | 'all'
  page?: number
  pageSize?: number
}

export function fetchNamespaceList(query: NamespaceQuery): Promise<NamespaceListResponse> {
  return request('GET', `/admin/v2/namespaces${buildQuery({ ...query })}`)
}

export function fetchNamespaceLifecycleImpact(id: number, action: 'archive' | 'restore' | 'permanent-delete'): Promise<NamespaceLifecycleImpact> {
  return request('GET', `/admin/v2/namespaces/${String(id)}/lifecycle-impact${buildQuery({ action })}`)
}

export function fetchNamespacePermanentDeletionImpact(id: number): Promise<NamespaceLifecycleImpact> {
  return request('GET', `/admin/v2/namespaces/${String(id)}/permanent-deletion-impact`)
}

export interface CreateNamespaceBody {
  name?: string
  code?: string
  displayName?: string
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

export function grantTrust(body: GrantTrustBody): Promise<ApprovalTicket> {
  return request('POST', '/admin/v2/namespace-trusts', body)
}

export function revokeTrust(id: number, reason: string): Promise<NamespaceTrustItem> {
  return request('POST', `/admin/v2/namespace-trusts/${String(id)}/revoke`, { reason })
}

// ---- /envs env 展示维度（v2，FR-178）----
// env 是纯展示 / 过滤维度：env→namespace 映射整体替换（PUT 幂等），一个 namespace 至多属一个 env。

export interface EnvQuery {
  keyword?: string
  page?: number
  pageSize?: number
}

export function fetchEnvList(query: EnvQuery): Promise<EnvListResponse> {
  return request('GET', `/admin/v2/envs${buildQuery({ ...query })}`)
}

export interface CreateEnvBody {
  name?: string
  code?: string
  displayName?: string
  description?: string
}

export function createEnv(body: CreateEnvBody): Promise<EnvItem> {
  return request('POST', '/admin/v2/envs', body)
}

export interface UpdateEnvBody {
  name?: string
  displayName?: string
  description?: string
}

export function updateEnv(id: number, body: UpdateEnvBody): Promise<EnvItem> {
  return request('PATCH', `/admin/v2/envs/${String(id)}`, body)
}

export function deleteEnv(id: number): Promise<void> {
  return request('DELETE', `/admin/v2/envs/${String(id)}`)
}

/** 整体替换 env→namespace 映射：被其他 env 占用的 namespace 返回 409 指明冲突方。 */
export function setEnvNamespaces(id: number, namespaceIds: number[]): Promise<EnvItem> {
  return request('PUT', `/admin/v2/envs/${String(id)}/namespaces`, { namespaceIds })
}
