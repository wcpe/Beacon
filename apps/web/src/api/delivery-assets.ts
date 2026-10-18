// 文件资产域数据获取（/assets）：清单元数据、扫描概要、跨服哈希比对与敏感内容读取入口。

import type {
  AssetCompareResponse,
  AssetDiffResponse,
  AssetListResponse,
  AssetPreviewResponse,
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

// ---- 敏感路径规则（FR-164）：读 / 整体替换；正文读取直执并记审计。 ----

export function fetchSensitiveRules(): Promise<AssetSensitiveRulesResponse> {
  return request('GET', '/admin/v2/assets/sensitive-rules')
}

export function updateSensitiveRules(patterns: string[]): Promise<AssetSensitiveRulesResponse> {
  return request('PUT', '/admin/v2/assets/sensitive-rules', { patterns })
}

/** 直接读取单文件正文，读取结果由后端先行写入元数据审计。 */
export function fetchAssetPreview(serverId: string, path: string, reason: string): Promise<AssetPreviewResponse> {
  return request('POST', '/admin/v2/assets/preview', { serverId, path, reason })
}

export type AssetPairDiffResponse = Omit<AssetDiffResponse, 'left' | 'right'> & {
  left?: NonNullable<AssetDiffResponse['left']> | null
  right?: NonNullable<AssetDiffResponse['right']> | null
}

/** 直接读取并比较两侧文件；正文仅随本次响应返回。 */
export function fetchAssetPairDiff(
  left: { serverId: string; path: string },
  right: { serverId: string; path: string },
  reason: string,
): Promise<AssetPairDiffResponse> {
  return request('POST', '/admin/v2/assets/diff', { left, right, reason })
}

/** 保留旧兼容路径封装；当前服务端直接读取正文，不再创建审批。 */
export function requestAssetPreviewApproval(serverId: string, path: string, reason: string, idempotencyKey: string): Promise<SensitiveAccessApprovalResponse> {
  return request('POST', '/admin/v2/assets/preview/approval-requests', { serverId, path, reason }, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

/** 保留旧兼容路径封装；当前服务端经此路径直接读取并返回差异。 */
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

/** 保留旧 grant 路径封装；当前服务端忽略 grant 授权并重新读取正文。 */
export async function consumeAssetPreviewGrant(grantId: string, commandId: number): Promise<void> {
  await request('POST', `/admin/v2/assets/preview/grants/${encodeURIComponent(grantId)}/consume`, { commandId })
}

/** 保留旧 pair grant 路径封装；当前服务端按旧记录定位目标并返回脱敏差异摘要。 */
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
