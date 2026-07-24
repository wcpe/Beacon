// 向导第 5 步：预览与提交。标题输入 + 「简单 / 详细」两种概要模式：
// 简单 = 人话概要句 + 关键数字 KPI；详细 = 变更内容预览 + 完整编排预览（共享控件，
// 数据来自草稿详情与 impact）。底部保留「审批 → 启动 → 逐批放行」流程说明。
// 进入本步前父级已把草稿单同步到位（prepared 递增触发重取）。
import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { Info } from 'lucide-react'

import { AsyncSection, Input, Label, SummaryStrip, cn, type SummaryItem } from '@beacon/ui'

import { fetchZoneTree } from '../../api/cluster'
import { fetchChangeImpact, fetchChangeOrder, type ChangeOrderItem } from '../../api/delivery-changes'
import ChangePreview, { type ConfigChangeLabel } from '../../features/delivery/change-preview'
import ConfigVersionDiff from '../../features/delivery/config-version-diff'
import FileDiffPreview from '../../features/delivery/file-diff-preview'
import OrchestrationPreview from '../../features/delivery/orchestration-preview'
import { formatBytes } from './format'
import {
  type WizardBatch,
  type WizardConfigPick,
  type WizardScope,
} from './wizard-state'

interface StepReviewProps {
  orderId: number | null
  // 每次进入本步 +1，作为 impact / 详情重新拉取的 key（返回改范围后再进来要重算）
  prepared: number
  namespaceId: number
  picks: WizardConfigPick[]
  scope: WizardScope
  batch: WizardBatch
  title: string
  onTitleChange: (title: string) => void
}

export default function WizardStepReview({
  orderId,
  prepared,
  namespaceId,
  picks,
  scope,
  batch,
  title,
  onTitleChange,
}: StepReviewProps) {
  const { t } = useTranslation()
  // 概要模式：简单（默认，人话 + KPI）/ 详细（两个完整预览控件）
  const [mode, setMode] = useState<'simple' | 'detailed'>('simple')

  const impactQuery = useQuery({
    queryKey: ['change-orders', 'wizard-impact', orderId, prepared],
    queryFn: () => fetchChangeImpact(orderId ?? 0, 1, 5),
    enabled: orderId !== null,
  })
  const summary = impactQuery.data?.summary

  // 草稿详情：变更内容预览的 items 真源（PATCH 后含文件差异 + 配置变更两类项）
  const detailQuery = useQuery({
    queryKey: ['change-orders', 'wizard-review-detail', orderId, prepared],
    queryFn: () => fetchChangeOrder(orderId ?? 0),
    enabled: orderId !== null,
  })

  // 结构树：把已选大区 / 小区 id 翻译成名称（与范围步共用查询缓存）
  const treeQuery = useQuery({
    queryKey: ['change-orders', 'wizard-zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    enabled: scope.mode === 'regions' || scope.mode === 'zones',
  })
  const pickedNames = resolvePickedNames(scope, treeQuery.data)

  // 配置项展示信息：按目标版本 id 对回向导里已解析的 picks
  const configLabelOf = (item: ChangeOrderItem): ConfigChangeLabel | null => {
    const pick = picks.find((p) => p.toVersionId === item.configToVersionId)
    return pick === undefined
      ? null
      : { fileName: pick.fileName, fromVersionNo: pick.fromVersionNo, toVersionNo: pick.toVersionNo }
  }

  const renderConfigDiff = (item: ChangeOrderItem): ReactNode => {
    if (item.configToVersionId === null) {
      return null
    }
    const label = configLabelOf(item)
    return (
      <ConfigVersionDiff
        fromVersionId={item.configFromVersionId}
        toVersionId={item.configToVersionId}
        fromLabel={
          label?.fromVersionNo == null
            ? t('delivery.preview.versionDiff.fromEmpty')
            : t('delivery.preview.versionDiff.fromLabel', { no: label.fromVersionNo })
        }
        toLabel={t('delivery.preview.versionDiff.toLabel', { no: label === null ? '-' : label.toVersionNo })}
      />
    )
  }

  // 文件内容预览懒加载（orderId 就绪时提供）：变更内容预览的文件行可点开查看前后内容
  const renderFileDiff = (item: ChangeOrderItem): ReactNode =>
    orderId === null ? null : <FileDiffPreview orderId={orderId} item={item} />

  // 生效方式以已同步的草稿详情为准，详情未就绪时沿用向导默认值「仅推送」。
  const activationMethod = detailQuery.data?.activationMethod ?? 'push_only'

  const kpiItems: SummaryItem[] = summary
    ? [
        { label: t('delivery.changes.wizard.review.simple.kpiTargets'), value: summary.targetTotal },
        { label: t('delivery.changes.wizard.review.simple.kpiBatches'), value: summary.batches.length },
        { label: t('delivery.changes.wizard.review.simple.kpiFiles'), value: summary.fileTotal },
        { label: t('delivery.changes.wizard.review.simple.kpiConfigs'), value: summary.configScopeCount },
        {
          label: t('delivery.changes.wizard.review.simple.kpiTransfer'),
          value: formatBytes(summary.transferBytes),
        },
      ]
    : []

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

      {/* 概要模式切换 */}
      <div
        className="flex w-fit overflow-hidden rounded-md border border-border"
        role="group"
        aria-label={t('delivery.changes.wizard.review.modeLabel')}
      >
        {(['simple', 'detailed'] as const).map((value) => (
          <button
            key={value}
            type="button"
            aria-pressed={mode === value}
            onClick={() => {
              setMode(value)
            }}
            className={cn(
              'px-3 py-1 text-xs transition-colors',
              mode === value ? 'bg-brand text-white' : 'bg-background text-ink-2 hover:bg-muted',
            )}
          >
            {t(`delivery.changes.wizard.review.mode.${value}`)}
          </button>
        ))}
      </div>

      <AsyncSection
        isLoading={impactQuery.isLoading || detailQuery.isLoading}
        isError={impactQuery.isError || detailQuery.isError}
        error={impactQuery.error ?? detailQuery.error}
      >
        {summary && (
          <div className="grid gap-3">
            {summary.targetTotal === 0 && (
              <p className="text-sm text-warn">{t('delivery.changes.wizard.review.impactEmpty')}</p>
            )}

            {mode === 'simple' ? (
              <div className="grid gap-3">
                {/* 人话概要句 */}
                <p className="rounded-xl border border-border bg-surface-2 px-4 py-3 text-sm leading-relaxed text-ink-1">
                  {buildSentence(t, {
                    scope,
                    pickedNames,
                    batchMode: batch.mode,
                    targetTotal: summary.targetTotal,
                    batchCount: summary.batches.length,
                    fileTotal: summary.fileTotal,
                    configCount: summary.configScopeCount,
                    activationMethod,
                  })}
                </p>
                {/* 关键数字 KPI */}
                <SummaryStrip items={kpiItems} />
              </div>
            ) : (
              <div className="grid gap-4">
                <ChangePreview
                  items={detailQuery.data?.items ?? []}
                  configLabelOf={configLabelOf}
                  renderConfigDiff={renderConfigDiff}
                  renderFileDiff={renderFileDiff}
                />
                <OrchestrationPreview
                  scope={{ mode: scope.mode, picked: pickedNames }}
                  targetTotal={summary.targetTotal}
                  batches={summary.batches}
                  activationMethod={activationMethod}
                  fileTotal={summary.fileTotal}
                  configScopeCount={summary.configScopeCount}
                  transferBytes={summary.transferBytes}
                />
              </div>
            )}
          </div>
        )}
      </AsyncSection>

      {/* 提交去向说明：审批 → 启动 → 逐批放行 */}
      <p className="flex items-start gap-2 rounded-lg border border-border bg-surface-2 px-3 py-2.5 text-xs leading-relaxed text-ink-2">
        <Info className="mt-0.5 size-3.5 shrink-0 text-brand" aria-hidden />
        {t('delivery.changes.wizard.review.submitNote')}
      </p>
    </div>
  )
}

