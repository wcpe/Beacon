// 告警详情面板内容（非模态右侧列）：单条告警全字段 + 处理写闭环（确认 / 标记已处理）在面板内完成。
// 待处理态展示确认 / 标记已处理表单（resolved 备注必填），已处理态展示处理人 / 时间 / 备注 + 互跳。
// message 为人读摘要；detail 用 JsonDetail 键值可视化（非 JSON 则原文）。
// FR-230：内嵌「该服近期状态」与「该服告警时间线」，供 5 秒内判断「已过去 / 需立刻处理」。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { ArrowUpRight } from 'lucide-react'

import { Badge, Button, Label, Textarea } from '@beacon/ui'
import type { AlertEventItem } from '@beacon/contracts'

import { fetchAlertContext } from '../../api/observability'
import {
  healthStatusLabel,
  parseHealthTransition,
} from '../../features/observability/alert-transition'
import JsonDetail from '../../features/observability/json-detail'

// 处理意图：确认或标记已处理
export type HandleIntent = 'acknowledged' | 'resolved'

// 告警级别 → 状态药丸语义 variant
function levelBadgeVariant(level: AlertEventItem['level']): 'crit' | 'warn' | 'off' {
  if (level === 'critical') {
    return 'crit'
  }
  if (level === 'warning') {
    return 'warn'
  }
  return 'off'
}

// 处理状态 → 状态药丸语义 variant
function statusBadgeVariant(status: AlertEventItem['status']): 'crit' | 'ok' | 'off' {
  if (status === 'open') {
    return 'crit'
  }
  if (status === 'resolved') {
    return 'ok'
  }
  return 'off'
}

interface AlertDetailPanelProps {
  // 展示的告警行
  item: AlertEventItem
  // 处理中
  pending: boolean
  // 脱敏错误文案（内联展示）
  errorText: string | null
  // 处理写操作提交（resolved 时携带备注）
  onHandle: (intent: HandleIntent, note: string) => void
  // FR-231：人工升降告警级别
  onOverrideLevel: (level: AlertEventItem['level']) => void
  levelPending: boolean
}

export default function AlertDetailPanel({
  item,
  pending,
  errorText,
  onHandle,
  onOverrideLevel,
  levelPending,
}: AlertDetailPanelProps) {
  const { t } = useTranslation()
  // 处理备注（仅 resolved 需要）
  const [note, setNote] = useState('')

  // 切换到另一条告警时清空备注输入
  useEffect(() => {
    setNote('')
  }, [item.id])

  const isOpen = item.status === 'open'

  return (
    <div className="grid gap-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={levelBadgeVariant(item.level)}>{t(`observability.alertEvents.level.${item.level}`)}</Badge>
        {item.severityOverride != null && (
          <Badge
            variant="secondary"
            title={t('observability.alertEvents.override.by', { by: item.overriddenBy ?? '—', at: item.overriddenAt ? new Date(item.overriddenAt).toLocaleString() : '—' })}
          >
            {t('observability.alertEvents.override.badge')}
          </Badge>
        )}
        <Badge variant={statusBadgeVariant(item.status)}>{t(`observability.alertEvents.status.${item.status}`)}</Badge>
      </div>
      {/* FR-231：人工升降级别（色 + 文字双编码；改级落审计） */}
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-xs text-ink-4">{t('observability.alertEvents.override.title')}</span>
        {(['info', 'warning', 'critical'] as const).map((lv) => (
          <Button
            key={lv}
            size="sm"
            variant={item.level === lv ? 'default' : 'outline'}
            disabled={levelPending || item.level === lv}
            onClick={() => {
              onOverrideLevel(lv)
            }}
          >
            {t(`observability.alertEvents.level.${lv}`)}
          </Button>
        ))}
      </div>

      <Field label={t('observability.alertEvents.columns.time')} value={new Date(item.createdAt).toLocaleString()} />
      <Field label={t('observability.alertEvents.columns.type')} value={t(`observability.alertEvents.type.${item.type}`)} />
      <Field label={t('observability.alertEvents.columns.serverId')} value={item.serverId} mono />
      {/* 状态流转：detail JSON / message 解析后 i18n 展示，不再甩英文枚举 */}
      {(() => {
        const tr = parseHealthTransition(item)
        if (tr === null) {
          return null
        }
        return (
          <div className="grid gap-1">
            <span className="text-xs text-ink-4">{t('observability.alertEvents.columns.transition')}</span>
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="off">{healthStatusLabel(t, tr.from)}</Badge>
              <span className="text-ink-4">→</span>
              <Badge variant={item.level === 'critical' ? 'crit' : 'warn'}>
                {healthStatusLabel(t, tr.to)}
              </Badge>
            </div>
          </div>
        )
      })()}
      {/* 人读摘要：健康流转用 i18n 副标题，其它类型保留 message */}
      <div className="grid gap-1">
        <span className="text-xs text-ink-4">{t('observability.alertEvents.columns.message')}</span>
        <p className="rounded-lg bg-secondary/60 px-2.5 py-2 text-xs text-ink-2">
          {(() => {
            const tr = parseHealthTransition(item)
            if (tr !== null) {
              return t('observability.alertEvents.transitionArrow', {
                from: healthStatusLabel(t, tr.from),
                to: healthStatusLabel(t, tr.to),
              })
            }
            return item.message || '—'
          })()}
        </p>
      </div>
      {/* detail JSON 键值可视化；非 JSON / 空则组件内降级 */}
      <JsonDetail
        raw={item.detail}
        title={t('observability.alertEvents.columns.detail')}
        keyPrefix="observability.alertEvents.detailKeys"
      />

      {/* 已处理 / 已确认态：展示处理人 / 时间 / 备注 */}
      {item.handledBy !== null && (
        <>
          <Field label={t('observability.alertEvents.handledBy')} value={item.handledBy} />
          {item.handledAt !== null && (
            <Field label={t('observability.alertEvents.handledAt')} value={new Date(item.handledAt).toLocaleString()} />
          )}
          {item.handleNote !== null && (
            <div className="grid gap-1">
              <span className="text-xs text-ink-4">{t('observability.alertEvents.handleNote')}</span>
              <p className="rounded-lg bg-secondary/60 px-2.5 py-2 text-xs text-ink-2">{item.handleNote}</p>
            </div>
          )}
        </>
      )}

      {/* FR-230：该服近期状态 + 该服告警时间线（数据实时取健康真源与 alert_event，不复制存储） */}
      <AlertContextSection eventId={item.id} />

      {/* 待处理态：面板内处理写闭环（确认无需备注，标记已处理备注必填） */}
      {isOpen && (
        <div className="grid gap-2 border-t border-border pt-3">
          <Label htmlFor="alert-handle-note">{t('observability.alertEvents.note')}</Label>
          <Textarea
            id="alert-handle-note"
            value={note}
            placeholder={t('observability.alertEvents.notePlaceholder')}
            onChange={(e) => {
              setNote(e.target.value)
            }}
          />
          {errorText !== null && <p className="text-sm text-destructive">{errorText}</p>}
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={pending}
              onClick={() => {
                onHandle('acknowledged', note.trim())
              }}
            >
              {t('observability.alertEvents.actions.acknowledge')}
            </Button>
            <Button
              size="sm"
              disabled={pending || note.trim() === ''}
              onClick={() => {
                onHandle('resolved', note.trim())
              }}
            >
              {t('observability.alertEvents.actions.resolve')}
            </Button>
          </div>
        </div>
      )}

      {/* 互跳（FR-157） */}
      <div className="flex flex-wrap items-center gap-3 border-t border-border pt-3 text-xs">
        <Link
          className="inline-flex items-center gap-0.5 text-brand-600 hover:underline"
          to={`/audits?targetRef=${encodeURIComponent(item.serverId)}`}
        >
          {t('observability.alertEvents.viewInAudits')}
          <ArrowUpRight className="size-3" />
        </Link>
        <Link className="inline-flex items-center gap-0.5 text-brand-600 hover:underline" to="/servers">
          {t('observability.alertEvents.viewInServers')}
          <ArrowUpRight className="size-3" />
        </Link>
      </div>
    </div>
  )
}

