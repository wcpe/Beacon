// 统一审批中心 API：列表、详情、批准并执行、拒绝、撤回。
// 页面只调用统一审批端点，批准后由服务端持久 worker 自动执行领域副作用。

import type { ApprovalListResponse, ApprovalRequest, ApprovalQueue, ApprovalStatus, DemoApprovalRequestInput } from '@beacon/contracts'

import { buildQuery, request } from './http'

export interface ApprovalListQuery {
  queue?: ApprovalQueue
  status?: ApprovalStatus | 'all'
  operationKey?: string
  riskLevel?: string
  requesterType?: string
  requesterId?: string
  namespaceId?: number | 'global'
  keyword?: string
  createdFrom?: string
  createdTo?: string
  expiresFrom?: string
  expiresTo?: string
  page?: number
  pageSize?: number
}

export function fetchApprovals(query: ApprovalListQuery): Promise<ApprovalListResponse> {
  return request('GET', `/admin/v2/approval-requests${buildQuery(normalizeQuery(query))}`)
}

export function fetchApprovalDetail(requestId: string): Promise<ApprovalRequest> {
  return request('GET', `/admin/v2/approval-requests/${encodeURIComponent(requestId)}`)
}

/** 仅演示模式的领域申请入口；生产领域不得调用。 */
export function createDemoApproval(body: DemoApprovalRequestInput): Promise<ApprovalRequest> {
  return request('POST', '/admin/v2/approval-requests', body)
}

/**
 * 创建统一生命周期审批申请。生产页面只提交操作、目标参数和原因，服务端负责冻结影响快照。
 */
export interface LifecycleApprovalRequestBody {
  operationKey: 'namespace.archive' | 'namespace.restore' | 'namespace.permanent_delete' | 'server.archive' | 'server.restore' | 'server.permanent_delete'
  parameters: {
    namespaceId?: number
    serverRowId?: number
    confirmationCode?: string
    confirmationServerId?: string
  }
  reason: string
}

export interface LifecycleApprovalTicket {
  approvalRequestId: string
  status: string
  operationKey: string
}

export function createLifecycleApprovalRequest(body: LifecycleApprovalRequestBody, idempotencyKey: string): Promise<LifecycleApprovalTicket> {
  return request('POST', '/admin/v2/approval-requests', body, { headers: { 'Idempotency-Key': idempotencyKey } })
}

export interface DemoSensitiveAccessResult {
  approvalRequestId: string
  operationKey: string
  targetRef: string
  contentVersionHash: string
  summary: string
}

/** 仅开发 mock 的一次性授权消费；生产领域不得调用或注册该路径。 */
export function consumeDemoSensitiveAccess(requestId: string): Promise<DemoSensitiveAccessResult> {
  return request('POST', `/_demo/approval-requests/${encodeURIComponent(requestId)}/sensitive-access/consume`)
}

export function approveApproval(requestId: string, decisionNote = '批准并执行'): Promise<ApprovalRequest> {
  return request('POST', `/admin/v2/approval-requests/${encodeURIComponent(requestId)}/approve`, {
    decisionNote,
  })
}

export function rejectApproval(requestId: string, reason: string): Promise<ApprovalRequest> {
  const normalizedReason = reason.trim()
  if (normalizedReason === '') {
    return Promise.reject(new Error('拒绝审批必须填写原因'))
  }
  return request('POST', `/admin/v2/approval-requests/${encodeURIComponent(requestId)}/reject`, {
    reason: normalizedReason,
  })
}

export function withdrawApproval(requestId: string, reason?: string): Promise<ApprovalRequest> {
  const normalizedReason = reason?.trim()
  return request(
    'POST',
    `/admin/v2/approval-requests/${encodeURIComponent(requestId)}/withdraw`,
    normalizedReason ? { reason: normalizedReason } : undefined,
  )
}

function normalizeQuery(query: ApprovalListQuery): Record<string, string | number | undefined> {
  return {
    ...query,
    status: query.status === 'all' ? undefined : query.status,
  }
}