// 已选对象显示名：大区 / 小区从结构树翻名（小区带大区前缀），单服即 serverId，全量为空
function resolvePickedNames(
  scope: WizardScope,
  tree: { clusters: { regions: { id: number; name: string; zones: { id: number; name: string }[] }[] }[] } | undefined,
): string[] {
  if (scope.mode === 'servers') {
    return scope.servers
  }
  if (scope.mode === 'all' || tree === undefined) {
    return []
  }
  const regions = tree.clusters.flatMap((cluster) => cluster.regions)
  if (scope.mode === 'regions') {
    return regions.filter((region) => scope.regions.includes(region.id)).map((region) => region.name)
  }
  return regions.flatMap((region) =>
    region.zones
      .filter((zone) => scope.zones.includes(zone.id))
      .map((zone) => `${region.name} / ${zone.name}`),
  )
}

// 拼「人话概要」：将向 {范围} 共 N 台服务器分 B 批推送 {载荷}，生效方式：{方式}。
function buildSentence(
  t: TFunction,
  input: {
    scope: WizardScope
    pickedNames: string[]
    batchMode: WizardBatch['mode']
    targetTotal: number
    batchCount: number
    fileTotal: number
    configCount: number
    activationMethod: string
  },
): string {
  const base = 'delivery.changes.wizard.review.simple'
  // 范围片段：全量 / 「名称…」（超 3 个折叠为 等 N 个）
  let scopeText: string
  if (input.scope.mode === 'all') {
    scopeText = t(`${base}.scopeAll`)
  } else {
    const kind = t(`${base}.kind.${input.scope.mode}`)
    const names = input.pickedNames.slice(0, 3).join('、')
    scopeText =
      input.pickedNames.length > 3
        ? t(`${base}.scopeNamedMore`, { kind, names, count: input.pickedNames.length })
        : t(`${base}.scopeNamed`, { kind, names })
  }
  // 载荷片段：文件 / 配置 / 两者
  let payload: string
  if (input.fileTotal > 0 && input.configCount > 0) {
    payload = t(`${base}.payloadBoth`, { files: input.fileTotal, configs: input.configCount })
  } else if (input.configCount > 0) {
    payload = t(`${base}.payloadConfigs`, { count: input.configCount })
  } else {
    payload = t(`${base}.payloadFiles`, { count: input.fileTotal })
  }
  const activation = t(`${base}.activation.${input.activationMethod}`)
  const sentenceKey =
    input.batchMode === 'single' || input.batchCount <= 1 ? 'sentenceSingle' : 'sentenceStaged'
  return t(`${base}.${sentenceKey}`, {
    scope: scopeText,
    total: input.targetTotal,
    batches: input.batchCount,
    payload,
    activation,
  })
}
