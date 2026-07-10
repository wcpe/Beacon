// 进度时间线（共享控件）：一次性事件数组渲染成两种可切换模式——
// 「可视化」= 垂直时间轴（节点圆点语义色 + 连线 + 事件标题 / 相对时间，提交 / 审批 /
// 批次完成 / 回滚等关键节点放大标记）；「详细」= 紧凑表格（序号 / 时间 / 类型 / 批次 /
// 目标 / 状态全字段）。纯展示：取数与轮询由调用方负责，/changes 与历史页共用。
import { useMemo, useState } from 'react'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { Badge, DataTable, cn, type DataTableColumn } from '@beacon/ui'

import type { ChangeOrderEvent } from '../../api/delivery-changes'
import { formatTime } from './format'
import { eventStatusVariant, type PillVariant } from './status-badges'

interface EventsTimelineProps {
  events: ChangeOrderEvent[]
}

// 语义变体 → 时间轴节点圆点配色（关键节点叠加 ring 放大标记）
const DOT_CLASS: Record<PillVariant, string> = {
  ok: 'bg-ok ring-ok/20',
  brand: 'bg-brand ring-brand/20',
  warn: 'bg-warn ring-warn/20',
  crit: 'bg-crit ring-crit/20',
  off: 'bg-off ring-off/20',
}

// 状态文案的 i18n 前缀（按事件类型分流到既有状态字典）
const STATUS_NS: Record<ChangeOrderEvent['type'], string> = {
  order_status: 'delivery.changes.status',
  batch_status: 'delivery.changes.batchStatus',
  target_status: 'delivery.changes.targetStatus',
}

// 关键节点（放大标记）：单据级事件一律关键；批次完成 / 熔断也算关键
function isKeyEvent(event: ChangeOrderEvent): boolean {
  if (event.type === 'order_status') {
    return true
  }
  return event.type === 'batch_status' && (event.status === 'completed' || event.status === 'failed')
}

// 事件主体标签：变更单 / 批次 #n / 目标 serverId
function subjectOf(t: TFunction, event: ChangeOrderEvent): string {
  if (event.type === 'order_status') {
    return t('delivery.changes.detail.events.order')
  }
  if (event.type === 'batch_status') {
    const no = event.batchNo === null ? '' : ` #${String(event.batchNo)}`
    return `${t('delivery.changes.detail.events.batch')}${no}`
  }
  return `${t('delivery.changes.detail.events.target')} ${event.serverId ?? ''}`.trim()
}

// 状态中文标签（未知状态回退原串，不渲染 i18n key）
function statusLabelOf(t: TFunction, event: ChangeOrderEvent): string {
  return t(`${STATUS_NS[event.type]}.${event.status}`, { defaultValue: event.status })
}

// 相对时间（刚刚 / N 分钟前 / N 小时前 / N 天前；超 30 天回退本地时间）
function relativeTimeOf(t: TFunction, iso: string): string {
  const diffMs = Date.now() - Date.parse(iso)
  if (Number.isNaN(diffMs) || diffMs < 60_000) {
    return t('delivery.preview.timeline.rel.justNow')
  }
  const minutes = Math.floor(diffMs / 60_000)
  if (minutes < 60) {
    return t('delivery.preview.timeline.rel.minutes', { count: minutes })
  }
  const hours = Math.floor(minutes / 60)
  if (hours < 24) {
    return t('delivery.preview.timeline.rel.hours', { count: hours })
  }
  const days = Math.floor(hours / 24)
  if (days < 30) {
    return t('delivery.preview.timeline.rel.days', { count: days })
  }
  return formatTime(iso)
}

