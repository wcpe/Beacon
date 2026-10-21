// Agent 配置下发的前端侧「禁区」与「高危键」清单（FR-279）。
//
// 与 agent 侧 PushedConfigPolicy.kt 同集合，属于防御性双写：即使后端误把这些键回给前端，
// 本页也不提供任何编辑入口——它们是「控制面被攻破 ≠ 被管理端被接管」这道边界的体现。
// 后端另有 400 拒发兜底，前端这层只是让运维在界面上就看到「这些只能本地配置」。

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

/** 高危键：可执行控制台命令的闸门（ADR-0011），界面必须给出显式警告 */
export const HIGH_RISK_AGENT_SETTING_KEYS = ['override.command-whitelist'] as const

/** 该键是否禁止由控制面下发 */
export function isForbiddenAgentSettingKey(key: string): boolean {
  const trimmed = key.trim()
  if ((FORBIDDEN_AGENT_SETTING_KEYS as readonly string[]).includes(trimmed)) {
    return true
  }
  return FORBIDDEN_AGENT_SETTING_PREFIXES.some((prefix) => trimmed.startsWith(prefix))
}

/** 该键是否为高危键 */
export function isHighRiskAgentSettingKey(key: string): boolean {
  return (HIGH_RISK_AGENT_SETTING_KEYS as readonly string[]).includes(key.trim())
}

/**
 * 命令白名单取值校验：必须是 JSON 字符串数组（如 `["reload"]`），空串表示关闭命令派发。
 * 前端先挡一道，避免把一个必然被 agent 拒绝的值发下去——这是唯一可执行命令的开关，
 * 静默失败比报错更危险。
 */
export function validateCommandWhitelist(value: string): boolean {
  const trimmed = value.trim()
  if (trimmed === '') {
    return true
  }
  try {
    const parsed: unknown = JSON.parse(trimmed)
    if (!Array.isArray(parsed)) {
      return false
    }
    return parsed.every((entry: unknown) => typeof entry === 'string')
  } catch {
    return false
  }
}
