// 交付编排域 mock（/admin/v2/change-orders 全套管理面端点）。
// 契约真源：docs/specs/v2-delivery-orchestration.md §5.1（202 票据见 docs/specs/delivery-mcp-tools.md §3.6）；
// 三层状态机 §4.1（非法迁移 409）。
// 与真机对齐的三条硬契约：
// ① 六类申请动作（提审 / 删除 / 继续 / 批次确认 / 回滚 / 结束回滚）返回 **202 + 票据**，本域不迁移单据状态；
// ② 审批决定只在统一审批中心完成：旧 approve / reject / withdraw / start 入口一律 403；
// ③ 变更项文件内容预览恒 409 operation_requires_approval（读取内容必须先走统一审批）。
// 事件端点按 Accept 内容协商：text/event-stream → SSE 帧（补发快照后关流，演示模式无后台推进事件），
// 否则回一次性数组（页面据此在断流后回退 5s 轮询）。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  ActivationMethod,
  ChangeBatch,
  ChangeBatchStatus,
  ChangeImpactResponse,
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
  DeliveryApprovalTicket,
  DeliveryImpactSummary,
  Paged,
} from '@beacon/contracts'
import { jsonError, mockDelete, mockGet, mockPatch, mockPost, paginate, pathParam, queryStr, readBody } from '../http'
import { getClusterState, type ClusterState, type ServerRow } from '../data/cluster'
import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { createRng, hashString, isoOffset, pseudoSha256 } from '../support'
import { deliveryApprovalSpecs, registerDeliveryApproval, type DeliveryApprovalSpec } from './delivery-approval-bridge'

interface OrderState extends ChangeOrderDetail {
  targets: ChangeTarget[]
  events: ChangeOrderEvent[]
  /**
   * 演示态自用：本单提审票据的申请号（真机变更单表不落该列、详情响应也不带）。
   * mock 只拿它把「审批通过」回拨到对应单据；前端不得依赖该字段（审批进度按单号反查）。
   */
  approvalRequestId: string | null
}

interface DeliveryState {
  orders: OrderState[]
  nextId: number
}

const DAY = 86_400_000
const HOUR = 3_600_000

function emptySelector(): ChangeSelector {
  return { all: false, regions: [], zones: [], servers: [], excludes: [] }
}

let eventSeq = 0

function pushEvent(order: OrderState, type: ChangeOrderEvent['type'], status: string, batchNo: number | null = null, serverId: string | null = null, at: string = isoOffset(0)): void {
  eventSeq += 1
  order.events.push({ seq: eventSeq, at, type, orderId: order.id, batchNo, serverId, status })
}

/** 按种子单的生命周期字段补齐历史事件（时间与各阶段字段一致），供进度时间线回放 */
function seedEvents(order: OrderState): void {
  pushEvent(order, 'order_status', 'draft', null, null, order.createdAt)
  if (order.submittedAt !== null) {
    pushEvent(order, 'order_status', 'pending_approval', null, null, order.submittedAt)
  }
  if (order.approvedAt !== null) {
    pushEvent(order, 'order_status', 'approved', null, null, order.approvedAt)
  }
  if (order.startedAt !== null) {
    pushEvent(order, 'order_status', 'rolling', null, null, order.startedAt)
  }
  for (const batch of order.batches) {
    if (batch.startedAt !== null) {
      pushEvent(order, 'batch_status', 'running', batch.batchNo, null, batch.startedAt)
    }
    if (batch.observeStartedAt !== null) {
      pushEvent(order, 'batch_status', 'observing', batch.batchNo, null, batch.observeStartedAt)
    }
    if (batch.status === 'completed' && batch.finishedAt !== null) {
      pushEvent(order, 'batch_status', 'completed', batch.batchNo, null, batch.finishedAt)
    }
    if (batch.status === 'awaiting_confirm' || batch.status === 'failed') {
      pushEvent(order, 'batch_status', batch.status, batch.batchNo, null, batch.observeStartedAt ?? batch.startedAt ?? order.updatedAt)
    }
  }
  // 失败目标逐台补事件（成功目标不铺开，避免时间线被淹没）
  for (const target of order.targets) {
    if (target.status === 'failed') {
      pushEvent(order, 'target_status', 'failed', target.batchNo, target.serverId, target.pushedAt ?? order.updatedAt)
    }
  }
  if (order.status === 'paused') {
    pushEvent(order, 'order_status', 'paused', null, null, order.updatedAt)
  }
  if (order.status === 'cancelled') {
    pushEvent(order, 'order_status', 'cancelled', null, null, order.updatedAt)
  }
  if (order.rollbackAt !== null) {
    pushEvent(order, 'order_status', 'rolling_back', null, null, order.rollbackAt)
  }
  if (order.status === 'rolled_back' && order.finishedAt !== null) {
    pushEvent(order, 'order_status', 'rolled_back', null, null, order.finishedAt)
  }
  if (order.status === 'completed' && order.finishedAt !== null) {
    pushEvent(order, 'order_status', 'completed', null, null, order.finishedAt)
  }
}

function fileItems(orderId: number, count: number): ChangeOrderItem[] {
  const rng = createRng(orderId * 31)
  // .jar 为二进制形态样本（file-diff 只回元数据）；database-password.yml 命中敏感规则（无原因 403）
  const paths = [
    'plugins/Essentials.jar',
    'plugins/Essentials/config.yml',
    'plugins/Quests.jar',
    'plugins/Quests/config.yml',
    'plugins/Economy/database-password.yml',
    'plugins/Economy.jar',
    'plugins/OldAddon.jar',
  ]
  const items: ChangeOrderItem[] = []
  for (let i = 0; i < Math.min(count, paths.length); i++) {
    const action: ChangeOrderItem['action'] = i === paths.length - 1 ? 'delete' : rng() < 0.4 ? 'add' : 'update'
    items.push({
      id: orderId * 100 + i,
      kind: 'file_diff',
      path: paths[i],
      action,
      sha256: action === 'delete' ? null : pseudoSha256(`blob:${String(orderId)}:${paths[i]}`),
      sizeBytes: action === 'delete' ? null : 10_000 + Math.floor(rng() * 2_000_000),
      configScopeKind: null,
      configScopeId: null,
      configFromVersionId: null,
      configToVersionId: null,
    })
  }
  return items
}

function configItem(orderId: number, index: number): ChangeOrderItem {
  return {
    id: orderId * 100 + 90 + index,
    kind: 'config_change',
    path: null,
    action: null,
    sha256: null,
    sizeBytes: null,
    configScopeKind: 'zone',
    configScopeId: 30,
    configFromVersionId: 9001,
    configToVersionId: 9002,
  }
}

