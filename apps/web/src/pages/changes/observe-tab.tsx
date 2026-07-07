// 观察窗 Tab：GET observe → 当前批各目标健康分 / TPS / 告警（取序列末点为当前值）。
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, DataTable, type DataTableColumn } from '@beacon/ui'

import { fetchChangeObserve, type ChangeObserveResponse } from '../../api/delivery-changes'

interface ObserveTabProps {
  orderId: number
}

type ObserveRow = ChangeObserveResponse['targets'][number]

export default function ObserveTab({ orderId }: ObserveTabProps) {
  const { t } = useTranslation()

  // 观察窗数据用一次性 fetch（契约为轮询替代 SSE），保持 5s 刷新但测试不依赖轮询。
  const query = useQuery({
    queryKey: ['change-orders', 'observe', orderId],
    queryFn: () => fetchChangeObserve(orderId),
    refetchInterval: 5000,
  })

  const columns = useMemo<DataTableColumn<ObserveRow>[]>(
    () => [
      {
        header: t('delivery.changes.detail.observe.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.changes.detail.observe.columns.score'),
        cell: (row) => row.series.at(-1)?.score ?? '-',
      },
      {
        header: t('delivery.changes.detail.observe.columns.tps'),
        cell: (row) => row.series.at(-1)?.tps ?? '-',
      },
      {
        header: t('delivery.changes.detail.observe.columns.alerts'),
        cell: (row) => row.series.reduce((sum, p) => sum + p.alerts, 0),
      },
    ],
    [t],
  )

  const data = query.data
  const batchNo = data?.batchNo ?? null

  return (
    <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
      <section className="grid gap-3">
        {batchNo !== null && (
          <span className="text-sm text-muted-foreground">
            {t('delivery.changes.detail.observe.batchNo', { no: batchNo })}
          </span>
        )}
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
