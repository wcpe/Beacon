// 变更单列表（主从布局主列）：ListCard 吸顶工具条（标题 / 新建 / 状态筛选 / 标题搜索）+ 自区滚列表 + 吸底分页。
// 点行选中（右侧非模态详情面板打开）；新建走模态表单。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { ClipboardList, Plus } from 'lucide-react'

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
import ListCard from '../../features/shared/list-card'
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
  selectedId: number | null
  onOpen: (order: ChangeOrderSummary) => void
}

export default function ListView({ namespaceId, selectedId, onOpen }: ListViewProps) {
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
      // 创建成功直接进入新单详情面板
      onOpen(detail)
    },
    onError: (error) => {
      setCreateError(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const columns = useMemo<DataTableColumn<ChangeOrderSummary>[]>(
    () => [
      {
        header: t('delivery.changes.list.columns.id'),
        cell: (row) => <span className="tnum text-xs text-ink-3">#{String(row.id)}</span>,
      },
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
        cell: (row) => <span className="tnum text-xs text-ink-3">{formatTime(row.updatedAt)}</span>,
      },
    ],
    [t],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <ClipboardList className="size-[15px]" />
          </span>
          {t('delivery.changes.list.title')}
          {total > 0 && <span className="text-xs font-normal text-ink-3">{total}</span>}
        </span>
        <Button
          size="sm"
          onClick={() => {
            setCreateError(null)
            setCreateOpen(true)
          }}
        >
          <Plus className="size-4" />
          {t('delivery.changes.list.create')}
        </Button>
      </div>
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
    </div>
  )

  return (
    <div className="grid gap-3.5">
      <ListCard
        toolbar={toolbar}
        footer={total > PAGE_SIZE ? <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} /> : undefined}
      >
        <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
          <DataTable
            columns={columns}
            rows={query.data?.items}
            rowKey={(row) => String(row.id)}
            emptyText={t('delivery.changes.list.empty')}
            density="compact"
            onRowClick={(row) => {
              onOpen(row)
            }}
            rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50/60' : undefined)}
          />
        </AsyncSection>
      </ListCard>

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
    </div>
  )
}