/** 生成批次 + 目标（按状态推进到指定形态）；missingBackup 时首个完成目标标记无备份（演示回滚残留失败） */
function makeExecution(
  order: OrderState,
  serverIds: string[],
  shape: 'rolling' | 'completed' | 'paused' | 'rolled_back',
  missingBackup = false,
): void {
  const sizes = [Math.max(1, Math.ceil(serverIds.length * 0.1)), Math.max(1, Math.ceil(serverIds.length * 0.3))]
  const batch1End = sizes[0]
  const batch2End = Math.min(serverIds.length, sizes[0] + sizes[1])
  const batchOf = (index: number): number => (index < batch1End ? 1 : index < batch2End ? 2 : 3)
  const batchCount = serverIds.length > batch2End ? 3 : serverIds.length > batch1End ? 2 : 1
  // rolling / paused 的"当前批"：多批时停在第 2 批，单批时即第 1 批
  const currentBatch = shape === 'rolling' || shape === 'paused' ? Math.min(2, batchCount) : 0
  for (let b = 1; b <= batchCount; b++) {
    const members = serverIds.filter((_, index) => batchOf(index) === b)
    const done = shape === 'completed' || shape === 'rolled_back' || b < currentBatch
    const isCurrent = shape === 'rolling' && b === currentBatch
    const isBroken = shape === 'paused' && b === currentBatch
    const status: ChangeBatchStatus = done ? 'completed' : isCurrent ? 'awaiting_confirm' : isBroken ? 'failed' : 'pending'
    const failedCount = isBroken ? Math.max(1, Math.floor(members.length / 2)) : 0
    order.batches.push({
      batchNo: b,
      status,
      plannedCount: members.length,
      successCount: done ? members.length : isCurrent ? members.length : isBroken ? members.length - failedCount : 0,
      failedCount,
      skippedCount: 0,
      startedAt: status === 'pending' ? null : isoOffset(-2 * HOUR + b * 600_000),
      observeStartedAt: done || isCurrent ? isoOffset(-2 * HOUR + b * 600_000 + 300_000) : null,
      finishedAt: done ? isoOffset(-2 * HOUR + b * 600_000 + 500_000) : null,
      gateConfirmedBy: done ? 'ops-chen' : null,
      gateConfirmedAt: done ? isoOffset(-2 * HOUR + b * 600_000 + 520_000) : null,
      breakReason: isBroken ? `批内失败率 ${String(Math.round((failedCount / Math.max(1, members.length)) * 100))}% ≥ 阈值 20%` : null,
    })
    members.forEach((serverId, memberIndex) => {
      const failed = isBroken && memberIndex < failedCount
      const finished = done || isCurrent
      const targetStatus: ChangeTargetStatus = failed ? 'failed' : finished ? 'activated' : 'pending'
      order.targets.push({
        serverId,
        batchNo: b,
        status: targetStatus,
        pushedAt: finished || failed ? isoOffset(-2 * HOUR + b * 600_000 + 60_000) : null,
        activatedAt: finished && !failed ? isoOffset(-2 * HOUR + b * 600_000 + 240_000) : null,
        changedFileCount: finished ? 5 : 0,
        skippedFileCount: finished ? 2 : 0,
        backupPresent: (finished || failed) && !(missingBackup && b === 1 && memberIndex === 0),
        error: failed ? '生效超时：重启后 300 秒内心跳未回归' : null,
        rollbackStatus:
          shape === 'rolled_back' ? (hashString(`rb:${String(order.id)}:${serverId}`) % 29 === 0 ? 'failed' : 'rolled_back') : null,
        rollbackError:
          shape === 'rolled_back' && hashString(`rb:${String(order.id)}:${serverId}`) % 29 === 0
            ? '备份不存在（已被保留策略清理），无法文件回滚'
            : null,
      })
    })
  }
}

function makeOrder(
  state: DeliveryState,
  namespaceId: number,
  title: string,
  status: ChangeOrderStatus,
  options: {
    sourceServerId?: string | null
    serverIds?: string[]
    configOnly?: boolean
    pauseKind?: 'manual' | 'circuit_break' | 'prepare_failed'
    ageDays?: number
    missingBackup?: boolean
  } = {},
): OrderState {
  const id = state.nextId
  state.nextId += 1
  const age = (options.ageDays ?? 1) * DAY
  const executed = status === 'rolling' || status === 'paused' || status === 'completed' || status === 'rolling_back' || status === 'rolled_back'
  const order: OrderState = {
    id,
    namespaceId,
    title,
    description: `${title}（mock 演示数据）`,
    sourceServerId: options.configOnly ? null : (options.sourceServerId ?? 'lobby-1'),
    scanDir: options.configOnly ? '' : 'plugins/',
    status,
    pauseKind: status === 'paused' ? (options.pauseKind ?? 'circuit_break') : null,
    pauseReason: status === 'paused' ? '批内失败率超过阈值，自动熔断' : null,
    batchMode: 'percent',
    batchSizes: [10, 30, 60],
    activationMethod: options.configOnly ? 'hot_reload' : 'restart',
    observeWindowSec: 120,
    activateTimeoutSec: 300,
    failureRateThresholdPercent: 20,
    unhealthyRateThresholdPercent: 30,
    payloadState: executed ? 'ready' : 'pending',
    diffSnapshotAt: options.configOnly ? null : isoOffset(-age - HOUR),
    createdBy: 'ops-chen',
    submittedAt: status === 'draft' ? null : isoOffset(-age + 600_000),
    approvedBy: status === 'draft' || status === 'pending_approval' ? null : 'admin',
    approvedAt: status === 'draft' || status === 'pending_approval' ? null : isoOffset(-age + 1_200_000),
    rejectReason: null,
    startedAt: executed ? isoOffset(-age + 1_800_000) : null,
    finishedAt: status === 'completed' || status === 'rolled_back' ? isoOffset(-age + 4 * HOUR) : null,
    cancelReason: null,
    rollbackBy: status === 'rolled_back' || status === 'rolling_back' ? 'admin' : null,
    rollbackReason: status === 'rolled_back' || status === 'rolling_back' ? '新版本导致刷怪异常，整单回滚' : null,
    rollbackAt: status === 'rolled_back' || status === 'rolling_back' ? isoOffset(-age + 5 * HOUR) : null,
    createdAt: isoOffset(-age),
    updatedAt: isoOffset(-age + 2 * HOUR),
    selector: { ...emptySelector(), zones: [30, 31] },
    items: options.configOnly ? [configItem(id, 0)] : [...fileItems(id, 6), configItem(id, 0)],
    batches: [],
    targetCounts: {},
    rollbackCounts: {},
    targets: [],
    events: [],
    approvalRequestId: null,
  }
  if (executed && options.serverIds && options.serverIds.length > 0) {
    const shape = status === 'rolling' ? 'rolling' : status === 'paused' ? 'paused' : status === 'rolled_back' || status === 'rolling_back' ? 'rolled_back' : 'completed'
    makeExecution(order, options.serverIds, shape, options.missingBackup ?? false)
  }
  seedEvents(order)
  refreshCounts(order)
  state.orders.push(order)
  return order
}

