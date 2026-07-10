// 指标时序 + 多服对比：所选服务器的指标时序（sparkline）与最新 / 均值 / 峰值。
// 指标（CPU / TPS / 内存 / 在线人数）与步长可切换；仅聚合数字，不涉及玩家名单。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Activity, ArrowUpRight, Gauge, MemoryStick, TrendingUp, Users } from 'lucide-react'

import {
  AsyncSection,
  Card,
  CardContent,
  IconStat,
  MiniSparkline,
  SectionHeader,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  TableSkeleton,
} from '@beacon/ui'
import type { MetricsSeriesPoint } from '@beacon/contracts'

import { fetchMetricsSeries } from '../../api/metrics'
import FilterSelect from '../../features/observability/filter-select'

// 可选指标 → MetricsSeriesPoint 字段与展示
const METRICS = ['cpu', 'tps', 'mem', 'online'] as const
type MetricKey = (typeof METRICS)[number]

// 指标 → 卡片图标（lucide），用于时序卡右上角。
const METRIC_ICON: Record<MetricKey, typeof Gauge> = {
  cpu: Gauge,
  tps: Activity,
  mem: MemoryStick,
  online: Users,
}

interface SeriesPanelProps {
  // 已选 serverId（顺序稳定）
  serverIds: string[]
  metric: MetricKey
  onMetricChange: (metric: MetricKey) => void
  step: number
  onStepChange: (step: number) => void
}

// 取某指标在某点上的值
function valueOf(point: MetricsSeriesPoint, metric: MetricKey): number {
  switch (metric) {
    case 'cpu':
      return point.cpuPctAvg
    case 'tps':
      return point.tpsAvg
    case 'mem':
      return point.memUsedMbAvg
    case 'online':
      return point.onlineAvg
  }
}

export default function SeriesPanel({
  serverIds,
  metric,
  onMetricChange,
  step,
  onStepChange,
}: SeriesPanelProps) {
  const { t } = useTranslation()
  const joined = serverIds.join(',')

  const query = useQuery({
    queryKey: ['service-analysis', 'series', joined, step],
    queryFn: () => fetchMetricsSeries({ serverId: joined, step }),
    enabled: serverIds.length > 0,
  })

  const metricLabel: Record<MetricKey, string> = {
    cpu: t('observability.serviceAnalysis.metricCpu'),
    tps: t('observability.serviceAnalysis.metricTps'),
    mem: t('observability.serviceAnalysis.metricMem'),
    online: t('observability.serviceAnalysis.metricOnline'),
  }

  const series = useMemo(() => query.data?.series ?? [], [query.data])
  const MetricIcon = METRIC_ICON[metric]

  return (
    <section className="grid gap-3">
      {/* 指标 / 步长控制条吸顶常驻：对比入口始终可见，选服后无需下滚 */}
      <div className="sticky top-11 z-10 -mx-1 bg-surface-2/95 px-1 py-1 backdrop-blur supports-backdrop-filter:bg-surface-2/80">
        <SectionHeader
          icon={<TrendingUp className="size-4" />}
          title={t('observability.serviceAnalysis.seriesTitle')}
          count={serverIds.length > 1 ? t('observability.serviceAnalysis.selectedCount', { count: serverIds.length }) : undefined}
          actions={
            <>
              <FilterSelectMetric metric={metric} onChange={onMetricChange} metricLabel={metricLabel} />
              <FilterSelect
                label={t('observability.serviceAnalysis.step')}
                value={String(step)}
                options={[
                  { value: '60', label: t('observability.serviceAnalysis.step1m') },
                  { value: '300', label: t('observability.serviceAnalysis.step5m') },
                ]}
                onChange={(value) => {
                  onStepChange(Number.parseInt(value, 10))
                }}
              />
            </>
          }
        />
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={2} rows={serverIds.length || 2} />}
      >
        {series.length === 0 ? (
          <p className="rounded-xl border border-dashed border-border bg-card/60 px-4 py-8 text-sm text-ink-3">
            {t('observability.serviceAnalysis.noSeries')}
          </p>
        ) : (
          <div className="grid gap-3 md:grid-cols-2">
            {series.map((s) => {
              const values = s.points.map((p) => valueOf(p, metric))
              const latest = values.at(-1) ?? 0
              const avg = values.length === 0 ? 0 : values.reduce((sum, v) => sum + v, 0) / values.length
              const peak = values.length === 0 ? 0 : Math.max(...values)
              return (
                <Card key={s.serverId}>
                  <CardContent className="grid gap-3">
                    <div className="flex items-center gap-2.5">
                      <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-brand-50 text-brand">
                        <MetricIcon className="size-[15px]" />
                      </span>
                      <span className="min-w-0 flex-1 truncate font-mono text-[13px] font-semibold text-ink-1">
                        {s.serverId}
                      </span>
                      <span className="text-[11px] font-medium text-ink-4">{metricLabel[metric]}</span>
                    </div>
                    <div className="rounded-lg bg-secondary/50 px-2 pt-2">
                      <MiniSparkline values={values} color="var(--brand)" height={48} />
                    </div>
                    <div className="flex flex-wrap gap-5 border-t border-border pt-3">
                      <IconStat
                        icon={<ArrowUpRight className="size-4" />}
                        label={t('observability.serviceAnalysis.latest')}
                        value={latest.toFixed(1)}
                      />
                      <IconStat
                        icon={<Activity className="size-4" />}
                        label={t('observability.serviceAnalysis.avg')}
                        value={avg.toFixed(1)}
                      />
                      <IconStat
                        icon={<TrendingUp className="size-4" />}
                        label={t('observability.serviceAnalysis.peak')}
                        value={peak.toFixed(1)}
                      />
                    </div>
                  </CardContent>
                </Card>
              )
            })}
          </div>
        )}
      </AsyncSection>
    </section>
  )
}

// 指标下拉（组件库 Select，label 走 i18n「指标」，便于测试定位）
function FilterSelectMetric({
  metric,
  onChange,
  metricLabel,
}: {
  metric: MetricKey
  onChange: (metric: MetricKey) => void
  metricLabel: Record<MetricKey, string>
}) {
  const { t } = useTranslation()
  return (
    <Select
      value={metric}
      onValueChange={(next) => {
        onChange(next as MetricKey)
      }}
    >
      <SelectTrigger className="h-9 w-32" aria-label={t('observability.serviceAnalysis.metric')}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {METRICS.map((m) => (
          <SelectItem key={m} value={m}>
            {metricLabel[m]}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
