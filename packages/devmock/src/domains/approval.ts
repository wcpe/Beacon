// 统一审批中心 mock：全局审批请求列表、详情、批准并执行、拒绝、撤回。
// 数据只模拟统一审批读模型，不复制任何领域状态真源。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  ApprovalListResponse,
  ApprovalPrincipalType,
  ApprovalRequest,
  ApprovalRiskLevel,
  ApprovalSnapshotLine,
  ApprovalStatus,
  ApprovalTimelineItem,
  DemoApprovalRequestInput,
} from '@beacon/contracts'

import {
  jsonError,
  mockGet,
  mockPost,
  paginate,
  pathParam,
  queryStr,
  queryTimeMs,
  readBody,
  type MockRequestInfo,
} from '../http'
import { defineScenarioStore } from '../store'
import type { MockScenario } from '../scenario'
import { isoOffset, pseudoSha256 } from '../support'
import { enqueueApprovedChangeOrder } from './delivery'

interface ApprovalState {
  rows: ApprovalRequest[]
  executionPolls: Record<string, number>
  demoGrantStatus: Record<string, 'active' | 'consumed'>
}

const HOUR = 3_600_000
const DAY = 24 * HOUR

function line(label: string, value: string): ApprovalSnapshotLine {
  return { label, value }
}

function event(
  type: ApprovalTimelineItem['type'],
  at: string,
  actorType: ApprovalPrincipalType | null,
  actorId: string | null,
  note: string | null = null,
): ApprovalTimelineItem {
  return { type, at, actorType, actorId, note }
}

function makeApproval(input: {
  ordinal: number
  requestId: string
  operationKey: string
  resourceType: string
  resourceId: string
  riskLevel: ApprovalRiskLevel
  status: ApprovalStatus
  requesterType: ApprovalPrincipalType
  requesterId: string
  namespaceId: number | null
  safeSummary: string
  requestReason?: string
  ageMs: number
  resultRef?: string | null
  evidenceStatus?: ApprovalRequest['evidenceStatus']
  driftStatus?: ApprovalRequest['driftStatus']
}): ApprovalRequest {
  const requestedAt = isoOffset(-input.ageMs)
  const updatedAt = input.status === 'pending' ? requestedAt : isoOffset(-Math.max(30 * 60_000, input.ageMs - HOUR))
  const timeline: ApprovalTimelineItem[] = [event('requested', requestedAt, input.requesterType, input.requesterId, '提交危险操作审批')]
  if (input.status !== 'pending') {
    const type = input.status === 'rejected' || input.status === 'withdrawn' || input.status === 'failed' ? input.status : input.status
    timeline.push(event(type, updatedAt, input.status === 'withdrawn' ? input.requesterType : 'human', input.status === 'withdrawn' ? input.requesterId : 'admin', decisionNote(input.status)))
  }
  return {
    id: input.ordinal,
    requestId: input.requestId,
    operationKey: input.operationKey,
    operationKind: input.operationKey,
    resourceType: input.resourceType,
    resourceId: input.resourceId,
    riskLevel: input.riskLevel,
    status: input.status,
    requestReason: input.requestReason ?? '按变更窗口执行，需统一审批留痕',
    safeSummary: input.safeSummary,
    frozenPayloadSha256: pseudoSha256(`${input.requestId}:${input.resourceId}`),
    evidenceStatus: input.evidenceStatus ?? 'available',
    driftStatus: input.driftStatus ?? 'none',
    requesterType: input.requesterType,
    requesterId: input.requesterId,
    requestedBy: input.requesterId,
    deciderType: input.status === 'pending' ? null : input.status === 'withdrawn' ? input.requesterType : 'human',
    deciderId: input.status === 'pending' ? null : input.status === 'withdrawn' ? input.requesterId : 'admin',
    approvedBy: input.status === 'succeeded' || input.status === 'executing' || input.status === 'failed' ? 'admin' : null,
    rejectReason: input.status === 'rejected' ? '风险窗口不匹配，需补充回滚计划' : null,
    decisionReason: input.status === 'pending' ? null : decisionNote(input.status),
    expiresAt: isoOffset(DAY - input.ageMs),
    version: 1,
    namespaceId: input.namespaceId,
    canApprove: input.status === 'pending' && input.requesterType !== 'mcp' && input.requesterType !== 'api_key' && input.requesterType !== 'system',
    canReject: input.status === 'pending' && input.requesterType !== 'mcp' && input.requesterType !== 'api_key' && input.requesterType !== 'system',
    canWithdraw: input.status === 'pending' && input.requesterId === 'admin',
    createdAt: requestedAt,
    updatedAt,
    resultRef: input.resultRef ?? null,
    frozenPayloadSummary: [
      line('目标资源', `${input.resourceType}:${input.resourceId}`),
      line('操作', input.operationKey),
      line('命名空间', input.namespaceId === null ? '全局' : String(input.namespaceId)),
      line('冻结摘要', input.safeSummary),
    ],
    currentFactsSummary: [
      line('当前状态', input.status === 'pending' ? '与冻结快照一致' : '已进入审批后状态'),
      line('漂移检查', input.driftStatus === 'detected' ? '发现资源漂移，审批被阻断' : '未发现阻断漂移'),
    ],
    currentDiff: [
      {
        label: '资源版本',
        snapshot: '冻结版本 v1',
        current: input.driftStatus === 'detected' ? '当前版本 v2' : '当前版本 v1',
        changed: input.driftStatus === 'detected',
      },
    ],
    riskSummary: `${input.riskLevel} 风险操作，需人工确认影响范围`,
    impactSummary: input.safeSummary,
    securitySummary: '敏感参数已脱敏，仅展示安全摘要与哈希',
    timeline,
  }
}