// 单个只读字段（标签 + 值）
function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-ink-4">{label}</span>
      <span className={mono ? 'font-mono text-xs text-ink-2' : 'text-sm text-ink-1'}>{value}</span>
    </div>
  )
}

// FR-230：该服近期状态卡 + 该服告警时间线（按需拉取聚合端点，切告警自动重取）。
function AlertContextSection({ eventId }: { eventId: number }) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['alert-events', 'context', eventId],
    queryFn: () => fetchAlertContext(eventId),
  })
  const data = query.data
  return (
    <div className="grid gap-2 border-t border-border pt-3">
      <span className="text-xs text-ink-4">{t('observability.alertEvents.context.serverTitle')}</span>
      {query.isLoading ? (
        <div className="h-12 animate-pulse rounded-lg bg-muted" />
      ) : data?.server == null ? (
        <p className="rounded-lg border border-dashed border-border px-2.5 py-2 text-xs text-ink-4">
          {t('observability.alertEvents.context.serverEmpty')}
        </p>
      ) : (
        <div className="grid gap-1.5 rounded-lg bg-secondary/50 px-2.5 py-2 text-xs">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant={data.server.level === 'healthy' ? 'ok' : data.server.level === 'degraded' ? 'warn' : 'crit'}>
              {healthStatusLabel(t, data.server.level)}
            </Badge>
            <Badge variant={data.server.online ? 'ok' : 'off'}>
              {t(data.server.online ? 'observability.alertEvents.context.online' : 'observability.alertEvents.context.offline')}
            </Badge>
            <span className="text-ink-2">
              {t('observability.alertEvents.context.score')} <span className="font-semibold">{data.server.score}</span>
            </span>
          </div>
          {data.server.reasons.length > 0 && (
            <span className="text-ink-4">{data.server.reasons.join(' · ')}</span>
          )}
        </div>
      )}

      <span className="text-xs text-ink-4">
        {t('observability.alertEvents.context.timelineTitle', { hours: data?.timelineWindowHours ?? 24 })}
      </span>
      {(data?.timeline.length ?? 0) === 0 ? (
        <p className="rounded-lg border border-dashed border-border px-2.5 py-2 text-xs text-ink-4">
          {t('observability.alertEvents.context.timelineEmpty')}
        </p>
      ) : (
        <ul className="grid gap-1">
          {data?.timeline.map((row) => (
            <li key={row.id} className="flex flex-wrap items-center gap-2 text-xs">
              <span className="text-ink-4">{new Date(row.createdAt).toLocaleString()}</span>
              <Badge variant={row.level === 'critical' ? 'crit' : row.level === 'warning' ? 'warn' : 'off'}>
                {t(`observability.alertEvents.level.${row.level}`)}
              </Badge>
              <Badge variant={row.status === 'open' ? 'crit' : row.status === 'resolved' ? 'ok' : 'off'}>
                {t(`observability.alertEvents.status.${row.status}`)}
              </Badge>
              <span className="truncate text-ink-3">{row.message}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
