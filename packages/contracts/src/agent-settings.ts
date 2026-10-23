// Agent 接入与配置契约（/admin/v2/agent-settings，FR-279）。
//
// 语义要点（四层优先级，agent 侧写死，见 ADR-0095）：
//   server 级覆盖 > namespace 默认 > 环境变量 > config.yml > 代码默认
// 其中前两层（下发）归控制面管，后三层（本地）只在被管理端生效且不回传，
// 所以 `source` 只表达「控制面能断言到哪一层」，详见 AgentSettingSource。
// 控制面只负责「下发」；本机权威键（接入目标 / 接入凭据 / 身份标识）永远不下发，
// 见 apps/agent/.../PushedConfigPolicy.kt 与 ADR-0011：
//   - beacon.endpoints：可下发 = 控制面能把 agent 指向另一个控制面（目标劫持）；
//   - beacon.bootstrap-token：可下发 = 换身份或让 agent 再也接不回来（身份劫持）；
//   - identity.*：身份字段由注册 / 审批流程权威回填，不走配置下发旁路。
//
// 形状一律以服务端 AgentSettingScopeView / AgentSettingsWriteResult 为准
// （apps/server/.../agent_settings_service.go）：契约少写一个字段，前端就会把
// 「服务端已经告诉它的事」当成不存在——历史上 confirmationScope 与 scopeLabel
// 的缺失就让「每次保存必 400」在测试全绿的情况下长期隐身。

/**
 * 生效值来源——**控制面能断言到的粒度**（不是 agent 侧优先级链的完整镜像）。
 *
 * agent 侧真实优先级（ADR-0095）是四层：
 *
 *   `server` 级下发 > `namespace` 级下发 > 环境变量 > config.yml > 代码默认
 *
 * 但控制面只拥有这条链的**上半段**：它知道自己下发过什么。下半段（环境变量 / config.yml /
 * 代码默认）全在被管理端本机，而 agent 只拉取、**不回传生效值**，因此控制面对这三者
 * 没有任何观测通道，也推不出来。据此本枚举只有三种取值：
 *
 * - `server`：有 server 级下发行，生效值就是它（优先级最高）；
 * - `namespace`：无 server 级行、有 namespace 级行，生效值就是它；
 * - `local-or-default`：两级都没有下发行。此时生效值由 agent 本地链决定
 *   （环境变量 > config.yml > 代码默认），**这三者控制面无法区分**。
 *   运维的处置方向是去该机器改本地配置，而不是在 Web 上改。
 *
 * 为什么不对 `local-or-default` 再细分：把不可知的事实说成已知（例如声称「这是代码默认值」），
 * 会让运维照着一个不成立的结论去 config.yml 里找一个并不存在的配置项，比不显示来源更糟。
 * 这一档相对旧值 `'unset'` 的改进在于：它明确表达「这段不由控制面管」，而不是一个看起来
 * 像「配置缺失」的「未设置」。
 *
 * 旧值兼容：`'unset'`（旧服务端取值）与 `'default'`（旧契约 / 旧 mock 取值）都仍被接受，
 * 语义等同 `local-or-default`。保留它们是为了让新前端连旧控制面时不崩，前端须按同义处理。
 */
export type AgentSettingSource =
  | 'server'
  | 'namespace'
  | 'local-or-default'
  /** @deprecated 旧服务端取值，语义等同 `local-or-default` */
  | 'unset'
  /** @deprecated 旧契约取值，语义等同 `local-or-default` */
  | 'default'

/** 单个可下发配置项：键、当前生效值、生效值来源、控制面侧中文说明 */
export interface AgentSettingItem {
  /** 配置键（点分命名，如 delivery.config-reload-timeout-ms） */
  key: string
  /**
   * 当前生效值（字符串化）。控制面未下发该键时为空串——它**不知道** agent 本地的生效值，
   * 故不回填代码默认值（回填等于把「未下发」伪装成「已设置」）。
   */
  value: string
  /** 生效值来源，见 {@link AgentSettingSource} */
  source: AgentSettingSource
  /** 该键的中文说明（控制面内置，供运维理解取值含义） */
  desc: string
  /**
   * 值类型：`int` / `bool` / `string` / `string-list`。
   * 其中 `string-list` 的分隔约定是**逗号或换行**（与 agent 侧 ConfigReader.stringList 一致），
   * 不是 JSON 数组——按 JSON 填写的值会被服务端当成单个 token 存下去。
   *
   * 可选：老控制面不返回该字段，前端此时不做类型相关的取值校验。
   */
  valueType?: string
  /**
   * 服务端判定是否高危项（如 override.command-whitelist，ADR-0011）。
   *
   * 高危判定必须以本字段为准：服务端新增危险键时，前端一份硬编码清单必然滞后，
   * 结果是新危险键既没有行内徽章也没有页顶警告（漏报）。前端本地清单只在该字段
   * **缺失**（老控制面）时兜底。
   */
  dangerous?: boolean
  /**
   * 该键是否在控制面已知清单内；`false` 表示库里有值但控制面不认识
   * （建议清理或升级控制面；向它写入会被 400 拒掉）。
   * 可选：老控制面不返回该字段。
   */
  known?: boolean
}