function decisionNote(status: ApprovalStatus): string {
  switch (status) {
    case 'executing':
      return '已批准，等待执行完成'
    case 'succeeded':
      return '执行已受理并完成'
    case 'failed':
      return '执行失败，保留结果引用'
    case 'rejected':
      return '风险过高，拒绝执行'
    case 'withdrawn':
      return '申请人撤回'
    case 'expired':
      return '申请已过期'
    default:
      return '等待审批'
  }
}

function buildApproval(scenario: MockScenario): ApprovalState {
  if (scenario === 'empty') {
    return { rows: [], executionPolls: {}, demoGrantStatus: {} }
  }
  const rows = [
    makeApproval({
      ordinal: 9101,
      requestId: 'apr_change_9101',
      operationKey: 'delivery.change_order.start',
      resourceType: 'change_order',
      resourceId: '5003',
      riskLevel: 'high',
      status: 'pending',
      requesterType: 'human',
      requesterId: 'ops-chen',
      namespaceId: 1,
      safeSummary: '启动反作弊组件热更，目标 12 台 backend',
      ageMs: 2 * HOUR,
    }),
    makeApproval({
      ordinal: 9102,
      requestId: 'apr_server_9102',
      operationKey: 'server.archive',
      resourceType: 'server',
      resourceId: 'backend-37',
      riskLevel: 'critical',
      status: 'pending',
      requesterType: 'mcp',
      requesterId: 'mcp:auto-remediator',
      namespaceId: 1,
      safeSummary: '归档离线 backend-37，退出默认观测范围',
      ageMs: 30 * 60_000,
    }),
    makeApproval({
      ordinal: 9103,
      requestId: 'apr_config_9103',
      operationKey: 'config.publish',
      resourceType: 'config_version',
      resourceId: 'cfg-1842',
      riskLevel: 'medium',
      status: 'executing',
      requesterType: 'human',
      requesterId: 'admin',
      namespaceId: 1,
      safeSummary: '发布经济系统配置版本 cfg-1842',
      ageMs: 5 * HOUR,
      resultRef: '/configs?version=cfg-1842',
    }),
    makeApproval({
      ordinal: 9104,
      requestId: 'apr_namespace_9104',
      operationKey: 'namespace.restore',
      resourceType: 'namespace',
      resourceId: 'test',
      riskLevel: 'high',
      status: 'succeeded',
      requesterType: 'human',
      requesterId: 'ops-li',
      namespaceId: 2,
      safeSummary: '恢复测试命名空间只读归档状态',
      ageMs: 2 * DAY,
      resultRef: '/namespaces',
    }),
    makeApproval({
      ordinal: 9105,
      requestId: 'apr_upgrade_9105',
      operationKey: 'system.upgrade',
      resourceType: 'control_plane',
      resourceId: 'release-0.21.7',
      riskLevel: 'critical',
      status: 'failed',
      requesterType: 'api_key',
      requesterId: 'api-key:upgrade-bot',
      namespaceId: null,
      safeSummary: '控制面升级到 release-0.21.7',
      ageMs: 3 * DAY,
      resultRef: '/system/version',
    }),
    makeApproval({
      ordinal: 9106,
      requestId: 'apr_drift_9106',
      operationKey: 'server.archive',
      resourceType: 'server',
      resourceId: 'backend-drift',
      riskLevel: 'high',
      status: 'pending',
      requesterType: 'human',
      requesterId: 'ops-drift',
      namespaceId: 1,
      safeSummary: '归档已发生资源变化的 backend-drift',
      ageMs: HOUR,
      driftStatus: 'detected',
    }),
    makeApproval({
      ordinal: 9107,
      requestId: 'apr_evidence_9107',
      operationKey: 'config.publish',
      resourceType: 'config_version',
      resourceId: 'cfg-evidence-expired',
      riskLevel: 'critical',
      status: 'pending',
      requesterType: 'human',
      requesterId: 'ops-evidence',
      namespaceId: 2,
      safeSummary: '证据已过期的配置发布申请',
      ageMs: 2 * DAY,
      evidenceStatus: 'expired',
    }),
    makeApproval({
      ordinal: 9108,
      requestId: 'apr_withdraw_9108',
      operationKey: 'identity.enable',
      resourceType: 'agent_identity',
      resourceId: 'identity-disabled-8',
      riskLevel: 'high',
      status: 'pending',
      requesterType: 'human',
      requesterId: 'admin',
      namespaceId: 1,
      safeSummary: '重新启用已禁用身份 identity-disabled-8',
      ageMs: 45 * 60_000,
    }),
  ]
  if (scenario === 'huge') {
    const statuses: ApprovalStatus[] = ['pending', 'executing', 'succeeded', 'failed']
    for (let i = 0; i < 1000; i += 1) {
      rows.push(
        makeApproval({
          ordinal: 9200 + i,
          requestId: `apr_bulk_${String(i + 1).padStart(3, '0')}`,
          operationKey: i % 2 === 0 ? 'delivery.change_order.start' : 'server.restore',
          resourceType: i % 2 === 0 ? 'change_order' : 'server',
          resourceId: i % 2 === 0 ? String(6000 + i) : `backend-${String(i + 1)}`,
          riskLevel: i % 3 === 0 ? 'critical' : 'high',
          status: statuses[i % statuses.length],
          requesterType: i % 5 === 0 ? 'mcp' : 'human',
          requesterId: i % 5 === 0 ? 'mcp:batch' : 'ops-chen',
          namespaceId: i % 4 === 0 ? 2 : 1,
          safeSummary: `批量审批演示 #${String(i + 1).padStart(3, '0')}`,
          ageMs: (i + 1) * HOUR,
        }),
      )
    }
  }
  return { rows, executionPolls: {}, demoGrantStatus: {} }
}

