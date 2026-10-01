// MCP 管理面契约（/admin/v2/mcp-clients*、/admin/v2/mcp/config 与 /admin/v2/mcp/invocations*）。
// 契约真源：docs/specs/built-in-admin-v2-mcp-and-oauth.md §3.2 / §4；
//          docs/specs/mcp-invocation-audit.md §3.2 / §3.4 / §3.7（工具调用流水，FR-240）。
//
// 语义要点：
// - 客户端 secret 明文只在创建 / 轮换的 202 响应出现一次，服务端不持久化明文；
//   查询接口一律只回非敏感前缀（secretPrefix）。
// - 创建 / 轮换 / 启用为「申请」语义（202 + 审批票据），需人工审批后由 worker 应用；
//   吊销是止损动作，直接执行、不等待审批。
// - MCP 入口配置全部是启动项（改后须重启控制面），管理面只读。
// - 工具调用流水只记**脱敏摘要**：参数正文与结果正文都不入库（末尾 §调用流水 有逐字段约束）。

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
  /**
   * MCP 生产模式（FR-237，启动项，默认 false）。
   * 开启时 critical 风险等级的工具对 MCP 客户端完全不可发现——
   * 即不可逆（永久删除）、影响控制面自身（自更新 / 系统设置）
   * 与可提权（凭据签发轮换、机器自批审批）的那一档。
   */
  productionMode: boolean
  /** true = 内网明文直连（无 TLS 反代）；false = 经受信反向代理 */
  directMode: boolean
}

// ---- 工具调用流水（FR-240，docs/specs/mcp-invocation-audit.md §3.2 / §3.4 / §3.7）----

/**
 * 调用结果三态（§3.4 冻结）：
 * - `ok`：正常完成；
 * - `fail`：handler 抛错 / SDK 包装错误 / 多轮未完成 / 流水层 panic；
 * - `rejected`：**已处理的拒绝**——未登记工具，或 handler 主动拒绝（`error` 返回 nil）。
 */
export type MCPInvocationResult = 'ok' | 'fail' | 'rejected'

/**
 * 原因码（§3.4 冻结，与 `result` 联合判定）：
 * - `''`：`result=ok`（成功恒为空串）；
 * - `unknown_tool`：工具名不在工具目录内（含 SDK 以 error 上抛的 unknown tool）；
 * - `production_mode`：生产模式执行面拒执（FR-242），恒伴随 `riskLevel=critical`；
 * - `handler_rejected`：业务拒绝（参数缺失 / 目标不可用 / 审批票据创建失败等）；
 * - `handler_error`：`result=fail` 且源于 error（含 SDK 包装错误）；
 * - `input_required`：多轮交互本轮未完成（防御分支，现无工具使用）；
 * - `internal_error`：流水层 panic 恢复路径。
 */
export type MCPInvocationReason =
  | ''
  | 'unknown_tool'
  | 'production_mode'
  | 'handler_rejected'
  | 'handler_error'
  | 'input_required'
  | 'internal_error'

/**
 * 工具风险等级（§3.2 冻结）：
 * `low` / `high` / `critical` 取自工具目录（FR-236）；未登记工具记 `unknown`
 * （不是空串——这样「撞目录的调用」可以直接筛出来）。
 */
export type MCPInvocationRiskLevel = 'low' | 'high' | 'critical' | 'unknown'

/**
 * 单条工具调用流水（§3.2 字段全集，camelCase 与后端 JSON 输出一一对应）。
 *
 * **脱敏约束（§2.2 / §3.3，契约级不变量）**：本类型只承载摘要，任何人不得在此追加
 * 参数正文 / 结果正文 / `_meta` / 请求头字段——参数只以三种形态出现：
 * 1. `targetDigest`：白名单目标标识键的 `k=v` 拼接（≤3 项、≤255 字符）；
 * 2. `argKeys`：顶层参数键名清单，内容类键只记 `键名:字节数`（值绝不出现）；
 * 3. `argBytes`：参数原文**字节数**（用于发现「传了内容但被丢掉」）。
 * 文本字段（`targetDigest` / `argKeys` / `errorSummary`）入库前已过脱敏并截断。
 */
export interface MCPInvocationItem {
  /** UUIDv7 文本（36 字符），主键；详情端点即按它直定日表查询 */
  invocationId: string
  /** 权威取自已认证主体（非请求自报值） */
  clientId: string
  /** 客户端能力档位，取自主体角色 */
  profile: MCPClientProfile
  /** 工具名原文（≤128 字符；未登记工具也原样记录） */
  toolName: string
  riskLevel: MCPInvocationRiskLevel
  result: MCPInvocationResult
  /** 成功为空串；其余取值见 {@link MCPInvocationReason} */
  reason: MCPInvocationReason
  /** 目标标识摘要 `k1=v1;k2=v2`（白名单键，≤255 字符）；无可用标量时为空串 */
  targetDigest: string
  /** 顶层参数键名清单（升序、`,` 分隔、≤24 项；内容类键记作 `键名:字节数`） */
  argKeys: string
  /** 参数原文字节数（不是字符数）；未传参数为 0 */
  argBytes: number
  /** 调用墙钟耗时（毫秒），不含响应序列化与网络 */
  durationMs: number
  /** 与响应头 X-Trace-Id 同值的 16 位十六进制串；可能为空串 */
  traceId: string
  /** 客户端地址（受信反代注入；直连部署可能为空串），观测字段而非安全边界 */
  clientIp: string
  /** 已脱敏截断的错误 / 拒绝摘要（≤255 字符）；成功为空串 */
  errorSummary: string
  /** 调用完成时刻，RFC3339（毫秒精度，UTC） */
  createdAt: string
}

/**
 * 调用流水列表响应（§3.7）。
 *
 * 不返回 `total`：跨日表精确总数需要全扫，故与 `/admin/v2/connections` 同口径改为**游标分页**。
 * `nextCursor` 为不透明令牌，**空串表示末页**（因此不复用 `CursorPage<T>` 的 `string | null`，
 * 与真后端「不引入 null 分支」的冻结口径一致；前端只透传不解析）。
 */
export interface MCPInvocationListResponse {
  items: MCPInvocationItem[]
  nextCursor: string
}
