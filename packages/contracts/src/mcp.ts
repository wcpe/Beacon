// MCP 管理面契约（/admin/v2/mcp-clients* 与 /admin/v2/mcp/config）。
// 契约真源：docs/specs/built-in-admin-v2-mcp-and-oauth.md §3.2 / §4。
//
// 语义要点：
// - 客户端 secret 明文只在创建 / 轮换的 202 响应出现一次，服务端不持久化明文；
//   查询接口一律只回非敏感前缀（secretPrefix）。
// - 创建 / 轮换 / 启用为「申请」语义（202 + 审批票据），需人工审批后由 worker 应用；
//   吊销是止损动作，直接执行、不等待审批。
// - MCP 入口配置全部是启动项（改后须重启控制面），管理面只读。

/** capability bundle：observer 仅只读；automation 含低风险写入与提交本人审批申请 */
export type MCPClientProfile = 'observer' | 'automation'

/** active 可换 token；revoked 不能换 token，重新启用需审批 */
export type MCPClientStatus = 'active' | 'revoked'

/** 单个 MCP OAuth 客户端（脱敏视图，不含 secret 明文/哈希） */
export interface MCPClientItem {
  clientId: string
  displayName: string
  /** secret 展示前缀，不可反推明文 */
  secretPrefix: string
  profile: MCPClientProfile
  status: MCPClientStatus
  /** 每次轮换单调递增；轮换后旧版本 secret 与已签发 token 即时失效 */
  secretVersion: number
  createdBy: string
  createdAt: string
  updatedAt: string
  /** 仅吊销后存在；未吊销时后端省略该字段 */
  revokedAt?: string | null
}

export interface MCPClientListResponse {
  items: MCPClientItem[]
}

/**
 * 危险生命周期申请票据（创建 / 轮换 / 启用共用）。
 * clientSecret 仅在「首次」创建或轮换的同步响应出现一次；幂等重放不返回明文，
 * 遗失只能重新申请轮换。
 */
export interface MCPClientApprovalTicket {
  approvalRequestId: string
  clientId: string
  clientSecret?: string
  status: string
}

/** 申请创建的请求体 */
export interface CreateMCPClientBody {
  displayName: string
  profile: MCPClientProfile
  reason: string
}

/** MCP 入口部署配置只读视图；不含任何凭据（如 agent token） */
export interface MCPConfigView {
  /** 未启用时协议端点不挂载，外部 Agent 无法连接 */
  enabled: boolean
  publicBaseUrl: string
  trustedProxyCidrs: string[]
  allowInsecureInternal: boolean
  allowedHosts: string[]
  /** 是否允许 automation 客户端执行审批决定（FR-223，默认 false） */
  allowApprovalDecide: boolean
  /** 是否允许受信内部调用方机器化注册 agent（FR-222，默认 false） */
  allowMachineRegister: boolean
  /** true = 内网明文直连（无 TLS 反代）；false = 经受信反向代理 */
  directMode: boolean
}
