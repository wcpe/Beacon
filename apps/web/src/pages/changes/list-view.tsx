// 变更单列表视图：状态筛选 + 标题搜索 + 服务端分页 + 行「详情」+ 顶部「新建变更单」。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Button,
  DataTable,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  type DataTableColumn,
} from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import {
  createChangeOrder,
  fetchChangeOrders,
  type ChangeOrderStatus,
  type ChangeOrderSummary,
} from '../../api/delivery-changes'
import Pager from '../../features/delivery/pager'
import CreateDialog, { type CreateDraftInput } from './create-dialog'
import { OrderStatusBadge } from './status-badge'
import { formatTime } from './format'

const PAGE_SIZE = 15

// 状态筛选下拉的候选（全部 + 各状态）
const STATUS_OPTIONS: ChangeOrderStatus[] = [
  'draft',
  'pending_approval',
  'approved',
  'rolling',
  'paused',
  'completed',
  'cancelled',
  'rolling_back',
  'rolled_back',
]

interface ListViewProps {
  namespaceId: number
  onOpen: (id: number) => void
}

export default function ListView({ namespaceId, onOpen }: ListViewProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState<string>('all')
  const [page, setPage] = useState(1)
  const [createOpen, setCreateOpen] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['change-orders', 'list', namespaceId, keyword, status, page],
    queryFn: () =>
      fetchChangeOrders({
        namespaceId,
        keyword: keyword.trim() === '' ? undefined : keyword.trim(),
        status: status === 'all' ? undefined : status,
        page,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  const total = query.data?.total ?? 0

  const createMutation = useMutation({
    mutationFn: (input: CreateDraftInput) =>
      createChangeOrder({
        namespaceId,
        title: input.title,
        description: input.description,
        sourceServerId: input.sourceServerId,
      }),
    onSuccess: async (detail) => {
      await queryClient.invalidateQueries({ queryKey: ['change-orders'] })
      setCreateOpen(false)
      // 创建成功直接进入新单详情
      onOpen(detail.id)
    },
    onError: (error) => {
      setCreateError(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const columns = useMemo<DataTableColumn<ChangeOrderSummary>[]>(
    () => [
      { header: t('delivery.changes.list.columns.title'), cell: (row) => row.title },
      {
        header: t('delivery.changes.list.columns.status'),
        cell: (row) => <OrderStatusBadge status={row.status} />,
      },
      {
        header: t('delivery.changes.list.columns.batch'),
        cell: (row) => (
          <span className="text-xs text-muted-foreground">
            {row.batchMode} · {row.batchSizes.join(' / ')}
          </span>
        ),
      },
      { header: t('delivery.changes.list.columns.createdBy'), cell: (row) => row.createdBy },
      {
        header: t('delivery.changes.list.columns.updatedAt'),
        cell: (row) => formatTime(row.updatedAt),
      },
      {
        header: '',
        cell: (row) => (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              onOpen(row.id)
            }}
          >
            {t('delivery.changes.list.view')}
          </Button>
        ),
      },
    ],
    [t, onOpen],
  )

  return (
    <section className="grid gap-3">
      <div className="flex flex-wrap items-center justify-end gap-2">
        <Button
          size="sm"
          onClick={() => {
            setCreateError(null)
            setCreateOpen(true)
          }}
        >
          {t('delivery.changes.list.create')}
        </Button>
      </div>

      {/* 筛选条：状态 + 标题搜索 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('delivery.changes.list.keyword')}
          placeholder={t('delivery.changes.list.keyword')}
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
          <SelectTrigger className="w-40" aria-label={t('delivery.changes.list.filterStatus')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('delivery.changes.list.allStatus')}</SelectItem>
            {STATUS_OPTIONS.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`delivery.changes.status.${s}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => String(row.id)}
          emptyText={t('delivery.changes.list.empty')}
          density="compact"
        />
      </AsyncSection>

      <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} />

      <CreateDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        pending={createMutation.isPending}
        errorText={createError}
        onSubmit={(input) => {
          setCreateError(null)
          createMutation.mutate(input)
        }}
      />
    </section>
  )
}
