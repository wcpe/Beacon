// 变更单域数据获取（/changes 与 /changes/history 复用）：
// 覆盖 /admin/v2/change-orders* 全部端点（列表 / 创建 / 详情 / 编辑 / 删除 /
// 差异重扫 / 影响预览 / 生命周期迁移 / 批次确认 / 整单回滚 / 目标 / 观察窗 / 事件）。

import type {
  ChangeImpactResponse,
  ChangeObserveResponse,
  ChangeOrderDetail,
  ChangeOrderEvent,
  ChangeOrderItem,
  ChangeOrderListResponse,
  ChangeSelector,
  ChangeTarget,
  ConfigChangeInput,
  FileDiffResponse,
  Paged,
} from '@beacon/contracts'

import { buildQuery, request } from './request'

// ---- 类型别名（供 /changes 与 /changes/history 页共用，避免各页重复 import devmock 深路径）----

export type {
  ActivationMethod,
  ChangeBatch,
  ChangeBatchStatus,
  ChangeImpactConfigScope,
  ChangeImpactResponse,
  ChangeImpactTarget,
  ChangeObserveResponse,
  ChangeOrderDetail,
  ChangeOrderEvent,
  ChangeOrderItem,
  ChangeOrderListResponse,
  ChangeOrderStatus,
  ChangeOrderSummary,
  ChangeSelector,
  ChangeTarget,
  ChangeTargetStatus,
  ConfigChangeInput,
  FileDiffResponse,
  PayloadState,
} from '@beacon/contracts'

/** 事件端点响应：SSE 的轮询替代形态（一次性数组） */
export interface ChangeEventsResponse {
  events: ChangeOrderEvent[]
}

/** 差异扫描响应：同步读最新文件资产快照算出的文件差异清单（重扫为单独动作，见 spec §4.2.1） */
export interface DiffScanResponse {
  status: string
  diffSnapshotAt: string | null
  items: ChangeOrderItem[]
}

// ---- 列表 ----

export interface ChangeOrderQuery {
  status?: string
  namespaceId?: number
  createdBy?: string
  keyword?: string
  page?: number
  pageSize?: number
}

export function fetchChangeOrders(query: ChangeOrderQuery): Promise<ChangeOrderListResponse> {
  return request('GET', `/admin/v2/change-orders${buildQuery({ ...query })}`)
}

// ---- 创建 / 编辑 ----

export interface ChangeOrderInput {
  namespaceId?: number
  title?: string
  description?: string
  sourceServerId?: string | null
  /** 差异扫描的服务器根内相对目录范围（如 plugins/）；重扫 / 重算用同一范围 */
  scanDir?: string
  selector?: ChangeSelector
  batchMode?: 'percent' | 'count'
  batchSizes?: number[]
  activationMethod?: 'restart' | 'hot_reload' | 'push_only'
  observeWindowSec?: number
  activateTimeoutSec?: number
  failureRateThresholdPercent?: number
  unhealthyRateThresholdPercent?: number
  /** 配置变更项（整组替换 config_change 项，PATCH 专用） */
  configChanges?: ConfigChangeInput[]
}

export function createChangeOrder(body: ChangeOrderInput): Promise<ChangeOrderDetail> {
  return request('POST', '/admin/v2/change-orders', body)
}

export function updateChangeOrder(id: number, body: ChangeOrderInput): Promise<ChangeOrderDetail> {
  return request('PATCH', `/admin/v2/change-orders/${String(id)}`, body)
}

export function deleteChangeOrder(id: number): Promise<undefined> {
  return request('DELETE', `/admin/v2/change-orders/${String(id)}`)
}

// ---- 详情 ----

export function fetchChangeOrder(id: number): Promise<ChangeOrderDetail> {
  return request('GET', `/admin/v2/change-orders/${String(id)}`)
}

// ---- 差异重扫 ----

export function diffScanChangeOrder(id: number): Promise<DiffScanResponse> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/diff-scan`)
}

// ---- 变更项文件内容预览 ----

/** 文件内容预览可选参数：before 侧目标服 + 敏感路径放行原因（无原因预览敏感项 → 403） */
export interface FileDiffQuery {
  serverId?: string
  reason?: string
}

export function fetchChangeItemFileDiff(
  id: number,
  itemId: number,
  query?: FileDiffQuery,
): Promise<FileDiffResponse> {
  return request(
    'GET',
    `/admin/v2/change-orders/${String(id)}/items/${String(itemId)}/file-diff${buildQuery({ ...query })}`,
  )
}

// ---- 影响预览 ----

export function fetchChangeImpact(
  id: number,
  page?: number,
  pageSize?: number,
): Promise<ChangeImpactResponse> {
  return request('GET', `/admin/v2/change-orders/${String(id)}/impact${buildQuery({ page, pageSize })}`)
}

// ---- 生命周期迁移 ----

export function submitChangeOrder(id: number): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/submit`)
}

export function withdrawChangeOrder(id: number): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/withdraw`)
}

export function approveChangeOrder(id: number): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/approve`)
}

export function rejectChangeOrder(id: number, reason: string): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/reject`, { reason })
}

export function startChangeOrder(id: number): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/start`)
}

export function pauseChangeOrder(id: number): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/pause`)
}

export interface ResumeBody {
  mode?: 'retry_failed' | 'skip_failed'
  reason?: string
}

export function resumeChangeOrder(id: number, body: ResumeBody): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/resume`, body)
}

export function cancelChangeOrder(id: number, reason: string): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/cancel`, { reason })
}

// ---- 批次推进确认 ----

export function confirmChangeBatch(id: number, batchNo: number): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/batches/${String(batchNo)}/confirm`)
}

// ---- 整单回滚（history 页用）----

export function rollbackChangeOrder(id: number, reason: string): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/rollback`, { reason })
}

export function finishRollbackChangeOrder(id: number): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/rollback/finish`)
}

// ---- 目标分页（批次 / 状态 / serverId 过滤）----

export interface ChangeTargetQuery {
  batch?: number
  status?: string
  serverId?: string
  page?: number
  pageSize?: number
}

export function fetchChangeTargets(
  id: number,
  query: ChangeTargetQuery,
): Promise<Paged<ChangeTarget>> {
  return request('GET', `/admin/v2/change-orders/${String(id)}/targets${buildQuery({ ...query })}`)
}

// ---- 观察窗 ----

export function fetchChangeObserve(id: number): Promise<ChangeObserveResponse> {
  return request('GET', `/admin/v2/change-orders/${String(id)}/observe`)
}

// ---- 进度事件（轮询替代 SSE）----

export function fetchChangeEvents(id: number): Promise<ChangeEventsResponse> {
  return request('GET', `/admin/v2/change-orders/${String(id)}/events`)
}
