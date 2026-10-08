// 变更单域数据获取（/changes 与 /changes/history 复用）：
// 覆盖 /admin/v2/change-orders* 全部端点（列表 / 创建 / 详情 / 编辑 / 删除申请 /
// 差异重扫 / 影响预览 / 生命周期迁移 / 批次确认申请 / 整单回滚申请 / 目标 / 观察窗 / 事件）。
// 审批决定（批准 / 拒绝 / 撤回）不在本域：只在统一审批中心 /approvals。

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
  DeliveryApprovalTicket,
  FileDiffResponse,
  Paged,
} from '@beacon/contracts'

import { ApiClientError, buildQuery, request } from './request'
import { clearAuth, currentToken, notifyUnauthorized } from '../state/auth'

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
  DeliveryApprovalTicket,
  DeliveryImpactSummary,
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

// ---- 申请审批（202 票据）----

// 票据形（DeliveryApprovalTicket，见 @beacon/contracts 与后端 DeliveryApprovalTicketView）：
// 六类申请动作（submit / delete / resume / batch confirm / rollback / rollback-finish）
// 一律 202 返回票据，变更单状态不在此刻迁移，审批决定只在 /approvals 完成。
//
// 申请动作共同契约：reason 必填 + 必须携带 Idempotency-Key。
// - reason：后端各 Request* 校验非空（缺则 400 approval_reason_required）；
// - 幂等键：创建审批申请时校验（缺则 400 INVALID_PARAM，见 approval_service.go 的 validIdempotencyKey），
//   且「先冻结状态、后建申请」两步非事务——submit 缺键会把单据卡死在 pending_approval。
// 键由调用方用 randomId() 生成后传入（与 createApiKey / createMcpClient 同一先例），
// 同一「申请意图」重试必须复用同一键（后端据此去重），弹窗关闭即作废重来。

export function submitChangeOrder(
  id: number,
  reason: string,
  idempotencyKey: string,
): Promise<DeliveryApprovalTicket> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/submit`, { reason }, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

/**
 * 判响应是 202 审批票据还是 200 变更单详情。
 * 同一详情页把申请动作（票据）与直执动作（暂停 / 终止，详情）放在一个 mutation 里，
 * 收到响应后据此分流：票据只报申请号，详情可继续读状态。
 */
export function isApprovalTicket(
  value: ChangeOrderDetail | DeliveryApprovalTicket,
): value is DeliveryApprovalTicket {
  return 'impactSummary' in value && 'approvalRequestId' in value
}

export function deleteChangeOrder(
  id: number,
  reason: string,
  idempotencyKey: string,
): Promise<DeliveryApprovalTicket> {
  // 删除草稿也走审批（后端 RequestDelete）：202 票据，不在此刻删除
  return request('DELETE', `/admin/v2/change-orders/${String(id)}`, { reason }, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

export function pauseChangeOrder(id: number): Promise<ChangeOrderDetail> {
  // 暂停 / 终止是直执动作（不经审批），仍 200 返回最新详情
  return request('POST', `/admin/v2/change-orders/${String(id)}/pause`)
}

export interface ResumeBody {
  mode?: 'retry_failed' | 'skip_failed'
  reason?: string
}

export function resumeChangeOrder(
  id: number,
  body: ResumeBody,
  idempotencyKey: string,
): Promise<DeliveryApprovalTicket> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/resume`, body, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

export function cancelChangeOrder(id: number, reason: string): Promise<ChangeOrderDetail> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/cancel`, { reason })
}

// ---- 批次推进确认（走审批）----

export function confirmChangeBatch(
  id: number,
  batchNo: number,
  idempotencyKey: string,
): Promise<DeliveryApprovalTicket> {
  return request(
    'POST',
    `/admin/v2/change-orders/${String(id)}/batches/${String(batchNo)}/confirm`,
    undefined,
    { headers: { 'Idempotency-Key': idempotencyKey } },
  )
}

// ---- 整单回滚（/changes 与历史页共用；均走审批）----

export function rollbackChangeOrder(
  id: number,
  reason: string,
  idempotencyKey: string,
): Promise<DeliveryApprovalTicket> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/rollback`, { reason }, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

