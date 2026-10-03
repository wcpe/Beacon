// 审计 action 展示标签共享工具：后端审计接口返回英文枚举字面值（model.Action*），
// 前端必须经 i18n 映射为可读文案；未映射的动作回退原始枚举（宁可显英文也不显裸 i18n key，
// 同时不阻断后端新增动作的展示）。
//
// 与 command-labels.ts 同构：单一 key 前缀常量 + 单一 t() 调用点，避免各处手拼 key 走形。

import type { TFunction } from 'i18next'

/** 审计 action i18n key 前缀；后端 enum 字面值（含点号，如 instance.register）直接拼在其后 */
export const AUDIT_ACTION_KEY_PREFIX = 'observability.audits.action'

/**
 * 审计 action → 本地化标签。
 *
 * @param t        i18next 翻译函数（非激活语言时由调用方自行切语言）
 * @param action   后端审计 action 枚举字面值（可为空串：调用方仅有 id 时保持原回退语义）
 * @param fallback 未映射时的回退文案；缺省回退 action 原文
 */
export function auditActionLabel(t: TFunction, action: string, fallback?: string): string {
  return t(`${AUDIT_ACTION_KEY_PREFIX}.${action}`, { defaultValue: fallback ?? action })
}
