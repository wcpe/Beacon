// 指标时序 + 多服对比：所选服务器的指标时序（sparkline）与最新 / 均值 / 峰值。
// 指标（CPU / TPS / 内存 / 在线人数）与步长可切换；仅聚合数字，不涉及玩家名单。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  IconStat,
  MiniSparkline,
  SectionHeader,
  TableSkeleton,
} from '@beacon/ui'
import type { MetricsSeriesPoint } from '@beacon/devmock'

import { fetchMetricsSeries } from '../../api/metrics'
import FilterSelect from '../../features/observability/filter-select'

// 可选指标 → MetricsSeriesPoint 字段与展示
const METRICS = ['cpu', 'tps', 'mem', 'online'] as const
type MetricKey = (typeof METRICS)[number]

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

  return (
    <section className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <SectionHeader title={t('observability.serviceAnalysis.seriesTitle')} />
        <div className="flex flex-wrap items-center gap-2">
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
        </div>
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={2} rows={serverIds.length || 2} />}
      >
        {series.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('observability.serviceAnalysis.noSeries')}</p>
        ) : (
          <div className="grid gap-3">
            {series.map((s) => {
              const values = s.points.map((p) => valueOf(p, metric))
              const latest = values.at(-1) ?? 0
              const avg = values.length === 0 ? 0 : values.reduce((sum, v) => sum + v, 0) / values.length
              const peak = values.length === 0 ? 0 : Math.max(...values)
              return (
                <div key={s.serverId} className="grid gap-2 rounded-md border px-3 py-3">
                  <div className="flex items-center justify-between">
                    <span className="font-mono text-sm">{s.serverId}</span>
                    <span className="text-xs text-muted-foreground">{metricLabel[metric]}</span>
                  </div>
                  <MiniSparkline values={values} color="var(--primary)" height={40} />
                  <div className="flex flex-wrap gap-6">
                    <IconStat
                      icon={null}
                      label={t('observability.serviceAnalysis.latest')}
                      value={latest.toFixed(1)}
                    />
                    <IconStat
                      icon={null}
                      label={t('observability.serviceAnalysis.avg')}
                      value={avg.toFixed(1)}
                    />
                    <IconStat
                      icon={null}
                      label={t('observability.serviceAnalysis.peak')}
                      value={peak.toFixed(1)}
                    />
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </AsyncSection>
    </section>
  )
}

// 指标下拉（原生 select，label 走 i18n「指标」，便于测试定位）
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
    <select
      aria-label={t('observability.serviceAnalysis.metric')}
      value={metric}
      onChange={(e) => {
        onChange(e.target.value as MetricKey)
      }}
      className="h-9 rounded-md border bg-background px-2 text-sm"
    >
      {METRICS.map((m) => (
        <option key={m} value={m}>
          {metricLabel[m]}
        </option>
      ))}
    </select>
  )
}
