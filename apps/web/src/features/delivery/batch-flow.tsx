// 批次状态机可视化（共享控件）：把变更单批次渲染成纵向推进流——左侧节点圆点 + 连线，
// 每批一张卡（批次号 / 目标数 / 状态语义色 / 成功·失败·跳过计数 / 放行记录 / 熔断原因），
// 当前批高亮；灰度中的待确认批给醒目主按钮「确认放行下一批」（末批文案改「确认完成整单」）。
// readOnly（历史只读回放）不渲染操作。纯展示 + 回调：不自带取数与请求，/changes 与历史页共用。
import { useTranslation } from 'react-i18next'

import { Button, cn } from '@beacon/ui'

import type { ChangeBatch, ChangeOrderStatus } from '../../api/delivery-changes'
import { formatTime } from './format'
import { BATCH_VARIANT, BatchStatusBadge, type PillVariant } from './status-badges'

interface BatchFlowProps {
  batches: ChangeBatch[]
  orderStatus: ChangeOrderStatus
  /** 只读回放（历史页）：不渲染确认放行按钮 */
  readOnly?: boolean
  /** 点「确认放行」回调（仅灰度中 + 待确认批出现按钮） */
  onConfirm?: (batchNo: number) => void
  /** 确认请求进行中（按钮置灰防连点） */
  confirmPending?: boolean
}

// 语义变体 → 节点圆点配色（浅底 + 语义色数字 + 同色描边，与状态药丸同一套 token）
const NODE_CLASS: Record<PillVariant, string> = {
  ok: 'border-ok-bd bg-ok-bg text-ok',
  brand: 'border-brand-100 bg-brand-50 text-brand-600',
  warn: 'border-warn-bd bg-warn-bg text-warn',
  crit: 'border-crit-bd bg-crit-bg text-crit',
  off: 'border-off-bd bg-off-bg text-off',
}

// 「当前批」= 推进指针所在批：第一个未到终态（非已完成 / 已跳过）的批
const TERMINAL_BATCH = new Set<ChangeBatch['status']>(['completed', 'skipped'])

export default function BatchFlow({
  batches,
  orderStatus,
  readOnly = false,
  onConfirm,
  confirmPending = false,
}: BatchFlowProps) {
  const { t } = useTranslation()

  if (batches.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
        {t('delivery.changes.detail.batches.empty')}
      </p>
    )
  }

  // 仅执行期（灰度中 / 已暂停）标注当前批；终态单是全程回放，不再有"当前"
  const executing = orderStatus === 'rolling' || orderStatus === 'paused'
  const currentBatchNo = executing
    ? (batches.find((batch) => !TERMINAL_BATCH.has(batch.status))?.batchNo ?? null)
    : null

  return (
    <ol className="grid gap-0">
      {batches.map((batch, index) => {
        const variant = BATCH_VARIANT[batch.status]
        const isCurrent = batch.batchNo === currentBatchNo
        const isLast = index === batches.length - 1
        const confirmable =
          !readOnly && orderStatus === 'rolling' && batch.status === 'awaiting_confirm' && onConfirm !== undefined
        return (
          <li key={batch.batchNo} className="grid grid-cols-[2rem_minmax(0,1fr)] gap-x-2.5">
            {/* 左轨：节点圆点 + 连线 */}
            <div className="flex flex-col items-center">
              <span
                className={cn(
                  'grid size-7 shrink-0 place-items-center rounded-full border text-xs font-semibold tnum',
                  NODE_CLASS[variant],
                  isCurrent && 'ring-2 ring-brand/30',
                )}
                aria-hidden
              >
                {batch.batchNo}
              </span>
              {!isLast && <span className="w-px flex-1 bg-border" aria-hidden />}
            </div>

            {/* 批次卡 */}
            <div
              className={cn(
                'mb-3 grid gap-1.5 rounded-lg border border-border px-3 py-2.5',
                isCurrent && 'border-brand-100 bg-brand-50/40',
              )}
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-[13px] font-semibold text-ink-1">
                  {t('delivery.preview.batchFlow.batchTitle', { no: batch.batchNo })}
                </span>
                <BatchStatusBadge status={batch.status} />
                {isCurrent && (
                  <span className="rounded-md bg-brand-50 px-1.5 py-0.5 text-[11px] font-semibold text-brand-600">
                    {t('delivery.preview.batchFlow.current')}
                  </span>
                )}
                <span className="tnum ml-auto text-xs text-ink-3">
                  {t('delivery.preview.batchFlow.planned', { count: batch.plannedCount })}
                </span>
              </div>

              {/* 成功 / 失败 / 跳过计数（失败 > 0 走危险色） */}
              <div className="flex flex-wrap items-center gap-3 text-xs">
                <span className="tnum text-ink-2">
                  {t('delivery.preview.batchFlow.success', { count: batch.successCount })}
                </span>
                <span className={cn('tnum', batch.failedCount > 0 ? 'font-semibold text-crit' : 'text-ink-3')}>
                  {t('delivery.preview.batchFlow.failed', { count: batch.failedCount })}
                </span>
                <span className="tnum text-ink-3">
                  {t('delivery.preview.batchFlow.skipped', { count: batch.skippedCount })}
                </span>
              </div>

              {/* 熔断提示 */}
              {batch.breakReason !== null && (
                <p className="rounded-md border border-crit-bd bg-crit-bg px-2 py-1.5 text-xs text-crit">
                  {t('delivery.changes.detail.batches.breakReason', { reason: batch.breakReason })}
                </p>
              )}

              {/* 放行记录 */}
              {batch.gateConfirmedBy !== null && batch.gateConfirmedAt !== null && (
                <p className="text-xs text-muted-foreground">
                  {t('delivery.preview.batchFlow.gateBy', {
                    who: batch.gateConfirmedBy,
                    at: formatTime(batch.gateConfirmedAt),
                  })}
                </p>
              )}

              {/* 推进门：醒目主按钮（末批确认即整单完成） */}
              {confirmable && (
                <div className="pt-0.5">
                  <Button
                    size="sm"
                    disabled={confirmPending}
                    onClick={() => {
                      onConfirm(batch.batchNo)
                    }}
                  >
                    {isLast
                      ? t('delivery.preview.batchFlow.confirmLast')
                      : t('delivery.preview.batchFlow.confirmNext')}
                  </Button>
                </div>
              )}
            </div>
          </li>
        )
      })}
    </ol>
  )
}
