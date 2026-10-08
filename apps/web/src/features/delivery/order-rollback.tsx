// 回滚共享控件（/changes 详情与历史详情共用，承契约 rollback / rollback/finish / rollback/targets / rollback-records）：
//   - OrderRollbackActions = 动作簇：整单回滚（高摩擦确认：手输「回滚」+ 原因）、回滚重试（FR-262r，
//     仅当单在回滚中且存在回滚失败目标时出现）、人工结束回滚（残留失败收单）；
//   - 三者都是**申请动作**（202 票据 + 幂等键），成功后只提示申请号与去审批中心，
//     单据状态要到审批通过后才变化（不假设「点完即回滚」）；
//   - RollbackBanner = 回滚信息横幅（谁 / 何时 / 为何）+ 回滚中的逐目标进度（来自详情 rollbackCounts）；
//   - RollbackRecordsSection = 回滚动作记录（FR-271）：谁 / 何时 / 为何 / 台数 / 是否回退配置 / 逐台结果。
import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { RotateCcw } from 'lucide-react'

import { AsyncSection, Button, DestructiveConfirmDialog } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import {
  fetchChangeTargets,
  fetchRollbackRecords,
  finishRollbackChangeOrder,
  rollbackChangeOrder,
  type ChangeOrderDetail,
  type ChangeRollbackRecord,
} from '../../api/delivery-changes'
import { randomId } from '../../lib/random-id'
import { useApprovalTicketFeedback } from './approval-ticket'
import RollbackDialog, { type RollbackPrecheck } from './rollback-dialog'
import { formatTime } from './format'

// 允许发起整单回滚的状态（契约 §5.1：completed / paused / cancelled；rolling_back 重复调用为重试）
const ROLLBACKABLE = new Set<ChangeOrderDetail['status']>(['completed', 'paused', 'cancelled'])

// 预检一次拉取的目标行数上限：一次请求取齐，不逐页拼接；超出时界面显式标注「预检仅覆盖前 N 台」。
const PRECHECK_PAGE_SIZE = 200

/**
 * useRollbackPrecheck 回滚预检（FR-255r，spec §4.7.2「预检阶段即在前端明示哪些目标不可文件回滚」）：
 * 取目标页的 backupPresent 汇总出「不可文件回滚」清单。读不到数据时返回 unavailable 而非空清单——
 * 「未知」与「全部可回滚」在界面上必须可区分，否则前端会把一次查询失败显示成安全。
 */
export function useRollbackPrecheck(orderId: number, enabled: boolean): RollbackPrecheck | null {
  const query = useQuery({
    queryKey: ['change-orders', 'targets', orderId, 'precheck'],
    queryFn: () => fetchChangeTargets(orderId, { page: 1, pageSize: PRECHECK_PAGE_SIZE }),
    enabled,
  })
  if (!enabled) {
    return null
  }
  if (query.isError) {
    return { unavailable: true, blocked: [], checked: 0, total: 0 }
  }
  const items = query.data?.items
  if (items == null) {
    return { unavailable: true, blocked: [], checked: 0, total: 0 }
  }
  return {
    unavailable: false,
    blocked: items.filter((row) => !row.backupPresent).map((row) => row.serverId),
    checked: items.length,
    total: query.data?.total ?? items.length,
  }
}

interface OrderRollbackProps {
  order: ChangeOrderDetail
}

