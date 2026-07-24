// 灰度批次 Tab：批次状态机可视化（共享 batch-flow：纵向推进流 / 当前批高亮 /
// 熔断提示 / 待确认批醒目放行按钮）+ 执行期快捷操作（暂停 / 继续 / 紧急终止，
// 回调父级统一确认弹窗）。放行走本 Tab 内确认弹窗，推进后随详情失效即时刷新。
import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import { confirmChangeBatch, type ChangeOrderDetail } from '../../api/delivery-changes'
import BatchFlow from '../../features/delivery/batch-flow'
import ConfirmDialog from './confirm-dialog'

/** 执行期快捷操作（与详情头部生命周期动作同源，由父级打开统一确认弹窗） */
export type BatchQuickAction = 'pause' | 'resume' | 'cancel'

interface BatchesTabProps {
  order: ChangeOrderDetail
  onQuickAction: (kind: BatchQuickAction) => void
}

// 状态 → 可用快捷操作
function quickActionsOf(status: ChangeOrderDetail['status']): BatchQuickAction[] {
  if (status === 'rolling') {
    return ['pause', 'cancel']
  }
  if (status === 'paused') {
    return ['resume', 'cancel']
  }
  return []
}

export default function BatchesTab({ order, onQuickAction }: BatchesTabProps) {
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

  const quickActions = quickActionsOf(order.status)

  return (
    <section className="grid gap-3">
      {/* 执行期快捷操作：暂停 / 继续 / 紧急终止（统一走父级确认弹窗） */}
      {quickActions.length > 0 && (
        <div className="flex flex-wrap justify-end gap-2">
          {quickActions.map((kind) => (
            <Button
              key={kind}
              size="sm"
              variant={kind === 'cancel' ? 'outline' : 'default'}
              onClick={() => {
                onQuickAction(kind)
              }}
            >
              {t(`delivery.changes.actions.${kind}`)}
            </Button>
          ))}
        </div>
      )}

      {/* 批次状态机可视化（共享控件） */}
      <BatchFlow
        batches={order.batches}
        orderStatus={order.status}
        confirmPending={confirmMutation.isPending}
        onConfirm={(batchNo) => {
          setErrorText(null)
          setConfirmBatchNo(batchNo)
        }}
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
