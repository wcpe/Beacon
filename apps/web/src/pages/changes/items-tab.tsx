// 变更项 Tab：展示 items（文件差异 / 配置变更）+ 差异快照时间 + 「重扫差异」（仅 draft）。
import { useMemo } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button, DataTable, type DataTableColumn } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import {
  diffScanChangeOrder,
  type ChangeOrderDetail,
  type ChangeOrderItem,
} from '../../api/delivery-changes'
import { formatBytes, formatTime } from './format'

interface ItemsTabProps {
  order: ChangeOrderDetail
}

export default function ItemsTab({ order }: ItemsTabProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const diffScanMutation = useMutation({
    mutationFn: () => diffScanChangeOrder(order.id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['change-orders'] })
    },
  })

  const columns = useMemo<DataTableColumn<ChangeOrderItem>[]>(
    () => [
      {
        header: t('delivery.changes.detail.items.columns.kind'),
        cell: (row) =>
          row.kind === 'file_diff'
            ? t('delivery.changes.detail.items.fileDiff')
            : t('delivery.changes.detail.items.configChange'),
      },
      {
        header: t('delivery.changes.detail.items.columns.path'),
        cell: (row) =>
          row.kind === 'file_diff'
            ? (row.path ?? '-')
            : `${row.configScopeKind ?? '-'} #${row.configScopeId === null ? '-' : String(row.configScopeId)}`,
      },
      {
        header: t('delivery.changes.detail.items.columns.action'),
        cell: (row) => row.action ?? '-',
      },
      {
        header: t('delivery.changes.detail.items.columns.size'),
        cell: (row) => (row.sizeBytes === null ? '-' : formatBytes(row.sizeBytes)),
      },
    ],
    [t],
  )

  const errorText =
    diffScanMutation.error instanceof ApiClientError ? diffScanMutation.error.message : null

  return (
    <section className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm text-muted-foreground">
          {order.diffSnapshotAt === null
            ? t('delivery.changes.detail.items.noSnapshot')
            : t('delivery.changes.detail.items.snapshotAt', { at: formatTime(order.diffSnapshotAt) })}
        </span>
        {order.status === 'draft' && (
          <Button
            size="sm"
            variant="outline"
            disabled={diffScanMutation.isPending}
            onClick={() => {
              diffScanMutation.mutate()
            }}
          >
            {t('delivery.changes.detail.items.diffScan')}
          </Button>
        )}
      </div>

      {errorText !== null && <p className="text-sm text-destructive">{errorText}</p>}

      <DataTable
        columns={columns}
        rows={order.items}
        rowKey={(row) => String(row.id)}
        emptyText={t('delivery.changes.detail.items.empty')}
        density="compact"
      />
    </section>
  )
}