/** 某作用域下的配置项全量视图（GET 响应，也是写结果里的 `scope`） */
export interface AgentSettingsResponse {
  items: AgentSettingItem[]
  /**
   * 该作用域最近一次下发时间（RFC3339）；从未下发过时为 null。
   * 页面统一按空串处理（历史契约写作空串，后端实际返回 null，两者都当作「从未下发」）。
   */
  updatedAt: string
  /**
   * 作用域标识：`环境码` 或 `环境码/serverId`（服务端 agentSettingScopeLabel）。
   *
   * 它同时是写端点的**二次确认串**：PUT 的 `confirmationScope` 必须与它逐字一致，
   * 否则服务端 400（AGENT_SETTING_CONFIRM_MISMATCH）。页面只能把这里的值原样回填，
   * 绝不能凭 namespaceId / serverId 自行拼装——那样这道「防止改错作用域」的闸门
   * 就退化成前端自说自话，反而让「以为在改这台服、实际改了整个环境」无法被发现。
   *
   * 可选：老控制面不返回该字段，此时页面必须**禁止提交**（而不是发一个空串过去换 400）。
   */
  scopeLabel?: string
}

/**
 * 下发请求体（批量：一次提交本次改动的全部键）。
 * `serverId` 省略 = 写 namespace 默认层；给出 = 写该服务器的覆盖层。
 */
export interface UpdateAgentSettingsBody {
  namespaceId: number
  /** 服务器标识；省略表示 namespace 默认层 */
  serverId?: string
  /**
   * 二次确认串：**必须原样回填 GET 响应里的 `scopeLabel`**（服务端逐字比对）。
   * 缺失或不符一律 400 AGENT_SETTING_CONFIRM_MISMATCH。
   */
  confirmationScope: string
  /**
   * 本次改动的键值对（字符串化值）；未列出的键保持原状。
   *
   * - 字符串值 = 写入该层的覆盖。**不得为空串 / 纯空白**：服务端一律 400
   *   （agent 侧把空白值当「未下发」，存进去等于写了个永不生效的值）。
   * - `null` = **撤销该层覆盖**（删除这一行），该键随即回退到上一级覆盖 / 本地配置 / 代码默认。
   *
   * 区分「撤销」与「设为空」是刻意的：`null` 才是撤销，空串是非法值。
   * 前者让运维能把控制权交还回去，后者只会造出一个永不生效的覆盖。
   */
  values: Record<string, string | null>
  /** 下发原因（必填，审批与审计都会记录） */
  reason: string
}

/**
 * 直执成功的响应体（200）：真实写入后的作用域视图 + 三类变更集。
 *
 * 服务端**不返回** `{ok:true}`（历史契约写错了这个形状）；判定「是否走审批」必须看
 * 票据是否存在，不能靠响应里有没有某个只有成功路径才有的字段。
 */
export interface AgentSettingsWriteResult {
  /** 写入后的最新作用域视图（走审批时是**提交前**的状态，库内尚未变更） */
  scope: AgentSettingsResponse
  /** 本次实际写入（新增或改值）的键 */
  applied: string[]
  /** 本次实际撤销（删除覆盖）的键 */
  removed: string[]
  /** 提交值与库内一致、未产生任何写入的键（幂等重放时全是这一类） */
  unchanged: string[]
  /**
   * 非空表示本次提交已被拦到审批：库内**未变更**，上面三个变更集必为空。
   * 当前服务端 202 直接返回票据本身（见 AgentSettingsApprovalTicket），该嵌套字段
   * 是服务端结构体上的兼容位；判定票据时两种形态都要认。
   */
  approvalTicket?: AgentSettingsApprovalTicket
}

/**
 * 高危键（如命令白名单）下发被拦到审批：批准后才真正写入。
 *
 * 服务端 202 的响应体就是本结构本身（不是包在写结果里）。
 */
export interface AgentSettingsApprovalTicket {
  /** 审批申请号，用于跳转审批中心 */
  approvalRequestId: string
  /**
   * 票据状态。取值见服务端 model.ApprovalStatus*：
   * `pending` / `executing` / `rejected` / `withdrawn` / `expired` / `succeeded` / `failed`。
   *
   * 必须读它：命中幂等键时服务端直接回既有申请**而不判状态**，一张早已 rejected 的旧票据
   * 长得和刚创建的一模一样。只认 approvalRequestId 就会把「已终结」谎报成「待审批」，
   * 而那条变更实际上再也提交不出去。
   */
  status: string
  /** 危险操作类型键（服务端 OperationKey） */
  operationKey?: string
  /** 本端点不回传任何机密，恒为 false；保留字段与其它危险操作票据同形 */
  secretReturned?: boolean
}

/** 下发结果：200 直执（{@link AgentSettingsWriteResult}）或 202 审批票据（{@link AgentSettingsApprovalTicket}） */
export type AgentSettingsSaveResult = AgentSettingsWriteResult | AgentSettingsApprovalTicket
