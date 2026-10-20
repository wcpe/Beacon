// 变更单列表（主从布局主列）：ListCard 吸顶工具条（条数 / 状态筛选 / 标题搜索）+ 自区滚列表 + 吸底分页。
// 页面级标题与创建入口已上移页头（PageHeader），此处只留列表自己的筛选与计数；
// 两个弹窗（高级创建 / 引导向导）本体仍在本视图内，开关由页面持有。点行选中（右侧非模态详情面板打开）。
// 引导创建走五步向导（新手主入口），高级创建保留原有直建表单；空态用任务卡引导进向导。
import { useEffect, useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  Combobox,
  Input,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import PageDataTable from '../../features/shared/page-data-table'
import PageAsyncSection from '../../features/shared/page-async-section'

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
import EmptyGuide from './empty-guide'
import GuidedWizard from './guided-wizard'
import { OrderStatusBadge } from '../../features/delivery/status-badges'
import { formatTime } from './format'
import type { WizardContent } from './wizard-state'

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
  // 高级创建弹窗开关（页头按钮驱动）
  createOpen: boolean
  onCreateOpenChange: (open: boolean) => void
  // 引导向导开关与预选交付内容（页头按钮与空态任务卡共用）
  wizardOpen: boolean
  onWizardOpenChange: (open: boolean) => void
  wizardPreset: WizardContent
  onWizardPresetChange: (preset: WizardContent) => void
}

export default function ListView({
  namespaceId,
  selectedId,
  onOpen,
  createOpen,
  onCreateOpenChange,
  wizardOpen,
  onWizardOpenChange,
  wizardPreset,
  onWizardPresetChange,
}: ListViewProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState<string>('all')
  const [page, setPage] = useState(1)
  const [createError, setCreateError] = useState<string | null>(null)

  // 弹窗每次打开都清掉上一次的失败提示（开关由页头按钮控制，这里只跟随开合清理）
  useEffect(() => {
    if (createOpen) {
      setCreateError(null)
    }
  }, [createOpen])

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
    // 未选到具体命名空间（0＝全部 / 范围失效）不发请求：列表按命名空间取数，不跨范围兜底
    enabled: namespaceId > 0,
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
      onCreateOpenChange(false)
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
        headClassName: 'text-right',
        className: 'text-right tabular-nums',
        cell: (row) => <span className="tnum text-xs text-ink-3">#{String(row.id)}</span>,
      },
      {
        header: t('delivery.changes.list.columns.title'),
        cell: (row) => <span className="block max-w-64 truncate" title={row.title}>{row.title}</span>,
      },
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
        <Combobox
          className="w-40"
          aria-label={t('delivery.changes.list.filterStatus')}
          value={status}
          onChange={(value) => {
            setStatus(value)
            setPage(1)
          }}
          options={[
            { value: 'all', label: t('delivery.changes.list.allStatus') },
            ...STATUS_OPTIONS.map((s) => ({ value: s, label: t(`delivery.changes.status.${s}`) })),
          ]}
          allowCustom={false}
          emptyText={t('common.combobox.noMatch')}
        />
        {/* 标题已上移到页头，工具条只留条数，让「当前筛选命中几单」始终可见 */}
        {total > 0 && (
          <span className="tnum ml-auto text-xs text-ink-3">{t('delivery.changes.list.total', { total })}</span>
        )}
      </div>
    </div>
  )

  // 空态引导：所属命名空间确无任何变更单（非筛选造成的空、且作用域已就绪）时用任务卡代替空表格
  const showEmptyGuide =
    namespaceId > 0 &&
    !query.isLoading &&
    !query.isError &&
    total === 0 &&
    keyword.trim() === '' &&
    status === 'all'

  const startWizard = (preset: WizardContent): void => {
    onWizardPresetChange(preset)
    onWizardOpenChange(true)
  }

  return (
    <div className="grid gap-3.5 md:min-h-0 md:flex-1 md:flex md:flex-col">
      <ListCard
        fill
        toolbar={toolbar}
        footer={total > PAGE_SIZE ? <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} /> : undefined}
      >
        {showEmptyGuide ? (
          <EmptyGuide onStart={startWizard} />
        ) : (
          <PageAsyncSection
            isLoading={query.isLoading}
            isError={query.isError}
            error={query.error}
            skeleton={<TableSkeleton columns={6} rows={6} />}
          >
            <PageDataTable
              columns={columns}
              rows={query.data?.items}
              rowKey={(row) => String(row.id)}
              emptyText={t('delivery.changes.list.empty')}
              density="compact"
              stickyHeader
              onRowClick={(row) => {
                onOpen(row)
              }}
              rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50 hover:bg-brand-50' : undefined)}
            />
          </PageAsyncSection>
        )}
      </ListCard>

      <CreateDialog
        namespaceId={namespaceId}
        open={createOpen}
        onOpenChange={onCreateOpenChange}
        pending={createMutation.isPending}
        errorText={createError}
        onSubmit={(input) => {
          setCreateError(null)
          createMutation.mutate(input)
        }}
      />

      <GuidedWizard
        open={wizardOpen}
        onOpenChange={onWizardOpenChange}
        namespaceId={namespaceId}
        initialContent={wizardPreset}
        onCreated={onOpen}
      />
    </div>
  )
}
