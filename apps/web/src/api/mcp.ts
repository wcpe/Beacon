// MCP 管理面数据获取（/mcp-clients 页面）：
// 客户端生命周期（列表 / 详情 / 申请创建 / 申请轮换 / 申请启用 / 立即吊销）
// 与 MCP 入口部署配置的只读视图。
//
// 契约真源：docs/specs/built-in-admin-v2-mcp-and-oauth.md §3.2 / §4。
// 统一走集群域请求封装（含鉴权注入与 401 处理，FR-179），错误按脱敏 message 抛出（ADR-0057）。

import type {
  CreateMCPClientBody,
  MCPClientApprovalTicket,
  MCPClientItem,
  MCPClientListResponse,
  MCPConfigView,
} from '@beacon/contracts'

import { ApiClientError, request } from './cluster'

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
