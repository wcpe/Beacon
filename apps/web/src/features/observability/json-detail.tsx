// 通用 JSON 详情可视化：解析对象/数组为键值表，字段名与常见枚举值走 i18n，原文折叠。
// 供告警 detail、审计 detail、命令 resultDetail 等复用，避免各页 dump JSON 原文。
import { useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'

interface JsonDetailProps {
  // 原始 JSON 文本（或非 JSON 原文）
  raw: string
  // 区块标题（已翻译）
  title: string
  // 字段名 i18n 前缀，如 observability.alertEvents.detailKeys
  keyPrefix: string
  // 可选：值翻译回调（key + 原始值 → 展示文案）；不传则用通用规则
  valueLabel?: (t: TFunction, key: string, value: unknown) => string
  // 空内容占位
  emptyText?: string
}

/** 尝试解析 JSON；非对象/数组形态或解析失败返回 null */
export function tryParseJson(raw: string): unknown {
  const trimmed = raw.trim()
  if (!(trimmed.startsWith('{') || trimmed.startsWith('['))) {
    return null
  }
  try {
    return JSON.parse(trimmed) as unknown
  } catch {
    return null
  }
}

/** 扁平化一层键值，供表格展示 */
export function flattenJsonEntries(value: unknown): [string, unknown][] {
  if (value === null || value === undefined) {
    return [['value', null]]
  }
  if (typeof value !== 'object') {
    return [['value', value]]
  }
  if (Array.isArray(value)) {
    return value.map((item, i) => [String(i), item])
  }
  return Object.entries(value as Record<string, unknown>)
}

/** 通用字段名 → 中文（走 keyPrefix；未映射回退 key） */
export function jsonKeyLabel(t: TFunction, keyPrefix: string, key: string): string {
  return t(`${keyPrefix}.${key}`, { defaultValue: key })
}

/**
 * 通用值翻译：布尔 → 是/否；已知 status/prevStatus 健康态走 cluster 文案；对象 → 紧凑 JSON。
 */
export function defaultJsonValueLabel(t: TFunction, key: string, value: unknown): string {
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
  if (typeof value !== 'string' && typeof value !== 'number') {
    return '—'
  }
  const raw = String(value)
  // 健康流转常见状态值
  if (key === 'status' || key === 'prevStatus' || key === 'level') {
    const healthKey = `cluster.servers.health.${raw}`
    const healthLabel = t(healthKey, { defaultValue: '' })
    if (healthLabel !== '' && healthLabel !== healthKey) {
      return healthLabel
    }
    // online/offline/degraded/lost 等
    const statusMapKey = `observability.alertEvents.healthStatus.${raw}`
    return t(statusMapKey, { defaultValue: raw })
  }
  return raw
}

/**
 * JSON 详情块：键值表 + 原文折叠；非 JSON 则原文；空则占位。
 */
export default function JsonDetail({
  raw,
  title,
  keyPrefix,
  valueLabel,
  emptyText,
}: JsonDetailProps): ReactNode {
  const { t } = useTranslation()
  const parsed = useMemo(() => tryParseJson(raw), [raw])
  const resolveValue = valueLabel ?? defaultJsonValueLabel

  if (!raw || raw.trim() === '') {
    return (
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{title}</span>
        <p className="rounded-lg bg-secondary/60 px-2.5 py-2 text-xs text-ink-4">
          {emptyText ?? '—'}
        </p>
      </div>
    )
  }

  if (parsed === null) {
    return (
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{title}</span>
        <p className="rounded-lg bg-secondary/60 px-2.5 py-2 text-xs text-ink-2 whitespace-pre-wrap">{raw}</p>
      </div>
    )
  }

  const entries = flattenJsonEntries(parsed)
  return (
    <div className="grid gap-1.5">
      <span className="text-xs text-ink-4">{title}</span>
      {entries.length > 0 ? (
        <div className="overflow-hidden rounded-lg border border-border bg-secondary/40">
          <table className="w-full border-collapse text-left text-[12px]">
            <thead>
              <tr className="border-b border-border text-[11px] text-ink-4">
                <th className="px-2.5 py-1.5 font-medium">
                  {t('observability.common.jsonField', { defaultValue: '字段' })}
                </th>
                <th className="px-2.5 py-1.5 font-medium">
                  {t('observability.common.jsonValue', { defaultValue: '值' })}
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map(([key, value]) => (
                <tr key={key} className="border-b border-border/60 last:border-0">
                  <td className="px-2.5 py-1.5 align-top text-[11px] text-ink-3">
                    {jsonKeyLabel(t, keyPrefix, key)}
                  </td>
                  <td className="px-2.5 py-1.5 align-top break-all text-ink-1">
                    {resolveValue(t, key, value)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      <details className="group">
        <summary className="cursor-pointer text-[11px] text-ink-4 hover:text-ink-2">
          {t('observability.common.jsonRaw', { defaultValue: '查看原文' })}
        </summary>
        <pre className="mt-1 max-h-40 overflow-auto rounded-lg bg-secondary/60 px-2.5 py-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-ink-2">
          {typeof parsed === 'string' ? parsed : JSON.stringify(parsed, null, 2)}
        </pre>
      </details>
    </div>
  )
}
