// 交付历史列表（主从布局主列）：ListCard 吸顶工具条（条数 / 状态筛选 / 标题搜索）+ 自区滚列表 + 吸底分页。
// 页面级标题与文档入口已上移页头（PageHeader），此处只留列表自己的筛选与计数。
// 点行选中（右侧非模态详情面板打开）；行「在变更单中打开」跳 /changes。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import {
  Button,
  Combobox,
  DataTable,
  Input,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import PageAsyncSection from '../../features/shared/page-async-section'

import ListCard from '../../features/shared/list-card'
import Pager from '../../features/delivery/pager'
import {
  fetchChangeOrders,
  type ChangeOrderStatus,
  type ChangeOrderSummary,
} from '../../api/delivery-changes'
import StatusBadge from './status-badge'
import EmptyGuide from './empty-guide'
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
  selectedId: number | null
  onView: (order: ChangeOrderSummary) => void
}

export default function ListView({ namespaceId, selectedId, onView }: ListViewProps) {
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
      {
        header: t('delivery.changesHistory.list.columns.id'),
        headClassName: 'text-right',
        className: 'text-right tabular-nums',
        cell: (row) => <span className="tnum text-xs text-ink-3">#{String(row.id)}</span>,
      },
      {
        header: t('delivery.changesHistory.list.columns.title'),
        cell: (row) => <span className="block max-w-64 truncate" title={row.title}>{row.title}</span>,
      },
      {
        header: t('delivery.changesHistory.list.columns.status'),
        cell: (row) => <StatusBadge status={row.status} />,
      },
      {
        header: t('delivery.changesHistory.list.columns.finishedAt'),
        cell: (row) => <span className="tnum text-xs text-ink-3">{formatTime(row.finishedAt)}</span>,
      },
      { header: t('delivery.changesHistory.list.columns.createdBy'), cell: (row) => row.createdBy },
      {
        header: '',
        headClassName: 'w-40 text-right',
        className: 'w-40 text-right',
        cell: (row) => (
          <div className="flex justify-end">
            <Button size="sm" variant="ghost" asChild>
              <Link
                to={`/changes?order=${String(row.id)}`}
                onClick={(e) => {
                  // 阻止冒泡到行点击（行点击用于打开详情面板）
                  e.stopPropagation()
                }}
              >
                {t('delivery.changesHistory.list.openInChanges')}
              </Link>
            </Button>
          </div>
        ),
      },
    ],
    [t],
  )

  const toolbar = (
    <div className="grid gap-2.5">
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
        <Combobox
          className="w-40"
          aria-label={t('delivery.changesHistory.list.filterStatus')}
          value={status}
          onChange={(value) => {
            setStatus(value)
            setPage(1)
          }}
          options={[
            { value: 'all', label: t('delivery.changesHistory.list.allStatus') },
            ...STATUS_OPTIONS.map((s) => ({ value: s, label: t(`delivery.changes.status.${s}`) })),
          ]}
          allowCustom={false}
          emptyText={t('common.combobox.noMatch')}
        />
        {/* 标题已上移到页头，工具条只留条数，让「当前筛选命中几单」始终可见 */}
        {total > 0 && (
          <span className="tnum ml-auto text-xs text-ink-3">
            {t('delivery.changesHistory.list.total', { total })}
          </span>
        )}
      </div>
      {/* 白话提示：交代本页状态口径（已完成 / 已回滚各代表什么） */}
      <p className="text-xs text-ink-3">{t('delivery.tips.historyStatusScope')}</p>
    </div>
  )

  // 空态引导：所属命名空间确无任何变更单（非筛选造成的空、且作用域已就绪）时给出下一步
  const showEmptyGuide =
    namespaceId > 0 &&
    !query.isLoading &&
    !query.isError &&
    total === 0 &&
    keyword.trim() === '' &&
    status === 'all'

  return (
    <ListCard
      fill
      toolbar={toolbar}
      footer={total > PAGE_SIZE ? <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} /> : undefined}
    >
      {showEmptyGuide ? (
        <EmptyGuide />
      ) : (
        <PageAsyncSection
          isLoading={query.isLoading}
          isError={query.isError}
          error={query.error}
          skeleton={<TableSkeleton columns={6} rows={6} />}
        >
          <DataTable
            columns={columns}
            rows={query.data?.items}
            rowKey={(row) => String(row.id)}
            emptyText={t('delivery.changesHistory.list.empty')}
            density="compact"
            stickyHeader
            onRowClick={(row) => {
              onView(row)
            }}
            rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50 hover:bg-brand-50' : undefined)}
          />
        </PageAsyncSection>
      )}
    </ListCard>
  )
}
