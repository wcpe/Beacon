// 注册待确认抽屉（Sheet）：从吸顶操作条的「待确认 N」入口打开，右侧抽屉里处理 approve / reject，
// 不在主列表上方铺开占屏。approve 含 Q3 占用冲突强制解绑，reject 带原因。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Network, Server, UserPlus } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  Label,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  cn,
  type DataTableColumn,
} from '@beacon/ui'
import type { AgentIdentityItem } from '@beacon/contracts'

import { ApiClientError, approveIdentity, fetchIdentities, rejectIdentity } from '../../api/cluster'
import {
  filterItemsByEnvScope,
  needsClientEnvFilter,
  resolveApiNamespaceId,
  useEnvNamespaceScope,
} from '../../features/env/use-env-scope'
import ReasonDialog from './reason-dialog'

// 当前操作意图：approve 或 reject
type PendingAction = { kind: 'approve'; row: AgentIdentityItem } | { kind: 'reject'; row: AgentIdentityItem }

interface PendingSheetProps {
  namespaceId?: number
  // 抽屉开关（由父级吸顶入口控制）
  open: boolean
  onOpenChange: (open: boolean) => void
}

export default function PendingSheet({ namespaceId, open, onOpenChange }: PendingSheetProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // FR-178：待确认列表跟随顶栏 env
  const envScope = useEnvNamespaceScope()
  const apiNamespaceId = resolveApiNamespaceId(namespaceId, envScope)
  const clientFilter = needsClientEnvFilter(envScope)
  const [action, setAction] = useState<PendingAction | null>(null)
  // Q3 占用冲突强制解绑勾选
  const [forceUnbind, setForceUnbind] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['identities', 'pending', apiNamespaceId, envScope],
    queryFn: () => fetchIdentities({ status: 'pending', namespaceId: apiNamespaceId, pageSize: 100 }),
  })
  const pendingRows = useMemo(() => {
    const items = query.data?.items ?? []
    return clientFilter ? filterItemsByEnvScope(items, envScope) : items
  }, [query.data, clientFilter, envScope])

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
      {
        header: t('cluster.servers.columns.serverId'),
        cell: (row) => {
          const isProxy = row.kind === 'proxy'
          return (
            <div className="flex items-center gap-2 font-mono font-semibold text-ink-1">
              <span
                className={cn(
                  'grid size-5 place-items-center rounded-md',
                  isProxy ? 'bg-brand-100 text-brand-600' : 'bg-brand-50 text-brand',
                )}
                aria-hidden
              >
                {isProxy ? <Network className="size-3" /> : <Server className="size-3" />}
              </span>
              {row.serverId}
            </div>
          )
        },
      },
      {
        header: t('cluster.servers.columns.kind'),
        cell: (row) => <span className="text-ink-2">{t(`cluster.servers.kind.${row.kind}`)}</span>,
      },
      {
        header: t('cluster.servers.columns.status'),
        cell: (row) => (
          <div className="flex flex-wrap gap-1">
            {row.conflictReason === 'server-id-occupied' && (
              <Badge variant="crit" className="gap-1.5">
                <span className="size-1.5 rounded-full bg-current" />
                {t('cluster.servers.pending.conflictOccupied')}
              </Badge>
            )}
            {row.pendingExpiresAt !== null && row.conflictReason !== 'server-id-occupied' && (
              <Badge variant="warn" className="gap-1.5">
                <span className="size-1.5 rounded-full bg-current" />
                {t('cluster.servers.identityStatus.pending')}
              </Badge>
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
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <UserPlus className="size-4 text-brand" />
            {t('cluster.servers.pending.title')}
          </SheetTitle>
          <SheetDescription>{t('cluster.servers.pending.sheetDesc')}</SheetDescription>
        </SheetHeader>
        <div className="px-4 pb-6">
          <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
            <DataTable
              columns={columns}
              rows={pendingRows}
              rowKey={(row) => row.identityId}
              emptyText={t('cluster.servers.pending.empty')}
              density="compact"
            />
          </AsyncSection>
        </div>

        {/* 确认接入：占用冲突时强制解绑勾选（不勾选则后端 409） */}
        <ReasonDialog
          open={approving !== null}
          onOpenChange={(isOpen) => {
            if (!isOpen) {
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
            <label className="flex items-start gap-2 rounded-md border border-crit-bd bg-crit-bg px-3 py-2 text-sm text-crit">
              <Checkbox
                checked={forceUnbind}
                onCheckedChange={(value) => {
                  setForceUnbind(value === true)
                }}
                aria-label={t('cluster.servers.pending.forceUnbind')}
              />
              <Label className="cursor-pointer font-normal text-crit">
                {t('cluster.servers.pending.forceUnbind')}
              </Label>
            </label>
          )}
        </ReasonDialog>

        {/* 拒绝接入：原因必填 */}
        <ReasonDialog
          open={rejecting !== null}
          onOpenChange={(isOpen) => {
            if (!isOpen) {
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
      </SheetContent>
    </Sheet>
  )
}
