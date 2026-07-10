// 交付历史详情：溯任务 / 批次 / 单服状态 + 整单回滚（共享 order-rollback：高摩擦确认 +
// 残留失败人工结束 + 回滚进度横幅）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { Layers, Server } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  SectionHeader,
  type DataTableColumn,
} from '@beacon/ui'
import type { ChangeBatch, ChangeTarget } from '@beacon/devmock'

import Pager from '../../features/delivery/pager'
import { fetchChangeOrder, fetchChangeTargets } from '../../api/delivery-changes'
import { OrderRollbackActions, RollbackBanner } from '../../features/delivery/order-rollback'
import { BatchStatusBadge, TargetStatusBadge } from '../../features/delivery/status-badges'
import StatusBadge from './status-badge'

const TARGET_PAGE_SIZE = 20

interface DetailViewProps {
  orderId: number
}

export default function DetailView({ orderId }: DetailViewProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)

  const detailQuery = useQuery({
    queryKey: ['change-orders', 'detail', orderId],
    queryFn: () => fetchChangeOrder(orderId),
  })

  const targetsQuery = useQuery({
    queryKey: ['change-orders', 'targets', orderId, page],
    queryFn: () => fetchChangeTargets(orderId, { page, pageSize: TARGET_PAGE_SIZE }),
    placeholderData: keepPreviousData,
  })

  const detail = detailQuery.data
  const targetsTotal = targetsQuery.data?.total ?? 0

  const batchColumns = useMemo<DataTableColumn<ChangeBatch>[]>(
    () => [
      {
        header: t('delivery.changes.detail.batches.columns.batch'),
        cell: (row) => <span className="tnum">#{String(row.batchNo)}</span>,
      },
      {
        header: t('delivery.changes.detail.batches.columns.status'),
        cell: (row) => <BatchStatusBadge status={row.status} />,
      },
      { header: t('delivery.changes.detail.batches.columns.planned'), cell: (row) => row.plannedCount },
      { header: t('delivery.changes.detail.batches.columns.success'), cell: (row) => row.successCount },
      { header: t('delivery.changes.detail.batches.columns.failed'), cell: (row) => row.failedCount },
    ],
    [t],
  )

  const targetColumns = useMemo<DataTableColumn<ChangeTarget>[]>(
    () => [
      {
        header: t('delivery.changesHistory.detail.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.changesHistory.detail.columns.batch'),
        cell: (row) => <span className="tnum">#{String(row.batchNo)}</span>,
      },
      {
        header: t('delivery.changesHistory.detail.columns.status'),
        cell: (row) => <TargetStatusBadge status={row.status} />,
      },
      {
        header: t('delivery.changesHistory.detail.columns.rollback'),
        cell: (row) =>
          row.rollbackStatus === null ? (
            <span className="text-ink-4">
              {t('delivery.changesHistory.detail.rollbackStatus.none')}
            </span>
          ) : (
            <Badge
              variant={row.rollbackStatus === 'failed' ? 'crit' : 'ok'}
              className="gap-1.5"
            >
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t(`delivery.changesHistory.detail.rollbackStatus.${row.rollbackStatus}`)}
            </Badge>
          ),
      },
    ],
    [t],
  )

  return (
    <div className="grid gap-4">
      {/* 状态 + 回滚 / 结束操作（面板标题已由 MasterDetail 头部承担） */}
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-surface-2 px-3 py-2.5">
        <div className="flex flex-wrap items-center gap-3">
          {detail && <StatusBadge status={detail.status} />}
        </div>
        <div className="flex items-center gap-2">
          {detail && <OrderRollbackActions order={detail} />}
          {detail && (
            <Button variant="ghost" size="sm" asChild>
              <Link to={`/changes?order=${String(detail.id)}`}>
                {t('delivery.changesHistory.list.openInChanges')}
              </Link>
            </Button>
          )}
        </div>
      </div>

      {/* 回滚信息横幅 + 回滚中逐目标进度 */}
      {detail && <RollbackBanner order={detail} />}

      {/* 批次状态 */}
      <div className="grid gap-2">
        <SectionHeader
          icon={<Layers className="size-4" />}
          title={t('delivery.changesHistory.detail.batchesTitle')}
        />
        <AsyncSection isLoading={detailQuery.isLoading} isError={detailQuery.isError} error={detailQuery.error}>
          <DataTable
            columns={batchColumns}
            rows={detail?.batches}
            rowKey={(row) => String(row.batchNo)}
            emptyText={t('delivery.changes.detail.batches.empty')}
            density="compact"
          />
        </AsyncSection>
      </div>

      {/* 单服状态 */}
      <div className="grid gap-2">
        <SectionHeader
          icon={<Server className="size-4" />}
          title={t('delivery.changesHistory.detail.targetsTitle')}
        />
        <AsyncSection isLoading={targetsQuery.isLoading} isError={targetsQuery.isError} error={targetsQuery.error}>
          <DataTable
            columns={targetColumns}
            rows={targetsQuery.data?.items}
            rowKey={(row) => `${row.serverId}:${String(row.batchNo)}`}
            emptyText={t('delivery.changes.detail.batches.empty')}
            density="compact"
          />
        </AsyncSection>
        <Pager page={page} total={targetsTotal} pageSize={TARGET_PAGE_SIZE} onPageChange={setPage} />
      </div>
    </div>
  )
}