function refreshCounts(order: OrderState): void {
  const counts: Record<string, number> = {}
  const rollbackCounts: Record<string, number> = {}
  for (const target of order.targets) {
    counts[target.status] = (counts[target.status] ?? 0) + 1
    if (target.rollbackStatus !== null) {
      rollbackCounts[target.rollbackStatus] = (rollbackCounts[target.rollbackStatus] ?? 0) + 1
    }
  }
  order.targetCounts = counts
  order.rollbackCounts = rollbackCounts
}

/** 解析 selector 为合格目标集（§4.3.1）：backend + 已分配 + 同 namespace，
 *  all / regions（大区展开为小区）/ zones / servers 取并集后减 excludes，模板源自身自动排除。 */
function resolveSelectorTargets(order: OrderState, cluster: ClusterState): string[] {
  const regionZoneIds = new Set(
    cluster.zones.filter((z) => order.selector.regions.includes(z.regionId)).map((z) => z.id),
  )
  return cluster.servers
    .filter((s) => s.kind === 'backend' && s.zoneId !== null && s.namespaceId === order.namespaceId)
    .filter(
      (s) =>
        order.selector.all ||
        (s.zoneId !== null && (order.selector.zones.includes(s.zoneId) || regionZoneIds.has(s.zoneId))) ||
        order.selector.servers.includes(s.serverId),
    )
    .filter((s) => !order.selector.excludes.includes(s.serverId) && s.serverId !== order.sourceServerId)
    .map((s) => s.serverId)
    .sort()
}

/** 判 config_change 作用域是否覆盖某目标服（mock 以集群归属关系近似控制面语义） */
function scopeCoversServer(cluster: ClusterState, server: ServerRow, kind: string | null, id: number | null): boolean {
  if (kind === null || id === null) {
    return false
  }
  const zone = cluster.zones.find((z) => z.id === server.zoneId)
  switch (kind) {
    case 'namespace':
      return server.namespaceId === id
    case 'bc_cluster':
      return cluster.regions.find((r) => r.id === zone?.regionId)?.bcClusterId === id
    case 'region':
      return zone?.regionId === id
    case 'zone':
      return server.zoneId === id
    case 'server':
      return server.id === id
    default:
      return false
  }
}

/** 影响预览逐目标行的配置命中：本单 config_change 项中作用域覆盖该目标的部分 */
function configScopesFor(
  order: OrderState,
  cluster: ClusterState,
  serverId: string,
): { scopeKind: string; scopeId: number; fromVersionId: number | null; toVersionId: number }[] {
  const server = cluster.servers.find((s) => s.serverId === serverId)
  if (server === undefined) {
    return []
  }
  return order.items
    .filter(
      (item) =>
        item.kind === 'config_change' &&
        item.configToVersionId !== null &&
        scopeCoversServer(cluster, server, item.configScopeKind, item.configScopeId),
    )
    .map((item) => ({
      scopeKind: item.configScopeKind ?? '',
      scopeId: item.configScopeId ?? 0,
      fromVersionId: item.configFromVersionId,
      // 上方过滤已保证非空，此处兜底 0
      toVersionId: item.configToVersionId ?? 0,
    }))
}

/** 批次划分预览（§4.4.1）：percent 逐批向上取整、count 逐批固定台数，均不超过剩余；剩余进末批。 */
function planBatches(order: OrderState, targetTotal: number): { batchNo: number; count: number }[] {
  const result: { batchNo: number; count: number }[] = []
  let remaining = targetTotal
  for (const size of order.batchSizes) {
    if (remaining <= 0) {
      break
    }
    const raw = order.batchMode === 'percent' ? Math.ceil((targetTotal * size) / 100) : size
    const count = Math.min(raw, remaining)
    if (count <= 0) {
      continue
    }
    remaining -= count
    result.push({ batchNo: result.length + 1, count })
  }
  if (remaining > 0) {
    result.push({ batchNo: result.length + 1, count: remaining })
  }
  return result
}

function buildDelivery(scenario: MockScenario): DeliveryState {
  const state: DeliveryState = { orders: [], nextId: 5001 }
  if (scenario === 'empty') {
    return state
  }
  const cluster = getClusterState()
  const backends = cluster.servers
    .filter((s) => s.kind === 'backend' && s.zoneId !== null && s.namespaceId === 1)
    .map((s) => s.serverId)
    .sort()
  const smallSet = backends.slice(0, Math.min(12, backends.length))
  makeOrder(state, 1, '大厅插件升级 v2.4', 'draft', { ageDays: 0 })
  const pendingOrder = makeOrder(state, 1, '经济系统配置调优', 'pending_approval', { configOnly: true, ageDays: 1 })
  pendingOrder.approvalRequestId = 'apr_change_9101'
  makeOrder(state, 1, '反作弊组件热更', 'approved', { ageDays: 2 })
  // rolling 与 paused 各占一段目标，留出未被活动单占用的服（冲突守卫演示 / 新单可启动）
  makeOrder(state, 1, 'Quests 插件灰度 v1.9', 'rolling', { serverIds: smallSet.slice(0, 6), ageDays: 0 })
  makeOrder(state, 1, '全网核心配置基线对齐', 'completed', { configOnly: true, serverIds: smallSet, ageDays: 5 })
  makeOrder(state, 1, 'PVP 平衡性补丁', 'paused', { serverIds: smallSet.slice(6, 7), ageDays: 3 })
  makeOrder(state, 1, '坏更新整单回滚示例', 'rolled_back', { serverIds: smallSet, ageDays: 8 })
  // 首目标缺失备份的完成单：整单回滚会留下残留失败，用于演示「结束回滚」人工收尾路径
  makeOrder(state, 1, '排行榜插件升级 v3.1', 'completed', { serverIds: smallSet.slice(7, 10), ageDays: 6, missingBackup: true })
  if (scenario === 'huge') {
    // 1200 目标的滚动大单 + 批量历史单
    makeOrder(state, 1, '全网 Essentials 大版本升级', 'rolling', { serverIds: backends, ageDays: 0 })
    const statuses: ChangeOrderStatus[] = ['completed', 'completed', 'rolled_back', 'cancelled', 'draft', 'pending_approval']
    for (let i = 0; i < 80; i++) {
      makeOrder(state, 1, `历史变更单 #${String(i + 1).padStart(3, '0')}`, statuses[i % statuses.length], {
        serverIds: statuses[i % statuses.length] === 'completed' || statuses[i % statuses.length] === 'rolled_back' ? smallSet : undefined,
        configOnly: i % 3 === 0,
        ageDays: 10 + i,
      })
    }
  }
  return state
}

