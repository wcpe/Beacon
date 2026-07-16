// 变更单详情视图：标题 + 状态徽标 + 生命周期操作区（按 status 显示可用动作）+ 五个 Tab。
// 每个写操作走确认弹窗；reject/cancel 必填原因，熔断恢复必填 mode+reason，start 提示冲突守卫。
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Button,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import {
  approveChangeOrder,
  cancelChangeOrder,
  deleteChangeOrder,
  fetchChangeOrder,
  pauseChangeOrder,
  rejectChangeOrder,
  resumeChangeOrder,
  startChangeOrder,
  submitChangeOrder,
  withdrawChangeOrder,
  type ChangeOrderDetail,
} from '../../api/delivery-changes'
import ConfirmDialog, { type ConfirmResult } from './confirm-dialog'
import { OrderRollbackActions, RollbackBanner } from '../../features/delivery/order-rollback'
import { OrderStatusBadge } from '../../features/delivery/status-badges'
import ItemsTab from './items-tab'
import ImpactTab from './impact-tab'
import BatchesTab from './batches-tab'
import ObserveTab from './observe-tab'
import EventsTab from './events-tab'

// 生命周期动作类型
type ActionKind =
  | 'submit'
  | 'delete'
  | 'withdraw'
  | 'approve'
  | 'reject'
  | 'start'
  | 'pause'
  | 'resume'
  | 'cancel'

interface DetailViewProps {
  orderId: number
  onBack: () => void
}

export default function DetailView({ orderId, onBack }: DetailViewProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [action, setAction] = useState<ActionKind | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['change-orders', 'detail', orderId],
    queryFn: () => fetchChangeOrder(orderId),
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['change-orders'] })

  const runMutation = useMutation({
    mutationFn: ({ kind, result }: { kind: ActionKind; result: ConfirmResult }) => {
      switch (kind) {
        case 'submit':
          return submitChangeOrder(orderId)
        case 'delete':
          return deleteChangeOrder(orderId, result.reason)
        case 'withdraw':
          return withdrawChangeOrder(orderId)
        case 'approve':
          return approveChangeOrder(orderId)
        case 'reject':
          return rejectChangeOrder(orderId, result.reason)
        case 'start':
          return startChangeOrder(orderId)
        case 'pause':
          return pauseChangeOrder(orderId)
        case 'resume':
          return resumeChangeOrder(orderId, {
            mode: result.mode ?? undefined,
            reason: result.reason === '' ? undefined : result.reason,
          })
        case 'cancel':
          return cancelChangeOrder(orderId, result.reason)
      }
    },
    onSuccess: async (_data, variables) => {
      await invalidate()
      setAction(null)
      // 删除草稿后返回列表
      if (variables.kind === 'delete') {
        onBack()
      }
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const order = query.data

  return (
    <div className="grid gap-4">
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        {order && (
          <div className="grid gap-4">
            {/* 状态徽标 + 生命周期操作区（面板标题已由 MasterDetail 头部承担，此处只留状态与可做动作） */}
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-surface-2 px-3 py-2.5">
              <OrderStatusBadge status={order.status} />
              <div className="flex flex-wrap items-center gap-2">
                <LifecycleActions
                  order={order}
                  onPick={(kind) => {
                    setErrorText(null)
                    setAction(kind)
                  }}
                />
                {/* 合法终态（已完成 / 已暂停 / 已终止）整单回滚；回滚中人工结束 */}
                <OrderRollbackActions order={order} />
              </div>
            </div>

            {/* 回滚信息横幅 + 回滚中逐目标进度 */}
            <RollbackBanner order={order} />

            {/* Tabs */}
            <Tabs defaultValue="items">
              <TabsList>
                <TabsTrigger value="items">{t('delivery.changes.detail.tabs.items')}</TabsTrigger>
                <TabsTrigger value="impact">{t('delivery.changes.detail.tabs.impact')}</TabsTrigger>
                <TabsTrigger value="batches">{t('delivery.changes.detail.tabs.batches')}</TabsTrigger>
                <TabsTrigger value="observe">{t('delivery.changes.detail.tabs.observe')}</TabsTrigger>
                <TabsTrigger value="events">{t('delivery.changes.detail.tabs.events')}</TabsTrigger>
              </TabsList>
              <TabsContent value="items" className="pt-3">
                <ItemsTab order={order} />
              </TabsContent>
              <TabsContent value="impact" className="pt-3">
                <ImpactTab order={order} />
              </TabsContent>
              <TabsContent value="batches" className="pt-3">
                <BatchesTab
                  order={order}
                  onQuickAction={(kind) => {
                    setErrorText(null)
                    setAction(kind)
                  }}
                />
              </TabsContent>
              <TabsContent value="observe" className="pt-3">
                <ObserveTab orderId={orderId} />
              </TabsContent>
              <TabsContent value="events" className="pt-3">
                <EventsTab orderId={orderId} />
              </TabsContent>
            </Tabs>
          </div>
        )}
      </AsyncSection>

      {order && action !== null && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setAction(null)
            }
          }}
          title={confirmConfig(action, t).title}
          description={confirmConfig(action, t).description}
          confirmLabel={confirmConfig(action, t).confirmLabel}
          requireReason={needsReason(action)}
          requireMode={action === 'resume' && order.pauseKind !== 'manual'}
          pending={runMutation.isPending}
          errorText={errorText}
          onConfirm={(result) => {
            runMutation.mutate({ kind: action, result })
          }}
        />
      )}
    </div>
  )
}

