// 告警事件页（/alert-events）：主从布局——KPI + 主列（吸顶过滤 + 自区滚列表 + 分页），右侧非模态详情面板。
// 详情面板内完成确认 / 标记已处理写闭环，状态即时更新；与 /audits、/servers 互跳（FR-157）。
// 列表支持多选 open 行，工具栏批量确认 / 批量标记已处理（顺序 POST 单条 handle API）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { TriangleAlert } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  PageHeader,
  TableSkeleton,
  Textarea,
  type DataTableColumn,
} from '@beacon/ui'
import type { AlertEventItem } from '@beacon/contracts'

import { ApiClientError } from '../api/http'
import { fetchAlertEvents, handleAlertEvent } from '../api/observability'
import {
  filterItemsByEnvCodes,
  useEnvNamespaceCodes,
} from '../features/env/use-env-scope'
import {
  alertSubtitle,
  healthStatusLabel,
  parseHealthTransition,
} from '../features/observability/alert-transition'
import FilterSelect from '../features/observability/filter-select'
import ListCard from '../features/shared/list-card'
import MasterDetail from '../features/shared/master-detail'
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

// 健康流转解析与标签：见 features/observability/alert-transition

export default function AlertEventsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // FR-178：列表跟随顶栏 env（namespace 字符串；单 ns 走 API，多 ns 客户端滤）
  const envCodes = useEnvNamespaceCodes()
  const apiNamespace = envCodes !== null && envCodes.length === 1 ? envCodes[0] : undefined

  const [level, setLevel] = useState('all')
  const [type, setType] = useState('all')
  const [status, setStatus] = useState('all')
  const [page, setPage] = useState(1)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  // 批量勾选：仅 open 行 id
  const [checkedIds, setCheckedIds] = useState<Set<number>>(new Set())
  // 批量 resolved 时展开备注区
  const [batchResolveOpen, setBatchResolveOpen] = useState(false)
  const [batchNote, setBatchNote] = useState('')
  // 单条处理错误（详情面板内联）
  const [errorText, setErrorText] = useState<string | null>(null)
  // 批量进度 / 批量错误（工具栏内联，与详情错误分离）
  const [batchProgress, setBatchProgress] = useState<string | null>(null)
  const [batchErrorText, setBatchErrorText] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['alert-events', level, type, status, page, apiNamespace, envCodes],
    queryFn: () =>
      fetchAlertEvents({
        level: level === 'all' ? undefined : level,
        type: type === 'all' ? undefined : type,
        namespace: apiNamespace,
        page,
        size: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  // 处理状态无独立后端参数，客户端二次过滤；env 多 ns 时再按 code 收窄
  const rows = useMemo(() => {
    let items = query.data?.items ?? []
    if (envCodes !== null && envCodes.length !== 1) {
      items = filterItemsByEnvCodes(items, envCodes)
    }
    if (status === 'all') {
      return items
    }
    return items.filter((row) => row.status === status)
  }, [query.data, status, envCodes])

  // 选中行从最新数据派生，写操作后状态即时反映到详情面板
  const selected = useMemo(
    () => rows.find((row) => row.id === selectedId) ?? null,
    [rows, selectedId],
  )

  // 当前页 open 行（批量勾选范围）
  const openRows = useMemo(() => rows.filter((row) => row.status === 'open'), [rows])
  const openIds = useMemo(() => openRows.map((row) => row.id), [openRows])
  const allOpenChecked = openIds.length > 0 && openIds.every((id) => checkedIds.has(id))
  const someOpenChecked = openIds.some((id) => checkedIds.has(id))

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  // 切换筛选时清空勾选（level / type / status / page 任一变）
  const clearSelection = () => {
    setCheckedIds(new Set())
    setBatchResolveOpen(false)
    setBatchNote('')
    setBatchProgress(null)
    setBatchErrorText(null)
  }

  const toggleOne = (id: number, open: boolean) => {
    if (!open) {
      return
    }
    setCheckedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  const toggleAllOpen = () => {
    setCheckedIds((prev) => {
      if (openIds.length === 0) {
        return prev
      }
      // 已全选则清空当前页 open；否则并入当前页 open
      if (openIds.every((id) => prev.has(id))) {
        const next = new Set(prev)
        for (const id of openIds) {
          next.delete(id)
        }
        return next
      }
      const next = new Set(prev)
      for (const id of openIds) {
        next.add(id)
      }
      return next
    })
  }

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

  // 批量：对选中 id 顺序 await 单条 handle；全部成功后 invalidate；部分失败展示 batchErrorText
  const runBatch = async (intent: HandleIntent, note: string) => {
    const ids = Array.from(checkedIds)
    if (ids.length === 0) {
      return
    }
    setBatchErrorText(null)
    setBatchProgress(t('observability.alertEvents.batchProgress', { done: 0, total: ids.length }))
    let ok = 0
    let fail = 0
    let lastError: string | null = null
    for (let i = 0; i < ids.length; i += 1) {
      const id = ids[i]
      try {
        await handleAlertEvent(id, { status: intent, note: note === '' ? undefined : note })
        ok += 1
      } catch (error) {
        fail += 1
        lastError = error instanceof ApiClientError ? error.message : String(error)
      }
      setBatchProgress(t('observability.alertEvents.batchProgress', { done: i + 1, total: ids.length }))
    }
    await queryClient.invalidateQueries({ queryKey: ['alert-events'] })
    setBatchProgress(null)
    setCheckedIds(new Set())
    setBatchResolveOpen(false)
    setBatchNote('')
    if (fail > 0) {
      setBatchErrorText(
        lastError
          ? `${t('observability.alertEvents.batchPartialFail', { ok, fail })}：${lastError}`
          : t('observability.alertEvents.batchPartialFail', { ok, fail }),
      )
    }
  }

  const batchPending = batchProgress !== null

  const columns = useMemo<DataTableColumn<AlertEventItem>[]>(
    () => [
      {
        header: (
          <Checkbox
            checked={allOpenChecked ? true : someOpenChecked ? 'indeterminate' : false}
            disabled={openIds.length === 0 || batchPending}
            onCheckedChange={() => {
              toggleAllOpen()
            }}
            aria-label={t('observability.alertEvents.batchSelected', { count: openIds.length })}
            onClick={(e) => {
              e.stopPropagation()
            }}
          />
        ),
        cell: (row) => {
          const isOpen = row.status === 'open'
          return (
            <span
              onClick={(e) => {
                e.stopPropagation()
              }}
              onKeyDown={(e) => {
                e.stopPropagation()
              }}
            >
              <Checkbox
                checked={checkedIds.has(row.id)}
                disabled={!isOpen || batchPending}
                onCheckedChange={() => {
                  toggleOne(row.id, isOpen)
                }}
                aria-label={String(row.id)}
              />
            </span>
          )
        },
      },
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
      {
        header: t('observability.alertEvents.columns.transition'),
        cell: (row) => {
          const tr = parseHealthTransition(row)
          if (tr === null) {
            // 非健康流转类告警：用摘要截断，避免空列
            if (row.type !== 'health-transition') {
              return (
                <span className="line-clamp-1 max-w-[14rem] text-xs text-ink-3" title={row.message}>
                  {row.message || t('observability.alertEvents.transitionUnknown')}
                </span>
              )
            }
            return <span className="text-ink-4">{t('observability.alertEvents.transitionUnknown')}</span>
          }
          return (
            <span className="inline-flex flex-wrap items-center gap-1 text-xs">
              <Badge variant="off">{healthStatusLabel(t, tr.from)}</Badge>
              <span className="text-ink-4">→</span>
              <Badge variant={row.level === 'critical' ? 'crit' : 'warn'}>{healthStatusLabel(t, tr.to)}</Badge>
            </span>
          )
        },
      },
      {
        header: t('observability.alertEvents.columns.message'),
        cell: (row) => {
          // 健康流转摘要 i18n；其它类型保留后端原文
          const summary = row.type === 'health-transition' ? alertSubtitle(row, t) : row.message
          return (
            <span className="line-clamp-2 max-w-[18rem] text-ink-2" title={summary}>
              {summary || '—'}
            </span>
          )
        },
      },
      {
        header: t('observability.alertEvents.columns.status'),
        cell: (row) => (
          <Badge variant={statusBadgeVariant(row.status)}>{t(`observability.alertEvents.status.${row.status}`)}</Badge>
        ),
      },
    ],
    [t, checkedIds, allOpenChecked, someOpenChecked, openIds.length, batchPending],
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
            clearSelection()
          }}
        />
        <FilterSelect
          label={t('observability.alertEvents.filterType')}
          value={type}
          options={TYPES.map((v) => ({ value: v, label: t(`observability.alertEvents.type.${v}`) }))}
          onChange={(value) => {
            setType(value)
            setPage(1)
            clearSelection()
          }}
        />
        <FilterSelect
          label={t('observability.alertEvents.filterStatus')}
          value={status}
          options={STATUSES.map((v) => ({ value: v, label: t(`observability.alertEvents.status.${v}`) }))}
          onChange={(value) => {
            setStatus(value)
            setPage(1)
            clearSelection()
          }}
        />
      </div>
      {/* 批量操作条：已选 N + 批量确认 / 批量标记已处理 */}
      {checkedIds.size > 0 && (
        <div className="grid gap-2 rounded-lg border border-border bg-secondary/40 px-2.5 py-2">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs font-medium text-brand-600">
              {t('observability.alertEvents.batchSelected', { count: checkedIds.size })}
            </span>
            <Button
              size="sm"
              variant="outline"
              disabled={batchPending}
              onClick={() => {
                void runBatch('acknowledged', '')
              }}
            >
              {t('observability.alertEvents.batchAcknowledge')}
            </Button>
            <Button
              size="sm"
              disabled={batchPending}
              onClick={() => {
                setBatchResolveOpen((v) => !v)
                setBatchErrorText(null)
              }}
            >
              {t('observability.alertEvents.batchResolve')}
            </Button>
            {batchProgress !== null && <span className="text-xs text-ink-3">{batchProgress}</span>}
          </div>
          {batchResolveOpen && (
            <div className="grid gap-2">
              <Textarea
                value={batchNote}
                placeholder={t('observability.alertEvents.batchNotePlaceholder')}
                onChange={(e) => {
                  setBatchNote(e.target.value)
                }}
                disabled={batchPending}
              />
              <div className="flex flex-wrap gap-2">
                <Button
                  size="sm"
                  disabled={batchPending || batchNote.trim() === ''}
                  onClick={() => {
                    void runBatch('resolved', batchNote.trim())
                  }}
                >
                  {t('observability.alertEvents.confirmResolve')}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={batchPending}
                  onClick={() => {
                    setBatchResolveOpen(false)
                    setBatchNote('')
                  }}
                >
                  {t('observability.alertEvents.cancel')}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
      {batchErrorText !== null && <p className="text-sm text-destructive">{batchErrorText}</p>}
    </div>
  )

  const master = (
    <div className="grid gap-3.5">
      <AlertKpi total={total} items={query.data?.items ?? []} />
      <ListCard
        toolbar={toolbar}
        footer={
          total > PAGE_SIZE ? (
            <Pager
              page={page}
              pageCount={pageCount}
              total={total}
              onPageChange={(next) => {
                setPage(next)
                clearSelection()
              }}
            />
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
      <PageHeader
        icon={<TriangleAlert className="size-5" />}
        title={t('nav.alertEvents')}
        description={t('observability.alertEvents.mission')}
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
