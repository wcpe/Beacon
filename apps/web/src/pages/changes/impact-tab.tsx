// 影响预览 Tab：GET impact → 汇总条（目标/文件/字节/传输/配置作用域）+ 批次划分 + 逐目标分页表。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  DataTable,
  SummaryStrip,
  type DataTableColumn,
  type SummaryItem,
} from '@beacon/ui'

import { fetchChangeImpact } from '../../api/delivery-changes'
import Pager from '../../features/delivery/pager'
import { formatBytes } from './format'

const PAGE_SIZE = 20

interface ImpactTabProps {
  orderId: number
}

interface TargetRow {
  serverId: string
  online: boolean
  level: string
  addCount: number
  updateCount: number
  deleteCount: number
  skipCount: number
}

export default function ImpactTab({ orderId }: ImpactTabProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)

  const query = useQuery({
    queryKey: ['change-orders', 'impact', orderId, page],
    queryFn: () => fetchChangeImpact(orderId, page, PAGE_SIZE),
    placeholderData: keepPreviousData,
  })

  const summary = query.data?.summary
  const summaryItems = useMemo<SummaryItem[]>(() => {
    if (!summary) {
      return []
    }
    return [
      { label: t('delivery.changes.detail.impact.targetTotal'), value: summary.targetTotal },
      { label: t('delivery.changes.detail.impact.fileTotal'), value: summary.fileTotal },
      { label: t('delivery.changes.detail.impact.totalBytes'), value: formatBytes(summary.totalBytes) },
      {
        label: t('delivery.changes.detail.impact.transferBytes'),
        value: formatBytes(summary.transferBytes),
      },
      {
        label: t('delivery.changes.detail.impact.configScope'),
        value: summary.configScopeCount,
      },
    ]
  }, [summary, t])

  const columns = useMemo<DataTableColumn<TargetRow>[]>(
    () => [
      {
        header: t('delivery.changes.detail.impact.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.changes.detail.impact.columns.online'),
        cell: (row) =>
          row.online ? (
            <Badge variant="ok" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t('delivery.changes.detail.impact.online')}
            </Badge>
          ) : (
            <Badge variant="off" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t('delivery.changes.detail.impact.offline')}
            </Badge>
          ),
      },
      { header: t('delivery.changes.detail.impact.columns.level'), cell: (row) => row.level },
      { header: t('delivery.changes.detail.impact.columns.add'), cell: (row) => row.addCount },
      { header: t('delivery.changes.detail.impact.columns.update'), cell: (row) => row.updateCount },
      { header: t('delivery.changes.detail.impact.columns.delete'), cell: (row) => row.deleteCount },
      { header: t('delivery.changes.detail.impact.columns.skip'), cell: (row) => row.skipCount },
    ],
    [t],
  )

  const targetsTotal = query.data?.targets.total ?? 0

  return (
    <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
      <section className="grid gap-4">
        <SummaryStrip items={summaryItems} />

        {/* 批次划分 */}
        {summary && summary.batches.length > 0 && (
          <div className="grid gap-1.5">
            <h4 className="text-[13px] font-semibold text-ink-2">
              {t('delivery.changes.detail.impact.batchesTitle')}
            </h4>
            <ul className="flex flex-wrap gap-2 text-sm">
              {summary.batches.map((b) => (
                <li
                  key={b.batchNo}
                  className="rounded-lg border border-brand-100 bg-brand-50 px-3 py-1 text-brand-600 tnum"
                >
                  {t('delivery.changes.detail.impact.batchLine', { no: b.batchNo, count: b.count })}
                </li>
              ))}
            </ul>
          </div>
        )}

        {/* 逐目标分页表 */}
        <div className="grid gap-2">
          <h4 className="text-[13px] font-semibold text-ink-2">
            {t('delivery.changes.detail.impact.targetsTitle')}
          </h4>
          <DataTable
            columns={columns}
            rows={query.data?.targets.items}
            rowKey={(row) => row.serverId}
            density="compact"
          />
          <Pager
            page={page}
            total={targetsTotal}
            pageSize={PAGE_SIZE}
            onPageChange={setPage}
          />
        </div>
      </section>
    </AsyncSection>
  )
}
