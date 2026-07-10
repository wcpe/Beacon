// 配置文件列表（主从布局主列）：ListCard 吸顶工具条（标题 / 新建 / 回收站 / keyword）+ 自区滚列表 + 吸底分页。
// 点行选中（右侧非模态详情面板打开），行「删除」移入回收站（模态确认）。四态齐全（loading/error/empty/huge 分页）。
import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Files, Trash2 } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  DestructiveConfirmDialog,
  Input,
  SummaryStrip,
  type DataTableColumn,
  type SummaryItem,
} from '@beacon/ui'
import type { ConfigFileItem } from '@beacon/devmock'

import ListCard from '../../features/shared/list-card'
import Pager from '../../features/delivery/pager'
import { ApiClientError } from '../../api/delivery'
import { createConfigFile, deleteConfigFile, fetchConfigFiles } from '../../api/delivery-configs'
import CreateDialog from './create-dialog'

const PAGE_SIZE = 15

interface ListViewProps {
  namespaceId: number
  selectedId: number | null
  onSelect: (file: ConfigFileItem) => void
  onOpenTrash: () => void
}

export default function ListView({ namespaceId, selectedId, onSelect, onOpenTrash }: ListViewProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  const [createOpen, setCreateOpen] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [removeTarget, setRemoveTarget] = useState<ConfigFileItem | null>(null)
  const [removeError, setRemoveError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['configs', 'files', namespaceId, keyword, page],
    queryFn: () =>
      fetchConfigFiles({
        namespaceId,
        keyword: keyword.trim() === '' ? undefined : keyword.trim(),
        page,
        pageSize: PAGE_SIZE,
      }),
    enabled: namespaceId > 0,
    placeholderData: keepPreviousData,
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['configs'] })

  const createMutation = useMutation({
    mutationFn: (vars: { name: string; format: ConfigFileItem['format']; description: string }) =>
      createConfigFile({
        namespaceId,
        name: vars.name,
        format: vars.format,
        description: vars.description,
      }),
    onSuccess: async () => {
      await invalidate()
      setCreateOpen(false)
    },
    onError: (error) => {
      setCreateError(messageOf(error))
    },
  })

  const removeMutation = useMutation({
    mutationFn: (id: number) => deleteConfigFile(id),
    onSuccess: async () => {
      await invalidate()
      setRemoveTarget(null)
    },
    onError: (error) => {
      setRemoveError(messageOf(error))
    },
  })

  const total = query.data?.total ?? 0
  const items = query.data?.items ?? []

  // 概要条：从本页已取 items 聚合——文件总数（服务端 total 权威）+ 本页可见文件的贡献层合计
  const summaryItems: SummaryItem[] =
    items.length > 0
      ? [
          { label: t('delivery.configs.list.summary.files'), value: total },
          {
            label: t('delivery.configs.list.summary.layers'),
            value: items.reduce((sum, it) => sum + it.contributingLayerCount, 0),
          },
        ]
      : []

  const columns: DataTableColumn<ConfigFileItem>[] = [
    {
      header: t('delivery.configs.list.columns.name'),
      cell: (row) => <span className="font-mono text-ink-1">{row.name}</span>,
    },
    {
      header: t('delivery.configs.list.columns.format'),
      cell: (row) => <Badge variant="brand">{row.format}</Badge>,
    },
    {
      header: t('delivery.configs.list.columns.layers'),
      cell: (row) => <span className="tnum text-ink-2">{row.contributingLayerCount}</span>,
    },
    {
      header: t('delivery.configs.list.columns.hash'),
      cell: (row) => (
        <span className="font-mono text-xs text-ink-3">
          {row.effectiveHash ? row.effectiveHash.slice(0, 10) : '-'}
        </span>
      ),
    },
    {
      header: t('delivery.configs.list.columns.updatedAt'),
      cell: (row) => <span className="tnum text-xs text-ink-3">{new Date(row.updatedAt).toLocaleString()}</span>,
    },
    {
      header: '',
      cell: (row) => (
        <div className="flex flex-wrap justify-end gap-1.5">
          <Button
            size="sm"
            variant="ghost"
            onClick={(e) => {
              // 阻止冒泡到行点击（行点击用于打开详情面板）
              e.stopPropagation()
              setRemoveError(null)
              setRemoveTarget(row)
            }}
          >
            {t('delivery.configs.remove.action')}
          </Button>
        </div>
      ),
    },
  ]

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <Files className="size-[15px]" />
          </span>
          {t('delivery.configs.list.title')}
          {total > 0 && <span className="text-xs font-normal text-ink-3">{total}</span>}
        </span>
        <div className="flex items-center gap-1.5">
          <Button variant="outline" size="sm" onClick={onOpenTrash}>
            <Trash2 className="size-3.5" aria-hidden />
            {t('delivery.configs.list.trash')}
          </Button>
          <Button
            size="sm"
            onClick={() => {
              setCreateError(null)
              setCreateOpen(true)
            }}
          >
            {t('delivery.configs.list.create')}
          </Button>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('delivery.configs.list.keyword')}
          placeholder={t('delivery.configs.list.keyword')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="w-64"
        />
      </div>
    </div>
  )

  return (
    <div className="grid gap-3.5">
      {summaryItems.length > 0 && <SummaryStrip items={summaryItems} />}

      <ListCard
        toolbar={toolbar}
        footer={total > PAGE_SIZE ? <Pager page={page} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} /> : undefined}
      >
        <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
          <DataTable
            columns={columns}
            rows={query.data?.items}
            rowKey={(row) => String(row.id)}
            emptyText={t('delivery.configs.list.empty')}
            density="compact"
            onRowClick={(row) => {
              onSelect(row)
            }}
            rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50/60' : undefined)}
          />
        </AsyncSection>
      </ListCard>

      <CreateDialog
        open={createOpen}
        onOpenChange={(open) => {
          if (!open) {
            setCreateOpen(false)
          }
        }}
        pending={createMutation.isPending}
        errorText={createError}
        onSubmit={(name, format, description) => {
          setCreateError(null)
          createMutation.mutate({ name, format, description })
        }}
      />

      {removeTarget && (
        <DestructiveConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setRemoveTarget(null)
            }
          }}
          title={t('delivery.configs.remove.title')}
          description={removeError ?? t('delivery.configs.remove.desc')}
          confirmLabel={t('delivery.configs.remove.confirm')}
          cancelLabel={t('delivery.configs.create.cancel')}
          impacts={[removeTarget.name]}
          pending={removeMutation.isPending}
          onConfirm={() => {
            removeMutation.mutate(removeTarget.id)
          }}
        />
      )}
    </div>
  )
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}
