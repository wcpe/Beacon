// 变更单详情视图：标题 + 状态徽标 + 生命周期操作区（按 status 显示可用动作）+ 审批进度 + 五个 Tab。
// 每个直执写操作走确认弹窗；六类申请动作（提审 / 删除 / 继续 / 批次确认 / 回滚 / 结束回滚）返回 202 票据，
// 成功只提示「已提交审批 + 申请号」，单据状态要到审批通过后才变化。
// 审批决定（批准 / 拒绝 / 撤回）不在本页：只在统一审批中心 /approvals。
import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

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
  cancelChangeOrder,
  deleteChangeOrder,
  fetchChangeOrder,
  isApprovalTicket,
  pauseChangeOrder,
  resumeChangeOrder,
  submitChangeOrder,
  type ChangeOrderDetail,
  type DeliveryApprovalTicket,
} from '../../api/delivery-changes'
import ConfirmDialog, { type ConfirmResult } from './confirm-dialog'
import { randomId } from '../../lib/random-id'
import ApprovalProgress from '../../features/delivery/approval-progress'
import { useApprovalTicketFeedback } from '../../features/delivery/approval-ticket'
import { OrderRollbackActions, RollbackBanner, RollbackRecordsSection } from '../../features/delivery/order-rollback'
import { OrderStatusBadge } from '../../features/delivery/status-badges'
import ItemsTab from './items-tab'
import ImpactTab from './impact-tab'
import BatchesTab from './batches-tab'
import ObserveTab from './observe-tab'
import EventsTab from './events-tab'

// 本页可执行的动作类型（申请类动作走 202 票据，直执动作为暂停 / 终止）
type ActionKind = 'submit' | 'delete' | 'pause' | 'resume' | 'cancel'

// 生命周期动作（含两个跳转入口：待审批可撤回、已批准只能查看申请——审批撤回只允许 pending）
type LifecycleAction = ActionKind | 'withdraw' | 'viewApproval'

// 动作响应：申请类动作 → 202 票据；直执动作（暂停 / 终止）→ 200 最新详情
type ActionOutcome = ChangeOrderDetail | DeliveryApprovalTicket

interface DetailViewProps {
  orderId: number
}

export default function DetailView({ orderId }: DetailViewProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const notifyTicket = useApprovalTicketFeedback()

  const [action, setAction] = useState<ActionKind | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)
  // 幂等键按动作意图缓存：同一意图重试复用同一键（后端据此去重），弹窗关闭即作废重来
  const keysRef = useRef<Partial<Record<ActionKind, string>>>({})

  const query = useQuery({
    queryKey: ['change-orders', 'detail', orderId],
    queryFn: () => fetchChangeOrder(orderId),
  })

  const invalidate = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['change-orders'] }),
      // 申请类动作会新增 / 更新审批申请，审批进度卡与 /approvals 一并失效
      queryClient.invalidateQueries({ queryKey: ['approvals'] }),
    ])
  }

  const keyFor = (kind: ActionKind): string => {
    const existing = keysRef.current[kind] ?? randomId()
    keysRef.current[kind] = existing
    return existing
  }

  const runMutation = useMutation({
    mutationFn: ({ kind, result }: { kind: ActionKind; result: ConfirmResult }): Promise<ActionOutcome> => {
      switch (kind) {
        case 'submit':
          return submitChangeOrder(orderId, result.reason, keyFor('submit'))
        case 'delete':
          return deleteChangeOrder(orderId, result.reason, keyFor('delete'))
        case 'pause':
          return pauseChangeOrder(orderId)
        case 'resume':
          return resumeChangeOrder(
            orderId,
            {
              mode: result.mode ?? undefined,
              reason: result.reason === '' ? undefined : result.reason,
            },
            keyFor('resume'),
          )
        case 'cancel':
          return cancelChangeOrder(orderId, result.reason)
      }
    },
    onSuccess: async (data) => {
      await invalidate()
      // 申请意图已提交：清键，避免下一次动作复用旧键（真机按 idempotency_key_reused 拒掉）
      keysRef.current = {}
      setAction(null)
      // 申请动作返回 202 票据：反馈申请号并给去审批中心的入口（直执动作无需票据反馈）
      if (isApprovalTicket(data)) {
        notifyTicket(data)
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
                {/* 合法终态（已完成 / 已暂停 / 已终止）整单回滚申请；回滚中申请结束回滚 */}
                <OrderRollbackActions order={order} />
              </div>
            </div>

            {/* 审批进度：本单关联的统一审批申请（状态 / 审批人 / 驳回理由） */}
            <ApprovalProgress orderId={orderId} />

            {/* 回滚信息横幅 + 回滚中逐目标进度 + 回滚动作记录（FR-271） */}
            <RollbackBanner order={order} />
            <RollbackRecordsSection orderId={order.id} />

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
              // 关闭弹窗 = 放弃本次申请意图，下次重新生成幂等键
              keysRef.current = {}
            }
          }}
          title={confirmConfig(action, t).title}
          description={confirmConfig(action, t).description}
          confirmLabel={confirmConfig(action, t).confirmLabel}
          requireReason={needsReason(action) || needsResumeReason(action, order)}
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

// 按当前 status 渲染可用生命周期动作按钮。
// 「撤回」是审批动作：只在统一审批中心执行，这里给的是跳转入口（不在本页弹确认框、不调废弃的 /withdraw）。
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
      {actions.map((kind) =>
        kind === 'withdraw' || kind === 'viewApproval' ? (
          <Button key={kind} size="sm" variant="outline" asChild>
            <Link to="/approvals">
              {kind === 'withdraw'
                ? t('delivery.changes.actions.withdraw')
                : t('delivery.changes.approval.openCenter')}
            </Link>
          </Button>
        ) : (
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
        ),
      )}
    </div>
  )
}

// 状态 → 可用动作集合（批次内的「确认推进」在批次 Tab 内呈现，不在此处）
function availableActions(status: ChangeOrderDetail['status']): LifecycleAction[] {
  switch (status) {
    case 'draft':
      return ['submit', 'delete']
    case 'pending_approval':
      // 待审批可撤回：走审批中心（本页不再直调废弃的 /withdraw）
      return ['withdraw']
    case 'approved':
      // 已批准不可撤回（服务端只允许 pending 撤回）：给「去审批中心」看执行进度
      return ['viewApproval']
    case 'paused':
      return ['resume', 'cancel']
    case 'rolling':
      return ['pause']
    default:
      return []
  }
}

// 熔断 / 准备失败的「继续」也要填原因：真机 validateResumeArgs 对非人工暂停强制 reason 非空
// （缺则 400 missing_reason，弹窗不给输入框时该动作永远发不出去）
function needsResumeReason(kind: ActionKind, order: ChangeOrderDetail): boolean {
  return kind === 'resume' && order.pauseKind !== 'manual'
}

// 需要填写原因的动作：提审 / 终止 / 删除（后端三个 Request* 均校验原因非空）
function needsReason(kind: ActionKind): boolean {
  // 高风险操作填原因入审计与审批申请（spec §4.8.1）：提交审批、删除草稿、紧急终止。
  return kind === 'submit' || kind === 'cancel' || kind === 'delete'
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
