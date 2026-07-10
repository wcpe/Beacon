// 灰度批次 Tab：批次表（批次/状态/计划/成功/失败/跳过/推进确认）；
// awaiting_confirm 批显示「确认推进」（走确认弹窗），failed 批显示熔断原因。
import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button, DataTable, type DataTableColumn } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import {
  confirmChangeBatch,
  type ChangeBatch,
  type ChangeOrderDetail,
} from '../../api/delivery-changes'
import ConfirmDialog from './confirm-dialog'
import { BatchStatusBadge } from '../../features/delivery/status-badges'
import { formatTime } from './format'

interface BatchesTabProps {
  order: ChangeOrderDetail
}

export default function BatchesTab({ order }: BatchesTabProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [confirmBatchNo, setConfirmBatchNo] = useState<number | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const confirmMutation = useMutation({
    mutationFn: (batchNo: number) => confirmChangeBatch(order.id, batchNo),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['change-orders'] })
      setConfirmBatchNo(null)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const columns = useMemo<DataTableColumn<ChangeBatch>[]>(
    () => [
      { header: t('delivery.changes.detail.batches.columns.batch'), cell: (row) => row.batchNo },
      {
        header: t('delivery.changes.detail.batches.columns.status'),
        cell: (row) => <BatchStatusBadge status={row.status} />,
      },
      { header: t('delivery.changes.detail.batches.columns.planned'), cell: (row) => row.plannedCount },
      { header: t('delivery.changes.detail.batches.columns.success'), cell: (row) => row.successCount },
      { header: t('delivery.changes.detail.batches.columns.failed'), cell: (row) => row.failedCount },
      { header: t('delivery.changes.detail.batches.columns.skipped'), cell: (row) => row.skippedCount },
      {
        header: t('delivery.changes.detail.batches.columns.gate'),
        cell: (row) => {
          if (row.status === 'awaiting_confirm' && order.status === 'rolling') {
            return (
              <Button
                size="sm"
                onClick={() => {
                  setErrorText(null)
                  setConfirmBatchNo(row.batchNo)
                }}
              >
                {t('delivery.changes.detail.batches.confirm')}
              </Button>
            )
          }
          if (row.status === 'failed' && row.breakReason !== null) {
            return (
              <span className="text-xs text-destructive">
                {t('delivery.changes.detail.batches.breakReason', { reason: row.breakReason })}
              </span>
            )
          }
          if (row.gateConfirmedBy !== null && row.gateConfirmedAt !== null) {
            return (
              <span className="text-xs text-muted-foreground">
                {t('delivery.changes.detail.batches.gateBy', {
                  who: row.gateConfirmedBy,
                  at: formatTime(row.gateConfirmedAt),
                })}
              </span>
            )
          }
          return '-'
        },
      },
    ],
    [t, order.status],
  )

  return (
    <section className="grid gap-3">
      <DataTable
        columns={columns}
        rows={order.batches}
        rowKey={(row) => String(row.batchNo)}
        emptyText={t('delivery.changes.detail.batches.empty')}
        density="compact"
      />

      <ConfirmDialog
        open={confirmBatchNo !== null}
        onOpenChange={(open) => {
          if (!open) {
            setConfirmBatchNo(null)
          }
        }}
        title={t('delivery.changes.confirm.confirmBatchTitle')}
        description={t('delivery.changes.confirm.confirmBatchDesc')}
        confirmLabel={t('delivery.changes.detail.batches.confirm')}
        pending={confirmMutation.isPending}
        errorText={errorText}
        onConfirm={() => {
          if (confirmBatchNo !== null) {
            confirmMutation.mutate(confirmBatchNo)
          }
        }}
      />
    </section>
  )
}
