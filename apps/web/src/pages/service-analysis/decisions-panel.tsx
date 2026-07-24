// 调度决策下钻（/service-analysis 板块）：时间窗必选（默认近 1h）+ serverId / 结果过滤 + 服务端分页列表，
// 点行在右侧非模态详情面板看决策上下文与逐台排除原因（可解释「为什么没选某台」）。不依赖左侧选服。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Workflow } from 'lucide-react'

import { AsyncSection, Badge, Checkbox, DataTable, Input, TableSkeleton, type DataTableColumn } from '@beacon/ui'
import type { SchedDecisionItem } from '@beacon/contracts'

import { fetchSchedDecisions } from '../../api/metrics'
import {
  filterItemsByEnvScope,
  needsClientEnvFilter,
  resolveApiNamespaceId,
  useEnvNamespaceScope,
} from '../../features/env/use-env-scope'
import FilterSelect from '../../features/observability/filter-select'
import Pager from '../../features/observability/pager'
import CursorPager from '../../features/observability/cursor-pager'
import { useCursorStack } from '../../features/observability/use-cursor-stack'
import ListCard from '../../features/shared/list-card'
import MasterDetail from '../../features/shared/master-detail'
import DecisionDetail from './decision-detail'
import WindowSelect, { WINDOW_MS, type WindowKey } from '../../features/observability/window-select'

const PAGE_SIZE = 15
// 结果过滤候选（「全部」由 FilterSelect 自动前置）
const RESULTS = ['success', 'failed'] as const

