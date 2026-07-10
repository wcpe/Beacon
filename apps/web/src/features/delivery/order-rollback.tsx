// 整单回滚共享控件（/changes 详情与历史详情共用，承契约 rollback / rollback/finish）：
// OrderRollbackActions = 动作簇——合法状态（已完成 / 已暂停 / 已终止）给「整单回滚」
// 高摩擦确认（手输「回滚」+ 原因），回滚中给「人工结束回滚」（残留失败收单）；
// 自带 mutation 与内联脱敏错误。RollbackBanner = 回滚信息横幅（谁 / 何时 / 为何）+
// 回滚中的逐目标进度计数（来自详情 rollbackCounts）。
import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { RotateCcw } from 'lucide-react'

import { Button, DestructiveConfirmDialog } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import {
  finishRollbackChangeOrder,
  rollbackChangeOrder,
  type ChangeOrderDetail,
} from '../../api/delivery-changes'
import RollbackDialog from './rollback-dialog'
import { formatTime } from './format'

// 允许发起整单回滚的状态（契约 §5.1：completed / paused / cancelled；rolling_back 重复调用为重试）
const ROLLBACKABLE = new Set<ChangeOrderDetail['status']>(['completed', 'paused', 'cancelled'])

interface OrderRollbackProps {
  order: ChangeOrderDetail
}

/** 回滚动作簇：整单回滚 / 人工结束回滚按钮 + 高摩擦确认弹窗 + 自带写请求 */
export function OrderRollbackActions({ order }: OrderRollbackProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const [finishOpen, setFinishOpen] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['change-orders'] })

  const rollbackMutation = useMutation({
    mutationFn: (reason: string) => rollbackChangeOrder(order.id, reason),
    onSuccess: async () => {
      await invalidate()
      setRollbackOpen(false)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const finishMutation = useMutation({
    mutationFn: () => finishRollbackChangeOrder(order.id),
    onSuccess: async () => {
      await invalidate()
      setFinishOpen(false)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const canRollback = ROLLBACKABLE.has(order.status)
  const canFinish = order.status === 'rolling_back'
  if (!canRollback && !canFinish) {
    return null
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
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
          {t('delivery.rollback.action')}
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
          {t('delivery.rollback.finish')}
        </Button>
      )}

      {/* 结束回滚失败的脱敏错误（弹窗关闭后仍可见，不静默隐藏） */}
      {errorText !== null && !rollbackOpen && !finishOpen && (
        <p className="w-full text-xs text-destructive">{errorText}</p>
      )}

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

      {/* 人工结束回滚（残留失败收单） */}
      <DestructiveConfirmDialog
        open={finishOpen}
        onOpenChange={(open) => {
          setFinishOpen(open)
        }}
        title={t('delivery.rollback.finishTitle')}
        description={t('delivery.rollback.finishDesc')}
        confirmLabel={t('delivery.rollback.finishConfirm')}
        pending={finishMutation.isPending}
        onConfirm={() => {
          finishMutation.mutate()
        }}
      />
    </div>
  )
}

/** 回滚信息横幅：谁 / 何时 / 为何 + 回滚中的逐目标进度（已回滚 / 失败 / 未回滚） */
export function RollbackBanner({ order }: OrderRollbackProps) {
  const { t } = useTranslation()
  if (order.rollbackAt == null) {
    return null
  }
  // Partial 视图：计数字典缺键在运行期就是 undefined，补 0 兜底
  const counts: Partial<Record<string, number>> = order.rollbackCounts
  const done = counts.rolled_back ?? 0
  const failed = counts.failed ?? 0
  const pending = (counts.pending ?? 0) + (counts.running ?? 0)
  return (
    <div className="grid gap-1.5 rounded-lg border border-warn-bd bg-warn-bg px-3 py-2.5 text-sm text-warn">
      <p className="flex items-start gap-2">
        <RotateCcw className="mt-0.5 size-4 shrink-0" aria-hidden />
        <span>
          {t('delivery.rollback.info', {
            who: order.rollbackBy ?? '-',
            at: formatTime(order.rollbackAt),
            reason: order.rollbackReason ?? '-',
          })}
        </span>
      </p>
      {order.status === 'rolling_back' && (
        <p className="pl-6 text-xs">
          {t('delivery.rollback.progress', { done, failed, pending })}
          {failed > 0 && ` ${t('delivery.rollback.progressNote')}`}
        </p>
      )}
    </div>
  )
}