export function finishRollbackChangeOrder(
  id: number,
  idempotencyKey: string,
): Promise<DeliveryApprovalTicket> {
  return request('POST', `/admin/v2/change-orders/${String(id)}/rollback/finish`, undefined, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
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

// ---- 进度事件：轮询快照 + SSE 实时推送 ----

export function fetchChangeEvents(id: number): Promise<ChangeEventsResponse> {
  return request('GET', `/admin/v2/change-orders/${String(id)}/events`)
}

/** SSE 订阅回调：onOpen 收到响应头、onEvent 每帧事件、onClose 失败或流结束（调用方据此回退轮询） */
export interface ChangeEventStreamHandlers {
  onOpen?: () => void
  onEvent: (event: ChangeOrderEvent) => void
  onClose?: (error: unknown) => void
}

/**
 * 订阅变更单进度事件流（GET .../events + Accept: text/event-stream）。
 *
 * 为什么不用 EventSource：后端鉴权只认 Authorization 头（见 adminAuthMiddleware），
 * 而 EventSource 无法自定义请求头；故用 fetch + ReadableStream 手工读帧。
 * 返回值为退订函数（中止流并释放连接）。
 */
export function subscribeChangeEvents(id: number, handlers: ChangeEventStreamHandlers): () => void {
  const controller = new AbortController()
  void readChangeEventStream(id, handlers, controller.signal)
  return () => {
    controller.abort()
  }
}

// 读流：逐帧切分（空行分隔），保活注释行（":" 前缀）与非法帧直接丢弃。
async function readChangeEventStream(
  id: number,
  handlers: ChangeEventStreamHandlers,
  signal: AbortSignal,
): Promise<void> {
  try {
    const headers: Record<string, string> = { Accept: 'text/event-stream' }
    const token = currentToken()
    if (token !== '') {
      headers.Authorization = `Bearer ${token}`
    }
    const response = await fetch(`/admin/v2/change-orders/${String(id)}/events`, { headers, signal })
    if (response.status === 401) {
      // 与统一请求层同口径：令牌失效即清登录态并通知跳登录
      clearAuth()
      notifyUnauthorized()
    }
    if (!response.ok || response.body === null) {
      throw new ApiClientError(
        response.status,
        'event_stream_unavailable',
        `进度事件流不可用（HTTP ${String(response.status)}）`,
      )
    }
    handlers.onOpen?.()
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    for (;;) {
      const chunk = await reader.read()
      if (chunk.done) {
        break
      }
      buffer += decoder.decode(chunk.value, { stream: true })
      const frames = buffer.split('\n\n')
      // 末段可能是不完整帧，留待下一块拼接
      buffer = frames.pop() ?? ''
      for (const frame of frames) {
        const event = parseSseFrame(frame)
        if (event !== null) {
          handlers.onEvent(event)
        }
      }
    }
    handlers.onClose?.(null)
  } catch (error) {
    // 调用方主动退订（卸载）不算断线，不通知回退
    if (signal.aborted) {
      return
    }
    handlers.onClose?.(error)
  }
}

/** 解析一帧 SSE 的 data 负载为进度事件；保活注释 / 空负载 / 非事件 JSON 返回 null。 */
export function parseSseFrame(frame: string): ChangeOrderEvent | null {
  const payload = frame
    .split('\n')
    .filter((line) => line.startsWith('data:'))
    .map((line) => line.slice('data:'.length).trim())
    .join('')
  if (payload === '') {
    return null
  }
  try {
    return JSON.parse(payload) as ChangeOrderEvent
  } catch {
    return null
  }
}
