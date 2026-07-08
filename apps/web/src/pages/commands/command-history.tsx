// 命令历史：类型 / 状态过滤 + serverId 搜索 + 服务端分页，展示命令生命周期结果。
// 与 /audits 互跳（FR-157）：按命令追溯审计记录。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ArrowUpRight, History } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  DataTable,
  Input,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { CommandItem } from '@beacon/devmock'

import { fetchCommands } from '../../api/observability'
import FilterSelect from '../../features/observability/filter-select'
import Pager from '../../features/observability/pager'

const PAGE_SIZE = 15
const COMMAND_TYPES = ['asset_rescan', 'asset_read', 'ingest-plugins', 'tail-logs', 'resync-config'] as const
const COMMAND_STATUSES = ['pending', 'fetched', 'done', 'failed', 'expired'] as const

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

export default function CommandHistory() {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  const [type, setType] = useState('all')
  const [status, setStatus] = useState('all')
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
      {
        header: '',
        headClassName: 'w-24',
        cell: (row) => (
          <Link
            className="inline-flex items-center gap-0.5 text-xs text-brand-600 hover:underline"
            to={`/audits?targetRef=${row.serverId}`}
          >
            {t('observability.commands.viewInAudits')}
            <ArrowUpRight className="size-3" />
          </Link>
        ),
      },
    ],
    [t],
  )

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<History className="size-4" />}
        title={t('observability.commands.historyTitle')}
        count={total > 0 ? t('observability.common.total', { count: total }) : undefined}
      />

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

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={columns.length} rows={8} />}
      >
        <div className="overflow-hidden rounded-xl border border-border bg-card shadow-card">
          <DataTable
            columns={columns}
            rows={query.data?.items}
            rowKey={(row) => String(row.commandId)}
            emptyText={t('observability.commands.historyEmpty')}
            density="compact"
          />
        </div>
      </AsyncSection>

      {total > PAGE_SIZE && (
        <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />
      )}
    </section>
  )
}
