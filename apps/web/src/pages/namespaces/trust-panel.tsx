// 互通信任关系面板：信任行列表（方向 / 能力 / 状态过滤）+ 授予（原因必填）+ 收回（原因必填，即时生效）。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  SectionHeader,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { NamespaceTrustItem, TrustCapability } from '@beacon/devmock'

import {
  ApiClientError,
  fetchNamespaceList,
  fetchTrusts,
  grantTrust,
  revokeTrust,
  type GrantTrustBody,
} from '../../api/system'
import SystemReasonDialog from '../../features/system/reason-dialog'
import { formatIso } from '../../features/system/format'
import GrantDialog from './grant-dialog'

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function TrustPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [statusFilter, setStatusFilter] = useState<string>('all')
  const [capabilityFilter, setCapabilityFilter] = useState<string>('all')
  const [grantOpen, setGrantOpen] = useState(false)
  const [grantError, setGrantError] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<NamespaceTrustItem | null>(null)
  const [revokeError, setRevokeError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['namespace-trusts', 'list', statusFilter, capabilityFilter],
    queryFn: () =>
      fetchTrusts({
        status: statusFilter === 'all' ? undefined : statusFilter,
        capability: capabilityFilter === 'all' ? undefined : capabilityFilter,
        pageSize: 100,
      }),
  })

  // 授予表单的 namespace 候选
  const namespacesQuery = useQuery({
    queryKey: ['namespaces', 'options'],
    queryFn: () => fetchNamespaceList({ pageSize: 100 }),
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['namespace-trusts'] })

  const grantMutation = useMutation({
    mutationFn: (body: GrantTrustBody) => grantTrust(body),
    onSuccess: async () => {
      await invalidate()
      await queryClient.invalidateQueries({ queryKey: ['namespaces'] })
      setGrantOpen(false)
    },
    onError: (error) => {
      setGrantError(messageOf(error))
    },
  })

  const revokeMutation = useMutation({
    mutationFn: ({ id, reason }: { id: number; reason: string }) => revokeTrust(id, reason),
    onSuccess: async () => {
      await invalidate()
      await queryClient.invalidateQueries({ queryKey: ['namespaces'] })
      setRevoking(null)
    },
    onError: (error) => {
      setRevokeError(messageOf(error))
    },
  })

  const capabilityLabel = (cap: TrustCapability): string => {
    if (cap === 'schedule') {
      return t('system.namespaces.trusts.capabilitySchedule')
    }
    if (cap === 'message') {
      return t('system.namespaces.trusts.capabilityMessage')
    }
    return t('system.namespaces.trusts.capabilityAgentOps')
  }

  const columns = useMemo<DataTableColumn<NamespaceTrustItem>[]>(
    () => [
      {
        header: t('system.namespaces.trusts.from'),
        cell: (row) => <span className="font-medium">{row.fromNamespaceName}</span>,
      },
      {
        header: t('system.namespaces.trusts.to'),
        cell: (row) => <span className="font-medium">{row.toNamespaceName}</span>,
      },
      { header: t('system.namespaces.trusts.capability'), cell: (row) => capabilityLabel(row.capability) },
      {
        header: t('system.namespaces.trusts.status'),
        cell: (row) => (
          <Badge variant={row.status === 'active' ? 'secondary' : 'outline'}>
            {row.status === 'active'
              ? t('system.namespaces.trusts.statusActive')
              : t('system.namespaces.trusts.statusRevoked')}
          </Badge>
        ),
      },
      { header: t('system.namespaces.trusts.note'), cell: (row) => row.note || '-' },
      { header: t('system.namespaces.trusts.grantedAt'), cell: (row) => formatIso(row.grantedAt) },
      {
        header: t('system.namespaces.trusts.actions'),
        cell: (row) =>
          row.status === 'active' ? (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setRevokeError(null)
                setRevoking(row)
              }}
            >
              {t('system.namespaces.trusts.revoke')}
            </Button>
          ) : (
            <span className="text-xs text-muted-foreground">{row.revokeReason ?? '-'}</span>
          ),
      },
    ],
    [t],
  )

  return (
    <section className="grid gap-3">
      <div className="flex items-center justify-between">
        <SectionHeader title={t('system.namespaces.trusts.title')} />
        <Button
          onClick={() => {
            setGrantError(null)
            setGrantOpen(true)
          }}
        >
          {t('system.namespaces.trusts.grant')}
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">{t('system.namespaces.trusts.desc')}</p>

      <div className="flex flex-wrap gap-2">
        <Select value={statusFilter} onValueChange={setStatusFilter}>
          <SelectTrigger className="w-32" aria-label={t('system.namespaces.trusts.filterStatus')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('system.namespaces.trusts.filterStatus')}</SelectItem>
            <SelectItem value="active">{t('system.namespaces.trusts.statusActive')}</SelectItem>
            <SelectItem value="revoked">{t('system.namespaces.trusts.statusRevoked')}</SelectItem>
          </SelectContent>
        </Select>
        <Select value={capabilityFilter} onValueChange={setCapabilityFilter}>
          <SelectTrigger className="w-36" aria-label={t('system.namespaces.trusts.filterCapability')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('system.namespaces.trusts.filterCapability')}</SelectItem>
            <SelectItem value="schedule">{t('system.namespaces.trusts.capabilitySchedule')}</SelectItem>
            <SelectItem value="message">{t('system.namespaces.trusts.capabilityMessage')}</SelectItem>
            <SelectItem value="agent_ops">{t('system.namespaces.trusts.capabilityAgentOps')}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={7} rows={3} />}
      >
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => String(row.id)}
          emptyText={t('system.namespaces.trusts.empty')}
          density="compact"
        />
      </AsyncSection>

      {/* 授予单向信任 */}
      <GrantDialog
        open={grantOpen}
        onOpenChange={setGrantOpen}
        namespaces={namespacesQuery.data?.items ?? []}
        pending={grantMutation.isPending}
        errorText={grantError}
        onSubmit={(body) => {
          setGrantError(null)
          grantMutation.mutate(body)
        }}
      />

      {/* 收回信任（原因必填，即时生效） */}
      <SystemReasonDialog
        open={revoking !== null}
        onOpenChange={(open) => {
          if (!open) {
            setRevoking(null)
          }
        }}
        title={t('system.namespaces.trusts.confirmRevokeTitle')}
        description={t('system.namespaces.trusts.confirmRevokeDesc')}
        confirmLabel={t('system.namespaces.trusts.confirmRevoke')}
        cancelLabel={t('system.common.cancel')}
        reasonLabel={t('system.namespaces.trusts.revokeReasonLabel')}
        reasonPlaceholder={t('system.namespaces.trusts.revokeReasonPlaceholder')}
        impacts={
          revoking
            ? [`${revoking.fromNamespaceName} → ${revoking.toNamespaceName} · ${capabilityLabel(revoking.capability)}`]
            : undefined
        }
        pending={revokeMutation.isPending}
        errorText={revokeError}
        onConfirm={(reason) => {
          if (revoking) {
            revokeMutation.mutate({ id: revoking.id, reason })
          }
        }}
      />
    </section>
  )
}
