// Agent 配置下发的前端侧「禁区」与「高危键」清单（FR-279）。
//
// 与 agent 侧 PushedConfigPolicy.kt 同集合，属于防御性双写：即使后端误把这些键回给前端，
// 本页也不提供任何编辑入口——它们是「控制面被攻破 ≠ 被管理端被接管」这道边界的体现。
// 后端另有 400 拒发兜底，前端这层只是让运维在界面上就看到「这些只能本地配置」。
//
// 高危判定不以此清单为准：服务端逐项返回 `dangerous`，页面用那个（见 isHighRiskAgentSetting）。
// 本地清单只在服务端字段缺失（老控制面）时兜底——把判定写死在前端，服务端新增危险键时页面
// 会整片漏报：既没有行内徽章，也没有页顶警告。

/** 禁止由控制面下发的键（键名逐条列出，便于审核者一眼看完整个禁区） */
export const FORBIDDEN_AGENT_SETTING_KEYS = [
  // 接入目标：改了等于把 agent 交给另一个控制面
  'beacon.endpoints',
  // 接入凭据：改了等于换身份或让 agent 再也接不回来
  'beacon.bootstrap-token',
  // 本地身份标识：只能由本机生成 / 迁移
  'identity.server-id',
  'identity.identity-id',
] as const

/** 整段禁止下发的前缀（identity. 由注册 / 审批流程权威回填，不走下发旁路） */
export const FORBIDDEN_AGENT_SETTING_PREFIXES = ['identity.'] as const

/**
 * 本机兜底的高危键清单：可执行控制台命令的闸门（ADR-0011）。
 * 仅在服务端未返回 `dangerous` 字段时使用。
 */
export const HIGH_RISK_AGENT_SETTING_KEYS = ['override.command-whitelist'] as const

/** 该键是否禁止由控制面下发 */
export function isForbiddenAgentSettingKey(key: string): boolean {
  const trimmed = key.trim()
  if ((FORBIDDEN_AGENT_SETTING_KEYS as readonly string[]).includes(trimmed)) {
    return true
  }
  return FORBIDDEN_AGENT_SETTING_PREFIXES.some((prefix) => trimmed.startsWith(prefix))
}

/** 该键是否在高危本地兜底清单内 */
export function isHighRiskAgentSettingKey(key: string): boolean {
  return (HIGH_RISK_AGENT_SETTING_KEYS as readonly string[]).includes(key.trim())
}

/**
 * 该来源是否「本作用域有一层覆盖可撤销」。
 *
 * 只有 server / namespace 两档代表控制面在该层真的写了一行；`local-or-default`
 * 表示控制面根本没下发，此时「撤销」是无操作——不该让运维点一个不存在的按钮，
 * 也不该因此产生一条空下发记录。
 *
 * 旧服务端的 `unset` / `default` 同样归为不可撤销。
 */
export function isRevocableAgentSettingSource(source: string): boolean {
  return source === 'server' || source === 'namespace'
}

/**
 * 该配置项是否高危：**以服务端 `dangerous` 为准**，字段缺失时回退本地清单。
 *
 * 服务端显式给 `false` 就按 false 处理（它才知道当前有哪些危险键），只有字段缺失
 * 才轮得到本地清单兜底——那是唯一允许「前端硬编码说了算」的场景。
 */
export function isHighRiskAgentSetting(item: { key: string; dangerous?: boolean }): boolean {
  return item.dangerous ?? isHighRiskAgentSettingKey(item.key)
}

/** 命令白名单取值的具体问题，用于给出**能照做**的提示（而不是笼统的「格式错误」） */
export type CommandWhitelistIssue =
  /** 空串 / 纯空白：清空不等于撤销，服务端对空值一律 400 */
  | 'empty'
  /** 误填成 JSON 字符串数组（["reload"]），会静默失效 */
  | 'json-array'
  /** 含命令注入字符（连接 / 分隔 / 替换 / 反引号 / 控制字符） */
  | 'unsafe'
  /** 切分后没有有效项，或某一项不是「单个命令首 token」 */
  | 'format'

/**
 * 单项里不允许出现的字符：分号、与 / 或、管道、重定向、命令替换与反引号。
 * 这些值最终会决定 agent 执行哪条控制台命令，前端的职责是别把明显的注入面发下去。
 */
