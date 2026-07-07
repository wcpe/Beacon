// 进度时间线 Tab：GET events → 事件列表（变更单 / 批次 / 目标 + 状态 + 时间）。
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, Badge, DataTable, type DataTableColumn } from '@beacon/ui'

import { fetchChangeEvents, type ChangeOrderEvent } from '../../api/delivery-changes'
import { formatTime } from './format'

interface EventsTabProps {
  orderId: number
}

export default function EventsTab({ orderId }: EventsTabProps) {
  const { t } = useTranslation()

  // 事件用一次性 fetch（契约为轮询替代 SSE），保持 5s 刷新但测试不依赖轮询。
  const query = useQuery({
    queryKey: ['change-orders', 'events', orderId],
    queryFn: () => fetchChangeEvents(orderId),
    refetchInterval: 5000,
  })

  const labelOf = (event: ChangeOrderEvent): string => {
    if (event.type === 'order_status') {
      return t('delivery.changes.detail.events.order')
    }
    if (event.type === 'batch_status') {
      const no = event.batchNo === null ? '' : ` #${String(event.batchNo)}`
      return `${t('delivery.changes.detail.events.batch')}${no}`
    }
    return `${t('delivery.changes.detail.events.target')} ${event.serverId ?? ''}`
  }

  const columns = useMemo<DataTableColumn<ChangeOrderEvent>[]>(
    () => [
      { header: t('delivery.changes.detail.events.order'), cell: (row) => labelOf(row) },
      { header: '', cell: (row) => <Badge variant="outline">{row.status}</Badge> },
      { header: '', cell: (row) => formatTime(row.at) },
    ],
    // labelOf 依赖 t，随 t 稳定即可
    [t],
  )

  // 事件按 seq 逆序（最新在前）
  const rows = useMemo(() => {
    const events = query.data?.events ?? []
    return [...events].sort((a, b) => b.seq - a.seq)
  }, [query.data])

  return (
    <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(row) => String(row.seq)}
        emptyText={t('delivery.changes.detail.events.empty')}
        density="compact"
      />
    </AsyncSection>
  )
}