/** 回滚动作簇：整单回滚 / 重试 / 人工结束回滚按钮 + 高摩擦确认弹窗 + 自带申请请求 */
export function OrderRollbackActions({ order }: OrderRollbackProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const notifyTicket = useApprovalTicketFeedback()
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const [retryOpen, setRetryOpen] = useState(false)
  const [finishOpen, setFinishOpen] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)
  // 幂等键按意图各存一个：同一意图重试复用同键（后端据此去重）；
  // 意图结束（成功 / 弹窗关闭）即清该键，否则下次回滚会拿旧键发新 payload（真机 409 idempotency_key_reused）
  const keysRef = useRef<{ rollback: string | null; retry: string | null; finish: string | null }>({
    rollback: null,
    retry: null,
    finish: null,
  })

  const canRollback = ROLLBACKABLE.has(order.status)
  const canFinish = order.status === 'rolling_back'
  // Partial 视图：计数字典缺键在运行期就是 undefined（与 RollbackBanner 同口径）
  const counts: Partial<Record<string, number>> = order.rollbackCounts
  const failedRollbacks = counts.failed ?? 0
  const canRetry = canFinish && failedRollbacks > 0
  const precheck = useRollbackPrecheck(order.id, rollbackOpen)

  const resetKey = (kind: 'rollback' | 'retry' | 'finish') => {
    keysRef.current[kind] = null
  }

  const invalidate = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['change-orders'] }),
      queryClient.invalidateQueries({ queryKey: ['approvals'] }),
    ])
  }

  const rollbackMutation = useMutation({
    mutationFn: (reason: string) => {
      const key = keysRef.current.rollback ?? randomId()
      keysRef.current.rollback = key
      return rollbackChangeOrder(order.id, reason, key)
    },
    onSuccess: async (ticket) => {
      await invalidate()
      resetKey('rollback')
      setRollbackOpen(false)
      notifyTicket(ticket)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  // 回滚重试（FR-262r）：生产入口走同一 operation（delivery.rollback）、原因必填、单价幂等键独立。
  const retryMutation = useMutation({
    mutationFn: (reason: string) => {
      const key = keysRef.current.retry ?? randomId()
      keysRef.current.retry = key
      return rollbackChangeOrder(order.id, reason, key)
    },
    onSuccess: async (ticket) => {
      await invalidate()
      resetKey('retry')
      setRetryOpen(false)
      notifyTicket(ticket)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const finishMutation = useMutation({
    mutationFn: () => {
      const key = keysRef.current.finish ?? randomId()
      keysRef.current.finish = key
      return finishRollbackChangeOrder(order.id, key)
    },
    onSuccess: async (ticket) => {
      await invalidate()
      resetKey('finish')
      setFinishOpen(false)
      notifyTicket(ticket)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

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
      {canRetry && (
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            setErrorText(null)
            setRetryOpen(true)
          }}
        >
          <RotateCcw className="size-4" />
          {t('delivery.rollback.retry.action')}
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
      {errorText !== null && !rollbackOpen && !retryOpen && !finishOpen && (
        <p className="w-full text-xs text-destructive">{errorText}</p>
      )}

      {/* 整单回滚：高摩擦复述 + 原因 + 回滚预检（FR-255r） */}
      <RollbackDialog
        open={rollbackOpen}
        pending={rollbackMutation.isPending}
        errorText={rollbackMutation.isError ? errorText : null}
        precheck={precheck}
        onConfirm={(reason) => {
          rollbackMutation.mutate(reason)
        }}
        onOpenChange={(open) => {
          setRollbackOpen(open)
          if (!open) {
            resetKey('rollback')
            setErrorText(null)
          }
        }}
      />

      {/* 回滚重试（FR-262r）：只重推失败目标 */}
      <RollbackDialog
        open={retryOpen}
        pending={retryMutation.isPending}
        errorText={retryMutation.isError ? errorText : null}
        textKey="rollback.retry"
        onConfirm={(reason) => {
          retryMutation.mutate(reason)
        }}
        onOpenChange={(open) => {
          setRetryOpen(open)
          if (!open) {
            resetKey('retry')
            setErrorText(null)
          }
        }}
      />

      {/* 人工结束回滚（残留失败收单） */}
      <DestructiveConfirmDialog
        open={finishOpen}
        onOpenChange={(open) => {
          setFinishOpen(open)
          if (!open) {
            resetKey('finish')
          }
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

/**
 * RollbackRecordsSection 回滚动作记录（FR-271）：整单 / 子集 / 重试各一条，含
 * 谁 / 何时 / 为何 / 台数 / 是否回退配置 / 逐台结果——「回滚过几次、打到哪几台」不再只能翻审计。
 * 与审计同源：同一条动作在审计流里也带 recordId，两路互指。
 */
export function RollbackRecordsSection({ orderId }: { orderId: number }) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['change-orders', 'rollback-records', orderId],
    queryFn: () => fetchRollbackRecords(orderId),
  })
  const records = query.data?.items ?? []
  return (
    <div className="grid gap-2">
      <p className="text-xs font-medium text-ink-2">
        {t('delivery.changes.detail.rollbackRecords.title')}
      </p>
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        {records.length === 0 ? (
          <p className="text-xs text-ink-3">{t('delivery.changes.detail.rollbackRecords.empty')}</p>
        ) : (
          <ul className="grid gap-2">
            {records.map((record) => (
              <RollbackRecordRow key={record.id} record={record} />
            ))}
          </ul>
        )}
      </AsyncSection>
    </div>
  )
}

function RollbackRecordRow({ record }: { record: ChangeRollbackRecord }) {
  const { t } = useTranslation()
  const kindLabel =
    record.kind === 'targets'
      ? t('delivery.changes.detail.rollbackRecords.kind.targets')
      : t('delivery.changes.detail.rollbackRecords.kind.order')
  return (
    <li className="rounded-md border border-line-1 px-3 py-2 text-xs">
      <p className="flex flex-wrap items-center gap-2 text-ink-2">
        <span className="font-medium">{kindLabel}</span>
        <span>
          {t('delivery.changes.detail.rollbackRecords.meta', {
            who: record.operator,
            at: formatTime(record.createdAt),
            count: record.targetCount,
          })}
        </span>
        <span className={record.configRolledBack ? 'text-warn' : 'text-ink-3'}>
          {record.configRolledBack
            ? t('delivery.changes.detail.rollbackRecords.configRolledBack')
            : t('delivery.changes.detail.rollbackRecords.configNotRolledBack')}
        </span>
      </p>
      <p className="mt-0.5 text-ink-3">{record.reason}</p>
      {record.targets.length > 0 && (
        <ul className="mt-1 grid gap-0.5 font-mono text-ink-2">
          {record.targets.map((target) => (
            <li key={target.serverId}>
              {t('delivery.changes.detail.rollbackRecords.targetLine', {
                serverId: target.serverId,
                result: target.result || '—',
              })}
              {target.error != null && target.error !== '' && (
                <span className="ml-1 text-crit" title={target.error}>
                  {t('delivery.changes.detail.rollbackRecords.errorLine', { error: target.error })}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </li>
  )
}
