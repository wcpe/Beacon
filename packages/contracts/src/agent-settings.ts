// Agent 接入与配置契约（/admin/v2/agent-settings，FR-279）。
//
// 语义要点（三层优先级，agent 侧写死）：
//   server 级覆盖 > namespace 默认 > 未设置（回退 agent 本地 config.yml / 代码默认）
// 控制面只负责「下发」；本机权威键（接入目标 / 接入凭据 / 身份标识）永远不下发，
// 见 apps/agent/.../PushedConfigPolicy.kt 与 ADR-0011：
//   - beacon.endpoints：可下发 = 控制面能把 agent 指向另一个控制面（目标劫持）；
//   - beacon.bootstrap-token：可下发 = 换身份或让 agent 再也接不回来（身份劫持）；
//   - identity.*：身份字段由注册 / 审批流程权威回填，不走配置下发旁路。
//
// 契约差异说明（待后端并行实现对齐，前端不擅自改后端）：
//   1. 响应 `source` 增加了 `'default'`（未设置）一档——前端要按「server 级覆盖 /
//      namespace 默认 / 未设置」三态展示，仅两值无法表达「尚未下发」。
//   2. 202 审批票据的具体字段（approvalRequestId / status）按 MCP 客户端票据同形约定，
//      若后端采用统一审批中心字段名需在此处对齐。

/**
 * 生效值来源：
 * - `server`：server 级覆盖（仅对某一台服务器生效，优先级最高）；
 * - `namespace`：namespace 默认值（对该 namespace 内未单独覆盖的服务器生效）；
 * - `default`：控制面未下发（agent 回退本地 config.yml，再回退代码默认）。
 */
export type AgentSettingSource = 'server' | 'namespace' | 'default'

/** 单个可下发配置项：键、当前生效值、生效值来源、控制面侧中文说明 */
export interface AgentSettingItem {
  /** 配置键（点分命名，如 delivery.config-reload-timeout-ms） */
  key: string
  /** 当前生效值（字符串化）；`source='default'` 时为空串（控制面不知道本地值） */
  value: string
  /** 生效值来源，见 {@link AgentSettingSource} */
  source: AgentSettingSource
  /** 该键的中文说明（控制面内置，供运维理解取值含义） */
  desc: string
}

/** 某作用域下的配置项全量视图 */
export interface AgentSettingsResponse {
  items: AgentSettingItem[]
  /** 该作用域最近一次下发时间（RFC3339）；从未下发过为空串 */
  updatedAt: string
}

/**
 * 下发请求体（批量：一次提交本次改动的全部键）。
 * `serverId` 省略 = 写 namespace 默认层；给出 = 写该服务器的覆盖层。
 */
export interface UpdateAgentSettingsBody {
  namespaceId: number
  /** 服务器标识；省略表示 namespace 默认层 */
  serverId?: string
  /** 本次改动的键值对（字符串化值）；未列出的键保持原状 */
  values: Record<string, string>
  /** 下发原因（必填，审批与审计都会记录） */
  reason: string
}

/** 直执成功：配置已写入并按 ≤30s 周期热生效 */
export interface AgentSettingsSaved {
  ok: true
}

/** 高危键（如命令白名单）下发被拦到审批：批准后才真正写入 */
export interface AgentSettingsApprovalTicket {
  approvalRequestId: string
  /** 票据状态（pending = 待审批） */
  status: string
}

/** 下发结果：200 直执（{@link AgentSettingsSaved}）或 202 审批票据（{@link AgentSettingsApprovalTicket}） */
export type AgentSettingsSaveResult = AgentSettingsSaved | AgentSettingsApprovalTicket
