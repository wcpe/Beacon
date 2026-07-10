// 交付编排域响应契约（/admin/v2/change-orders*）。
// 契约真源：docs/specs/v2-delivery-orchestration.md §5.1；三层状态机 §4.1。

import type { Paged } from './common'

export type ChangeOrderStatus =
  | 'draft'
  | 'pending_approval'
  | 'approved'
  | 'rolling'
  | 'paused'
  | 'completed'
  | 'cancelled'
  | 'rolling_back'
  | 'rolled_back'

export type ChangeBatchStatus = 'pending' | 'running' | 'observing' | 'awaiting_confirm' | 'completed' | 'failed' | 'skipped'
export type ChangeTargetStatus = 'pending' | 'pushing' | 'pushed' | 'activating' | 'activated' | 'failed' | 'skipped'
export type ActivationMethod = 'restart' | 'hot_reload' | 'push_only'
export type PayloadState = 'pending' | 'uploading' | 'ready' | 'failed'

/** 目标筛选器（§4.3.1） */
export interface ChangeSelector {
  all: boolean
  regions: number[]
  zones: number[]
  servers: string[]
  excludes: string[]
}

/** 变更项（文件差异 / 配置变更两种载荷） */
export interface ChangeOrderItem {
  id: number
  kind: 'file_diff' | 'config_change'
  path: string | null
  action: 'add' | 'update' | 'delete' | null
  sha256: string | null
  sizeBytes: number | null
  configScopeKind: string | null
  configScopeId: number | null
  configFromVersionId: number | null
  configToVersionId: number | null
}

/** 批次 */
export interface ChangeBatch {
  batchNo: number
  status: ChangeBatchStatus
  plannedCount: number
  successCount: number
  failedCount: number
  skippedCount: number
  startedAt: string | null
  observeStartedAt: string | null
  finishedAt: string | null
  gateConfirmedBy: string | null
  gateConfirmedAt: string | null
  breakReason: string | null
}

/** 目标服 */
export interface ChangeTarget {
  serverId: string
  batchNo: number
  status: ChangeTargetStatus
  pushedAt: string | null
  activatedAt: string | null
  changedFileCount: number
  skippedFileCount: number
  backupPresent: boolean
  error: string | null
  rollbackStatus: 'pending' | 'running' | 'rolled_back' | 'failed' | null
  rollbackError: string | null
}

/** 变更单（列表项） */
export interface ChangeOrderSummary {
  id: number
  namespaceId: number
  title: string
  description: string
  sourceServerId: string | null
  status: ChangeOrderStatus
  pauseKind: 'manual' | 'circuit_break' | 'prepare_failed' | null
  pauseReason: string | null
  batchMode: 'percent' | 'count'
  batchSizes: number[]
  activationMethod: ActivationMethod
  observeWindowSec: number
  activateTimeoutSec: number
  failureRateThresholdPercent: number
  unhealthyRateThresholdPercent: number
  payloadState: PayloadState
  diffSnapshotAt: string | null
  createdBy: string
  submittedAt: string | null
  approvedBy: string | null
  approvedAt: string | null
  rejectReason: string | null
  startedAt: string | null
  finishedAt: string | null
  cancelReason: string | null
  rollbackBy: string | null
  rollbackReason: string | null
  rollbackAt: string | null
  createdAt: string
  updatedAt: string
}

/** 变更单详情：单 + items + 批次概要 + 目标计数 + 回滚进度计数 */
export interface ChangeOrderDetail extends ChangeOrderSummary {
  selector: ChangeSelector
  items: ChangeOrderItem[]
  batches: ChangeBatch[]
  targetCounts: Record<string, number>
  /** 各目标 rollbackStatus 计数（未进入回滚的目标不计入），供前端展示回滚进度 */
  rollbackCounts: Record<string, number>
}

export type ChangeOrderListResponse = Paged<ChangeOrderSummary>

/** 影响预览 */
export interface ChangeImpactResponse {
  summary: {
    targetTotal: number
    batches: { batchNo: number; count: number }[]
    fileTotal: number
    totalBytes: number
    transferBytes: number
    configScopeCount: number
    snapshotAt: string | null
  }
  targets: Paged<{
    serverId: string
    online: boolean
    level: string
    addCount: number
    updateCount: number
    deleteCount: number
    skipCount: number
  }>
}

/** 观察窗数据（当前批逐目标健康序列） */
export interface ChangeObserveResponse {
  batchNo: number | null
  observeStartedAt: string | null
  targets: { serverId: string; series: { tsMs: number; score: number; level: string; tps: number; alerts: number }[] }[]
}

/** 进度事件（SSE 的轮询替代形态） */
export interface ChangeOrderEvent {
  seq: number
  at: string
  type: 'order_status' | 'batch_status' | 'target_status'
  orderId: number
  batchNo: number | null
  serverId: string | null
  status: string
}

/**
 * 变更项文件内容预览响应（mock 临时能力）。
 * 说明：本端点超出 docs/API.md v2 草案（草案未定义变更项文件内容查询），仅为 mock 支撑
 * 「点开预览文件内容」的前端评审；后端接真时需正式化契约（响应形状 / 截断阈值 / 二进制处理）。
 */
export interface FileDiffResponse {
  path: string
  changeType: 'added' | 'modified' | 'removed'
  before: string | null
  after: string | null
  truncated: boolean
}

/** 配置变更项输入（PATCH configChanges，整组替换 config_change 项） */
export interface ConfigChangeInput {
  configScopeKind: string
  configScopeId: number
  configFromVersionId: number | null
  configToVersionId: number
}
