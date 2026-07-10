// 完整编排预览（共享控件）：展示交付编排全貌——目标范围（模式 + 已选对象 + 目标总数）、
// 批次规划（每批数量 / 占比 / 累计）、生效方式、影响面汇总（目标数 / 传输量等）。
// 纯展示：全部数据由调用方传入（impact / 向导状态 / 详情接口均可作来源），
// 便于向导第五步与后续详情 / 历史页复用。
import { useTranslation } from 'react-i18next'

import { Radio, RefreshCcw, Send } from 'lucide-react'

import { Badge, SummaryStrip, type SummaryItem } from '@beacon/ui'

import type { ActivationMethod } from '../../api/delivery-changes'
import { formatBytes } from './format'

/** 目标范围展示信息（picked 为已选对象显示名；全量模式传空数组） */
export interface OrchestrationScopeInfo {
  mode: 'all' | 'regions' | 'zones' | 'servers'
  picked: string[]
}

/** 批次规划行（count 为该批实际台数，占比 / 累计由控件换算） */
export interface OrchestrationBatch {
  batchNo: number
  count: number
}

interface OrchestrationPreviewProps {
  scope: OrchestrationScopeInfo
  targetTotal: number
  batches: OrchestrationBatch[]
  activationMethod: ActivationMethod
  /** 影响面汇总（复用 impact 数据） */
  fileTotal: number
  configScopeCount: number
  transferBytes: number
}

// 生效方式 → 图标
const ACTIVATION_ICON = { restart: RefreshCcw, hot_reload: Radio, push_only: Send } as const

export default function OrchestrationPreview({
  scope,
  targetTotal,
  batches,
  activationMethod,
  fileTotal,
  configScopeCount,
  transferBytes,
}: OrchestrationPreviewProps) {
  const { t } = useTranslation()
  const ActivationIcon = ACTIVATION_ICON[activationMethod]

  const impactItems: SummaryItem[] = [
    { label: t('delivery.preview.orchestration.impact.targetTotal'), value: targetTotal },
    { label: t('delivery.preview.orchestration.impact.fileTotal'), value: fileTotal },
    { label: t('delivery.preview.orchestration.impact.configScopeCount'), value: configScopeCount },
    { label: t('delivery.preview.orchestration.impact.transferBytes'), value: formatBytes(transferBytes) },
  ]

  // 占比 / 累计换算（目标 0 台时占比记 0，避免除零）
  let cumulative = 0
  const batchRows = batches.map((batch) => {
    cumulative += batch.count
    return {
      ...batch,
      percent: targetTotal > 0 ? Math.round((batch.count / targetTotal) * 100) : 0,
      cumulative,
    }
  })

  return (
    <div className="grid gap-4">
      {/* 目标范围 */}
      <section className="grid gap-1.5">
        <h4 className="text-[13px] font-semibold text-ink-2">
          {t('delivery.preview.orchestration.scopeTitle')}
        </h4>
        <div className="grid gap-2 rounded-lg border border-border px-3 py-2.5">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <Badge variant="outline">{t(`delivery.preview.orchestration.scopeMode.${scope.mode}`)}</Badge>
            <span className="tnum text-xs text-ink-2">
              {t('delivery.preview.orchestration.scopeTotal', { count: targetTotal })}
            </span>
          </div>
          {scope.picked.length > 0 && (
            <ul className="flex max-h-24 flex-wrap gap-1.5 overflow-y-auto">
              {scope.picked.map((name) => (
                <li
                  key={name}
                  className="rounded-md bg-surface-2 px-2 py-0.5 font-mono text-xs text-ink-2"
                >
                  {name}
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>

      {/* 批次规划 */}
      <section className="grid gap-1.5">
        <h4 className="text-[13px] font-semibold text-ink-2">
          {t('delivery.preview.orchestration.batchesTitle')}
        </h4>
        {batchRows.length === 0 ? (
          <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-sm text-muted-foreground">
            {t('delivery.preview.orchestration.batchesEmpty')}
          </p>
        ) : (
          <ul className="divide-y divide-border rounded-lg border border-border">
            {batchRows.map((row) => (
              <li key={row.batchNo} className="grid grid-cols-[4rem_1fr_1fr_1fr] items-center gap-2 px-3 py-1.5 text-sm">
                <span className="text-xs font-semibold text-ink-2">
                  {t('delivery.preview.orchestration.batchRow', { no: row.batchNo })}
                </span>
                <span className="tnum text-xs text-ink-1">
                  {t('delivery.preview.orchestration.batchCount', { count: row.count })}
                </span>
                <span className="tnum text-xs text-ink-3">
                  {t('delivery.preview.orchestration.batchPercent', { percent: row.percent })}
                </span>
                <span className="tnum text-xs text-ink-3">
                  {t('delivery.preview.orchestration.batchCumulative', { cumulative: row.cumulative })}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* 生效方式 */}
      <section className="grid gap-1.5">
        <h4 className="text-[13px] font-semibold text-ink-2">
          {t('delivery.preview.orchestration.activationTitle')}
        </h4>
        <p className="flex items-center gap-2 rounded-lg border border-border px-3 py-2 text-sm text-ink-1">
          <ActivationIcon className="size-4 shrink-0 text-brand" aria-hidden />
          {t(`delivery.preview.orchestration.activation.${activationMethod}`)}
        </p>
      </section>

      {/* 影响面汇总 */}
      <section className="grid gap-1.5">
        <h4 className="text-[13px] font-semibold text-ink-2">
          {t('delivery.preview.orchestration.impactTitle')}
        </h4>
        <SummaryStrip items={impactItems} />
      </section>
    </div>
  )
}