export default function EventsTimeline({ events }: EventsTimelineProps) {
  const { t } = useTranslation()
  // 双模式：可视化时间轴（默认）/ 详细表格
  const [mode, setMode] = useState<'visual' | 'table'>('visual')

  // 事件按 seq 逆序（最新在前）
  const rows = useMemo(() => [...events].sort((a, b) => b.seq - a.seq), [events])

  const columns = useMemo<DataTableColumn<ChangeOrderEvent>[]>(
    () => [
      {
        header: t('delivery.preview.timeline.columns.seq'),
        cell: (row) => <span className="tnum text-xs text-ink-3">{row.seq}</span>,
      },
      {
        header: t('delivery.preview.timeline.columns.at'),
        cell: (row) => <span className="tnum text-xs">{formatTime(row.at)}</span>,
      },
      {
        header: t('delivery.preview.timeline.columns.kind'),
        cell: (row) => t(`delivery.changes.detail.events.${row.type === 'order_status' ? 'order' : row.type === 'batch_status' ? 'batch' : 'target'}`),
      },
      {
        header: t('delivery.preview.timeline.columns.batch'),
        cell: (row) => (row.batchNo === null ? '-' : <span className="tnum">#{String(row.batchNo)}</span>),
      },
      {
        header: t('delivery.preview.timeline.columns.target'),
        cell: (row) => (row.serverId === null ? '-' : <span className="font-mono text-xs">{row.serverId}</span>),
      },
      {
        header: t('delivery.preview.timeline.columns.status'),
        cell: (row) => (
          <Badge variant={eventStatusVariant(row.type, row.status)} className="gap-1.5">
            <span className="size-1.5 rounded-full bg-current" aria-hidden />
            {statusLabelOf(t, row)}
          </Badge>
        ),
      },
    ],
    [t],
  )

  return (
    <div className="grid gap-3">
      {/* 模式切换 */}
      <div
        className="flex w-fit overflow-hidden rounded-md border border-border"
        role="group"
        aria-label={t('delivery.preview.timeline.modeLabel')}
      >
        {(['visual', 'table'] as const).map((value) => (
          <button
            key={value}
            type="button"
            aria-pressed={mode === value}
            onClick={() => {
              setMode(value)
            }}
            className={cn(
              'px-3 py-1 text-xs transition-colors',
              mode === value ? 'bg-brand text-white' : 'bg-background text-ink-2 hover:bg-muted',
            )}
          >
            {t(`delivery.preview.timeline.mode.${value}`)}
          </button>
        ))}
      </div>

      {mode === 'table' ? (
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => String(row.seq)}
          emptyText={t('delivery.changes.detail.events.empty')}
          density="compact"
        />
      ) : rows.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
          {t('delivery.changes.detail.events.empty')}
        </p>
      ) : (
        <ol className="grid gap-0 pt-1">
          {rows.map((event, index) => {
            const key = isKeyEvent(event)
            const variant = eventStatusVariant(event.type, event.status)
            return (
              <li key={event.seq} className="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-x-2.5">
                {/* 左轨：节点圆点（关键节点放大 + ring 标记）+ 连线 */}
                <div className="flex flex-col items-center">
                  <span
                    className={cn(
                      'mt-1 shrink-0 rounded-full',
                      DOT_CLASS[variant],
                      key ? 'size-3 ring-4' : 'size-2',
                    )}
                    aria-hidden
                  />
                  {index < rows.length - 1 && <span className="w-px flex-1 bg-border" aria-hidden />}
                </div>

                {/* 事件行：标题（主体 · 状态）+ 相对时间（悬停看绝对时间） */}
                <div className={cn('pb-3', key ? 'pb-4' : undefined)}>
                  <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                    <span
                      className={cn(
                        'text-sm leading-tight',
                        key ? 'font-semibold text-ink-1' : 'text-ink-2',
                        event.type === 'target_status' && 'font-mono text-xs',
                      )}
                    >
                      {subjectOf(t, event)} · {statusLabelOf(t, event)}
                    </span>
                    <span className="tnum text-xs text-ink-3" title={formatTime(event.at)}>
                      {relativeTimeOf(t, event.at)}
                    </span>
                  </div>
                </div>
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}
