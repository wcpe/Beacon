// 命令详情面板内容（非模态右侧列）：单条命令双向生命周期（下发 → 取走 → 回执）全字段。
// 结果字段：若 resultDetail 为 JSON 则键值可视化 + 原文折叠；否则按原文展示。与 /audits 互跳（FR-157）。
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ArrowUpRight } from 'lucide-react'

import { Badge } from '@beacon/ui'
import type { CommandItem } from '@beacon/contracts'

import {
  commandResultKeyLabel,
  commandResultValueLabel,
  commandTypeLabel,
} from '../../features/observability/command-labels'

// 命令状态 → 状态药丸语义色：done 正常绿、failed/expired 危急红、其余次要。
function badgeVariant(status: CommandItem['status']): 'ok' | 'off' | 'crit' {
  if (status === 'failed' || status === 'expired') {
    return 'crit'
  }
  if (status === 'done') {
    return 'ok'
  }
  return 'off'
}

interface CommandDetailPanelProps {
  // 展示的命令行
  item: CommandItem
}

export default function CommandDetailPanel({ item }: CommandDetailPanelProps) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-3 text-sm">
      <Field label={t('observability.commands.columns.commandId')} value={String(item.commandId)} mono />
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.commands.columns.status')}</span>
        <Badge className="w-fit" variant={badgeVariant(item.status)}>
          {t(`observability.commands.status.${item.status}`)}
        </Badge>
      </div>
      <Field label={t('observability.commands.columns.serverId')} value={item.serverId} mono />
      <Field label={t('observability.commands.columns.type')} value={commandTypeLabel(t, item.type)} />
      <Field label={t('observability.commands.columns.operator')} value={item.operator} />

      {/* 双向生命周期时间线：下发 → 最近更新（取走 / 回执） */}
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.commands.lifecycle')}</span>
        <div className="grid gap-1 rounded-lg bg-secondary/50 px-2.5 py-2 text-xs text-ink-2">
          <div className="flex items-center justify-between gap-2">
            <span className="text-ink-4">{t('observability.commands.columns.createdAt')}</span>
            <span className="tabular-nums">{new Date(item.createdAt).toLocaleString()}</span>
          </div>
          <div className="flex items-center justify-between gap-2">
            <span className="text-ink-4">{t('observability.commands.updatedAt')}</span>
            <span className="tabular-nums">{new Date(item.updatedAt).toLocaleString()}</span>
          </div>
          <div className="flex items-center justify-between gap-2">
            <span className="text-ink-4">{t('observability.commands.columns.age')}</span>
            <span className="tabular-nums">{t('observability.commands.ageSeconds', { count: item.ageSeconds })}</span>
          </div>
        </div>
      </div>

      <ResultDetailView raw={item.resultDetail} />

      <Link
        className="inline-flex w-fit items-center gap-0.5 text-xs text-brand-600 hover:underline"
        to={`/audits?targetRef=${encodeURIComponent(item.serverId)}`}
      >
        {t('observability.commands.viewInAudits')}
        <ArrowUpRight className="size-3" />
      </Link>
    </div>
  )
}

// 结果可视化：JSON 对象/数组 → 键值表；解析失败 → 原文；空 → 占位
function ResultDetailView({ raw }: { raw: string }) {
  const { t } = useTranslation()
  const parsed = useMemo(() => tryParseJson(raw), [raw])

  if (!raw || raw.trim() === '') {
    return (
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.commands.columns.result')}</span>
        <p className="rounded-lg bg-secondary/60 px-2.5 py-2 text-xs text-ink-4">
          {t('observability.commands.resultEmpty')}
        </p>
      </div>
    )
  }

  if (parsed === null) {
    return (
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.commands.columns.result')}</span>
        <p className="mb-1 text-[11px] text-ink-4">{t('observability.commands.resultParseFail')}</p>
        <pre className="max-h-48 overflow-auto rounded-lg bg-secondary/60 px-2.5 py-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-ink-2">
          {raw}
        </pre>
      </div>
    )
  }

  const entries = flattenJsonEntries(parsed)
  return (
    <div className="grid gap-1.5">
      <span className="text-xs text-ink-4">{t('observability.commands.columns.result')}</span>
      {entries.length > 0 ? (
        <div className="overflow-hidden rounded-lg border border-border bg-secondary/40">
          <table className="w-full border-collapse text-left text-[12px]">
            <thead>
              <tr className="border-b border-border text-[11px] text-ink-4">
                <th className="px-2.5 py-1.5 font-medium">{t('observability.commands.resultFields')}</th>
                <th className="px-2.5 py-1.5 font-medium">{t('observability.commands.resultValue')}</th>
              </tr>
            </thead>
            <tbody>
              {entries.map(([key, value]) => (
                <tr key={key} className="border-b border-border/60 last:border-0">
                  <td className="px-2.5 py-1.5 align-top text-[11px] text-ink-3">
                    {commandResultKeyLabel(t, key)}
                  </td>
                  <td className="px-2.5 py-1.5 align-top break-all text-ink-1">
                    {commandResultValueLabel(t, key, value)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      <details className="group">
        <summary className="cursor-pointer text-[11px] text-ink-4 hover:text-ink-2">
          {t('observability.commands.resultRaw')}
        </summary>
        <pre className="mt-1 max-h-40 overflow-auto rounded-lg bg-secondary/60 px-2.5 py-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-ink-2">
          {typeof parsed === 'string' ? parsed : JSON.stringify(parsed, null, 2)}
        </pre>
      </details>
    </div>
  )
}

function tryParseJson(raw: string): unknown {
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

// 扁平化一层对象键值；值保持原始类型供 commandResultValueLabel 翻译（布尔 / phase / status）
function flattenJsonEntries(value: unknown): [string, unknown][] {
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

// 单个只读字段（标签 + 值）
function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-ink-4">{label}</span>
      <span className={mono ? 'font-mono text-xs text-ink-2' : 'text-sm text-ink-1'}>{value}</span>
    </div>
  )
}
