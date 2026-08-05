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

export type ApprovalEvidenceStatus = 'available' | 'unavailable' | 'expired'
export type ApprovalDriftStatus = 'none' | 'detected'

export interface ApprovalDiffLine {
  label: string
  snapshot: string
  current: string
  changed: boolean
}

/** 成功敏感内容审批仅向原申请主体返回的一次性消费授权引用。 */
export interface SensitiveAccessGrantReference {
  grantId: string
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
  evidenceStatus?: ApprovalEvidenceStatus
  driftStatus?: ApprovalDriftStatus
  frozenPayloadSummary?: ApprovalSnapshotLine[]
  currentFactsSummary?: ApprovalSnapshotLine[]
  currentDiff?: ApprovalDiffLine[]
  riskSummary?: string
  impactSummary?: string
  securitySummary?: string
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
  canApprove?: boolean
  canReject?: boolean
  canWithdraw?: boolean
  createdAt?: string
  updatedAt?: string
  resultRef?: string | null
  sensitiveAccessGrant?: SensitiveAccessGrantReference
  timeline?: ApprovalTimelineItem[]
}

export type ApprovalListResponse = Paged<ApprovalRequest>

/** 仅演示模式的领域入口申请载荷；生产仍由领域 adapter 规范化。 */
export interface DemoApprovalRequestInput {
  operationKey: 'agent.command.resync' | 'agent.command.tail_logs' | 'agent.command.fs_browse' | 'config.sensitive_plaintext_read' | 'message.payload.read'
  resourceType: string
  resourceId: string
  requestReason: string
  safeSummary: string
}
