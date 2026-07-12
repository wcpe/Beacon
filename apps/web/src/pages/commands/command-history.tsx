// 命令历史（主列）：吸顶工具条（类型 / 状态过滤 + serverId 搜索）+ 自区滚动列表 + 吸底服务端分页。
// 行点击交父级用右侧非模态详情面板承载（含双向生命周期与在审计中追溯，FR-157）；选中行高亮。
// 筛选初值消费 URL 查询参数（serverId/type/status），承接 /audits 详情等页的互跳链接（FR-157）；
// 页内变更筛选不回写 URL（最简策略）。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { History } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  DataTable,
  Input,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { CommandItem } from '@beacon/contracts'

import { fetchCommands } from '../../api/observability'
import FilterSelect from '../../features/observability/filter-select'
import ListCard from '../../features/shared/list-card'
import Pager from '../../features/observability/pager'

const PAGE_SIZE = 15
const COMMAND_TYPES = ['asset_rescan', 'asset_read', 'ingest-plugins', 'tail-logs', 'resync-config'] as const
const COMMAND_STATUSES = ['pending', 'fetched', 'done', 'failed', 'expired'] as const

// 从 URL 查询参数取筛选初值：值在候选集内才采纳，否则回退「全部」（防脏参数打乱下拉展示）
function initialOption(params: URLSearchParams, name: string, options: readonly string[]): string {
  const value = params.get(name)
  return value !== null && options.includes(value) ? value : 'all'
}

// 命令状态 → 状态药丸语义色：done 正常绿、failed/expired 危急红、其余次要。
function badgeVariant(status: CommandItem['status']): 'ok' | 'off' | 'crit' {
  if (status === 'failed' || status === 'expired') {
    return 'crit'
  }
  if (status === 'done') {
    return 'ok'
  }
  return 'off'
}

interface CommandHistoryProps {
  // 点击命令行看详情（父级用右侧非模态面板承载）
  onView: (item: CommandItem) => void
  // 当前选中命令 id（高亮用）
  selectedId: number | null
}

export default function CommandHistory({ onView, selectedId }: CommandHistoryProps) {
  const { t } = useTranslation()
  // 互跳承接：以 URL 查询参数为筛选初值（仅初始化，页内变更不回写 URL）
  const [searchParams] = useSearchParams()
  const [keyword, setKeyword] = useState(() => searchParams.get('serverId') ?? '')
  const [type, setType] = useState(() => initialOption(searchParams, 'type', COMMAND_TYPES))
  const [status, setStatus] = useState(() => initialOption(searchParams, 'status', COMMAND_STATUSES))
  const [page, setPage] = useState(1)

  const query = useQuery({
    queryKey: ['commands', 'history', keyword, type, status, page],
    queryFn: () =>
      fetchCommands({
        serverId: keyword.trim() === '' ? undefined : keyword.trim(),
        type: type === 'all' ? undefined : type,
        status: status === 'all' ? undefined : status,
        page,
        size: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const columns = useMemo<DataTableColumn<CommandItem>[]>(
    () => [
      {
        header: t('observability.commands.columns.createdAt'),
        cell: (row) => <span className="tabular-nums text-xs text-ink-3">{new Date(row.createdAt).toLocaleString()}</span>,
      },
      {
        header: t('observability.commands.columns.serverId'),
        cell: (row) => <span className="font-mono text-xs text-ink-2">{row.serverId}</span>,
      },
      { header: t('observability.commands.columns.type'), cell: (row) => <span className="text-ink-2">{row.type}</span> },
      {
        header: t('observability.commands.columns.status'),
        cell: (row) => (
          <Badge variant={badgeVariant(row.status)}>{t(`observability.commands.status.${row.status}`)}</Badge>
        ),
      },
      { header: t('observability.commands.columns.operator'), cell: (row) => <span className="text-ink-2">{row.operator}</span> },
      {
        header: t('observability.commands.columns.result'),
        cell: (row) => <span className="text-xs text-ink-3">{row.resultDetail || '—'}</span>,
      },
    ],
    [t],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <History className="size-[15px]" />
          </span>
          {t('observability.commands.historyTitle')}
        </span>
        {total > 0 && <span className="text-xs text-ink-3">{t('observability.common.total', { count: total })}</span>}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('observability.commands.filterServer')}
          placeholder={t('observability.commands.filterServer')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="w-52"
        />
        <FilterSelect
          label={t('observability.commands.filterType')}
          value={type}
          options={COMMAND_TYPES.map((v) => ({ value: v, label: v }))}
          onChange={(value) => {
            setType(value)
            setPage(1)
          }}
        />
        <FilterSelect
          label={t('observability.commands.filterStatus')}
          value={status}
          options={COMMAND_STATUSES.map((v) => ({
            value: v,
            label: t(`observability.commands.status.${v}`),
          }))}
          onChange={(value) => {
            setStatus(value)
            setPage(1)
          }}
        />
      </div>
    </div>
  )

  return (
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
          rowKey={(row) => String(row.commandId)}
          emptyText={t('observability.commands.historyEmpty')}
          density="compact"
          onRowClick={onView}
          rowClassName={(row) => (row.commandId === selectedId ? 'bg-brand-50/60' : undefined)}
        />
      </AsyncSection>
    </ListCard>
  )
}
