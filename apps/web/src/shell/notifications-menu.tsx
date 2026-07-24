// 页眉通知（FR-195）：未处理告警角标 + 下拉预览；展示 i18n 状态流转；行内一键确认/已处理。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { Bell, Check, CheckCheck, CircleAlert, Info, TriangleAlert } from 'lucide-react'

import {
  Badge,
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@beacon/ui'
import type { AlertEventItem } from '@beacon/contracts'

import { handleAlertEvent, fetchAlertEvents } from '../api/observability'
import { alertSubtitle } from '../features/observability/alert-transition'
import { notifyError, notifySuccess } from '../lib/notify'
import { useGlobalOpsMetrics } from './use-global-ops-metrics'

const PREVIEW_LIMIT = 5

function levelIcon(level: AlertEventItem['level']) {
  if (level === 'critical') {
    return CircleAlert
  }
  if (level === 'warning') {
    return TriangleAlert
  }
  return Info
}

export default function NotificationsMenu() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { metrics } = useGlobalOpsMetrics()
  const openCount = metrics.openAlerts
  const [busyId, setBusyId] = useState<number | null>(null)

  const query = useQuery({
    queryKey: ['shell', 'notifications', 'open-alerts'],
    queryFn: () => fetchAlertEvents({ page: 1, size: 20 }),
    staleTime: 15_000,
    retry: 1,
  })

  const openItems = useMemo(() => {
    const items = query.data?.items ?? []
    return items.filter((i) => i.status === 'open').slice(0, PREVIEW_LIMIT)
  }, [query.data])

  const badgeText =
    openCount === null ? null : openCount > 99 ? '99+' : openCount > 0 ? String(openCount) : null

  const invalidateAlerts = async () => {
    await queryClient.invalidateQueries({ queryKey: ['shell', 'notifications'] })
    await queryClient.invalidateQueries({ queryKey: ['alert-events'] })
    // 角标 openAlerts 来自 shell metrics 聚合
    await queryClient.invalidateQueries({ queryKey: ['shell', 'metrics'] })
  }

  const handleOne = useMutation({
    mutationFn: ({ id, status }: { id: number; status: 'acknowledged' | 'resolved' }) =>
      handleAlertEvent(id, {
        status,
        // 页眉一键：resolved 用固定短备注，避免下拉内再开输入
        note: status === 'resolved' ? t('common.header.quickResolveNote') : undefined,
      }),
    onMutate: ({ id }) => {
      setBusyId(id)
    },
    onSuccess: async (_data, vars) => {
      await invalidateAlerts()
      notifySuccess(
        vars.status === 'resolved'
          ? t('observability.alertEvents.actions.resolve')
          : t('observability.alertEvents.actions.acknowledge'),
      )
    },
    onError: (error) => {
      notifyError(error instanceof Error ? error.message : String(error))
    },
    onSettled: () => {
      setBusyId(null)
    },
  })

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          aria-label={t('common.header.notifications')}
          className="relative"
          data-slot="notifications-trigger"
        >
          <Bell className="size-4" />
          {badgeText !== null ? (
            <span
              className="absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-crit px-1 text-[10px] font-bold text-white"
              data-slot="notifications-badge"
            >
              {badgeText}
            </span>
          ) : null}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" sideOffset={6} className="w-[22rem] p-0" data-slot="notifications-menu">
        <DropdownMenuLabel className="flex items-center justify-between px-3 py-2.5">
          <span>{t('common.header.notifications')}</span>
          {openCount !== null && openCount > 0 ? (
            <Badge variant="crit" className="tnum">
              {openCount}
            </Badge>
          ) : null}
        </DropdownMenuLabel>
        <DropdownMenuSeparator className="m-0" />
        {query.isError ? (
          <p className="px-3 py-4 text-sm text-crit">{t('common.header.notificationsError')}</p>
        ) : query.isLoading ? (
          <p className="px-3 py-4 text-sm text-ink-4">{t('common.header.notificationsLoading')}</p>
        ) : openItems.length === 0 ? (
          <p className="px-3 py-4 text-sm text-ink-4">{t('common.header.notificationsEmpty')}</p>
        ) : (
          <div className="max-h-80 overflow-y-auto py-1">
            {openItems.map((item) => {
              const Icon = levelIcon(item.level)
              const subtitle = alertSubtitle(item, t)
              const busy = busyId === item.id
              return (
                <div
                  key={item.id}
                  className="flex items-start gap-2 border-b border-border/60 px-3 py-2 last:border-b-0"
                  data-slot="notification-row"
                >
                  <Icon
                    className={
                      item.level === 'critical'
                        ? 'mt-0.5 size-3.5 shrink-0 text-crit'
                        : item.level === 'warning'
                          ? 'mt-0.5 size-3.5 shrink-0 text-warn'
                          : 'mt-0.5 size-3.5 shrink-0 text-brand'
                    }
                    aria-hidden
                  />
                  <Link
                    to="/alert-events"
                    className="min-w-0 flex-1 outline-none hover:opacity-90"
                    onClick={(e) => {
                      // 点正文进详情页；按钮区 stopPropagation
                      e.stopPropagation()
                    }}
                  >
                    <span className="block truncate text-[13px] font-medium text-ink-1">
                      {item.serverId || t(`observability.alertEvents.type.${item.type}`, { defaultValue: item.type })}
                    </span>
                    {/* 状态流转 i18n，不再直接甩英文 degraded → lost */}
                    <span className="mt-0.5 flex flex-wrap items-center gap-1 text-[11px] text-ink-3">
                      <Badge variant={item.level === 'critical' ? 'crit' : item.level === 'warning' ? 'warn' : 'off'} className="text-[10px]">
                        {t(`observability.alertEvents.level.${item.level}`)}
                      </Badge>
                      <span className="truncate" title={subtitle}>
                        {subtitle}
                      </span>
                    </span>
                  </Link>
                  <div
                    className="flex shrink-0 flex-col gap-1"
                    onClick={(e) => {
                      e.preventDefault()
                      e.stopPropagation()
                    }}
                    onKeyDown={(e) => {
                      e.stopPropagation()
                    }}
                  >
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      className="h-7 gap-1 px-2 text-[11px]"
                      disabled={busy || handleOne.isPending}
                      title={t('observability.alertEvents.actions.acknowledge')}
                      onClick={() => {
                        handleOne.mutate({ id: item.id, status: 'acknowledged' })
                      }}
                    >
                      <Check className="size-3" />
                      {t('common.header.quickAck')}
                    </Button>
                    <Button
                      type="button"
                      size="sm"
                      className="h-7 gap-1 px-2 text-[11px]"
                      disabled={busy || handleOne.isPending}
                      title={t('observability.alertEvents.actions.resolve')}
                      onClick={() => {
                        handleOne.mutate({ id: item.id, status: 'resolved' })
                      }}
                    >
                      <CheckCheck className="size-3" />
                      {t('common.header.quickResolve')}
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>
        )}
        <DropdownMenuSeparator className="m-0" />
        <div className="p-1.5">
          <DropdownMenuItem asChild className="cursor-pointer justify-center text-brand">
            <Link to="/alert-events">{t('common.header.viewAllAlerts')}</Link>
          </DropdownMenuItem>
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
