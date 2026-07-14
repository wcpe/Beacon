// 配置中心 V2 域响应契约（/admin/v2/config-files*、config-versions*）。
// 契约真源：docs/specs/v2-config-center.md §5；合并语义 §4.1。

import type { Paged } from './common'

export type ConfigFormat = 'yaml' | 'json' | 'properties'
export type ConfigScopeLevel = 'namespace' | 'bc_cluster' | 'region' | 'zone' | 'server'

/** 配置文件行 */
export interface ConfigFileRow {
  id: number
  namespaceId: number
  name: string
  format: ConfigFormat
  description: string
  schemaJson: string | null
  sensitivePaths: string[]
  deletedAt: string | null
  deletedBy: string | null
  createdBy: string
  createdAt: string
  updatedAt: string
}

/** 层版本行（不可变链） */
export interface ConfigVersionRow {
  id: number
  configFileId: number
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  versionNo: number
  content: string
  contentHash: string
  isRemoval: boolean
  basedOnVersionId: number | null
  remark: string
  createdBy: string
  createdAt: string
}

/** 文件列表项 */
export interface ConfigFileItem {
  id: number
  namespaceId: number
  name: string
  format: ConfigFormat
  description: string
  contributingLayerCount: number
  updatedAt: string
  effectiveHash?: string
}

export type ConfigFileListResponse = Paged<ConfigFileItem>

/** 文件详情内嵌的各层覆盖概览（GET /admin/v2/config-files/{id}） */
export interface ConfigFileScopeOverview {
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  scopeName: string
  headVersionNo: number
  isRemoval: boolean
}

/** 文件详情：元数据 + 各层覆盖概览 */
export type ConfigFileDetail = ConfigFileRow & { scopes: ConfigFileScopeOverview[] }

/** 回收站列表项（GET /admin/v2/config-files/trash） */
export interface TrashItem {
  id: number
  namespaceId: number
  name: string
  format: ConfigFormat
  deletedBy: string | null
  deletedAt: string | null
}

export interface TrashListResponse {
  items: TrashItem[]
  total: number
}

/** 某链版本列表项（GET /admin/v2/config-files/{id}/versions，新 → 旧） */
export interface ConfigVersionListItem {
  versionId: number
  versionNo: number
  contentHash: string
  isRemoval: boolean
  basedOnVersionId: number | null
  remark: string
  createdBy: string
  createdAt: string
}

export interface VersionListResponse {
  items: ConfigVersionListItem[]
  total: number
}

/** 保存新版本 / 回退结果 */
export interface SaveVersionResult {
  versionId: number
  versionNo: number
  contentHash: string
}

/** 撤销层贡献结果 */
export interface RevokeResult {
  versionId: number
  versionNo: number
  isRemoval: true
}

/** 层贡献链概览 */
export interface ConfigScopeSummary {
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  scopeName: string
  headVersionNo: number
  headHash: string
  isRemoval: boolean
  updatedBy: string
  updatedAt: string
}

/** 逐键来源 */
export interface ConfigProvenanceEntry {
  path: string
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  scopeName: string
  versionNo: number
}

/** 被 null 删除的键（含执行删除层的名称，供页面直接展示） */
export interface ConfigDeletedKey {
  path: string
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  scopeName: string
  versionNo: number
}

/** 有效配置预览响应 */
export interface ConfigEffectiveResponse {
  effectiveContent: string
  effectiveHash: string
  provenance: ConfigProvenanceEntry[]
  deletedKeys: ConfigDeletedKey[]
  layers: {
    scopeLevel: ConfigScopeLevel
    scopeRefId: number | null
    scopeName: string | null
    contributing: boolean
    headVersionNo: number | null
    headHash: string | null
  }[]
}

/** 键级 diff 响应 */
export interface ConfigDiffResponse {
  added: { path: string; right: string }[]
  removed: { path: string; left: string }[]
  changed: { path: string; left: string; right: string }[]
  unifiedDiff: string
}

export interface ConfigValidateResponse {
  valid: boolean
  errors: { path: string; message: string }[]
}
