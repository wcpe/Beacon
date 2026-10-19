// 全局运维指标入口：控制面状态点作为触发器，四个指标（Agent 在线 / 待确认 /
// 未处理告警 / 进行中变更单）收进悬浮面板，不再常驻占用页眉一行。
// 指标属于「异常时才看」的信息，常驻会挤占最宝贵的顶部空间；收进弹层后
// 页眉得以保持单层，同时仍能一键查看并跳转到对应页面。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import {
  Badge,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@beacon/ui'

import { useGlobalOpsMetrics } from './use-global-ops-metrics'

function formatMetric(value: number | null): string {
  if (value === null) {
    return '—'
  }
  return String(value)
}

export default function MetricsStrip({
  isRefreshing = false,
}: {
  isRefreshing?: boolean
}) {
  const { t } = useTranslation()
  // isFetching 覆盖「主动刷新 + 周期轮询」；只盯主动刷新会让轮询时的数值硬替换，
  // 用户看不出它刚刚更新过。
  const { metrics, isFetching } = useGlobalOpsMetrics()
  const busy = isFetching || isRefreshing

  const controlPlaneState =
    metrics.controlPlaneOnline === null ? 'unknown' : metrics.controlPlaneOnline ? 'online' : 'offline'
  const controlPlaneLabel =
    metrics.controlPlaneOnline === null
      ? t('common.metrics.controlPlaneUnknown')
      : metrics.controlPlaneOnline
        ? t('common.controlPlaneOnline')
        : t('common.metrics.controlPlaneOffline')
  const controlPlaneDotClass =
    metrics.controlPlaneOnline === null ? 'bg-ink-4' : metrics.controlPlaneOnline ? 'bg-ok' : 'bg-crit'

  // 汇总里只要有异常数值就提醒（告警或待确认 > 0），让用户在收起状态下也能察觉
  const pendingCount = metrics.pendingRegistrations ?? 0
  const openAlertCount = metrics.openAlerts ?? 0
  const hasAttention = openAlertCount > 0 || pendingCount > 0
  const controlPlaneDescription = [
    controlPlaneLabel,
    pendingCount > 0 ? `${t('common.metrics.pendingRegistrations')}: ${String(pendingCount)}` : null,
    openAlertCount > 0 ? `${t('common.metrics.openAlerts')}: ${String(openAlertCount)}` : null,
  ]
    .filter((part): part is string => part !== null)
    .join(' · ')

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={controlPlaneDescription}
          aria-busy={busy ? true : undefined}
          data-slot="control-plane-status"
          data-state={controlPlaneState}
          title={controlPlaneDescription}
          className={[
            'inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg px-2 text-[12px] text-ink-3 transition-[color,background-color,opacity] duration-200 motion-reduce:transition-none hover:bg-muted hover:text-ink-1 focus-visible:ring-2 focus-visible:ring-ring/30 focus-visible:outline-none',
            busy ? 'opacity-60' : '',
          ].join(' ')}
        >
          <span
            aria-hidden="true"
            className={[
              'size-2 shrink-0 rounded-full',
              controlPlaneDotClass,
              controlPlaneState === 'unknown' ? '' : 'animate-status-breathe',
            ].join(' ')}
          />
          <span data-slot="control-plane-status-label" className="hidden whitespace-nowrap xl:inline">
            {controlPlaneLabel}
          </span>
          {/* 有需要关注项时挂一个小红点，避免收进弹层后完全失去提醒能力 */}
          {hasAttention && (
            <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-crit" />
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        sideOffset={6}
        collisionPadding={12}
        aria-busy={isRefreshing ? true : undefined}
        className={[
          'w-64 max-w-[calc(100vw-1.5rem)] p-1.5 transition-opacity duration-200 motion-reduce:transition-none',
          isRefreshing ? 'opacity-60' : '',
        ].join(' ')}
        data-slot="metrics-menu"
      >
        <DropdownMenuLabel className="px-2 py-1.5 text-[11px] font-medium text-ink-4">
          {t('common.metrics.title')}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <MetricItem href="/servers" label={t('common.metrics.agentOnline')} value={metrics.agentOnline} />
        <MetricItem
          href="/servers"
          label={t('common.metrics.pendingRegistrations')}
          value={metrics.pendingRegistrations}
          highlight={(metrics.pendingRegistrations ?? 0) > 0}
        />
        <MetricItem
          href="/alert-events"
          label={t('common.metrics.openAlerts')}
          value={metrics.openAlerts}
          highlight={(metrics.openAlerts ?? 0) > 0}
        />
        <MetricItem
          href="/changes"
          label={t('common.metrics.activeChanges')}
          value={metrics.activeChanges}
        />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function MetricItem({
  href,
  label,
  value,
  highlight = false,
}: {
  href: string
  label: string
  value: number | null
  highlight?: boolean
}) {
  return (
    <DropdownMenuItem asChild className="cursor-pointer gap-2 rounded-lg px-2 py-1.5">
      <Link to={href} className="flex w-full items-center justify-between gap-2">
        <span className="min-w-0 truncate text-[12.5px] text-ink-2">{label}</span>
        {highlight ? (
          <Badge variant="destructive" className="h-5 shrink-0 px-1.5 text-[10px] font-semibold tabular-nums">
            {formatMetric(value)}
          </Badge>
        ) : (
          <span className="shrink-0 text-[12.5px] font-semibold tabular-nums text-ink-1">
            {formatMetric(value)}
          </span>
        )}
      </Link>
    </DropdownMenuItem>
  )
}
