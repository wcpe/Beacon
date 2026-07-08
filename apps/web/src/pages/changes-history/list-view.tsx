// 交付历史列表：按状态筛选 + 标题搜索 + 服务端分页；行「查看」进详情、「在变更单中打开」跳 /changes。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { History } from 'lucide-react'

import {
  AsyncSection,
  Button,
  DataTable,
  Input,
  SectionHeader,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'

import Pager from '../../features/delivery/pager'
import {
  fetchChangeOrders,
  type ChangeOrderStatus,
  type ChangeOrderSummary,
} from '../../api/delivery-changes'
import StatusBadge from './status-badge'
import { formatTime } from './format'

const PAGE_SIZE = 15

// 历史页聚焦已终态的单，但也允许查看进行中的（回滚场景）
const STATUS_OPTIONS: ChangeOrderStatus[] = [
  'completed',
  'rolled_back',
  'cancelled',
  'rolling_back',
  'paused',
  'rolling',
]

interface ListViewProps {
  namespaceId: number
  onView: (id: number) => void
}

export default function ListView({ namespaceId, onView }: ListViewProps) {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState('all')
  const [page, setPage] = useState(1)

  const query = useQuery({
    queryKey: ['change-orders', 'history', namespaceId, keyword, status, page],
    queryFn: () =>
      fetchChangeOrders({
        namespaceId,
        keyword: keyword.trim() === '' ? undefined : keyword.trim(),
        status: status === 'all' ? undefined : status,
        page,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
    enabled: namespaceId > 0,
  })

  const total = query.data?.total ?? 0

  const columns = useMemo<DataTableColumn<ChangeOrderSummary>[]>(
    () => [
      { header: t('delivery.changesHistory.list.columns.title'), cell: (row) => row.title },
      {
        header: t('delivery.changesHistory.list.columns.status'),
        cell: (row) => <StatusBadge status={row.status} />,
      },
      {
        header: t('delivery.changesHistory.list.columns.finishedAt'),
        cell: (row) => formatTime(row.finishedAt),
      },
      { header: t('delivery.changesHistory.list.columns.createdBy'), cell: (row) => row.createdBy },
      {
        header: '',
        cell: (row) => (
          <div className="flex items-center gap-1.5">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                onView(row.id)
              }}
            >
              {t('delivery.changesHistory.list.view')}
            </Button>
            <Button size="sm" variant="ghost" asChild>
              <Link to={`/changes?order=${String(row.id)}`}>
                {t('delivery.changesHistory.list.openInChanges')}
              </Link>
            </Button>
          </div>
        ),
      },
    ],
    [t, onView],
  )

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<History className="size-4" />}
        title={t('delivery.changesHistory.list.title')}
      />

      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('delivery.changesHistory.list.keyword')}
          placeholder={t('delivery.changesHistory.list.keyword')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="w-52"
        />
        <Select
          value={status}
          onValueChange={(value) => {
            setStatus(value)
            setPage(1)
          }}
        >
          <SelectTrigger className="w-40" aria-label={t('delivery.changesHistory.list.filterStatus')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('delivery.changesHistory.list.allStatus')}</SelectItem>
            {STATUS_OPTIONS.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`delivery.changes.status.${s}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={5} rows={6} />}
      >
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => String(row.id)}
          emptyText={t('delivery.changesHistory.list.empty')}
          density="compact"
        />
      </AsyncSection>

      <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} />
    </section>
  )
}
