// 调度决策下钻（/service-analysis 板块）：时间窗必选（默认近 1h）+ serverId / 结果过滤 + 服务端分页列表，
// 点行在右侧非模态详情面板看决策上下文与逐台排除原因（可解释「为什么没选某台」）。不依赖左侧选服。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Workflow } from 'lucide-react'

import { AsyncSection, Badge, DataTable, Input, TableSkeleton, type DataTableColumn } from '@beacon/ui'
import type { SchedDecisionItem } from '@beacon/contracts'

import { fetchSchedDecisions } from '../../api/metrics'
import FilterSelect from '../../features/observability/filter-select'
import Pager from '../../features/observability/pager'
import ListCard from '../../features/shared/list-card'
import MasterDetail from '../../features/shared/master-detail'
import DecisionDetail from './decision-detail'
import WindowSelect, { WINDOW_MS, type WindowKey } from './window-select'

const PAGE_SIZE = 15
// 结果过滤候选（「全部」由 FilterSelect 自动前置）
const RESULTS = ['success', 'failed'] as const

export default function DecisionsPanel() {
  const { t } = useTranslation()
  const [windowKey, setWindowKey] = useState<WindowKey>('1h')
  const [keyword, setKeyword] = useState('')
  const [result, setResult] = useState('all')
  const [page, setPage] = useState(1)
  // 当前查看详情的 traceId（null 表示右侧详情列收起）
  const [selected, setSelected] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['service-analysis', 'sched-decisions', windowKey, keyword, result, page],
    queryFn: () => {
      // 时间范围必填：按预设时间窗自「现在」往前推（毫秒时间戳，对齐后端 from/to 必填约束）
      const to = Date.now()
      return fetchSchedDecisions({
        from: to - WINDOW_MS[windowKey],
        to,
        serverId: keyword.trim() === '' ? undefined : keyword.trim(),
        result: result === 'all' ? undefined : result,
        page,
        pageSize: PAGE_SIZE,
      })
    },
    placeholderData: keepPreviousData,
  })

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
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
            setPage(1)
          }}
        />
        <Input
          aria-label={t('observability.serviceAnalysis.decisions.filterServer')}
          placeholder={t('observability.serviceAnalysis.decisions.filterServer')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
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
            setPage(1)
          }}
        />
      </div>
    </div>
  )

  return (
    <MasterDetail
      master={
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
              rows={query.data?.items}
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