const UNSAFE_TOKEN_CHARS = /[;&|><$`]/

/**
 * 是否含 C0 控制字符或 DEL。
 *
 * 用码点判断而不是在正则里写 `\x00-\x1f`：后者会被 no-control-regex 拦下，
 * 而且码点判断读起来也更直白（控制字符本来就不该出现在命令名里）。
 */
function hasControlChar(value: string): boolean {
  for (const ch of value) {
    const code = ch.codePointAt(0) ?? 0
    if (code < 0x20 || code === 0x7f) {
      return true
    }
  }
  return false
}

/** JSON 字符串数组形态（前端历史文案误导运维写出 `["reload"]`，必须显式识别并拒绝） */
const JSON_ARRAY_SHAPE = /^\[[\s\S]*\]$/

/**
 * 按逗号或换行切分成「命令首 token」列表。
 *
 * 与服务端 agentSettingTypeStringList 及 agent 侧 ConfigReader.stringList 同一约定：
 * 逗号或换行分隔，逐项 trim，丢弃空项。**没有** JSON 解析这一步。
 */
export function splitAgentSettingStringList(value: string): string[] {
  return value
    .split(/[,\n]/)
    .map((item) => item.trim())
    .filter((item) => item !== '')
}

/**
 * 校验命令白名单取值，返回具体问题；合法返回 null。
 *
 * 口径必须与服务端 stringList 一致（逗号或换行分隔）。历史上前端要求 JSON 字符串数组，
 * 造成两种都很糟的结局：
 *   - 按旧提示填 `["reload"]` → 服务端视作单个 token，agent 按首 token 比对永远不命中，
 *     白名单静默失效（保存还「成功」）；
 *   - 按 agent 认得的 `taboolib, reload` 填 → 前端 JSON.parse 失败，保存按钮直接禁用。
 */
export function commandWhitelistIssue(value: string): CommandWhitelistIssue | null {
  const trimmed = value.trim()
  if (trimmed === '') {
    return 'empty'
  }
  if (JSON_ARRAY_SHAPE.test(trimmed)) {
    return 'json-array'
  }
  const tokens = splitAgentSettingStringList(trimmed)
  if (tokens.length === 0) {
    return 'format'
  }
  if (tokens.some((token) => UNSAFE_TOKEN_CHARS.test(token) || hasControlChar(token))) {
    return 'unsafe'
  }
  // 首 token 是单个词：带空格的一项永远不可能与 agent 取到的首 token 相等（又一个静默失效）
  if (tokens.some((token) => /\s/.test(token))) {
    return 'format'
  }
  return null
}

/**
 * 命令白名单取值是否可下发。前端先挡一道，避免把一个必然静默失效的值发下去——
 * 这是唯一可执行命令的开关，静默失败比报错更危险。
 */
export function validateCommandWhitelist(value: string): boolean {
  return commandWhitelistIssue(value) === null
}

/**
 * 该取值是否只是「清空」：空值不等于撤销覆盖。
 *
 * 撤销某层覆盖只能显式传 null（本版前端尚未提供该入口），空串 / 纯空白服务端一律 400
 * ——agent 侧把空白值当「未下发」，存进去等于写了个永不生效的值。
 */
export function isEmptyAgentSettingValue(value: string): boolean {
  return value.trim() === ''
}

/**
 * 行内取值问题：高危键（命令白名单）按 stringList 口径校验，其余键只拦「清空」。
 * 空值的提示统一走同一个文案——「清空不等于撤销」这件事对每个键都成立。
 */
export function agentSettingValueIssue(
  item: { key: string; dangerous?: boolean },
  value: string,
): CommandWhitelistIssue | null {
  if (isHighRiskAgentSetting(item)) {
    return commandWhitelistIssue(value)
  }
  return isEmptyAgentSettingValue(value) ? 'empty' : null
}

/** 取值问题 → i18n 文案键（页面与行内共用同一份映射，避免两处提示说法不一） */
export const ISSUE_MESSAGE_KEY: Record<CommandWhitelistIssue, string> = {
  empty: 'system.agentSettings.value.empty',
  'json-array': 'system.agentSettings.highRisk.issue.jsonArray',
  unsafe: 'system.agentSettings.highRisk.issue.unsafe',
  format: 'system.agentSettings.highRisk.issue.format',
}