const getApprovalState: () => ApprovalState = defineScenarioStore(buildApproval)

function advanceExecution(row: ApprovalRequest, state: ApprovalState): void {
  if (row.status !== 'executing') {
    return
  }
  const polls = (state.executionPolls[row.requestId] ?? 0) + 1
  state.executionPolls[row.requestId] = polls
  if (polls >= 2) {
    touch(row, 'succeeded', '执行已受理并完成', 'human', 'admin')
    projectDemoGrant(row, state)
  }
}

function projectDemoGrant(row: ApprovalRequest, state: ApprovalState): void {
  if (row.status !== 'succeeded' || !isSensitiveDemoOperation(row.operationKey)) {
    return
  }
  state.demoGrantStatus[row.requestId] ??= 'active'
}

function isSensitiveDemoOperation(operationKey: string): boolean {
  return operationKey === 'agent.command.fs_browse' || operationKey === 'message.payload.read'
}

function getApproval(info: MockRequestInfo): ApprovalRequest | undefined {
  const requestId = pathParam(info, 'requestId') || pathParam(info, 'id')
  const state = getApprovalState()
  const row = state.rows.find((item) => item.requestId === requestId || String(item.id) === requestId)
  if (row) {
    advanceExecution(row, state)
  }
  return row
}

function notFound(): Response {
  return jsonError(404, 'approval_not_found', '审批申请不存在')
}