export default function DecisionsPanel() {
  const { t } = useTranslation()
  // FR-178：调度决策跟随顶栏 env（namespaceId 维度）
  const envScope = useEnvNamespaceScope()
  const apiNamespaceId = resolveApiNamespaceId(undefined, envScope)
  const clientFilter = needsClientEnvFilter(envScope)
  const [windowKey, setWindowKey] = useState<WindowKey>('1h')
  const [keyword, setKeyword] = useState('')
  const [result, setResult] = useState('all')
  const [page, setPage] = useState(1)
  // 冷查询（含归档）开关：开启后跨热 / 冷并表、分页改 keyset 游标（FR-152）
  const [cold, setCold] = useState(false)
  const cursor = useCursorStack()
  // 当前查看详情的 traceId（null 表示右侧详情列收起）
  const [selected, setSelected] = useState<string | null>(null)

  // 切换过滤 / 时间窗 / 冷查询开关时回到首页（热重置页码、冷重置游标栈）
  const resetPaging = () => {
    setPage(1)
    cursor.reset()
  }

  const query = useQuery({
    queryKey: [
      'service-analysis',
      'sched-decisions',
      windowKey,
      keyword,
      result,
      cold,
      cold ? cursor.cursor : String(page),
      apiNamespaceId,
      envScope,
    ],
    queryFn: () => {
      // 时间范围必填：按预设时间窗自「现在」往前推（毫秒时间戳，对齐后端 from/to 必填约束）
      const to = Date.now()
      const from = to - WINDOW_MS[windowKey]
      const serverId = keyword.trim() === '' ? undefined : keyword.trim()
      const resultFilter = result === 'all' ? undefined : result
      if (cold) {
        return fetchSchedDecisions({
          from,
          to,
          namespaceId: apiNamespaceId,
          serverId,
          result: resultFilter,
          includeArchived: true,
          cursor: cursor.cursor,
          pageSize: PAGE_SIZE,
        })
      }
      return fetchSchedDecisions({
        from,
        to,
        namespaceId: apiNamespaceId,
        serverId,
        result: resultFilter,
        page,
        pageSize: PAGE_SIZE,
      })
    },
    placeholderData: keepPreviousData,
  })

  // env 多 ns 时 API 只能传单 id，对当前页结果再收窄
  const decisionRows = useMemo(() => {
    const items = query.data?.items ?? []
    return clientFilter ? filterItemsByEnvScope(items, envScope) : items
  }, [query.data, clientFilter, envScope])

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const nextCursor = query.data?.nextCursor ?? null
  const dash = t('observability.serviceAnalysis.dash')

  const columns = useMemo<DataTableColumn<SchedDecisionItem>[]>(
    () => [
      {
        header: t('observability.serviceAnalysis.decisions.columns.time'),
        cell: (row) => (
          <span className="tabular-nums text-xs text-ink-3">{new Date(row.tsMs).toLocaleString()}</span>
        ),
      },
      {
        header: t('observability.serviceAnalysis.decisions.columns.traceId'),
        cell: (row) => (
          <span className="block max-w-36 truncate font-mono text-xs text-ink-2" title={row.traceId}>
            {row.traceId}
          </span>
        ),
      },
      {
        header: t('observability.serviceAnalysis.decisions.columns.result'),
        cell: (row) => (
          <Badge variant={row.failReason === null ? 'ok' : 'crit'}>
            {row.failReason === null
              ? t('observability.serviceAnalysis.decisions.result.success')
              : t('observability.serviceAnalysis.decisions.result.failed')}
          </Badge>
        ),
      },
      {
        header: t('observability.serviceAnalysis.decisions.columns.chosen'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.chosenServerId ?? dash}</span>,
      },
      {
        header: t('observability.serviceAnalysis.decisions.columns.reason'),
        cell: (row) => (
          <span className="text-xs text-ink-3">
            {row.failReason ??
              (row.excludedCount > 0
                ? t('observability.serviceAnalysis.decisions.excludedSummary', { count: row.excludedCount })
                : dash)}
          </span>
        ),
      },
    ],
    [t, dash],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <Workflow className="size-[15px]" />
          </span>
          {t('observability.serviceAnalysis.decisions.title')}
        </span>
        <span className="text-xs text-ink-4">{t('observability.serviceAnalysis.decisions.mission')}</span>
        {total > 0 && <span className="text-xs text-ink-3">{t('observability.common.total', { count: total })}</span>}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <WindowSelect
          value={windowKey}
          keys={['1h', '6h', '24h', '7d']}
          onChange={(key) => {
            setWindowKey(key)
            resetPaging()
          }}
        />
        <Input
          aria-label={t('observability.serviceAnalysis.decisions.filterServer')}
          placeholder={t('observability.serviceAnalysis.decisions.filterServer')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            resetPaging()
          }}
          className="w-56"
        />
        <FilterSelect
          label={t('observability.serviceAnalysis.decisions.filterResult')}
          value={result}
          options={RESULTS.map((v) => ({
            value: v,
            label: t(`observability.serviceAnalysis.decisions.result.${v}`),
          }))}
          onChange={(value) => {
            setResult(value)
            resetPaging()
          }}
        />
        <label className="flex cursor-pointer items-center gap-2 text-sm text-ink-2" title={t('observability.common.includeArchivedHint')}>
          <Checkbox
            checked={cold}
            onCheckedChange={(v) => {
              setCold(v === true)
              resetPaging()
            }}
            aria-label={t('observability.common.includeArchived')}
          />
          {t('observability.common.includeArchived')}
        </label>
      </div>
    </div>
  )

  return (
    <MasterDetail
      master={
        <ListCard
          toolbar={toolbar}
          footer={
            cold
              ? nextCursor !== null || cursor.canPrev
                ? (
                    <CursorPager
                      pageIndex={cursor.pageIndex}
                      canPrev={cursor.canPrev}
                      canNext={nextCursor !== null}
                      onPrev={cursor.goPrev}
                      onNext={() => {
                        if (nextCursor !== null) {
                          cursor.goNext(nextCursor)
                        }
                      }}
                    />
                  )
                : undefined
              : total > PAGE_SIZE
                ? <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />
                : undefined
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
              rows={decisionRows}
              rowKey={(row) => row.traceId}
              emptyText={t('observability.serviceAnalysis.decisions.empty')}
              density="compact"
              onRowClick={(row) => {
                setSelected(row.traceId)
              }}
              rowClassName={(row) => (row.traceId === selected ? 'bg-brand-50/60' : undefined)}
            />
          </AsyncSection>
        </ListCard>
      }
      detail={selected === null ? null : <DecisionDetail traceId={selected} />}
      detailTitle={t('observability.serviceAnalysis.decisions.detailTitle')}
      closeLabel={t('observability.common.close')}
      onClose={() => {
        setSelected(null)
      }}
    />
  )
}
