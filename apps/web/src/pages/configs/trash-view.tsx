// 回收站视图：已软删除的配置文件分页列表，行「恢复」（名占用 409 内联）/「彻底删除」（原因必填）。
import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Button,
  DataTable,
  SectionHeader,
  type DataTableColumn,
} from '@beacon/ui'

import Pager from '../../features/delivery/pager'
import { ApiClientError } from '../../api/delivery'
import {
  fetchConfigTrash,
  purgeConfigFile,
  restoreConfigFile,
  type TrashItem,
} from '../../api/delivery-configs'
import ReasonDialog from './reason-dialog'

const PAGE_SIZE = 15

interface TrashViewProps {
  namespaceId: number
  onBack: () => void
}

export default function TrashView({ namespaceId, onBack }: TrashViewProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  // 按行 id 存恢复失败文案（名占用 409），行内展示
  const [restoreError, setRestoreError] = useState<Map<number, string>>(new Map())
  const [purgeTarget, setPurgeTarget] = useState<TrashItem | null>(null)
  const [purgeError, setPurgeError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['configs', 'trash', namespaceId, page],
    queryFn: () => fetchConfigTrash({ namespaceId, page, pageSize: PAGE_SIZE }),
    enabled: namespaceId > 0,
    placeholderData: keepPreviousData,
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['configs'] })

  const restoreMutation = useMutation({
    mutationFn: (id: number) => restoreConfigFile(id),
    onSuccess: async () => {
      await invalidate()
    },
    onError: (error, id) => {
      setRestoreError((prev) => new Map(prev).set(id, messageOf(error)))
    },
  })

  const purgeMutation = useMutation({
    mutationFn: (vars: { id: number; reason: string }) => purgeConfigFile(vars.id, vars.reason),
    onSuccess: async () => {
      await invalidate()
      setPurgeTarget(null)
    },
    onError: (error) => {
      setPurgeError(messageOf(error))
    },
  })

  const total = query.data?.total ?? 0

  const columns: DataTableColumn<TrashItem>[] = [
    {
      header: t('delivery.configs.trash.columns.name'),
      cell: (row) => <span className="font-mono">{row.name}</span>,
    },
    {
      header: t('delivery.configs.trash.columns.deletedBy'),
      cell: (row) => row.deletedBy ?? '-',
    },
    {
      header: t('delivery.configs.trash.columns.deletedAt'),
      cell: (row) => (row.deletedAt !== null ? new Date(row.deletedAt).toLocaleString() : '-'),
    },
    {
      header: '',
      cell: (row) => (
        <div className="flex flex-col items-end gap-1">
          <div className="flex flex-wrap justify-end gap-1.5">
            <Button
              size="sm"
              variant="ghost"
              disabled={restoreMutation.isPending}
              onClick={() => {
                setRestoreError((prev) => {
                  const next = new Map(prev)
                  next.delete(row.id)
                  return next
                })
                restoreMutation.mutate(row.id)
              }}
            >
              {t('delivery.configs.trash.restore')}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setPurgeError(null)
                setPurgeTarget(row)
              }}
            >
              {t('delivery.configs.trash.purge')}
            </Button>
          </div>
          {restoreError.get(row.id) !== undefined && (
            <span className="text-xs text-destructive">{restoreError.get(row.id)}</span>
          )}
        </div>
      ),
    },
  ]

  return (
    <section className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <SectionHeader title={t('delivery.configs.trash.title')} />
        <Button variant="outline" onClick={onBack}>
          {t('delivery.configs.trash.back')}
        </Button>
      </div>

      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => String(row.id)}
          emptyText={t('delivery.configs.trash.empty')}
          density="compact"
        />
      </AsyncSection>

      <Pager
        page={page}
        total={total}
        pageSize={PAGE_SIZE}
        onPageChange={(next) => {
          setPage(next)
        }}
      />

      {purgeTarget && (
        <ReasonDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setPurgeTarget(null)
            }
          }}
          title={t('delivery.configs.trash.purgeTitle')}
          description={t('delivery.configs.trash.purgeDesc')}
          confirmLabel={t('delivery.configs.trash.purgeConfirm')}
          impacts={[purgeTarget.name]}
          pending={purgeMutation.isPending}
          errorText={purgeError}
          onConfirm={(reason) => {
            purgeMutation.mutate({ id: purgeTarget.id, reason })
          }}
        />
      )}
    </section>
  )
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}
