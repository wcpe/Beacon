// 段 1 全局运维指标条（FR-188）：控制面在线 / Agent 在线 / 待确认 / 未处理告警 / 进行中变更单。
// 单项失败显示「—」；跨页固定，数据来自 useGlobalOpsMetrics。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { Badge } from '@beacon/ui'

import { useGlobalOpsMetrics } from './use-global-ops-metrics'

function formatMetric(value: number | null): string {
  if (value === null) {
    return '—'
  }
  return String(value)
}

export default function MetricsStrip() {
  const { t } = useTranslation()
  const { metrics } = useGlobalOpsMetrics()

  const onlineLabel =
    metrics.controlPlaneOnline === null
      ? t('common.metrics.controlPlaneUnknown')
      : metrics.controlPlaneOnline
        ? t('common.controlPlaneOnline')
        : t('common.metrics.controlPlaneOffline')

  const onlineVariant =
    metrics.controlPlaneOnline === true ? 'ok' : metrics.controlPlaneOnline === false ? 'crit' : 'secondary'

  return (
    <div
      data-slot="metrics-strip"
      className="flex h-9 shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-b border-border bg-surface-2/60 px-[22px] text-[12px] text-ink-3"
    >
      <Badge variant={onlineVariant} className="gap-1.5">
        <span className="size-[7px] rounded-full bg-current shadow-[0_0_0_3px_color-mix(in_srgb,currentColor_18%,transparent)]" />
        {onlineLabel}
      </Badge>

      <MetricLink href="/servers" label={t('common.metrics.agentOnline')} value={metrics.agentOnline} />
      <MetricLink
        href="/servers"
        label={t('common.metrics.pendingRegistrations')}
        value={metrics.pendingRegistrations}
      />
      <MetricLink href="/alert-events" label={t('common.metrics.openAlerts')} value={metrics.openAlerts} />
      <MetricLink href="/changes" label={t('common.metrics.activeChanges')} value={metrics.activeChanges} />
    </div>
  )
}

function MetricLink({
  href,
  label,
  value,
}: {
  href: string
  label: string
  value: number | null
}) {
  return (
    <Link
      to={href}
      className="inline-flex items-center gap-1.5 rounded-md px-1 py-0.5 text-ink-3 transition-colors hover:bg-surface-2 hover:text-ink-1"
    >
      <span className="text-ink-4">{label}</span>
      <span className="font-semibold tabular-nums text-ink-1">{formatMetric(value)}</span>
    </Link>
  )
}