const getDeliveryState: () => DeliveryState = defineScenarioStore(buildDelivery)

/** 审批通过后的领域入队投影（approval 域批准后调用）：按票据意图重放领域副作用，不复制审批状态。 */
export function enqueueApprovedChangeOrder(requestId: string): void {
  // 提审票据：本单唯一审批，批准即启动灰度（真机由批准 worker 直接启动，无第二次启动）
  const order = getDeliveryState().orders.find((item) => item.approvalRequestId === requestId)
  if (order !== undefined) {
    applyStartApproved(order)
    return
  }
  const spec = deliveryApprovalSpecs().find((item) => item.requestId === requestId)
  if (spec === undefined) {
    return
  }
  applyApprovedSpec(spec)
}

// 提审批准 → 启动灰度（冲突守卫 + 首批推进到待确认门）
function applyStartApproved(order: OrderState): void {
  if (order.status !== 'pending_approval') {
    return
  }
  const cluster = getClusterState()
  const selected = resolveSelectorTargets(order, cluster)
  if (selected.length === 0) {
    return
  }
  // 冲突守卫：与其他活动单目标集有交集则不启动（派错就停在待审批，不再往下走）
  const activeOrders = getDeliveryState().orders.filter(
    (o) => o.id !== order.id && (o.status === 'rolling' || o.status === 'paused' || o.status === 'rolling_back'),
  )
  for (const active of activeOrders) {
    if (active.targets.some((t) => selected.includes(t.serverId))) {
      return
    }
  }
  order.status = 'rolling'
  order.payloadState = 'ready'
  order.approvedBy = 'admin'
  order.approvedAt = isoOffset(0)
  order.startedAt = isoOffset(0)
  order.updatedAt = isoOffset(0)
  makeExecution(order, selected, 'rolling')
  refreshCounts(order)
  pushEvent(order, 'order_status', 'rolling')
}


function findOrder(info: { params: Record<string, string | readonly string[] | undefined> }): OrderState | undefined {
  const raw = info.params.id
  const id = Number.parseInt(typeof raw === 'string' ? raw : '', 10)
  return getDeliveryState().orders.find((o) => o.id === id)
}

function orderNotFound(): Response {
  return jsonError(404, 'change_order_not_found', '变更单不存在')
}

function illegalState(current: ChangeOrderStatus, action: string): Response {
  return jsonError(409, 'illegal_state', `当前状态 ${current} 不允许 ${action}`)
}

/** 幂等键校验：对齐后端 validIdempotencyKey（非空、长度 ≤64、全为可打印 ASCII 33..126） */
function validIdempotencyKey(key: string): boolean {
  if (key.length === 0 || key.length > 64) {
    return false
  }
  for (const ch of key) {
    const code = ch.codePointAt(0) ?? 0
    if (code < 33 || code > 126) {
      return false
    }
  }
  return true
}

/** 票据影响摘要：与真机同口径的即时读数（目标 / 批次 / 文件与配置载荷计数） */
function impactSummaryOf(order: OrderState): DeliveryImpactSummary {
  const selected =
    order.targets.length > 0
      ? order.targets.map((t) => t.serverId)
      : resolveSelectorTargets(order, getClusterState())
  const planned = order.batches.length > 0
    ? order.batches.map((b) => b.batchNo)
    : planBatches(order, selected.length).map((b) => b.batchNo)
  return {
    targetCount: selected.length,
    batchCount: planned.length,
    payloadFiles: order.items.filter((item) => item.kind === 'file_diff').length,
    payloadConfigs: order.items.filter((item) => item.kind === 'config_change').length,
  }
}

/**
 * 登记一次交付申请并返回 202 票据（真机由交付服务建统一审批申请，mock 经 bridge 交给审批域建行）。
 * 不迁移单据状态：真机的领域副作用由审批 worker 在批准后执行（见 enqueueApprovedChangeOrder）。
 */
function issueTicket(
  order: OrderState,
  spec: Omit<DeliveryApprovalSpec, 'requestId' | 'orderId' | 'namespaceId'>,
): DeliveryApprovalTicket {
  const registered = registerDeliveryApproval({
    ...spec,
    orderId: order.id,
    namespaceId: order.namespaceId,
  })
  return {
    approvalRequestId: registered.requestId,
    status: 'pending',
    operationKey: registered.operationKey,
    orderId: order.id,
    impactSummary: impactSummaryOf(order),
  }
}

/** 旧公开入口（start / approve / reject / withdraw）对齐真机一律 403：审批决定只在 /approvals */
function legacyEntryForbidden(): Response {
  return jsonError(403, 'FORBIDDEN', '该入口已废弃：审批决定只在统一审批中心完成')
}

// 非提审类申请（删除 / 继续 / 批次确认 / 回滚 / 结束回滚）批准后的领域副作用。
// 真机由统一审批 worker 在批准后执行对应领域迁移，此处按登记项重放同一效果。
function applyApprovedSpec(spec: DeliveryApprovalSpec): void {
  const state = getDeliveryState()
  const order = state.orders.find((item) => item.id === spec.orderId)
  if (order === undefined) {
    return
  }
  switch (spec.operationKey) {
    case 'delivery.draft_delete':
      // 删除草稿：批准即真正移除（真机删库行）
      state.orders = state.orders.filter((item) => item.id !== spec.orderId)
      return
    case 'delivery.resume':
      applyResumeApproved(order, spec.resumeMode)
      return
    case 'delivery.confirm_batch':
      applyConfirmApproved(order, spec.batchNo)
      return
    case 'delivery.rollback':
      applyRollbackApproved(order, spec.reason)
      return
    case 'delivery.rollback_finish':
      applyFinishRollbackApproved(order)
      return
    default:
      return
  }
}