function isMachineRequest(request: Request): boolean {
  const token = request.headers.get('Authorization')?.toLowerCase() ?? ''
  return token.includes('mcp') || token.includes('api-key') || token.includes('system')
}

function touch(
  row: ApprovalRequest,
  status: Exclude<ApprovalStatus, 'pending'>,
  note: string,
  actor: ApprovalPrincipalType,
  actorId: string,
): void {
  row.status = status
  row.decisionReason = note
  row.deciderType = actor
  row.deciderId = actorId
  row.approvedBy = status === 'executing' || status === 'succeeded' ? actorId : row.approvedBy
  row.rejectReason = status === 'rejected' ? note : row.rejectReason
  row.updatedAt = isoOffset(0)
  row.version += 1
  row.timeline = [...(row.timeline ?? []), event(status, row.updatedAt, actor, actorId, note)]
}

function listApprovals(request: Request): Response {
  const url = new URL(request.url)
  const status = queryStr(url, 'status')
  const queue = queryStr(url, 'queue')
  const operationKey = queryStr(url, 'operationKey')
  const riskLevel = queryStr(url, 'riskLevel')
  const requesterType = queryStr(url, 'requesterType')
  const requesterId = queryStr(url, 'requesterId')
  const namespaceId = queryStr(url, 'namespaceId')
  const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
  const createdFrom = queryTimeMs(url, 'createdFrom')
  const createdTo = queryTimeMs(url, 'createdTo')
  const expiresFrom = queryTimeMs(url, 'expiresFrom')
  const expiresTo = queryTimeMs(url, 'expiresTo')
  const rows = getApprovalState().rows.filter((row) => {
    if (status !== null && row.status !== status) return false
    if (operationKey !== null && row.operationKey !== operationKey) return false
    if (riskLevel !== null && row.riskLevel !== riskLevel) return false
    if (requesterType !== null && row.requesterType !== requesterType) return false
    if (requesterId !== null && row.requesterId !== requesterId) return false
    if (namespaceId !== null && String(row.namespaceId ?? '') !== namespaceId) return false
    if (queue === 'mine_todo' && row.status !== 'pending') return false
    if (queue === 'mine_requested' && row.requesterId !== 'admin') return false
    if (keyword !== null && !`${row.safeSummary} ${row.operationKey} ${row.resourceId}`.toLowerCase().includes(keyword)) return false
    const createdAt = Date.parse(row.createdAt ?? '')
    const expiresAt = Date.parse(row.expiresAt ?? '')
    if (createdFrom !== null && (Number.isNaN(createdAt) || createdAt < createdFrom)) return false
    if (createdTo !== null && (Number.isNaN(createdAt) || createdAt > createdTo)) return false
    if (expiresFrom !== null && (Number.isNaN(expiresAt) || expiresAt < expiresFrom)) return false
    if (expiresTo !== null && (Number.isNaN(expiresAt) || expiresAt > expiresTo)) return false
    return true
  }).sort((left, right) => {
    if (left.status === 'pending' && right.status !== 'pending') return -1
    if (left.status !== 'pending' && right.status === 'pending') return 1
    if (left.status === 'pending' && right.status === 'pending') {
      return Date.parse(left.expiresAt ?? '') - Date.parse(right.expiresAt ?? '')
    }
    return Date.parse(right.createdAt ?? '') - Date.parse(left.createdAt ?? '')
  })
  const { items, total } = paginate(rows, url)
  return HttpResponse.json({ items, total } satisfies ApprovalListResponse)
}

