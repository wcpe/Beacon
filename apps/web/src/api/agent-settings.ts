// Agent 接入与配置数据获取（/agent-settings 页面，FR-279）：
// 按 namespace / server 作用域读取「生效值 + 来源」，批量下发本次改动。
// 契约真源：packages/contracts/src/agent-settings.ts。
// 统一走集群域请求封装（含鉴权注入与 401 处理，FR-179），错误按脱敏 message 抛出（ADR-0057）。

import type {
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

/** 读取某作用域下的配置项全量视图（含未设置项，来源标 default）。 */
export function fetchAgentSettings(scope: AgentSettingsScope): Promise<AgentSettingsResponse> {
  return request('GET', `/admin/v2/agent-settings${buildQuery({ ...scope })}`)
}

/**
 * 批量下发配置项。
 * 返回 200 直执（`{ok:true}`）或 202 审批票据（高危键被拦到审批），
 * 两者都算「已受理」，页面分别给出「已下发」与「已提交审批」两种提示。
 */
export function updateAgentSettings(body: UpdateAgentSettingsBody): Promise<AgentSettingsSaveResult> {
  return request('PUT', '/admin/v2/agent-settings', body)
}

/**
 * 判定下发结果是否为审批票据（202）。
 * 用形状判定而非 HTTP 状态码：统一请求封装只透传响应体，不暴露状态码。
 */
export function isAgentSettingsApprovalTicket(
  result: AgentSettingsSaveResult,
): result is Extract<AgentSettingsSaveResult, { approvalRequestId: string }> {
  return 'approvalRequestId' in result
}
