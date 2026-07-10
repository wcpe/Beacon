// 交付编排域 mock（/admin/v2/change-orders 全套 21 个管理面端点）。
// 契约真源：docs/specs/v2-delivery-orchestration.md §5.1；三层状态机 §4.1（非法迁移 409）。
// 说明：`GET /{id}/events` 契约为 SSE，mock 以"一次性事件数组"替代（页面可轮询），已在汇报列明。

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
  FileDiffResponse,
  Paged,
} from '@beacon/contracts'
import { jsonError, mockDelete, mockGet, mockPatch, mockPost, paginate, pathParam, queryStr, readBody } from '../http'
import { getClusterState, type ClusterState } from '../data/cluster'
import type { MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { createRng, hashString, isoOffset, pseudoSha256 } from '../support'

interface OrderState extends ChangeOrderDetail {
  targets: ChangeTarget[]
  events: ChangeOrderEvent[]
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
  const paths = [
    'plugins/Essentials.jar',
    'plugins/Essentials/config.yml',
    'plugins/Quests.jar',
    'plugins/Quests/config.yml',
    'plugins/Lodestone/config.yml',
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

// 大文件截断阈值（字节）：超过即只回前若干行并置 truncated
const FILE_DIFF_TRUNCATE_BYTES = 512_000

/**
 * 生成确定性文件文本内容（仿 server.properties / config.yml 片段），供文件内容预览。
 * before / after 共享结构行（同 seed），仅在 debug 行与构建标记行按 variant 变化，
 * 使 modified 的行级 diff 有真实且最小的差异。
 */
function fileDiffText(path: string, seed: number, variant: 'before' | 'after'): string {
  const rng = createRng(seed)
  const maxPlayers = 20 + Math.floor(rng() * 80)
  const viewDistance = 6 + Math.floor(rng() * 6)
  const mark = pseudoSha256(`file-diff:${path}:${variant}`).slice(0, 8)
  return [
    `# ${path}`,
    'enabled: true',
    `max-players: ${String(maxPlayers)}`,
    `view-distance: ${String(viewDistance)}`,
    'options:',
    '  language: zh_CN',
    `  debug: ${variant === 'after' ? 'true' : 'false'}`,
    `  build-mark: ${mark}`,
  ].join('\n')
}

// 截断大文件预览：仅保留前 4 行并追加提示
function truncateContent(content: string | null): string | null {
  if (content === null) {
    return null
  }
  return `${content.split('\n').slice(0, 4).join('\n')}\n# …（大文件已截断预览）`
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
  makeOrder(state, 1, '经济系统配置调优', 'pending_approval', { configOnly: true, ageDays: 1 })
  makeOrder(state, 1, '反作弊组件热更', 'approved', { ageDays: 2 })
  // rolling 与 paused 各占一段目标，留出未被活动单占用的服（冲突守卫演示 / 新单可启动）
  makeOrder(state, 1, 'Quests 插件灰度 v1.9', 'rolling', { serverIds: smallSet.slice(0, 6), ageDays: 0 })
  makeOrder(state, 1, '全网核心配置基线对齐', 'completed', { configOnly: true, serverIds: smallSet, ageDays: 5 })
  makeOrder(state, 1, 'PVP 平衡性补丁', 'paused', { serverIds: smallSet.slice(6, 9), ageDays: 3 })
  makeOrder(state, 1, '坏更新整单回滚示例', 'rolled_back', { serverIds: smallSet, ageDays: 8 })
  // 首目标缺失备份的完成单：整单回滚会留下残留失败，用于演示「结束回滚」人工收尾路径
  makeOrder(state, 1, '排行榜插件升级 v3.1', 'completed', { serverIds: smallSet.slice(9, 12), ageDays: 6, missingBackup: true })
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

function toSummary(order: OrderState): ChangeOrderSummary {
  const summary: ChangeOrderSummary & { selector?: unknown; items?: unknown; batches?: unknown; targets?: unknown; events?: unknown; targetCounts?: unknown; rollbackCounts?: unknown } = { ...order }
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
  const detail: ChangeOrderDetail & { targets?: unknown; events?: unknown } = { ...order }
  delete detail.targets
  delete detail.events
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

  // 提交审批
  mockPost('/admin/v2/change-orders/:id/submit', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'draft') {
      return illegalState(order.status, '提交审批')
    }
    order.status = 'pending_approval'
    order.submittedAt = isoOffset(0)
    order.updatedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'pending_approval')
    return HttpResponse.json(toDetail(order))
  }),

  // 创建人撤回
  mockPost('/admin/v2/change-orders/:id/withdraw', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'pending_approval' && order.status !== 'approved') {
      return illegalState(order.status, '撤回')
    }
    order.status = 'draft'
    order.updatedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'draft')
    return HttpResponse.json(toDetail(order))
  }),

  // 审批通过
  mockPost('/admin/v2/change-orders/:id/approve', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'pending_approval') {
      return illegalState(order.status, '审批')
    }
    order.status = 'approved'
    order.approvedBy = 'admin'
    order.approvedAt = isoOffset(0)
    order.updatedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'approved')
    return HttpResponse.json(toDetail(order))
  }),

  // 驳回（原因必填）→ draft
  mockPost('/admin/v2/change-orders/:id/reject', async (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'pending_approval') {
      return illegalState(order.status, '驳回')
    }
    const body = await readBody<{ reason?: string }>(info.request)
    if (!body.reason) {
      return jsonError(400, 'missing_reason', '驳回原因必填')
    }
    order.status = 'draft'
    order.rejectReason = body.reason
    order.updatedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'draft')
    return HttpResponse.json(toDetail(order))
  }),

  // 启动（冲突守卫 + payload 准备；mock 直接把首批推进到 awaiting_confirm）
  mockPost('/admin/v2/change-orders/:id/start', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'approved') {
      return illegalState(order.status, '启动')
    }
    const state = getDeliveryState()
    const cluster = getClusterState()
    const selected = resolveSelectorTargets(order, cluster)
    if (selected.length === 0) {
      return jsonError(400, 'no_target', 'selector 未解析出任何合格目标')
    }
    // 冲突守卫：与其他活动单目标集有交集则拒绝启动
    const activeOrders = state.orders.filter(
      (o) => o.id !== order.id && (o.status === 'rolling' || o.status === 'paused' || o.status === 'rolling_back'),
    )
    for (const active of activeOrders) {
      const overlap = active.targets.some((t) => selected.includes(t.serverId))
      if (overlap) {
        return jsonError(409, 'target_conflict', `目标集与活动单 #${String(active.id)} 交叠，拒绝启动`)
      }
    }
    order.status = 'rolling'
    order.payloadState = 'ready'
    order.startedAt = isoOffset(0)
    order.updatedAt = isoOffset(0)
    makeExecution(order, selected, 'rolling')
    refreshCounts(order)
    pushEvent(order, 'order_status', 'rolling')
    return HttpResponse.json(toDetail(order))
  }),

  // 人工暂停
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

  // 继续（熔断暂停需 mode + reason）
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
    const broken = order.batches.find((b) => b.status === 'failed')
    if (broken && body.mode === 'retry_failed') {
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
    } else if (broken && body.mode === 'skip_failed') {
      broken.status = 'completed'
      broken.finishedAt = isoOffset(0)
    }
    order.status = 'rolling'
    order.pauseKind = null
    order.pauseReason = null
    order.updatedAt = isoOffset(0)
    refreshCounts(order)
    pushEvent(order, 'order_status', 'rolling')
    return HttpResponse.json(toDetail(order))
  }),

  // 紧急终止（原因必填）
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

  // 批次推进门放行（末批确认即完成）
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
    advanceAfterConfirm(order, batch)
    order.updatedAt = isoOffset(0)
    return HttpResponse.json(toDetail(order))
  }),

  // 整单回滚（原因必填；重复调用对 failed 目标重试）
  mockPost('/admin/v2/change-orders/:id/rollback', async (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    const allowed: ChangeOrderStatus[] = ['completed', 'paused', 'cancelled', 'rolling_back']
    if (!allowed.includes(order.status)) {
      return illegalState(order.status, '整单回滚')
    }
    const body = await readBody<{ reason?: string }>(info.request)
    if (!body.reason) {
      return jsonError(400, 'missing_reason', '回滚原因必填')
    }
    order.status = 'rolling_back'
    order.rollbackBy = 'admin'
    order.rollbackReason = body.reason
    order.rollbackAt = isoOffset(0)
    order.updatedAt = isoOffset(0)
    for (const target of order.targets) {
      if (target.status === 'pushed' || target.status === 'activated' || target.status === 'failed') {
        // 无备份的目标回滚失败（演示"结束回滚"路径），其余成功
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
    // 全部回滚目标成功 → 直接收单
    if (order.targets.every((t) => t.rollbackStatus !== 'failed')) {
      order.status = 'rolled_back'
      order.finishedAt = isoOffset(0)
      pushEvent(order, 'order_status', 'rolled_back')
    }
    return HttpResponse.json(toDetail(order))
  }),

  // 残留失败时人工结束回滚
  mockPost('/admin/v2/change-orders/:id/rollback/finish', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'rolling_back') {
      return illegalState(order.status, '结束回滚')
    }
    order.status = 'rolled_back'
    order.finishedAt = isoOffset(0)
    order.updatedAt = isoOffset(0)
    pushEvent(order, 'order_status', 'rolled_back')
    return HttpResponse.json(toDetail(order))
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

  // SSE 实时进度的轮询替代：一次性返回事件数组
  mockGet('/admin/v2/change-orders/:id/events', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    return HttpResponse.json({ events: order.events })
  }),

  // 变更项文件内容预览（mock 临时能力，见 FileDiffResponse 注释）：按变更项 id 回文件前后内容
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
    const seed = hashString(`file-diff:${String(order.id)}:${item.path}`)
    const truncated = (item.sizeBytes ?? 0) > FILE_DIFF_TRUNCATE_BYTES
    const beforeRaw = item.action === 'add' ? null : fileDiffText(item.path, seed, 'before')
    const afterRaw = item.action === 'delete' ? null : fileDiffText(item.path, seed, 'after')
    const changeType: FileDiffResponse['changeType'] =
      item.action === 'add' ? 'added' : item.action === 'delete' ? 'removed' : 'modified'
    const response: FileDiffResponse = {
      path: item.path,
      changeType,
      before: truncated ? truncateContent(beforeRaw) : beforeRaw,
      after: truncated ? truncateContent(afterRaw) : afterRaw,
      truncated,
    }
    return HttpResponse.json(response)
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

  // 删除 draft 单
  mockDelete('/admin/v2/change-orders/:id', (info) => {
    const order = findOrder(info)
    if (!order) {
      return orderNotFound()
    }
    if (order.status !== 'draft') {
      return illegalState(order.status, '删除')
    }
    const state = getDeliveryState()
    state.orders = state.orders.filter((o) => o.id !== order.id)
    return new HttpResponse(null, { status: 204 })
  }),
]