function detailApproval(info: MockRequestInfo): Response {
  const row = getApproval(info)
  return row ? HttpResponse.json(row) : notFound()
}

async function approve(info: MockRequestInfo): Promise<Response> {
  if (isMachineRequest(info.request)) {
    return jsonError(403, 'approval_machine_forbidden', '机器主体不能批准或拒绝审批')
  }
  const row = getApproval(info)
  if (!row) return notFound()
  if (row.status !== 'pending') return jsonError(409, 'approval_state_changed', '审批状态已变化')
  if (row.driftStatus === 'detected' || row.evidenceStatus !== 'available' || (row.expiresAt !== null && Date.parse(row.expiresAt) <= Date.now())) {
    return jsonError(409, 'approval_not_eligible', '审批证据不可用、已漂移或已过期')
  }
  const body = await readBody<{ decision_note?: string; decisionNote?: string }>(info.request)
  touch(row, 'executing', body.decision_note ?? body.decisionNote ?? '批准并执行', 'human', 'admin')
  enqueueApprovedChangeOrder(row.requestId)
  return HttpResponse.json(row, { status: 202 })
}

async function reject(info: MockRequestInfo): Promise<Response> {
  if (isMachineRequest(info.request)) {
    return jsonError(403, 'approval_machine_forbidden', '机器主体不能批准或拒绝审批')
  }
  const row = getApproval(info)
  if (!row) return notFound()
  if (row.status !== 'pending') return jsonError(409, 'approval_state_changed', '审批状态已变化')
  const body = await readBody<{ reason?: string; decision_note?: string; decisionNote?: string }>(info.request)
  const reason = body.reason ?? body.decision_note ?? body.decisionNote ?? ''
  if (reason.trim() === '') return jsonError(400, 'missing_reason', '拒绝审批必须填写原因')
  touch(row, 'rejected', reason, 'human', 'admin')
  return HttpResponse.json(row)
}

async function withdraw(info: MockRequestInfo): Promise<Response> {
  const row = getApproval(info)
  if (!row) return notFound()
  if (row.status !== 'pending') return jsonError(409, 'approval_state_changed', '审批状态已变化')
  const principal = info.request.headers.get('X-Principal-Id')
  if (principal !== null && principal !== row.requesterId) {
    return jsonError(403, 'approval_withdraw_forbidden', '只能撤回自己的审批申请')
  }
  const body = await readBody<{ reason?: string; decision_note?: string; decisionNote?: string }>(info.request)
  touch(row, 'withdrawn', body.reason ?? body.decision_note ?? body.decisionNote ?? '申请人撤回', row.requesterType, row.requesterId)
  return HttpResponse.json(row)
}

async function createDemoApproval(info: MockRequestInfo): Promise<Response> {
  const body = await readBody<DemoApprovalRequestInput>(info.request)
  const allowed = new Set<DemoApprovalRequestInput['operationKey']>(['agent.command.resync', 'agent.command.tail_logs', 'agent.command.fs_browse', 'config.sensitive_plaintext_read', 'message.payload.read'])
  const { operationKey, resourceType, resourceId, requestReason, safeSummary } = body
  if (!operationKey || !resourceType || !resourceId || !requestReason || !safeSummary || !allowed.has(operationKey) || resourceId.trim() === '' || requestReason.trim() === '' || safeSummary.trim() === '') {
    return jsonError(400, 'invalid_demo_approval', '演示审批申请参数无效')
  }
  return createApproval(operationKey, resourceType, resourceId, requestReason, safeSummary)
}

