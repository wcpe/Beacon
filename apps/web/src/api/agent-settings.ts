// Agent 接入与配置数据获取（/agent-settings 页面，FR-279）：
// 按 namespace / server 作用域读取「生效值 + 来源」，批量下发本次改动。
// 契约真源：packages/contracts/src/agent-settings.ts。
// 统一走集群域请求封装（含鉴权注入与 401 处理，FR-179），错误按脱敏 message 抛出（ADR-0057）。

import type {
  AgentSettingsApprovalTicket,
  AgentSettingsResponse,
  AgentSettingsSaveResult,
  UpdateAgentSettingsBody,
} from '@beacon/contracts'

import { ApiClientError, buildQuery, request } from './cluster'

export { ApiClientError }

/** 作用域：namespaceId 必填；serverId 省略表示只看 namespace 默认层 */
export interface AgentSettingsScope {
  namespaceId: number
  serverId?: string
}

/**
 * 读取某作用域下的配置项全量视图：逐项生效值 + 来源 + 服务端高危标记，
 * 以及写端点要回填的二次确认串 `scopeLabel`。
 */
export function fetchAgentSettings(scope: AgentSettingsScope): Promise<AgentSettingsResponse> {
  return request('GET', `/admin/v2/agent-settings${buildQuery({ ...scope })}`)
}

/**
 * 批量下发配置项。
 *
 * 返回 200 直执（`{scope,applied,removed,unchanged}`）或 202 审批票据，
 * 两者都算「已受理」，页面分别给出「已下发」与「已提交审批」两种提示。
 */
export function updateAgentSettings(body: UpdateAgentSettingsBody): Promise<AgentSettingsSaveResult> {
  return request('PUT', '/admin/v2/agent-settings', body)
}

/** 判定「这个值是不是票据」：有非空字符串类型的 approvalRequestId 才算 */
function toApprovalTicket(value: unknown): AgentSettingsApprovalTicket | null {
  if (value === null || typeof value !== 'object') {
    return null
  }
  const candidate = value as Partial<AgentSettingsApprovalTicket>
  if (typeof candidate.approvalRequestId !== 'string' || candidate.approvalRequestId === '') {
    return null
  }
  return {
    approvalRequestId: candidate.approvalRequestId,
    // 状态缺失时留空串，由页面按「未知状态」保守处理（不得当作 pending）
    status: typeof candidate.status === 'string' ? candidate.status : '',
    operationKey: candidate.operationKey,
    secretReturned: candidate.secretReturned,
  }
}

/**
 * 从下发响应里取审批票据，非票据返回 null。
 *
 * 用形状判定而非 HTTP 状态码：统一请求封装只透传响应体，不暴露状态码。判定必须同时认
 * 两种形态——顶层票据（当前服务端 202 直接回票据）与嵌套 `approvalTicket`（服务端若改成
 * 202 返回整个写结果体）。只认前者时，后者的待审批会被误判成「已下发」，那是最坏的一种
 * 谎报：运维以为配置已生效，实际还躺在审批中心等签发。
 */
export function readAgentSettingsApprovalTicket(result: unknown): AgentSettingsApprovalTicket | null {
  const direct = toApprovalTicket(result)
  if (direct !== null) {
    return direct
  }
  if (result === null || typeof result !== 'object') {
    return null
  }
  return toApprovalTicket((result as { approvalTicket?: unknown }).approvalTicket)
}
