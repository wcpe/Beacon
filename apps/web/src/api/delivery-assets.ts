// 文件资产域数据获取（/assets，只读）：清单列表 / 扫描概要 / 跨服比对 / 预览 / diff / 触发重扫。
// 全部走 /admin/v2/assets* mock 端点。

import type {
  AssetCompareResponse,
  AssetDiffResponse,
  AssetListResponse,
  AssetPreviewResponse,
  AssetRescanResponse,
  AssetScanStatusItem,
} from '@beacon/contracts'

import { buildQuery, request } from './request'

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

// ---- 文本预览（敏感命中需填原因）----

export interface PreviewBody {
  serverId: string
  path: string
  reason?: string
}

export function previewAsset(body: PreviewBody): Promise<AssetPreviewResponse> {
  return request('POST', '/admin/v2/assets/preview', body)
}

// ---- 两侧 diff（敏感命中需填原因）----

export interface DiffBody {
  left: { serverId: string; path: string }
  right: { serverId: string; path: string }
  reason?: string
}

export function diffAssets(body: DiffBody): Promise<AssetDiffResponse> {
  return request('POST', '/admin/v2/assets/diff', body)
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
