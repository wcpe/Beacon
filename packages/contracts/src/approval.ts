// 统一审批中心响应契约（/admin/v2/approval-requests*）。
// 真源：docs/specs/approval-center.md 与后端 approval_handler.go；前端只消费统一审批读模型。

import type { Paged } from './common'

export type ApprovalStatus = 'pending' | 'executing' | 'succeeded' | 'failed' | 'rejected' | 'withdrawn' | 'expired'
export type ApprovalRiskLevel = 'low' | 'medium' | 'high' | 'critical'
export type ApprovalPrincipalType = 'human' | 'api_key' | 'mcp' | 'system'
export type ApprovalQueue = 'mine_todo' | 'mine_requested' | 'all'

export interface ApprovalTimelineItem {
  at: string
  type: 'requested' | 'approved' | 'rejected' | 'withdrawn' | 'executing' | 'succeeded' | 'failed' | 'expired'
  actorType: ApprovalPrincipalType | null
  actorId: string | null
  note: string | null
}

export interface ApprovalSnapshotLine {
  label: string
  value: string
}

export interface ApprovalRequest {
  id: number | string
  requestId: string
  operationKey: string
  operationKind: string
  resourceType: string
  resourceId: string
  riskLevel: ApprovalRiskLevel
  status: ApprovalStatus
  requestReason: string
  safeSummary: string
  frozenPayloadSha256: string
  requesterType: ApprovalPrincipalType
  requesterId: string
  requestedBy?: string
  deciderType: ApprovalPrincipalType | null
  deciderId: string | null
  approvedBy?: string | null
  rejectReason: string | null
  decisionReason: string | null
  expiresAt: string | null
  version: number
  namespaceId?: number | null
  createdAt?: string
  updatedAt?: string
  resultRef?: string | null
  frozenPayloadSummary?: ApprovalSnapshotLine[]
  currentFactsSummary?: ApprovalSnapshotLine[]
  timeline?: ApprovalTimelineItem[]
}

export type ApprovalListResponse = Paged<ApprovalRequest>
