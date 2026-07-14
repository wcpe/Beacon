// 文件资产域响应契约（/admin/v2/assets*，只读索引 + 预览 / diff / 敏感规则）。
// 契约真源：docs/specs/v2-file-assets.md §5.2；敏感放行 §4.6。

import type { Paged } from './common'

/** 资产行（每服最新清单快照的一个文件） */
export interface AssetItem {
  serverId: string
  namespaceId: number
  path: string
  ext: string
  sha256: string
  size: number
  mtimeMs: number
  isText: boolean
  scannedAt: string
}

export type AssetListResponse = Paged<AssetItem>

/** 每服扫描概要 */
export interface AssetScanStatusItem {
  serverId: string
  namespaceId: number
  manifestDigest: string
  fileCount: number
  totalSize: number
  truncated: boolean
  scannedAt: string
  scanDurationMs: number
}

/** 跨服哈希比对分组 */
export interface AssetCompareGroup {
  sha256: string
  size: number
  servers: { serverId: string; mtimeMs: number; scannedAt: string }[]
}

export interface AssetCompareResponse {
  path: string
  groups: AssetCompareGroup[]
  missing: string[]
}

/** 重扫下发结果 */
export interface AssetRescanResponse {
  results: { serverId: string; commandId: string | null; offline: boolean }[]
}

/** 文本预览响应 */
export interface AssetPreviewResponse {
  content: string | null
  truncated: boolean
  binary: boolean
  sha256: string
  size: number
  sensitive: boolean
}

/** diff 响应：两侧内容或 identical 短路 */
export interface AssetDiffResponse {
  identical: boolean
  left?: { serverId: string; path: string; content: string; sha256: string }
  right?: { serverId: string; path: string; content: string; sha256: string }
}

/** 敏感路径规则清单（GET/PUT /admin/v2/assets/sensitive-rules；整体替换语义，命中即禁止预览 / diff 内容） */
export interface AssetSensitiveRulesResponse {
  patterns: string[]
}
