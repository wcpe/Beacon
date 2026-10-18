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

export interface AssetReadTarget {
  namespaceId: number
  serverId: string
  path: string
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

/** 直读单文件正文，读取结果由后端先行写入元数据审计。 */
export function previewAsset(target: AssetReadTarget, reason: string): Promise<AssetPreviewResponse> {
  return request<AssetPreviewResponse>('POST', '/admin/v2/assets/preview', { ...target, reason })
}

export type AssetPairDiffResponse = Omit<AssetDiffResponse, 'left' | 'right'> & {
  left?: NonNullable<AssetDiffResponse['left']> | null
  right?: NonNullable<AssetDiffResponse['right']> | null
}

/** 直读并比较两侧文件；两侧各自携带命名空间，正文仅随本次响应返回。 */
export function readAssetPair(
  left: AssetReadTarget,
  right: AssetReadTarget,
  reason: string,
): Promise<AssetPairDiffResponse> {
  return request<AssetPairDiffResponse>('POST', '/admin/v2/assets/diff', { left, right, reason })
}
