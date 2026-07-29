// 统一审批中心 API：列表、详情、批准并执行、拒绝、撤回。
// 页面只调用统一审批端点，批准后由服务端持久 worker 自动执行领域副作用。

import type { ApprovalListResponse, ApprovalRequest, ApprovalQueue, ApprovalStatus } from '@beacon/contracts'

import { buildQuery, request } from './http'

export interface ApprovalListQuery {
  queue?: ApprovalQueue
  status?: ApprovalStatus | 'all'
  operationKey?: string
  riskLevel?: string
  requesterType?: string
  requesterId?: string
  namespaceId?: number
  keyword?: string
  page?: number
  pageSize?: number
}

export function fetchApprovals(query: ApprovalListQuery): Promise<ApprovalListResponse> {
  return request('GET', `/admin/v2/approval-requests${buildQuery(normalizeQuery(query))}`)
}

export function fetchApprovalDetail(requestId: string): Promise<ApprovalRequest> {
  return request('GET', `/admin/v2/approval-requests/${encodeURIComponent(requestId)}`)
}

export function approveApproval(requestId: string, decisionNote: string): Promise<ApprovalRequest> {
  return request('POST', `/admin/v2/approval-requests/${encodeURIComponent(requestId)}/approve`, {
    decision_note: decisionNote,
    decisionNote,
  })
}

export function rejectApproval(requestId: string, reason: string): Promise<ApprovalRequest> {
  return request('POST', `/admin/v2/approval-requests/${encodeURIComponent(requestId)}/reject`, {
    reason,
    decision_note: reason,
    decisionNote: reason,
  })
}

export function withdrawApproval(requestId: string, reason: string): Promise<ApprovalRequest> {
  return request('POST', `/admin/v2/approval-requests/${encodeURIComponent(requestId)}/withdraw`, {
    reason,
    decision_note: reason,
    decisionNote: reason,
  })
}

function normalizeQuery(query: ApprovalListQuery): Record<string, string | number | undefined> {
  return {
    ...query,
    status: query.status === 'all' ? undefined : query.status,
  }
}
