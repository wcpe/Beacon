// 向导第 5 步：影响预览与提交。标题输入 + 本单概要卡 + impact 影响面（目标台数 / 批次
// 划分 / 传输量）+「提交审批」去向说明。进入本步前父级已把草稿单同步到位（prepared 递增）。
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Info } from 'lucide-react'

import { AsyncSection, Input, Label, SummaryStrip, type SummaryItem } from '@beacon/ui'

import { fetchChangeImpact } from '../../api/delivery-changes'
import { formatBytes } from './format'
import {
  includesConfigs,
  includesFiles,
  type WizardBatch,
  type WizardConfigPick,
  type WizardContent,
  type WizardScope,
} from './wizard-state'

interface StepReviewProps {
  orderId: number | null
  // 每次进入本步 +1，作为 impact 重新拉取的 key（返回改范围后再进来要重算）
  prepared: number
  content: WizardContent
  source: string
  fileCount: number
  picks: WizardConfigPick[]
  scope: WizardScope
  batch: WizardBatch
  title: string
  onTitleChange: (title: string) => void
}

export default function WizardStepReview({
  orderId,
  prepared,
  content,
  source,
  fileCount,
  picks,
  scope,
  batch,
  title,
  onTitleChange,
}: StepReviewProps) {
  const { t } = useTranslation()

  const impactQuery = useQuery({
    queryKey: ['change-orders', 'wizard-impact', orderId, prepared],
    queryFn: () => fetchChangeImpact(orderId ?? 0, 1, 5),
    enabled: orderId !== null,
  })
  const summary = impactQuery.data?.summary

  const impactItems = useMemo<SummaryItem[]>(() => {
    if (!summary) {
      return []
    }
    return [
      { label: t('delivery.changes.detail.impact.targetTotal'), value: summary.targetTotal },
      { label: t('delivery.changes.detail.impact.fileTotal'), value: summary.fileTotal },
      { label: t('delivery.changes.detail.impact.transferBytes'), value: formatBytes(summary.transferBytes) },
      { label: t('delivery.changes.detail.impact.configScope'), value: summary.configScopeCount },
    ]
  }, [summary, t])

  // 概要字段：交付内容 / 模板源 / 文件差异 / 配置项 / 范围 / 批次 / 生效方式
  const rows: { label: string; value: string }[] = [
    { label: t('delivery.changes.wizard.review.fields.content'), value: t(`delivery.changes.wizard.content.${content}.title`) },
    {
      label: t('delivery.changes.wizard.review.fields.source'),
      value: includesFiles(content) ? source : t('delivery.changes.wizard.review.noSource'),
    },
    {
      label: t('delivery.changes.wizard.review.fields.files'),
      value: includesFiles(content)
        ? t('delivery.changes.wizard.review.filesCount', { count: fileCount })
        : t('delivery.changes.wizard.review.noSource'),
    },
    {
      label: t('delivery.changes.wizard.review.fields.configs'),
      value: includesConfigs(content)
        ? t('delivery.changes.wizard.review.configsCount', { count: picks.length })
        : t('delivery.changes.wizard.review.noSource'),
    },
    {
      label: t('delivery.changes.wizard.review.fields.scope'),
      value: t(`delivery.changes.wizard.review.scopeText.${scope.mode}`, { count: scopePickCount(scope) }),
    },
    {
      label: t('delivery.changes.wizard.review.fields.batch'),
      value:
        batch.mode === 'single'
          ? t('delivery.changes.wizard.review.batchSingleText')
          : t('delivery.changes.wizard.review.batchStagedText', { count: batch.perBatch }),
    },
    {
      label: t('delivery.changes.wizard.review.fields.activation'),
      value: includesFiles(content)
        ? t('delivery.changes.wizard.review.activationRestart')
        : t('delivery.changes.wizard.review.activationHotReload'),
    },
  ]

  return (
    <div className="grid gap-4">
      <p className="text-sm text-muted-foreground">{t('delivery.changes.wizard.review.lead')}</p>

      <div className="grid gap-1.5">
        <Label htmlFor="wizard-order-title">{t('delivery.changes.wizard.review.titleLabel')}</Label>
        <Input
          id="wizard-order-title"
          value={title}
          onChange={(e) => {
            onTitleChange(e.target.value)
          }}
          placeholder={t('delivery.changes.wizard.review.titlePlaceholder')}
        />
      </div>

      {/* 本单概要 */}
      <div className="grid gap-1.5">
        <h4 className="text-[13px] font-semibold text-ink-2">{t('delivery.changes.wizard.review.summaryTitle')}</h4>
        <dl className="grid grid-cols-1 gap-x-6 gap-y-1.5 rounded-xl border border-border bg-surface-2 px-4 py-3 text-sm sm:grid-cols-2">
          {rows.map((row) => (
            <div key={row.label} className="flex items-baseline justify-between gap-3">
              <dt className="shrink-0 text-ink-3">{row.label}</dt>
              <dd className="min-w-0 truncate text-right text-ink-1">{row.value}</dd>
            </div>
          ))}
        </dl>
      </div>

      {/* 影响面预览 */}
      <div className="grid gap-1.5">
        <h4 className="text-[13px] font-semibold text-ink-2">{t('delivery.changes.wizard.review.impactTitle')}</h4>
        <AsyncSection isLoading={impactQuery.isLoading} isError={impactQuery.isError} error={impactQuery.error}>
          {summary && (
            <div className="grid gap-2">
              <SummaryStrip items={impactItems} />
              {summary.targetTotal === 0 ? (
                <p className="text-sm text-warn">{t('delivery.changes.wizard.review.impactEmpty')}</p>
              ) : (
                <ul className="flex flex-wrap gap-2 text-sm">
                  {summary.batches.map((b) => (
                    <li key={b.batchNo} className="tnum rounded-lg border border-brand-100 bg-brand-50 px-3 py-1 text-brand-600">
                      {t('delivery.changes.detail.impact.batchLine', { no: b.batchNo, count: b.count })}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </AsyncSection>
      </div>

      {/* 提交去向说明 */}
      <p className="flex items-start gap-2 rounded-lg border border-border bg-surface-2 px-3 py-2.5 text-xs leading-relaxed text-ink-2">
        <Info className="mt-0.5 size-3.5 shrink-0 text-brand" aria-hidden />
        {t('delivery.changes.wizard.review.submitNote')}
      </p>
    </div>
  )
}

// 当前范围模式下已选项数（全量返回 0，仅用于插值）
function scopePickCount(scope: WizardScope): number {
  switch (scope.mode) {
    case 'all':
      return 0
    case 'regions':
      return scope.regions.length
    case 'zones':
      return scope.zones.length
    case 'servers':
      return scope.servers.length
  }
}
