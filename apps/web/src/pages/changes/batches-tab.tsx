// 灰度批次 Tab：批次状态机可视化（共享 batch-flow：纵向推进流 / 当前批高亮 /
// 熔断提示 / 待确认批醒目放行按钮）+ 单服级状态墙（逐台进度与失败原因）
// + 执行期快捷操作（暂停 / 继续 / 紧急终止，回调父级统一确认弹窗）。
// 放行走本 Tab 内确认弹窗：批次确认是申请动作（202 票据，携幂等键），批准后才推进，
// 故成功后提示申请号并让状态墙继续反映真实进度。
import { useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import { confirmChangeBatch, type ChangeOrderDetail } from '../../api/delivery-changes'
import { randomId } from '../../lib/random-id'
import { useApprovalTicketFeedback } from '../../features/delivery/approval-ticket'
import BatchFlow from '../../features/delivery/batch-flow'
import TargetStatusWall from '../../features/delivery/target-status-wall'
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
  const notifyTicket = useApprovalTicketFeedback()

  const [confirmBatchNo, setConfirmBatchNo] = useState<number | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)
  // 同一放行意图重试复用同一幂等键（后端据此去重），弹窗关闭即作废重来
  const keyRef = useRef<string | null>(null)

  const confirmMutation = useMutation({
    mutationFn: (batchNo: number) => {
      const key = keyRef.current ?? randomId()
      keyRef.current = key
      return confirmChangeBatch(order.id, batchNo, key)
    },
    onSuccess: async (ticket) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['change-orders'] }),
        queryClient.invalidateQueries({ queryKey: ['approvals'] }),
      ])
      setConfirmBatchNo(null)
      notifyTicket(ticket)
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

      {/* 单服级状态墙：逐台正推 / 回滚状态、失败原因与备份标记（执行中 5s 自动刷新） */}
      <TargetStatusWall orderId={order.id} orderStatus={order.status} />

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
