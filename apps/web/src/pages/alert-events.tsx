// 告警事件页（/alert-events）：告警事件列表与处理状态。
// KPI + 过滤列表（级别/类型/状态 + 分页）+ 行内确认 / 标记已处理写闭环；与 /audits、/servers 互跳（FR-157）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  SectionHeader,
  TableSkeleton,
  levelSoft,
  type DataTableColumn,
} from '@beacon/ui'
import type { AlertEventItem } from '@beacon/devmock'

import { ApiClientError } from '../api/http'
import { fetchAlertEvents, handleAlertEvent } from '../api/observability'
import FilterSelect from '../features/observability/filter-select'
import Pager from '../features/observability/pager'
import AlertKpi from './alert-events/alert-kpi'
import HandleDialog, { type HandleIntent } from './alert-events/handle-dialog'

const PAGE_SIZE = 15
const LEVELS = ['info', 'warning', 'critical'] as const
const TYPES = ['health-transition', 'publish-fail', 'backend-unreachable'] as const
const STATUSES = ['open', 'acknowledged', 'resolved'] as const

// 告警级别 → 健康等级（徽标配色）
function levelToHealth(level: AlertEventItem['level']): 'ok' | 'warn' | 'danger' | 'muted' {
  if (level === 'critical') {
    return 'danger'
  }
  if (level === 'warning') {
    return 'warn'
  }
  return 'muted'
}

// 处理动作意图（携带目标行）
interface HandleAction {
  intent: HandleIntent
  row: AlertEventItem
}

export default function AlertEventsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [level, setLevel] = useState('all')
  const [type, setType] = useState('all')
  const [status, setStatus] = useState('all')
  const [page, setPage] = useState(1)
  const [action, setAction] = useState<HandleAction | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['alert-events', level, type, status, page],
    queryFn: () =>
      fetchAlertEvents({
        level: level === 'all' ? undefined : level,
        type: type === 'all' ? undefined : type,
        page,
        size: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  // 处理状态无独立后端参数，客户端二次过滤（保留服务端分页 total 明示）
  const rows = useMemo(() => {
    const items = query.data?.items ?? []
    if (status === 'all') {
      return items
    }
    return items.filter((row) => row.status === status)
  }, [query.data, status])

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const mutation = useMutation({
    mutationFn: ({ row, intent, note }: { row: AlertEventItem; intent: HandleIntent; note: string }) =>
      handleAlertEvent(row.id, { status: intent, note: note === '' ? undefined : note }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['alert-events'] })
      setAction(null)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const columns = useMemo<DataTableColumn<AlertEventItem>[]>(
    () => [
      {
        header: t('observability.alertEvents.columns.time'),
        cell: (row) => <span className="text-xs">{new Date(row.createdAt).toLocaleString()}</span>,
      },
      {
        header: t('observability.alertEvents.columns.level'),
        cell: (row) => (
          <span className={`rounded-md px-1.5 py-0.5 text-xs ${levelSoft(levelToHealth(row.level))}`}>
            {t(`observability.alertEvents.level.${row.level}`)}
          </span>
        ),
      },
      {
        header: t('observability.alertEvents.columns.type'),
        cell: (row) => t(`observability.alertEvents.type.${row.type}`),
      },
      {
        header: t('observability.alertEvents.columns.serverId'),
        cell: (row) => <span className="font-mono text-xs">{row.serverId}</span>,
      },
      { header: t('observability.alertEvents.columns.message'), cell: (row) => row.message },
      {
        header: t('observability.alertEvents.columns.status'),
        cell: (row) => (
          <Badge variant={row.status === 'open' ? 'destructive' : row.status === 'resolved' ? 'secondary' : 'outline'}>
            {t(`observability.alertEvents.status.${row.status}`)}
          </Badge>
        ),
      },
      {
        header: t('observability.alertEvents.columns.actions'),
        cell: (row) =>
          row.status === 'open' ? (
            <div className="flex flex-wrap gap-1.5">
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setErrorText(null)
                  setAction({ intent: 'acknowledged', row })
                }}
              >
                {t('observability.alertEvents.actions.acknowledge')}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setErrorText(null)
                  setAction({ intent: 'resolved', row })
                }}
              >
                {t('observability.alertEvents.actions.resolve')}
              </Button>
            </div>
          ) : (
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <Link className="text-primary hover:underline" to={`/audits?targetRef=${row.serverId}`}>
                {t('observability.alertEvents.viewInAudits')}
              </Link>
              <Link className="text-primary hover:underline" to="/servers">
                {t('observability.alertEvents.viewInServers')}
              </Link>
            </div>
          ),
      },
    ],
    [t],
  )

  return (
    <section className="grid gap-6">
      <SectionHeader size="lg" title={t('nav.alertEvents')} />
      <AlertKpi total={total} items={query.data?.items ?? []} />

      <div className="flex flex-wrap items-center gap-2">
        <FilterSelect
          label={t('observability.alertEvents.filterLevel')}
          value={level}
          options={LEVELS.map((v) => ({ value: v, label: t(`observability.alertEvents.level.${v}`) }))}
          onChange={(value) => {
            setLevel(value)
            setPage(1)
          }}
        />
        <FilterSelect
          label={t('observability.alertEvents.filterType')}
          value={type}
          options={TYPES.map((v) => ({ value: v, label: t(`observability.alertEvents.type.${v}`) }))}
          onChange={(value) => {
            setType(value)
            setPage(1)
          }}
        />
        <FilterSelect
          label={t('observability.alertEvents.filterStatus')}
          value={status}
          options={STATUSES.map((v) => ({ value: v, label: t(`observability.alertEvents.status.${v}`) }))}
          onChange={(value) => {
            setStatus(value)
            setPage(1)
          }}
        />
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={columns.length} rows={8} />}
      >
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => String(row.id)}
          emptyText={t('observability.alertEvents.listEmpty')}
          density="compact"
        />
      </AsyncSection>

      {total > PAGE_SIZE && <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />}

      <HandleDialog
        intent={action?.intent ?? null}
        pending={mutation.isPending}
        errorText={errorText}
        onOpenChange={(open) => {
          if (!open) {
            setAction(null)
          }
        }}
        onConfirm={(note) => {
          if (action) {
            mutation.mutate({ row: action.row, intent: action.intent, note })
          }
        }}
      />
    </section>
  )
}
