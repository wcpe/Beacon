// namespace 列表面板：keyword 搜索 + 服务端分页 + 创建（返回一次性接入 token）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Boxes, Search } from 'lucide-react'

import {
  AsyncSection,
  Button,
  DataTable,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
  SectionHeader,
  TableSkeleton,
  Textarea,
  type DataTableColumn,
} from '@beacon/ui'
import type { NamespaceItem } from '@beacon/devmock'

import { ApiClientError, createNamespace, fetchNamespaceList } from '../../api/system'
import { formatIso } from '../../features/system/format'
import TokenDialog from './token-dialog'

const PAGE_SIZE = 15

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function NamespacePanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [createError, setCreateError] = useState<string | null>(null)
  const [token, setToken] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['namespaces', 'list', keyword, page],
    queryFn: () =>
      fetchNamespaceList({
        keyword: keyword.trim() === '' ? undefined : keyword.trim(),
        page,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const createMutation = useMutation({
    mutationFn: () => createNamespace({ name: name.trim(), description: description.trim() || undefined }),
    onSuccess: async (created) => {
      // 信任面板依赖 namespace 列表，一并失效
      await queryClient.invalidateQueries({ queryKey: ['namespaces'] })
      await queryClient.invalidateQueries({ queryKey: ['namespace-trusts'] })
      setCreateOpen(false)
      setToken(created.accessToken)
    },
    onError: (error) => {
      setCreateError(messageOf(error))
    },
  })

  const openCreate = () => {
    setName('')
    setDescription('')
    setCreateError(null)
    setCreateOpen(true)
  }

  const columns = useMemo<DataTableColumn<NamespaceItem>[]>(
    () => [
      { header: t('system.namespaces.columns.name'), cell: (row) => <span className="font-medium">{row.name}</span> },
      { header: t('system.namespaces.columns.description'), cell: (row) => row.description || '-' },
      { header: t('system.namespaces.columns.serverCount'), cell: (row) => row.serverCount },
      { header: t('system.namespaces.columns.bcClusterCount'), cell: (row) => row.bcClusterCount },
      { header: t('system.namespaces.columns.trustCount'), cell: (row) => row.activeTrustCount },
      { header: t('system.namespaces.columns.createdAt'), cell: (row) => formatIso(row.createdAt) },
    ],
    [t],
  )

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<Boxes className="size-4" />}
        title={t('system.namespaces.title')}
        actions={<Button onClick={openCreate}>{t('system.namespaces.create')}</Button>}
      />

      <div className="relative w-64">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-ink-4" aria-hidden />
        <Input
          aria-label={t('system.namespaces.keyword')}
          placeholder={t('system.namespaces.keyword')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="pl-8"
        />
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={6} rows={4} />}
      >
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => String(row.id)}
          emptyText={t('system.namespaces.empty')}
          density="compact"
        />
      </AsyncSection>

      {total > PAGE_SIZE && (
        <div className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
          <span>{t('system.namespaces.listHint', { page, pageCount, total })}</span>
          <Button
            size="sm"
            variant="outline"
            disabled={page <= 1}
            onClick={() => {
              setPage((p) => Math.max(1, p - 1))
            }}
          >
            {t('system.common.prev')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={page >= pageCount}
            onClick={() => {
              setPage((p) => Math.min(pageCount, p + 1))
            }}
          >
            {t('system.common.next')}
          </Button>
        </div>
      )}

      {/* 创建 namespace 弹窗 */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('system.namespaces.createTitle')}</DialogTitle>
            <DialogDescription>{t('system.namespaces.isolationHint')}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="namespace-name">{t('system.namespaces.nameLabel')}</Label>
              <Input
                id="namespace-name"
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                }}
                placeholder={t('system.namespaces.namePlaceholder')}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="namespace-desc">{t('system.namespaces.descLabel')}</Label>
              <Textarea
                id="namespace-desc"
                value={description}
                onChange={(e) => {
                  setDescription(e.target.value)
                }}
                rows={2}
              />
            </div>
            {createError && <p className="text-sm text-destructive">{createError}</p>}
          </div>
          <DialogFooter>
            <Button
              disabled={name.trim() === '' || createMutation.isPending}
              onClick={() => {
                setCreateError(null)
                createMutation.mutate()
              }}
            >
              {createMutation.isPending ? t('system.namespaces.creating') : t('system.namespaces.createConfirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 一次性接入 token */}
      <TokenDialog
        open={token !== null}
        onOpenChange={(open) => {
          if (!open) {
            setToken(null)
          }
        }}
        token={token ?? ''}
      />
    </section>
  )
}