async function createMessagePayloadApproval(info: MockRequestInfo): Promise<Response> {
  const body = await readBody<{ reason?: string }>(info.request)
  const messageId = pathParam(info, 'messageId')
  const reason = body.reason?.trim()
  if (!messageId || !reason) {
    return jsonError(400, 'invalid_approval_request', '消息 payload 审批申请参数无效')
  }
  return createApproval('message.payload.read', 'message', messageId, reason, '申请查看消息 payload；正文不进入审批记录')
}

function createApproval(
  operationKey: DemoApprovalRequestInput['operationKey'],
  resourceType: string,
  resourceId: string,
  requestReason: string,
  safeSummary: string,
): Response {
  const state = getApprovalState()
  const ordinal = 10_000 + state.rows.length
  const requestId = `apr_demo_${String(ordinal)}`
  const row = makeApproval({ ordinal, requestId, operationKey, resourceType, resourceId, requestReason, riskLevel: 'high', status: 'pending', requesterType: 'human', requesterId: 'admin', namespaceId: null, safeSummary, ageMs: 0 })
  state.rows.unshift(row)
  return HttpResponse.json(row, { status: 202 })
}

function consumeDemoGrant(info: MockRequestInfo): Response {
  const row = getApproval(info)
  if (!row) return notFound()
  if (row.status !== 'succeeded') {
    return jsonError(409, 'demo_grant_not_active', '审批尚未执行成功，演示授权不可消费')
  }
  if (!isSensitiveDemoOperation(row.operationKey)) {
    return jsonError(409, 'demo_grant_not_available', '该审批操作不提供敏感内容演示授权')
  }
  const state = getApprovalState()
  projectDemoGrant(row, state)
  if (state.demoGrantStatus[row.requestId] === 'consumed') {
    return jsonError(409, 'demo_grant_consumed', '演示授权已消费，请重新提交审批申请')
  }
  if (state.demoGrantStatus[row.requestId] !== 'active') {
    return jsonError(409, 'demo_grant_not_active', '演示授权尚未生成')
  }
  state.demoGrantStatus[row.requestId] = 'consumed'
  return HttpResponse.json({
    approvalRequestId: row.requestId,
    operationKey: row.operationKey,
    targetRef: `${row.resourceType}:${row.resourceId}`,
    contentVersionHash: row.frozenPayloadSha256,
    summary: '演示授权已一次性消费；Mock 不保存或展示敏感正文。',
  })
}

export const approvalHandlers: HttpHandler[] = [
  mockPost('/admin/v2/approval-requests', createDemoApproval),
  mockPost('/admin/v2/messages/:messageId/payload/approval-requests', createMessagePayloadApproval),
  mockPost('/_demo/approval-requests/:requestId/sensitive-access/consume', consumeDemoGrant),
  mockGet('/admin/v2/approval-requests', ({ request }) => listApprovals(request)),
  mockGet('/admin/v2/approvals', ({ request }) => listApprovals(request)),
  mockGet('/admin/v2/approval-requests/:requestId', detailApproval),
  mockGet('/admin/v2/approvals/:id', detailApproval),
  mockPost('/admin/v2/approval-requests/:requestId/approve', approve),
  mockPost('/admin/v2/approvals/:id/approve', approve),
  mockPost('/admin/v2/approval-requests/:requestId/reject', reject),
  mockPost('/admin/v2/approvals/:id/reject', reject),
  mockPost('/admin/v2/approval-requests/:requestId/withdraw', withdraw),
  mockPost('/admin/v2/approvals/:id/withdraw', withdraw),
]
