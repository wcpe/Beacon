// 注册待确认区：pending 身份列表 + approve（含 Q3 占用冲突强制解绑）/ reject（带原因）。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  Label,
  SectionHeader,
  type DataTableColumn,
} from '@beacon/ui'
import type { AgentIdentityItem } from '@beacon/devmock'

import { ApiClientError, approveIdentity, fetchIdentities, rejectIdentity } from '../../api/cluster'
import ReasonDialog from './reason-dialog'

// 当前操作意图：approve 或 reject
type PendingAction = { kind: 'approve'; row: AgentIdentityItem } | { kind: 'reject'; row: AgentIdentityItem }

export default function PendingPanel({ namespaceId }: { namespaceId?: number }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [action, setAction] = useState<PendingAction | null>(null)
  // Q3 占用冲突强制解绑勾选
  const [forceUnbind, setForceUnbind] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['identities', 'pending', namespaceId],
    queryFn: () => fetchIdentities({ status: 'pending', namespaceId, pageSize: 100 }),
  })

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['identities'] })
    await queryClient.invalidateQueries({ queryKey: ['servers'] })
  }

  const approveMutation = useMutation({
    mutationFn: (row: AgentIdentityItem) =>
      approveIdentity(row.identityId, {
        forceUnbindOccupier: row.conflictReason === 'server-id-occupied' ? forceUnbind : undefined,
      }),
    onSuccess: async () => {
      await invalidate()
      setAction(null)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const rejectMutation = useMutation({
    mutationFn: ({ row, reason }: { row: AgentIdentityItem; reason: string }) =>
      rejectIdentity(row.identityId, reason),
    onSuccess: async () => {
      await invalidate()
      setAction(null)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  const columns = useMemo<DataTableColumn<AgentIdentityItem>[]>(
    () => [
      { header: t('cluster.servers.columns.serverId'), cell: (row) => <span className="font-mono">{row.serverId}</span> },
      { header: t('cluster.servers.columns.namespace'), cell: (row) => `#${String(row.namespaceId)}` },
      {
        header: t('cluster.servers.columns.kind'),
        cell: (row) => t(`cluster.servers.kind.${row.kind}`),
      },
      {
        header: t('cluster.servers.columns.status'),
        cell: (row) => (
          <div className="flex flex-wrap gap-1">
            {row.conflictReason === 'server-id-occupied' && (
              <Badge variant="destructive">{t('cluster.servers.pending.conflictOccupied')}</Badge>
            )}
            {row.pendingExpiresAt !== null && row.conflictReason !== 'server-id-occupied' && (
              <Badge variant="outline">{t('cluster.servers.identityStatus.pending')}</Badge>
            )}
          </div>
        ),
      },
      {
        header: t('cluster.servers.columns.actions'),
        cell: (row) => (
          <div className="flex gap-2">
            <Button
              size="sm"
              onClick={() => {
                setErrorText(null)
                setForceUnbind(false)
                setAction({ kind: 'approve', row })
              }}
            >
              {t('cluster.servers.pending.approve')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setErrorText(null)
                setAction({ kind: 'reject', row })
              }}
            >
              {t('cluster.servers.pending.reject')}
            </Button>
          </div>
        ),
      },
    ],
    [t],
  )

  const approving = action?.kind === 'approve' ? action.row : null
  const rejecting = action?.kind === 'reject' ? action.row : null
  const occupied = approving?.conflictReason === 'server-id-occupied'

  return (
    <section className="grid gap-3">
      <SectionHeader title={t('cluster.servers.pending.title')} />
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => row.identityId}
          emptyText={t('cluster.servers.pending.empty')}
          density="compact"
        />
      </AsyncSection>

      {/* 确认接入：占用冲突时强制解绑勾选（不勾选则后端 409） */}
      <ReasonDialog
        open={approving !== null}
        onOpenChange={(open) => {
          if (!open) {
            setAction(null)
          }
        }}
        title={t('cluster.servers.pending.approveTitle')}
        description={t('cluster.servers.pending.approveDesc')}
        confirmLabel={t('cluster.servers.pending.approve')}
        requireReason={false}
        pending={approveMutation.isPending}
        errorText={errorText}
        impacts={approving ? [`serverId ${approving.serverId}`] : undefined}
        onConfirm={() => {
          if (approving) {
            approveMutation.mutate(approving)
          }
        }}
      >
        {occupied && (
          <label className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-sm">
            <Checkbox
              checked={forceUnbind}
              onCheckedChange={(value) => {
                setForceUnbind(value === true)
              }}
              aria-label={t('cluster.servers.pending.forceUnbind')}
            />
            <Label className="cursor-pointer font-normal">{t('cluster.servers.pending.forceUnbind')}</Label>
          </label>
        )}
      </ReasonDialog>

      {/* 拒绝接入：原因必填 */}
      <ReasonDialog
        open={rejecting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setAction(null)
          }
        }}
        title={t('cluster.servers.pending.rejectTitle')}
        description={t('cluster.servers.pending.rejectDesc')}
        confirmLabel={t('cluster.servers.pending.reject')}
        pending={rejectMutation.isPending}
        errorText={errorText}
        onConfirm={(reason) => {
          if (rejecting) {
            rejectMutation.mutate({ row: rejecting, reason })
          }
        }}
      />
    </section>
  )
}
