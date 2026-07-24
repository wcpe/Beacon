// 命令 type / resultDetail 展示用：统一 i18n，避免列表与详情直接 dump 英文枚举或 JSON 原文。
// 字面值与后端 model.CommandType* 对齐；未知 key 经 defaultValue 回退原文。

import type { TFunction } from 'i18next'

/** 后端权威命令类型字面值（筛选下拉 + 文档对齐） */
export const COMMAND_TYPES = [
  'ingest-plugins',
  'tail-logs',
  'resync-config',
  'fs-browse',
  'file-sync-source',
  'file-sync-apply',
  'file-sync-rollback',
  'asset-rescan',
  'asset-read',
  'delivery_upload',
  'delivery_push',
  'delivery_activate',
  'delivery_rollback',
] as const

export type CommandType = (typeof COMMAND_TYPES)[number]

/** 命令 type → 中文（未映射回退原文） */
export function commandTypeLabel(t: TFunction, type: string): string {
  return t(`observability.commands.type.${type}`, { defaultValue: type })
}

/** resultDetail JSON 字段名 → 中文 */
export function commandResultKeyLabel(t: TFunction, key: string): string {
  return t(`observability.commands.resultKeys.${key}`, { defaultValue: key })
}

/**
 * 结果值翻译：phase/status 等已知枚举走 i18n；布尔 → 是/否；其余原样。
 */
export function commandResultValueLabel(t: TFunction, key: string, value: unknown): string {
  if (value === null || value === undefined) {
    return '—'
  }
  if (typeof value === 'boolean') {
    return value
      ? t('observability.serviceAnalysis.yes', { defaultValue: '是' })
      : t('observability.serviceAnalysis.no', { defaultValue: '否' })
  }
  if (typeof value === 'object') {
    return JSON.stringify(value)
  }
  if (typeof value === 'string' || typeof value === 'number') {
    const raw = String(value)
    if (key === 'phase') {
      return t(`observability.commands.resultPhase.${raw}`, { defaultValue: raw })
    }
    if (key === 'status') {
      return t(`observability.commands.resultStatus.${raw}`, { defaultValue: raw })
    }
    return raw
  }
  return '—'
}

/**
 * 列表「结果」列摘要：不 dump 整段 JSON。
 * - 有 error → 失败摘要
 * - 有 changed/skipped → 变更计数
 * - 有 status=success → 成功
 * - 空 → —
 * - 非 JSON 原文截断
 */
export function commandResultSummary(t: TFunction, raw: string): string {
  if (!raw || raw.trim() === '') {
    return '—'
  }
  const trimmed = raw.trim()
  if (!(trimmed.startsWith('{') || trimmed.startsWith('['))) {
    return trimmed.length > 48 ? `${trimmed.slice(0, 48)}…` : trimmed
  }
  try {
    const parsed = JSON.parse(trimmed) as Record<string, unknown>
    if (typeof parsed.error === 'string' && parsed.error !== '') {
      return t('observability.commands.resultSummaryFail', { error: parsed.error })
    }
    const changed = typeof parsed.changedFileCount === 'number' ? parsed.changedFileCount : null
    const skipped = typeof parsed.skippedFileCount === 'number' ? parsed.skippedFileCount : null
    if (changed !== null || skipped !== null) {
      return t('observability.commands.resultSummaryCounts', {
        changed: changed ?? 0,
        skipped: skipped ?? 0,
      })
    }
    if (parsed.status === 'success' || parsed.status === 'ok') {
      return t('observability.commands.resultSummaryOk')
    }
    if (typeof parsed.status === 'string') {
      return commandResultValueLabel(t, 'status', parsed.status)
    }
    const keys = Object.keys(parsed)
    if (keys.length > 0) {
      return t('observability.commands.resultSummaryGeneric', { count: keys.length })
    }
    return t('observability.commands.resultSummaryOk')
  } catch {
    return trimmed.length > 48 ? `${trimmed.slice(0, 48)}…` : trimmed
  }
}
