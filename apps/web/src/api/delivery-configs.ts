// 配置中心域数据获取（/configs）：配置文件列表 / 回收站 / 详情 / 作用域 / 有效配置 /
// 版本链 / 版本详情 / diff / 校验 / 保存新版本 / 回退 / 撤销层贡献 / 软删除 / 恢复 / 彻底删除。
// 全部走 /admin/v2/config-files* 与 /admin/v2/config-versions* mock 端点。

import type {
  ConfigDiffResponse,
  ConfigEffectiveResponse,
  ConfigFileListResponse,
  ConfigFileRow,
  ConfigFormat,
  ConfigScopeLevel,
  ConfigScopeSummary,
  ConfigValidateResponse,
  ConfigVersionRow,
} from '@beacon/contracts'

import { buildQuery, request } from './request'

// ---- 配置文件列表 ----

export interface ConfigFileQuery {
  namespaceId: number
  keyword?: string
  serverId?: string
  page?: number
  pageSize?: number
}

export function fetchConfigFiles(query: ConfigFileQuery): Promise<ConfigFileListResponse> {
  return request('GET', `/admin/v2/config-files${buildQuery({ ...query })}`)
}

// ---- 回收站列表 ----

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

export interface TrashQuery {
  namespaceId: number
  keyword?: string
  page?: number
  pageSize?: number
}

export function fetchConfigTrash(query: TrashQuery): Promise<TrashListResponse> {
  return request('GET', `/admin/v2/config-files/trash${buildQuery({ ...query })}`)
}

// ---- 新建配置文件 ----

export interface CreateConfigFileBody {
  namespaceId: number
  name: string
  format: ConfigFormat
  description?: string
  schemaJson?: string
  sensitivePaths?: string[]
}

export function createConfigFile(body: CreateConfigFileBody): Promise<ConfigFileRow> {
  return request('POST', '/admin/v2/config-files', body)
}

// ---- 文件详情（元数据 + 各层概览） ----

export interface ConfigFileScopeOverview {
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  scopeName: string
  headVersionNo: number
  isRemoval: boolean
}

export type ConfigFileDetail = ConfigFileRow & { scopes: ConfigFileScopeOverview[] }

export function fetchConfigFileDetail(id: number): Promise<ConfigFileDetail> {
  return request('GET', `/admin/v2/config-files/${String(id)}`)
}

// ---- 软删除（移入回收站） ----

export function deleteConfigFile(id: number): Promise<void> {
  return request('DELETE', `/admin/v2/config-files/${String(id)}`)
}

// ---- 从回收站恢复 ----

export function restoreConfigFile(id: number): Promise<ConfigFileRow> {
  return request('POST', `/admin/v2/config-files/${String(id)}/restore`)
}

// ---- 彻底删除（原因必填） ----

export function purgeConfigFile(id: number, reason: string): Promise<void> {
  return request('POST', `/admin/v2/config-files/${String(id)}/purge`, { reason })
}

// ---- 各层贡献链概览 ----

export interface ScopesResponse {
  scopes: ConfigScopeSummary[]
}

export function fetchConfigScopes(id: number): Promise<ScopesResponse> {
  return request('GET', `/admin/v2/config-files/${String(id)}/scopes`)
}

// ---- 某链版本列表（新 → 旧） ----

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

export interface VersionListQuery {
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  page?: number
  pageSize?: number
}

export function fetchConfigVersions(id: number, query: VersionListQuery): Promise<VersionListResponse> {
  return request('GET', `/admin/v2/config-files/${String(id)}/versions${buildQuery({ ...query })}`)
}

// ---- 版本详情（内容脱敏） ----

export function fetchConfigVersion(versionId: number): Promise<ConfigVersionRow> {
  return request('GET', `/admin/v2/config-versions/${String(versionId)}`)
}

// ---- 保存新版本 ----

// SaveVersionBody 未从 devmock 导出，此处按契约自定义（basedOnVersionId 传当前 head，链空传 null）
export interface SaveVersionBody {
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  content: string
  remark?: string
  basedOnVersionId: number | null
}

export interface SaveVersionResult {
  versionId: number
  versionNo: number
  contentHash: string
}

export function saveConfigVersion(id: number, body: SaveVersionBody): Promise<SaveVersionResult> {
  return request('POST', `/admin/v2/config-files/${String(id)}/versions`, body)
}

// ---- 回退到某历史版本 ----

export function rollbackConfigVersion(versionId: number, remark?: string): Promise<SaveVersionResult> {
  return request('POST', `/admin/v2/config-versions/${String(versionId)}/rollback`, { remark })
}

// ---- 撤销某层贡献（原因必填） ----

export interface RevokeResult {
  versionId: number
  versionNo: number
  isRemoval: true
}

export function revokeConfigScope(
  id: number,
  scopeLevel: ConfigScopeLevel,
  scopeRefId: number,
  reason: string,
): Promise<RevokeResult> {
  return request(
    'DELETE',
    `/admin/v2/config-files/${String(id)}/scopes/${scopeLevel}/${String(scopeRefId)}`,
    { reason },
  )
}

// ---- 只读语法校验 ----

export interface ValidateBody {
  scopeLevel?: ConfigScopeLevel
  content: string
}

export function validateConfig(id: number, body: ValidateBody): Promise<ConfigValidateResponse> {
  return request('POST', `/admin/v2/config-files/${String(id)}/validate`, body)
}

// ---- 有效配置预览 ----

export interface EffectiveQuery {
  serverId?: string
  zoneId?: number
  regionId?: number
  bcClusterId?: number
}

export function fetchConfigEffective(id: number, query: EffectiveQuery): Promise<ConfigEffectiveResponse> {
  return request('GET', `/admin/v2/config-files/${String(id)}/effective${buildQuery({ ...query })}`)
}

// ---- 键级 diff（左右两侧标识串） ----

export function fetchConfigDiff(id: number, left: string, right: string): Promise<ConfigDiffResponse> {
  return request('GET', `/admin/v2/config-files/${String(id)}/diff${buildQuery({ left, right })}`)
}
