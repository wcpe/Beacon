// 变更项 Tab：共享变更内容预览（文件差异分组清单 + 配置变更 from→to 版本、行级 diff
// 懒展开）+ 差异快照时间 + 「重扫差异」（仅 draft）。
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import { diffScanChangeOrder, type ChangeOrderDetail } from '../../api/delivery-changes'
import OrderChangePreview from '../../features/delivery/order-change-preview'
import { formatTime } from './format'

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

      {/* 共享变更内容预览（含配置版本反查与行级 diff 懒展开） */}
      <OrderChangePreview items={order.items} />
    </section>
  )
}