// 继续灰度批准：按恢复方式重放（熔断重试 / 跳过失败），回 rolling
function applyResumeApproved(order: OrderState, mode: 'retry_failed' | 'skip_failed' | undefined): void {
  if (order.status !== 'paused') {
    return
  }
  const broken = order.batches.find((b) => b.status === 'failed')
  if (broken && mode === 'retry_failed') {
    broken.status = 'awaiting_confirm'
    broken.successCount = broken.plannedCount
    broken.failedCount = 0
    for (const target of order.targets) {
      if (target.batchNo === broken.batchNo && (target.status === 'failed' || target.status === 'skipped')) {
        target.status = 'activated'
        target.error = null
        target.activatedAt = isoOffset(0)
      }
    }
  } else if (broken && mode === 'skip_failed') {
    broken.status = 'completed'
    broken.finishedAt = isoOffset(0)
  }
  order.status = 'rolling'
  order.pauseKind = null
  order.pauseReason = null
  order.updatedAt = isoOffset(0)
  refreshCounts(order)
  pushEvent(order, 'order_status', 'rolling')
}

// 批次推进批准：末批确认即完成，否则推进下一批到待确认门
function applyConfirmApproved(order: OrderState, batchNo: number | undefined): void {
  if (order.status !== 'rolling' || batchNo === undefined) {
    return
  }
  const batch = order.batches.find((b) => b.batchNo === batchNo)
  if (batch?.status !== 'awaiting_confirm') {
    return
  }
  advanceAfterConfirm(order, batch)
  order.updatedAt = isoOffset(0)
}

// 整单回滚批准：逐目标回滚（无备份目标失败），全部成功即收单
function applyRollbackApproved(order: OrderState, reason: string): void {
  const allowed: ChangeOrderStatus[] = ['completed', 'paused', 'cancelled', 'rolling_back']
  if (!allowed.includes(order.status)) {
    return
  }
  order.status = 'rolling_back'
  order.rollbackBy = 'admin'
  order.rollbackReason = reason
  order.rollbackAt = isoOffset(0)
  order.updatedAt = isoOffset(0)
  for (const target of order.targets) {
    if (target.status === 'pushed' || target.status === 'activated' || target.status === 'failed') {
      // 无备份的目标回滚失败（演示「结束回滚」人工收尾路径），其余成功
      if (target.backupPresent) {
        target.rollbackStatus = 'rolled_back'
        target.rollbackError = null
      } else {
        target.rollbackStatus = 'failed'
        target.rollbackError = '备份不存在（已被保留策略清理），无法文件回滚'
      }
    }
  }
  refreshCounts(order)
  pushEvent(order, 'order_status', 'rolling_back')
  if (order.targets.every((t) => t.rollbackStatus !== 'failed')) {
    order.status = 'rolled_back'
    order.finishedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'rolled_back')
  }
}

// 结束回滚批准：残留失败目标人工收单
function applyFinishRollbackApproved(order: OrderState): void {
  if (order.status !== 'rolling_back') {
    return
  }
  order.status = 'rolled_back'
  order.finishedAt = isoOffset(0)
  order.updatedAt = isoOffset(0)
  pushEvent(order, 'order_status', 'rolled_back')
}

function toSummary(order: OrderState): ChangeOrderSummary {
  const summary: ChangeOrderSummary & { selector?: unknown; items?: unknown; batches?: unknown; targets?: unknown; events?: unknown; targetCounts?: unknown; rollbackCounts?: unknown; approvalRequestId?: unknown } = { ...order }
  // 真机变更单表不落该列、列表/详情都不带：mock 内部保留，响应一律剔除（防演示模式外泄已被证伪的字段）
  delete summary.approvalRequestId
  delete summary.selector
  delete summary.items
  delete summary.batches
  delete summary.targets
  delete summary.events
  delete summary.targetCounts
  delete summary.rollbackCounts
  return summary
}

function toDetail(order: OrderState): ChangeOrderDetail {
  const detail: ChangeOrderDetail & { targets?: unknown; events?: unknown; approvalRequestId?: unknown } = { ...order }
  delete detail.targets
  delete detail.events
  delete detail.approvalRequestId
  return detail
}

/** 末批确认即完成；否则推进下一批到 awaiting_confirm（mock 直接把目标推到 activated） */
function advanceAfterConfirm(order: OrderState, confirmed: ChangeBatch): void {
  confirmed.status = 'completed'
  confirmed.gateConfirmedBy = 'admin'
  confirmed.gateConfirmedAt = isoOffset(0)
  confirmed.finishedAt = isoOffset(0)
  pushEvent(order, 'batch_status', 'completed', confirmed.batchNo)
  const next = order.batches.find((b) => b.status === 'pending')
  if (!next) {
    order.status = 'completed'
    order.finishedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'completed')
    refreshCounts(order)
    return
  }
  next.status = 'awaiting_confirm'
  next.startedAt = isoOffset(0)
  next.observeStartedAt = isoOffset(0)
  next.successCount = next.plannedCount
  pushEvent(order, 'batch_status', 'awaiting_confirm', next.batchNo)
  for (const target of order.targets) {
    if (target.batchNo === next.batchNo && target.status === 'pending') {
      target.status = 'activated'
      target.pushedAt = isoOffset(0)
      target.activatedAt = isoOffset(0)
      target.changedFileCount = 5
      target.skippedFileCount = 2
      target.backupPresent = true
      pushEvent(order, 'target_status', 'activated', next.batchNo, target.serverId)
    }
  }
  refreshCounts(order)
}

interface CreateOrderBody {
  namespaceId?: number
  title?: string
  description?: string
  sourceServerId?: string | null
  scanDir?: string
  selector?: Partial<ChangeSelector>
  batchMode?: 'percent' | 'count'
  batchSizes?: number[]
  activationMethod?: ActivationMethod
  observeWindowSec?: number
  activateTimeoutSec?: number
  failureRateThresholdPercent?: number
  unhealthyRateThresholdPercent?: number
  configChanges?: ConfigChangeInput[]
}

