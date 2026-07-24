// 观察窗 Tab：GET observe → 观察说明 + 汇总（均值 / 最差健康分、均值 / 最低 TPS、
// 告警总数）+ 当前批逐目标表（取序列末点为当前值）；批次推进期间可手动刷新。
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { RefreshCcw } from 'lucide-react'

import {
  AsyncSection,
  Button,
  DataTable,
  SummaryStrip,
  type DataTableColumn,
  type SummaryItem,
} from '@beacon/ui'

import { fetchChangeObserve, type ChangeObserveResponse } from '../../api/delivery-changes'

interface ObserveTabProps {
  orderId: number
}

type ObserveRow = ChangeObserveResponse['targets'][number]

// 逐目标当前值（序列末点）+ 全窗告警数
interface ObserveCurrent {
  score: number | null
  tps: number | null
  alerts: number
}

function currentOf(row: ObserveRow): ObserveCurrent {
  const last = row.series.at(-1)
  return {
    score: last?.score ?? null,
    tps: last?.tps ?? null,
    alerts: row.series.reduce((sum, p) => sum + p.alerts, 0),
  }
}

export default function ObserveTab({ orderId }: ObserveTabProps) {
  const { t } = useTranslation()

  // 观察窗数据用一次性 fetch（契约为轮询替代 SSE），保持 5s 刷新但测试不依赖轮询。
  const query = useQuery({
    queryKey: ['change-orders', 'observe', orderId],
    queryFn: () => fetchChangeObserve(orderId),
    refetchInterval: 5000,
  })

  const data = query.data
  const batchNo = data?.batchNo ?? null

  // 汇总：均值 / 最差健康分、均值 / 最低 TPS、告警总数（无数据不渲染汇总条）
  const summaryItems = useMemo<SummaryItem[]>(() => {
    const targets = data?.targets ?? []
    const currents = targets.map(currentOf)
    const scores = currents.map((c) => c.score).filter((v): v is number => v !== null)
    const tpsValues = currents.map((c) => c.tps).filter((v): v is number => v !== null)
    if (scores.length === 0 && tpsValues.length === 0) {
      return []
    }
    const avg = (values: number[]): number => values.reduce((sum, v) => sum + v, 0) / values.length
    const worstScore = scores.length > 0 ? Math.min(...scores) : null
    const alertTotal = currents.reduce((sum, c) => sum + c.alerts, 0)
    return [
      {
        label: t('delivery.changes.detail.observe.summary.avgScore'),
        value: scores.length > 0 ? Math.round(avg(scores)) : '-',
      },
      {
        label: t('delivery.changes.detail.observe.summary.worstScore'),
        value: worstScore ?? '-',
        tone: worstScore === null ? 'muted' : worstScore >= 80 ? 'success' : worstScore >= 50 ? 'warning' : 'danger',
      },
      {
        label: t('delivery.changes.detail.observe.summary.avgTps'),
        value: tpsValues.length > 0 ? avg(tpsValues).toFixed(1) : '-',
      },
      {
        label: t('delivery.changes.detail.observe.summary.minTps'),
        value: tpsValues.length > 0 ? Math.min(...tpsValues).toFixed(1) : '-',
      },
      {
        label: t('delivery.changes.detail.observe.summary.alertTotal'),
        value: alertTotal,
        tone: alertTotal > 0 ? 'warning' : 'default',
      },
    ]
  }, [data, t])

  const columns = useMemo<DataTableColumn<ObserveRow>[]>(
    () => [
      {
        header: t('delivery.changes.detail.observe.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.changes.detail.observe.columns.score'),
        cell: (row) => currentOf(row).score ?? '-',
      },
      {
        header: t('delivery.changes.detail.observe.columns.tps'),
        cell: (row) => currentOf(row).tps ?? '-',
      },
      {
        header: t('delivery.changes.detail.observe.columns.alerts'),
        cell: (row) => currentOf(row).alerts,
      },
    ],
    [t],
  )

  return (
    <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
      <section className="grid gap-3">
        {/* 观察窗说明：确认放行前先看这些指标 */}
        <p className="rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs leading-relaxed text-ink-2">
          {t('delivery.changes.detail.observe.lead')}
        </p>

        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-sm text-muted-foreground">
            {batchNo !== null && t('delivery.changes.detail.observe.batchNo', { no: batchNo })}
          </span>
          <Button
            size="sm"
            variant="outline"
            disabled={query.isFetching}
            onClick={() => {
              void query.refetch()
            }}
          >
            <RefreshCcw className="size-3.5" />
            {t('delivery.changes.detail.observe.refresh')}
          </Button>
        </div>

        <SummaryStrip items={summaryItems} />

        <DataTable
          columns={columns}
          rows={data?.targets}
          rowKey={(row) => row.serverId}
          emptyText={t('delivery.changes.detail.observe.empty')}
          density="compact"
        />
      </section>
    </AsyncSection>
  )
}
