// 文件资产域数据获取（/assets）：清单元数据、扫描概要、跨服哈希比对与敏感内容审批入口。

import type {
  AssetCompareResponse,
  AssetListResponse,
  AssetRescanResponse,
  AssetScanStatusItem,
  AssetSensitiveRulesResponse,
} from '@beacon/contracts'

import { buildQuery, request } from './request'

export interface SensitiveAccessApprovalResponse {
  requestId: string
  status: string
}

/** 双侧文件差异的两份独立审批申请。 */
export interface AssetPairReadApprovalResponse {
  leftRequestId: string
  rightRequestId: string
  status: string
}

export interface AssetPairDiffSummary {
  identical: boolean
  changed: boolean
  unsupported: boolean
  left: { serverId: string; path: string; sha256: string; size: number }
  right: { serverId: string; path: string; sha256: string; size: number }
}

// ---- 资产清单列表 ----

export interface AssetQuery {
  namespaceId: number
  serverId?: string
  pathPrefix?: string
  name?: string
  ext?: string
  sha256?: string
  page?: number
  pageSize?: number
}

export function fetchAssets(query: AssetQuery): Promise<AssetListResponse> {
  return request('GET', `/admin/v2/assets${buildQuery({ ...query })}`)
}

// ---- 扫描概要（每服清单摘要）----

export interface ScanStatusResponse {
  items: AssetScanStatusItem[]
  total: number
}

export function fetchScanStatus(
  namespaceId: number,
  serverId?: string,
  page?: number,
  pageSize?: number,
): Promise<ScanStatusResponse> {
  return request('GET', `/admin/v2/assets/scan-status${buildQuery({ namespaceId, serverId, page, pageSize })}`)
}

// ---- 跨服比对（同路径按哈希分组 + 缺失服）----

export interface CompareQuery {
  namespaceId: number
  path: string
  serverIds?: string
  zoneId?: number
}

export function fetchCompare(query: CompareQuery): Promise<AssetCompareResponse> {
  return request('GET', `/admin/v2/assets/compare${buildQuery({ ...query })}`)
}

// ---- 触发重扫（批量下发）----

export interface RescanBody {
  namespaceId?: number
  serverIds: string[]
  force?: boolean
}

export function rescanAssets(body: RescanBody): Promise<AssetRescanResponse> {
  return request('POST', '/admin/v2/assets/rescan', body)
}

// ---- 敏感路径规则（FR-164）：读 / 整体替换；命中 glob 的内容读取走审批流程。 ----

export function fetchSensitiveRules(): Promise<AssetSensitiveRulesResponse> {
  return request('GET', '/admin/v2/assets/sensitive-rules')
}

export function updateSensitiveRules(patterns: string[]): Promise<AssetSensitiveRulesResponse> {
  return request('PUT', '/admin/v2/assets/sensitive-rules', { patterns })
}

/** 创建单文件读取专用审批，批准前不读取正文。 */
export function requestAssetPreviewApproval(serverId: string, path: string, reason: string, idempotencyKey: string): Promise<SensitiveAccessApprovalResponse> {
  return request('POST', '/admin/v2/assets/preview/approval-requests', { serverId, path, reason }, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

/** 创建左右两侧各自独立的文件读取审批；审批本身不读取正文。 */
export function requestAssetPairReadApprovals(
  left: { serverId: string; path: string },
  right: { serverId: string; path: string },
  reason: string,
  idempotencyKey: string,
): Promise<AssetPairReadApprovalResponse> {
  return request('POST', '/admin/v2/assets/pair-read/approval-requests', { left, right, reason }, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

/** 单次消费 Agent 已回传的文件内容；调用方不得保存或渲染响应正文。 */
export async function consumeAssetPreviewGrant(grantId: string, commandId: number): Promise<void> {
  await request('POST', `/admin/v2/assets/preview/grants/${encodeURIComponent(grantId)}/consume`, { commandId })
}

/** 消费任一侧 grant 时服务端会原子校验并消费双侧授权，只返回可展示的元数据差异摘要。 */
export async function consumeAssetPairReadGrant(grantId: string, commandId: number): Promise<AssetPairDiffSummary> {
  const result = await request<AssetPairDiffSummary>('POST', `/admin/v2/assets/pair-read/grants/${encodeURIComponent(grantId)}/consume`, { commandId })
  return {
    identical: result.identical,
    changed: result.changed,
    unsupported: result.unsupported,
    left: { serverId: result.left.serverId, path: result.left.path, sha256: result.left.sha256, size: result.left.size },
    right: { serverId: result.right.serverId, path: result.right.path, sha256: result.right.sha256, size: result.right.size },
  }
}

/** 从审批执行结果提取已冻结的 Agent 读取命令标识。 */
export function assetReadCommandId(resultRef: string | null | undefined): number | null {
  const matched = /^agent-sensitive-operation:command:(\d+):grant:[^:]+$/.exec(resultRef ?? '')
  return matched === null ? null : Number(matched[1])
}
