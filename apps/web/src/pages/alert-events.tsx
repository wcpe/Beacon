// 告警事件页（/alert-events）：主从布局——KPI + 主列（吸顶过滤 + 自区滚列表 + 分页），右侧非模态详情面板。
// 详情面板内完成确认 / 标记已处理写闭环，状态即时更新；与 /audits、/servers 互跳（FR-157）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { TriangleAlert } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  DataTable,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { AlertEventItem } from '@beacon/devmock'

import { ApiClientError } from '../api/http'
import { fetchAlertEvents, handleAlertEvent } from '../api/observability'
import FilterSelect from '../features/observability/filter-select'
import ListCard from '../features/observability/list-card'
import MasterDetail from '../features/observability/master-detail'
import Pager from '../features/observability/pager'
import AlertDetailPanel, { type HandleIntent } from './alert-events/alert-detail-panel'
import AlertKpi from './alert-events/alert-kpi'

const PAGE_SIZE = 15
const LEVELS = ['info', 'warning', 'critical'] as const
const TYPES = ['health-transition', 'publish-fail', 'backend-unreachable'] as const
const STATUSES = ['open', 'acknowledged', 'resolved'] as const

// 告警级别 → 状态药丸语义 variant：严重危急、警告注意、提示次要。
function levelBadgeVariant(level: AlertEventItem['level']): 'crit' | 'warn' | 'off' {
  if (level === 'critical') {
    return 'crit'
  }
  if (level === 'warning') {
    return 'warn'
  }
  return 'off'
}

// 处理状态 → 状态药丸语义 variant：待处理危急、已处理正常、已确认次要。
function statusBadgeVariant(status: AlertEventItem['status']): 'crit' | 'ok' | 'off' {
  if (status === 'open') {
    return 'crit'
  }
  if (status === 'resolved') {
    return 'ok'
  }
  return 'off'
}

export default function AlertEventsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [level, setLevel] = useState('all')
  const [type, setType] = useState('all')
  const [status, setStatus] = useState('all')
  const [page, setPage] = useState(1)
  const [selectedId, setSelectedId] = useState<number | null>(null)
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

  // 选中行从最新数据派生，写操作后状态即时反映到详情面板
  const selected = useMemo(
    () => (query.data?.items ?? []).find((row) => row.id === selectedId) ?? null,
    [query.data, selectedId],
  )

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const mutation = useMutation({
    mutationFn: ({ id, intent, note }: { id: number; intent: HandleIntent; note: string }) =>
      handleAlertEvent(id, { status: intent, note: note === '' ? undefined : note }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['alert-events'] })
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const columns = useMemo<DataTableColumn<AlertEventItem>[]>(
    () => [
      {
        header: t('observability.alertEvents.columns.time'),
        cell: (row) => <span className="tabular-nums text-xs text-ink-3">{new Date(row.createdAt).toLocaleString()}</span>,
      },
      {
        header: t('observability.alertEvents.columns.level'),
        cell: (row) => (
          <Badge variant={levelBadgeVariant(row.level)}>{t(`observability.alertEvents.level.${row.level}`)}</Badge>
        ),
      },
      {
        header: t('observability.alertEvents.columns.type'),
        cell: (row) => <span className="text-ink-3">{t(`observability.alertEvents.type.${row.type}`)}</span>,
      },
      {
        header: t('observability.alertEvents.columns.serverId'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.serverId}</span>,
      },
      { header: t('observability.alertEvents.columns.message'), cell: (row) => <span className="text-ink-2">{row.message}</span> },
      {
        header: t('observability.alertEvents.columns.status'),
        cell: (row) => (
          <Badge variant={statusBadgeVariant(row.status)}>{t(`observability.alertEvents.status.${row.status}`)}</Badge>
        ),
      },
    ],
    [t],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <TriangleAlert className="size-[15px]" />
          </span>
          {t('observability.alertEvents.listTitle')}
        </span>
        {total > 0 && <span className="text-xs text-ink-3">{t('observability.common.total', { count: total })}</span>}
      </div>
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
    </div>
  )

  const master = (
    <div className="grid gap-3.5">
      <AlertKpi total={total} items={query.data?.items ?? []} />
      <ListCard
        toolbar={toolbar}
        footer={
          total > PAGE_SIZE ? (
            <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />
          ) : undefined
        }
      >
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
            onRowClick={(row) => {
              setErrorText(null)
              setSelectedId(row.id)
            }}
            rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50/60' : undefined)}
          />
        </AsyncSection>
      </ListCard>
    </div>
  )

  return (
    <section className="grid gap-5">
      <SectionHeader
        size="lg"
        icon={<TriangleAlert className="size-5" />}
        title={t('nav.alertEvents')}
        count={t('observability.alertEvents.mission')}
      />
      <MasterDetail
        master={master}
        detail={
          selected ? (
            <AlertDetailPanel
              item={selected}
              pending={mutation.isPending}
              errorText={errorText}
              onHandle={(intent, note) => {
                setErrorText(null)
                mutation.mutate({ id: selected.id, intent, note })
              }}
            />
          ) : null
        }
        detailTitle={t('observability.alertEvents.detailTitle')}
        closeLabel={t('observability.common.close')}
        onClose={() => {
          setSelectedId(null)
          setErrorText(null)
        }}
      />
    </section>
  )
}