export const deliveryHandlers: HttpHandler[] = [
  // 创建 draft 单
  mockPost('/admin/v2/change-orders', async ({ request }) => {
    const body = await readBody<CreateOrderBody>(request)
    if (typeof body.namespaceId !== 'number' || !body.title) {
      return jsonError(400, 'invalid_param', 'namespaceId / title 必填')
    }
    const state = getDeliveryState()
    const order: OrderState = {
      id: state.nextId,
      namespaceId: body.namespaceId,
      title: body.title,
      description: body.description ?? '',
      sourceServerId: body.sourceServerId ?? null,
      scanDir: body.scanDir ?? '',
      status: 'draft',
      pauseKind: null,
      pauseReason: null,
      batchMode: body.batchMode ?? 'percent',
      batchSizes: body.batchSizes ?? [10, 30, 60],
      activationMethod: body.activationMethod ?? 'restart',
      observeWindowSec: body.observeWindowSec ?? 120,
      activateTimeoutSec: body.activateTimeoutSec ?? 300,
      failureRateThresholdPercent: body.failureRateThresholdPercent ?? 20,
      unhealthyRateThresholdPercent: body.unhealthyRateThresholdPercent ?? 30,
      payloadState: 'pending',
      diffSnapshotAt: null,
      createdBy: 'admin',
      submittedAt: null,
      approvedBy: null,
      approvedAt: null,
      rejectReason: null,
      startedAt: null,
      finishedAt: null,
      cancelReason: null,
      rollbackBy: null,
      rollbackReason: null,
      rollbackAt: null,
      createdAt: isoOffset(0),
      updatedAt: isoOffset(0),
      selector: { ...emptySelector(), ...body.selector },
      items: [],
      batches: [],
      targetCounts: {},
      rollbackCounts: {},
      targets: [],
      events: [],
      approvalRequestId: null,
    }
    state.nextId += 1
    state.orders.unshift(order)
    pushEvent(order, 'order_status', 'draft')
    return HttpResponse.json(toDetail(order), { status: 201 })
  }),

  // 变更单列表
  mockGet('/admin/v2/change-orders', ({ request }) => {
    const url = new URL(request.url)
    const status = queryStr(url, 'status')
    const namespaceId = queryStr(url, 'namespaceId')
    const createdBy = queryStr(url, 'createdBy')
    const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
    const rows = getDeliveryState()
      .orders.filter((order) => {
        if (status !== null && order.status !== status) {
          return false
        }
        if (namespaceId !== null && String(order.namespaceId) !== namespaceId) {
          return false
        }
        if (createdBy !== null && order.createdBy !== createdBy) {
          return false
        }
        if (keyword !== null && !order.title.toLowerCase().includes(keyword)) {
          return false
        }
        return true
      })
      .map(toSummary)
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies ChangeOrderListResponse)
  }),

  // 触发模板源重扫并重算差异：重建 file_diff 项（保留 config_change 项）并返回差异清单
  mockPost('/admin/v2/change-orders/:id/diff-scan', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'draft') {
      return illegalState(order.status, '重扫差异')
    }
    if (order.sourceServerId === null) {
      return jsonError(400, 'missing_source', '未指定黄金模板源，无法扫描文件差异')
    }
    const diffItems = fileItems(order.id, 7)
    order.items = [...diffItems, ...order.items.filter((i) => i.kind === 'config_change')]
    order.diffSnapshotAt = isoOffset(0)
    order.updatedAt = isoOffset(0)
    return HttpResponse.json({ status: 'done', diffSnapshotAt: order.diffSnapshotAt, items: diffItems })
  }),

  // 影响预览（汇总 + 逐目标分页）
  mockGet('/admin/v2/change-orders/:id/impact', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    const url = new URL(info.request.url)
    const cluster = getClusterState()
    const selected =
      order.targets.length > 0 ? order.targets.map((t) => t.serverId) : resolveSelectorTargets(order, cluster)
    const fileItemsOnly = order.items.filter((i) => i.kind === 'file_diff')
    const totalBytes = fileItemsOnly.reduce((sum, i) => sum + (i.sizeBytes ?? 0), 0)
    const rng = createRng(order.id * 13)
    const targetRows = selected.map((serverId) => ({
      serverId,
      online: cluster.servers.find((s) => s.serverId === serverId)?.online ?? false,
      level: rng() < 0.85 ? 'healthy' : 'degraded',
      addCount: fileItemsOnly.filter((i) => i.action === 'add').length,
      updateCount: fileItemsOnly.filter((i) => i.action === 'update').length,
      deleteCount: fileItemsOnly.filter((i) => i.action === 'delete').length,
      skipCount: Math.floor(rng() * 3),
      configScopes: configScopesFor(order, cluster, serverId),
    }))
    const paged = paginate(targetRows, url)
    const response: ChangeImpactResponse = {
      summary: {
        targetTotal: selected.length,
        batches: planBatches(order, selected.length),
        fileTotal: fileItemsOnly.length,
        totalBytes,
        transferBytes: Math.floor(totalBytes * 0.6),
        configScopeCount: order.items.filter((i) => i.kind === 'config_change').length,
        snapshotAt: order.diffSnapshotAt,
      },
      targets: paged,
    }
    return HttpResponse.json(response)
  }),

  // 提交审批（提审原因必填 + 必须携带幂等键）→ 202 票据 + pending_approval
  // mock 对齐后端 RequestSubmit 的守卫与**判定顺序**：先校验 reason 去空白后非空（空即 400），
  // 再查单（404）与状态（409），最后校验 Idempotency-Key（缺 / 非法即 400 INVALID_PARAM）。
  // 顺序必须一致，否则「非 draft 单 + 缺原因」在真机是 400、在演示模式是 409，前端错误分支会被带偏。
  // 注意：真机在建申请前已先把状态冻结为 pending_approval（两步非事务），缺键会把单据卡死；
  // mock 刻意**不**复现这个卡死（键校验置于任何状态变更之前），演示模式不该产生不可恢复的单据。
  // 提审是六类申请里唯一迁移单据状态的（draft → pending_approval），且建的是本单唯一的统一审批申请。
  mockPost('/admin/v2/change-orders/:id/submit', async (info) => {
    const body = await readBody<{ reason?: string }>(info.request)
    if ((body.reason ?? '').trim() === '') {
      return jsonError(400, 'approval_reason_required', '审批原因不能为空')
    }
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'draft') {
      return illegalState(order.status, '提交审批')
    }
    const key = info.request.headers.get('Idempotency-Key') ?? ''
    if (!validIdempotencyKey(key)) {
      return jsonError(400, 'INVALID_PARAM', '缺少或非法的幂等键（Idempotency-Key）')
    }
    const ticket = issueTicket(order, {
      operationKey: 'delivery.approve',
      reason: (body.reason ?? '').trim(),
      safeSummary: `启动变更单 #${String(order.id)}：${order.title}`,
    })
    order.approvalRequestId = ticket.approvalRequestId
    order.status = 'pending_approval'
    order.submittedAt = isoOffset(0)
    order.updatedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'pending_approval')
    return HttpResponse.json(ticket, { status: 202 })
  }),

  // 旧公开入口：撤回 / 审批 / 驳回 / 启动一律 403（对齐真机，审批决定只在 /approvals）
  mockPost('/admin/v2/change-orders/:id/withdraw', () => legacyEntryForbidden()),
  mockPost('/admin/v2/change-orders/:id/approve', () => legacyEntryForbidden()),
  mockPost('/admin/v2/change-orders/:id/reject', () => legacyEntryForbidden()),
  mockPost('/admin/v2/change-orders/:id/start', () => legacyEntryForbidden()),

  // 人工暂停（直执动作：不经审批，200 返回最新详情）
  mockPost('/admin/v2/change-orders/:id/pause', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'rolling') {
      return illegalState(order.status, '暂停')
    }
    order.status = 'paused'
    order.pauseKind = 'manual'
    order.pauseReason = '人工暂停'
    order.updatedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'paused')
    return HttpResponse.json(toDetail(order))
  }),

  // 继续灰度（202 申请票据；熔断 / 准备失败需 mode + reason）
  // 判定顺序对齐真机 RequestResume：404 → 409（状态）→ 400（恢复参数）→ 400（幂等键）
  mockPost('/admin/v2/change-orders/:id/resume', async (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'paused') {
      return illegalState(order.status, '继续')
    }
    const body = await readBody<{ mode?: 'retry_failed' | 'skip_failed'; reason?: string }>(info.request)
    if (order.pauseKind !== 'manual' && (!body.mode || !body.reason)) {
      return jsonError(400, 'missing_reason', '熔断 / 准备失败恢复必须携带 mode 与 reason')
    }
    if (!validIdempotencyKey(info.request.headers.get('Idempotency-Key') ?? '')) {
      return jsonError(400, 'INVALID_PARAM', '缺少或非法的幂等键（Idempotency-Key）')
    }
    const ticket = issueTicket(order, {
      operationKey: 'delivery.resume',
      reason: body.reason ?? '继续灰度',
      safeSummary: `继续变更单 #${String(order.id)}：${order.title}`,
      resumeMode: body.mode,
    })
    return HttpResponse.json(ticket, { status: 202 })
  }),

  // 紧急终止（直执动作：原因必填）
  mockPost('/admin/v2/change-orders/:id/cancel', async (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    const cancellable: ChangeOrderStatus[] = ['draft', 'pending_approval', 'approved', 'rolling', 'paused']
    if (!cancellable.includes(order.status)) {
      return illegalState(order.status, '终止')
    }
    const body = await readBody<{ reason?: string }>(info.request)
    if (!body.reason) {
      return jsonError(400, 'missing_reason', '终止原因必填')
    }
    order.status = 'cancelled'
    order.cancelReason = body.reason
    order.updatedAt = isoOffset(0)
    for (const batch of order.batches) {
      if (batch.status === 'pending') {
        batch.status = 'skipped'
      }
    }
    for (const target of order.targets) {
      if (target.status === 'pending') {
        target.status = 'skipped'
      }
    }
    refreshCounts(order)
    pushEvent(order, 'order_status', 'cancelled')
    return HttpResponse.json(toDetail(order))
  }),

  // 批次推进门放行（202 申请票据；判定顺序对齐真机：404 → 409（单状态）→ 409（批次状态）→ 404（批不存在）→ 400（键））
  mockPost('/admin/v2/change-orders/:id/batches/:batchNo/confirm', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'rolling') {
      return illegalState(order.status, '批次确认')
    }
    const batchNo = Number.parseInt(pathParam(info, 'batchNo'), 10)
    const batch = order.batches.find((b) => b.batchNo === batchNo)
    if (!batch) {
      return jsonError(404, 'batch_not_found', '批次不存在')
    }
    if (batch.status !== 'awaiting_confirm') {
      return jsonError(409, 'illegal_state', `批次状态 ${batch.status} 不在待确认门`)
    }
    if (!validIdempotencyKey(info.request.headers.get('Idempotency-Key') ?? '')) {
      return jsonError(400, 'INVALID_PARAM', '缺少或非法的幂等键（Idempotency-Key）')
    }
    const ticket = issueTicket(order, {
      operationKey: 'delivery.confirm_batch',
      reason: `确认放行第 ${String(batchNo)} 批`,
      safeSummary: `放行变更单 #${String(order.id)} 第 ${String(batchNo)} 批`,
      batchNo,
    })
    return HttpResponse.json(ticket, { status: 202 })
  }),

  // 整单回滚（202 申请票据；原因必填。判定顺序对齐真机：400（原因）→ 404 → 409 → 400（键））
  mockPost('/admin/v2/change-orders/:id/rollback', async (info) => {
    const body = await readBody<{ reason?: string }>(info.request)
    if (!body.reason) {
      return jsonError(400, 'missing_reason', '回滚原因必填')
    }
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    const allowed: ChangeOrderStatus[] = ['completed', 'paused', 'cancelled']
    if (!allowed.includes(order.status)) {
      return illegalState(order.status, '整单回滚')
    }
    if (!validIdempotencyKey(info.request.headers.get('Idempotency-Key') ?? '')) {
      return jsonError(400, 'INVALID_PARAM', '缺少或非法的幂等键（Idempotency-Key）')
    }
    const ticket = issueTicket(order, {
      operationKey: 'delivery.rollback',
      reason: body.reason,
      safeSummary: `整单回滚变更单 #${String(order.id)}：${order.title}`,
    })
    return HttpResponse.json(ticket, { status: 202 })
  }),

  // 残留失败时人工结束回滚（202 申请票据）
  mockPost('/admin/v2/change-orders/:id/rollback/finish', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'rolling_back') {
      return illegalState(order.status, '结束回滚')
    }
    if (!validIdempotencyKey(info.request.headers.get('Idempotency-Key') ?? '')) {
      return jsonError(400, 'INVALID_PARAM', '缺少或非法的幂等键（Idempotency-Key）')
    }
    const ticket = issueTicket(order, {
      operationKey: 'delivery.rollback_finish',
      reason: '结束交付回滚',
      safeSummary: `结束变更单 #${String(order.id)} 的回滚，残留失败目标人工收单`,
    })
    return HttpResponse.json(ticket, { status: 202 })
  }),

  // 目标分页（批次 / 状态过滤）
  mockGet('/admin/v2/change-orders/:id/targets', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    const url = new URL(info.request.url)
    const batch = queryStr(url, 'batch')
    const status = queryStr(url, 'status')
    const serverId = queryStr(url, 'serverId')
    const rows = order.targets.filter((target) => {
      if (batch !== null && String(target.batchNo) !== batch) {
        return false
      }
      if (status !== null && target.status !== status) {
        return false
      }
      if (serverId !== null && !target.serverId.includes(serverId)) {
        return false
      }
      return true
    })
    const { items, total } = paginate(rows, url, { defaultSize: 50 })
    return HttpResponse.json({ items, total } satisfies Paged<ChangeTarget>)
  }),

  // 当前批观察窗数据（健康 / TPS / 告警序列）
  mockGet('/admin/v2/change-orders/:id/observe', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    const current = order.batches.find((b) => b.status === 'observing' || b.status === 'awaiting_confirm' || b.status === 'running')
    if (!current) {
      return HttpResponse.json({ batchNo: null, observeStartedAt: null, targets: [] } satisfies ChangeObserveResponse)
    }
    const members = order.targets.filter((t) => t.batchNo === current.batchNo).slice(0, 50)
    const response: ChangeObserveResponse = {
      batchNo: current.batchNo,
      observeStartedAt: current.observeStartedAt,
      targets: members.map((target) => {
        const rng = createRng(hashString(`observe:${String(order.id)}:${target.serverId}`))
        const base = 78 + Math.floor(rng() * 20)
        const series = []
        for (let i = 0; i < 24; i++) {
          const score = Math.max(20, Math.min(100, base + Math.round(Math.sin(i / 4) * 5 + rng() * 4 - 2)))
          series.push({
            tsMs: Date.parse(current.observeStartedAt ?? isoOffset(0)) + i * 5000,
            score,
            level: score >= 80 ? 'healthy' : score >= 50 ? 'degraded' : 'unhealthy',
            tps: Math.round((19.9 - rng()) * 10) / 10,
            alerts: rng() < 0.05 ? 1 : 0,
          })
        }
        return { serverId: target.serverId, series }
      }),
    }
    return HttpResponse.json(response)
  }),

  // 进度事件：按 Accept 内容协商，与真机同形
  // - text/event-stream → SSE 帧（补发派生快照后关流：演示模式没有后台推进进程，不会有后续实时事件，
  //   客户端按契约在流结束后回退 5s 轮询）；
  // - 否则 → 一次性事件数组（轮询形态）。
  mockGet('/admin/v2/change-orders/:id/events', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (!(info.request.headers.get('Accept') ?? '').includes('text/event-stream')) {
      return HttpResponse.json({ events: order.events })
    }
    const encoder = new TextEncoder()
    const frames = order.events
      .map((event) => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`)
      .join('')
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode(frames))
        controller.close()
      },
    })
    return new HttpResponse(stream, {
      headers: { 'Content-Type': 'text/event-stream; charset=utf-8', 'Cache-Control': 'no-cache' },
    })
  }),

  // 变更项文件内容预览：恒 409（对齐真机——读取源服文件内容必须先走统一审批，放行后才有内容形态）。
  // 不再按 403（敏感路径）/ 504（agent 离线）分流：那两条是审批放行后才可能出现的下游错误。
  mockGet('/admin/v2/change-orders/:id/items/:itemId/file-diff', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    const itemId = Number.parseInt(pathParam(info, 'itemId'), 10)
    const item = order.items.find((i) => i.id === itemId && i.kind === 'file_diff')
    if (item?.path == null || item.action === null) {
      return jsonError(404, 'item_not_found', '文件差异项不存在')
    }
    return jsonError(409, 'operation_requires_approval', '该操作必须先提交审批申请')
  }),

  // 详情（单 + items + 批次概要）
  mockGet('/admin/v2/change-orders/:id', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    return HttpResponse.json(toDetail(order))
  }),

  // 编辑（draft 可编辑；approved 编辑触发回 draft）
  mockPatch('/admin/v2/change-orders/:id', async (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'draft' && order.status !== 'approved') {
      return illegalState(order.status, '编辑')
    }
    const body = await readBody<CreateOrderBody>(info.request)
    if (order.status === 'approved') {
      // approved 后改单 = 审批自动作废回 draft
      order.status = 'draft'
      order.approvedBy = null
      order.approvedAt = null
      pushEvent(order, 'order_status', 'draft')
    }
    if (body.title !== undefined) {
      order.title = body.title
    }
    if (body.description !== undefined) {
      order.description = body.description
    }
    if (body.sourceServerId !== undefined) {
      order.sourceServerId = body.sourceServerId
    }
    if (body.scanDir !== undefined) {
      order.scanDir = body.scanDir
    }
    if (body.selector !== undefined) {
      order.selector = { ...emptySelector(), ...body.selector }
    }
    if (body.batchMode !== undefined) {
      order.batchMode = body.batchMode
    }
    if (body.batchSizes !== undefined) {
      order.batchSizes = body.batchSizes
    }
    if (body.activationMethod !== undefined) {
      order.activationMethod = body.activationMethod
    }
    if (body.configChanges !== undefined) {
      // 整组替换 config_change 项（file_diff 项保留），供组单向导挂接 / 重挂配置版本
      const files = order.items.filter((i) => i.kind === 'file_diff')
      order.items = [
        ...files,
        ...body.configChanges.map((change, index) => ({
          id: order.id * 100 + 90 + index,
          kind: 'config_change' as const,
          path: null,
          action: null,
          sha256: null,
          sizeBytes: null,
          configScopeKind: change.configScopeKind,
          configScopeId: change.configScopeId,
          configFromVersionId: change.configFromVersionId,
          configToVersionId: change.configToVersionId,
        })),
      ]
    }
    order.updatedAt = isoOffset(0)
    return HttpResponse.json(toDetail(order))
  }),

  // 删除 draft 单（走审批）：202 票据，批准后由审批 worker 真正移除
  // 判定顺序对齐真机 RequestDelete：400（原因）→ 404 → 409（状态）→ 400（幂等键）
  mockDelete('/admin/v2/change-orders/:id', async (info) => {
    const body = await readBody<{ reason?: string }>(info.request)
    if ((body.reason ?? '').trim() === '') {
      return jsonError(400, 'approval_reason_required', '审批原因不能为空')
    }
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'draft') {
      return illegalState(order.status, '申请删除')
    }
    if (!validIdempotencyKey(info.request.headers.get('Idempotency-Key') ?? '')) {
      return jsonError(400, 'INVALID_PARAM', '缺少或非法的幂等键（Idempotency-Key）')
    }
    const ticket = issueTicket(order, {
      operationKey: 'delivery.draft_delete',
      reason: (body.reason ?? '').trim(),
      safeSummary: `删除变更单草稿 #${String(order.id)}：${order.title}`,
    })
    return HttpResponse.json(ticket, { status: 202 })
  }),
]