// 按当前 status 渲染可用生命周期动作按钮
function LifecycleActions({
  order,
  onPick,
}: {
  order: ChangeOrderDetail
  onPick: (kind: ActionKind) => void
}) {
  const { t } = useTranslation()
  const actions = availableActions(order.status)
  if (actions.length === 0) {
    return null
  }
  return (
    <div className="flex flex-wrap gap-2">
      {actions.map((kind) => (
        <Button
          key={kind}
          size="sm"
          variant={kind === 'delete' || kind === 'cancel' ? 'outline' : 'default'}
          onClick={() => {
            onPick(kind)
          }}
        >
          {t(`delivery.changes.actions.${kind}`)}
        </Button>
      ))}
    </div>
  )
}

// 状态 → 可用动作集合（批次内的「确认推进」在批次 Tab 内呈现，不在此处）
function availableActions(status: ChangeOrderDetail['status']): ActionKind[] {
  switch (status) {
    case 'draft':
      return ['submit', 'delete']
    case 'pending_approval':
      return ['approve', 'reject', 'withdraw']
    case 'approved':
      return ['start', 'withdraw']
    case 'rolling':
      return ['pause']
    case 'paused':
      return ['resume', 'cancel']
    default:
      return []
  }
}

// 需要填写原因的动作：驳回 / 终止
function needsReason(kind: ActionKind): boolean {
  // 高风险操作填原因入审计（spec §4.8.1）：驳回 / 紧急终止 / 删除 draft 单。
  return kind === 'reject' || kind === 'cancel' || kind === 'delete'
}

function confirmConfig(
  kind: ActionKind,
  t: (key: string) => string,
): { title: string; description: string; confirmLabel: string } {
  const map: Record<ActionKind, { titleKey: string; descKey: string; labelKey: string }> = {
    submit: {
      titleKey: 'delivery.changes.confirm.submitTitle',
      descKey: 'delivery.changes.confirm.submitDesc',
      labelKey: 'delivery.changes.actions.submit',
    },
    delete: {
      titleKey: 'delivery.changes.confirm.deleteTitle',
      descKey: 'delivery.changes.confirm.deleteDesc',
      labelKey: 'delivery.changes.actions.delete',
    },
    withdraw: {
      titleKey: 'delivery.changes.confirm.withdrawTitle',
      descKey: 'delivery.changes.confirm.withdrawDesc',
      labelKey: 'delivery.changes.actions.withdraw',
    },
    approve: {
      titleKey: 'delivery.changes.confirm.approveTitle',
      descKey: 'delivery.changes.confirm.approveDesc',
      labelKey: 'delivery.changes.actions.approve',
    },
    reject: {
      titleKey: 'delivery.changes.confirm.rejectTitle',
      descKey: 'delivery.changes.confirm.rejectDesc',
      labelKey: 'delivery.changes.actions.reject',
    },
    start: {
      titleKey: 'delivery.changes.confirm.startTitle',
      descKey: 'delivery.changes.confirm.startDesc',
      labelKey: 'delivery.changes.actions.start',
    },
    pause: {
      titleKey: 'delivery.changes.confirm.pauseTitle',
      descKey: 'delivery.changes.confirm.pauseDesc',
      labelKey: 'delivery.changes.actions.pause',
    },
    resume: {
      titleKey: 'delivery.changes.confirm.resumeTitle',
      descKey: 'delivery.changes.confirm.resumeDesc',
      labelKey: 'delivery.changes.actions.resume',
    },
    cancel: {
      titleKey: 'delivery.changes.confirm.cancelTitle',
      descKey: 'delivery.changes.confirm.cancelDesc',
      labelKey: 'delivery.changes.actions.cancel',
    },
  }
  const entry = map[kind]
  return {
    title: t(entry.titleKey),
    description: t(entry.descKey),
    confirmLabel: t(entry.labelKey),
  }
}
