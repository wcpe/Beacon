// 交付历史详情：溯任务 / 批次 / 单服状态 + 整单回滚（高风险二次确认）+ 残留失败人工结束回滚。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { Layers, RotateCcw, Server } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  DestructiveConfirmDialog,
  SectionHeader,
  type DataTableColumn,
} from '@beacon/ui'
import type { ChangeBatch, ChangeTarget } from '@beacon/devmock'

import Pager from '../../features/delivery/pager'
import { ApiClientError } from '../../api/delivery'
import {
  fetchChangeOrder,
  fetchChangeTargets,
  finishRollbackChangeOrder,
  rollbackChangeOrder,
} from '../../api/delivery-changes'
import { BatchStatusBadge, TargetStatusBadge } from '../changes/status-badge'
import RollbackDialog from './rollback-dialog'
import StatusBadge from './status-badge'
import { formatTime } from './format'

const TARGET_PAGE_SIZE = 20

// 允许发起整单回滚的状态
const ROLLBACKABLE = new Set(['completed', 'paused', 'cancelled'])

interface DetailViewProps {
  orderId: number
}

export default function DetailView({ orderId }: DetailViewProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const [finishOpen, setFinishOpen] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)

  const detailQuery = useQuery({
    queryKey: ['change-orders', 'detail', orderId],
    queryFn: () => fetchChangeOrder(orderId),
  })

  const targetsQuery = useQuery({
    queryKey: ['change-orders', 'targets', orderId, page],
    queryFn: () => fetchChangeTargets(orderId, { page, pageSize: TARGET_PAGE_SIZE }),
    placeholderData: keepPreviousData,
  })

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['change-orders'] })

  const rollbackMutation = useMutation({
    mutationFn: (reason: string) => rollbackChangeOrder(orderId, reason),
    onSuccess: async () => {
      await invalidate()
      setRollbackOpen(false)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const finishMutation = useMutation({
    mutationFn: () => finishRollbackChangeOrder(orderId),
    onSuccess: async () => {
      await invalidate()
      setFinishOpen(false)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
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

  const canRollback = detail !== undefined && ROLLBACKABLE.has(detail.status)
  const canFinish = detail?.status === 'rolling_back'

  return (
    <div className="grid gap-4">
      {/* 状态 + 回滚 / 结束操作（面板标题已由 MasterDetail 头部承担） */}
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-surface-2 px-3 py-2.5">
        <div className="flex flex-wrap items-center gap-3">
          {detail && <StatusBadge status={detail.status} />}
        </div>
        <div className="flex items-center gap-2">
          {canRollback && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                setErrorText(null)
                setRollbackOpen(true)
              }}
            >
              <RotateCcw className="size-4" />
              {t('delivery.changesHistory.rollback.action')}
            </Button>
          )}
          {canFinish && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setErrorText(null)
                setFinishOpen(true)
              }}
            >
              {t('delivery.changesHistory.rollback.finish')}
            </Button>
          )}
          {detail && (
            <Button variant="ghost" size="sm" asChild>
              <Link to={`/changes?order=${String(detail.id)}`}>
                {t('delivery.changesHistory.list.openInChanges')}
              </Link>
            </Button>
          )}
        </div>
      </div>

      {/* 回滚信息横幅 */}
      {detail?.rollbackAt != null && (
        <p className="flex items-start gap-2 rounded-lg border border-warn-bd bg-warn-bg px-3 py-2.5 text-sm text-warn">
          <RotateCcw className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>
            {t('delivery.changesHistory.detail.rollbackInfo', {
              who: detail.rollbackBy ?? '-',
              at: formatTime(detail.rollbackAt),
              reason: detail.rollbackReason ?? '-',
            })}
          </span>
        </p>
      )}

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

      {errorText && <p className="text-sm text-destructive">{errorText}</p>}

      {/* 整单回滚：高摩擦复述 + 原因 */}
      <RollbackDialog
        open={rollbackOpen}
        pending={rollbackMutation.isPending}
        errorText={rollbackMutation.isError ? errorText : null}
        onConfirm={(reason) => {
          rollbackMutation.mutate(reason)
        }}
        onOpenChange={(open) => {
          setRollbackOpen(open)
          if (!open) {
            setErrorText(null)
          }
        }}
      />

      {/* 人工结束回滚（残留失败） */}
      <DestructiveConfirmDialog
        open={finishOpen}
        onOpenChange={(open) => {
          setFinishOpen(open)
          if (!open) {
            setErrorText(null)
          }
        }}
        title={t('delivery.changesHistory.rollback.finishTitle')}
        description={t('delivery.changesHistory.rollback.finishDesc')}
        confirmLabel={t('delivery.changesHistory.rollback.finishConfirm')}
        pending={finishMutation.isPending}
        onConfirm={() => {
          finishMutation.mutate()
        }}
      />
    </div>
  )
}
