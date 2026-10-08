// 单服级状态墙（FR-254）：逐台展示正推状态、回滚状态、失败原因（error / rollbackError）与备份标记，
// 让「执行中哪台到哪一步、哪台失败为什么」在详情页一眼可见。
// 数据源与历史页同一端点（fetchChangeTargets）；执行中 5s 自动刷新，终态不轮询。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Server } from 'lucide-react'

import { AsyncSection, Badge, DataTable, SectionHeader, type DataTableColumn } from '@beacon/ui'
import type { ChangeTarget } from '@beacon/contracts'

import { fetchChangeTargets, type ChangeOrderStatus } from '../../api/delivery-changes'
import Pager from './pager'
import { TargetStatusBadge } from './status-badges'

const TARGET_PAGE_SIZE = 20

// 执行中的单状态：状态墙自动刷新（草稿 / 待审批 / 终态无进度可看，不轮询）
const LIVE_STATUSES = new Set<ChangeOrderStatus>(['rolling', 'paused', 'rolling_back'])

interface TargetStatusWallProps {
  orderId: number
  /** 单状态：决定是否自动刷新 */
  orderStatus: ChangeOrderStatus
}

export default function TargetStatusWall({ orderId, orderStatus }: TargetStatusWallProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)

  const query = useQuery({
    queryKey: ['change-orders', 'targets', orderId, page],
    queryFn: () => fetchChangeTargets(orderId, { page, pageSize: TARGET_PAGE_SIZE }),
    placeholderData: keepPreviousData,
    refetchInterval: LIVE_STATUSES.has(orderStatus) ? 5000 : false,
  })

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const failedCount = items.filter((row) => row.status === 'failed').length
  const rollbackFailedCount = items.filter((row) => row.rollbackStatus === 'failed').length

  const columns = useMemo<DataTableColumn<ChangeTarget>[]>(
    () => [
      {
        header: t('delivery.changes.detail.targets.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.changes.detail.targets.columns.batch'),
        cell: (row) => <span className="tnum">#{String(row.batchNo)}</span>,
      },
      {
        header: t('delivery.changes.detail.targets.columns.status'),
        cell: (row) => <TargetStatusBadge status={row.status} />,
      },
      {
        header: t('delivery.changes.detail.targets.columns.rollback'),
        cell: (row) =>
          row.rollbackStatus === null ? (
            <span className="text-ink-4">{t('delivery.changes.detail.targets.rollbackNone')}</span>
          ) : (
            <Badge variant={row.rollbackStatus === 'failed' ? 'crit' : 'ok'} className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t(`delivery.changes.detail.targets.rollbackStatus.${row.rollbackStatus}`)}
            </Badge>
          ),
      },
      {
        header: t('delivery.changes.detail.targets.columns.error'),
        cell: (row) => <FailureReason text={row.error} />,
      },
      {
        header: t('delivery.changes.detail.targets.columns.rollbackError'),
        cell: (row) => <FailureReason text={row.rollbackError} />,
      },
      {
        header: t('delivery.changes.detail.targets.columns.backup'),
        cell: (row) =>
          row.backupPresent ? (
            <Badge variant="ok" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t('delivery.changes.detail.targets.backupPresent')}
            </Badge>
          ) : (
            <span className="text-ink-4">{t('delivery.changes.detail.targets.backupMissing')}</span>
          ),
      },
    ],
    [t],
  )

  return (
    <div className="grid gap-2">
      <SectionHeader
        icon={<Server className="size-4" />}
        title={t('delivery.changes.detail.targets.title')}
      />
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <div className="grid gap-2">
          {/* 汇总一行：总台数 + 失败 / 回滚失败计数（无失败不额外占位） */}
          <p className="text-xs text-ink-3">
            {t('delivery.changes.detail.targets.summary', { total })}
            {failedCount > 0 && ` · ${t('delivery.changes.detail.targets.failedCount', { count: failedCount })}`}
            {rollbackFailedCount > 0 &&
              ` · ${t('delivery.changes.detail.targets.rollbackFailedCount', { count: rollbackFailedCount })}`}
          </p>
          <DataTable
            columns={columns}
            rows={items}
            rowKey={(row) => `${row.serverId}:${String(row.batchNo)}`}
            emptyText={t('delivery.changes.detail.batches.empty')}
            density="compact"
          />
          <Pager page={page} total={total} pageSize={TARGET_PAGE_SIZE} onPageChange={setPage} />
        </div>
      </AsyncSection>
    </div>
  )
}

// 失败原因单元格：为空显示占位符，非空按危险色展示并保留全文 tooltip（长文案不截断语义）
function FailureReason({ text }: { text: string | null }) {
  const { t } = useTranslation()
  if (text === null || text === '') {
    return <span className="text-ink-4">{t('delivery.changes.detail.targets.noFailure')}</span>
  }
  return (
    <span className="text-crit" title={text}>
      {text}
    </span>
  )
}
