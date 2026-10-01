// MCP 管理面数据获取（/mcp-clients 页面与工具调用流水）：
// 客户端生命周期（列表 / 详情 / 申请创建 / 申请轮换 / 申请启用 / 立即吊销）、
// MCP 入口部署配置的只读视图，以及工具调用流水（列表 / 详情，FR-240）。
//
// 契约真源：docs/specs/built-in-admin-v2-mcp-and-oauth.md §3.2 / §4；
//          docs/specs/mcp-invocation-audit.md §3.7。
// 统一走集群域请求封装（含鉴权注入与 401 处理，FR-179），错误按脱敏 message 抛出（ADR-0057）。

import type {
  CreateMCPClientBody,
  MCPClientApprovalTicket,
  MCPClientItem,
  MCPClientListResponse,
  MCPConfigView,
  MCPInvocationItem,
  MCPInvocationListResponse,
  MCPInvocationReason,
  MCPInvocationResult,
  MCPInvocationRiskLevel,
} from '@beacon/contracts'

import { ApiClientError, buildQuery, request } from './cluster'

export { ApiClientError }

/** MCP 客户端全量列表（按创建时间倒序；客户端量天然有限，不做分页）。 */
export function fetchMcpClients(): Promise<MCPClientListResponse> {
  return request('GET', '/admin/v2/mcp-clients')
}

/** 单个客户端脱敏详情。 */
export function fetchMcpClient(clientId: string): Promise<MCPClientItem> {
  return request('GET', `/admin/v2/mcp-clients/${encodeURIComponent(clientId)}`)
}

/**
 * 申请创建客户端。
 * 必须带 Idempotency-Key：缺失时后端直接 403（requestCredentialChange 的前置校验）。
 * 响应 202 只代表「申请已提交」，需人工审批后由 worker 应用；
 * 返回的 clientSecret 是明文 secret 的唯一一次出现。
 */
export function createMcpClient(
  body: CreateMCPClientBody,
  idempotencyKey: string,
): Promise<MCPClientApprovalTicket> {
  return request('POST', '/admin/v2/mcp-clients', body, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

/** 申请轮换 secret：批准后旧 secret 与已签发 token 即时失效，新明文只出现一次。 */
export function rotateMcpClient(
  clientId: string,
  reason: string,
  idempotencyKey: string,
): Promise<MCPClientApprovalTicket> {
  return request('POST', `/admin/v2/mcp-clients/${encodeURIComponent(clientId)}/rotate`, { reason }, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

/** 申请重新启用已吊销客户端（需审批）。 */
export function enableMcpClient(
  clientId: string,
  reason: string,
  idempotencyKey: string,
): Promise<MCPClientApprovalTicket> {
  return request('POST', `/admin/v2/mcp-clients/${encodeURIComponent(clientId)}/enable`, { reason }, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

/**
 * 立即吊销客户端（止损动作，直接执行、不等待审批）。
 * 吊销后已签发 token 即时失效，且无法再换新 token。
 */
export function revokeMcpClient(clientId: string): Promise<{ ok: boolean }> {
  return request('POST', `/admin/v2/mcp-clients/${encodeURIComponent(clientId)}/revoke`)
}

/** MCP 入口部署配置只读视图（启动项，无写入端点）。 */
export function fetchMcpConfig(): Promise<MCPConfigView> {
  return request('GET', '/admin/v2/mcp/config')
}

// ---- 工具调用流水（FR-240；只回脱敏摘要，永不含参数 / 结果正文）----

/** 调用流水查询条件（§3.7 六维过滤 + 游标分页）；空值一律不参与过滤 */
export interface MCPInvocationQuery {
  /** 工具名精确匹配；可传未登记工具名（用于排查 unknown_tool 撞目录） */
  tool?: string
  /** 客户端 ID 精确匹配（权威值取自认证主体） */
  clientId?: string
  result?: MCPInvocationResult
  riskLevel?: MCPInvocationRiskLevel
  /** 原因码精确匹配；成功行为空串，故空串等价于不筛选 */
  reason?: MCPInvocationReason
  /** 时间窗（RFC3339）：按 createdAt 闭区间；from > to 时后端返回 400 */
  from?: string
  to?: string
  /** 不透明游标（热库 keyset 令牌，前端只透传不解析）；空串表示首页 */
  cursor?: string
  /** 单页条数；后端规整到 1..100（缺省 20） */
  limit?: number
}

/** 工具调用流水游标分页查询（nextCursor 空串=末页；响应不返回 total）。 */
export function fetchMCPInvocations(query: MCPInvocationQuery): Promise<MCPInvocationListResponse> {
  return request('GET', `/admin/v2/mcp/invocations${buildQuery({ ...query })}`)
}

/** 单条调用流水详情（流水页行点击）；非法 ID 与不存在一律 404，后端不区分。 */
export function fetchMCPInvocation(invocationId: string): Promise<MCPInvocationItem> {
  return request('GET', `/admin/v2/mcp/invocations/${encodeURIComponent(invocationId)}`)
}
