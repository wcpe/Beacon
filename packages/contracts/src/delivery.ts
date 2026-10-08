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

/** 某服「当前交付版本」（FR-271）：该服最近一条 activated 且未被回滚的交付记录 */
export interface DeliveredVersion {
  serverId: string
  orderId: number
  orderTitle: string
  activatedAt: string
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
  /** 该服当前交付版本（FR-271）；无交付记录为 null */
  deliveredVersion: DeliveredVersion | null
}

/** 交付版本批量查询响应（FR-271）：无交付记录的服不回行 */
export interface DeliveredVersionListResponse {
  items: DeliveredVersion[]
}

/** 回滚动作内逐台结果（FR-271） */
export interface ChangeRollbackRecordTarget {
  serverId: string
  result: string
  error: string | null
}

/**
 * 目标分页响应（/change-orders/{id}/targets）。
 * `rollbackEligibleCount` 是本单**可回滚目标数**（曾覆盖磁盘 = pushedAt 非空），与 `total` 不是一回事：
 * total 含从未推送的目标。界面「全选等价整单回滚」的判定基数必须用它——用 total 会在「存在未推送台」时
 * 把全覆盖误判成子集（界面说「配置不回退」而后端按整单执行），按批筛选时又反之。
 */
export interface ChangeTargetPage {
  items: ChangeTarget[]
  total: number
  rollbackEligibleCount: number
}

/** 一次回滚动作记录（FR-270 / FR-271）：整单 / 子集 / 重试各一条 */
export interface ChangeRollbackRecord {
  id: number
  kind: 'order' | 'targets'
  reason: string
  operator: string
  /** 本次动作是否回退了配置版本；子集回滚与重试恒为 false（界面据此明示「配置未回退」） */
  configRolledBack: boolean
  targetCount: number
  createdAt: string
  targets: ChangeRollbackRecordTarget[]
}

/** 回滚动作记录列表响应（倒序） */
export interface ChangeRollbackRecordListResponse {
  items: ChangeRollbackRecord[]
}

/** 变更单（列表项） */
export interface ChangeOrderSummary {
  id: number
  namespaceId: number
  title: string
  description: string
  sourceServerId: string | null
  /** 差异扫描的服务器根内相对目录范围（如 plugins/），纯配置单为空串 */
  scanDir: string
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

/** 影响预览逐目标行命中的配置作用域（该目标将应用的 config_change 项 from→to 版本） */
export interface ChangeImpactConfigScope {
  scopeKind: string
  scopeId: number
  fromVersionId: number | null
  toVersionId: number
}

/** 影响预览逐目标行 */
export interface ChangeImpactTarget {
  serverId: string
  online: boolean
  level: string
  addCount: number
  updateCount: number
  deleteCount: number
  skipCount: number
  /** 命中的配置作用域清单，空数组 = 本单配置变更不覆盖该目标 */
  configScopes: ChangeImpactConfigScope[]
}

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
  targets: Paged<ChangeImpactTarget>
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
 * 交付审批票据：六类申请动作（submit / delete / resume / batch confirm / rollback / rollback-finish）
 * 一律 202 返回本形（后端 DeliveryApprovalTicketView，HTTP 面直出结构体）。
 * 票据只说明「申请已受理」——变更单状态不在此刻迁移，审批决定只在 /approvals 完成。
 */
export interface DeliveryApprovalTicket {
  approvalRequestId: string
  status: string
  operationKey: string
  /** 票据归属的变更单号（六类交付申请恒有） */
  orderId: number
  /** 建申请时刻的影响摘要（即时读数，不随后续推进漂移） */
  impactSummary: DeliveryImpactSummary
}

/** 交付申请影响摘要（计数为 0 时保留 0，便于稳定解析） */
export interface DeliveryImpactSummary {
  targetCount: number
  batchCount: number
  payloadFiles: number
  payloadConfigs: number
}

/**
 * 变更项文件内容预览响应（GET /change-orders/{id}/items/{itemId}/file-diff）。
 * 可选 query：serverId（before 侧目标服）、reason（敏感路径放行原因）。
 * 错误形态：真机恒 409 operation_requires_approval（内容读取必须先走统一审批），
 * 前端据此展示「需审批」引导；本结构为审批放行后的成功形态。
 */
export interface FileDiffResponse {
  path: string
  changeType: 'added' | 'modified' | 'removed'
  /** add 项 before 恒为 null */
  before: string | null
  /** delete 项 after 恒为 null */
  after: string | null
  truncated: boolean
  /** 二进制项内容前后皆 null，仅展示元数据 */
  binary: boolean
  /** 实际所用的 before 侧目标服（无可用目标时为 null） */
  serverId: string | null
}

/** 配置变更项输入（PATCH configChanges，整组替换 config_change 项） */
export interface ConfigChangeInput {
  configScopeKind: string
  configScopeId: number
  configFromVersionId: number | null
  configToVersionId: number
}
