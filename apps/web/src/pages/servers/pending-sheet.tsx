// 注册待确认抽屉（Sheet）：从吸顶操作条的「待确认 N」入口打开，右侧抽屉里处理 approve / reject，
// 不在主列表上方铺开占屏。approve 含 Q3 占用冲突强制解绑，reject 带原因。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { Network, Server, UserPlus } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Checkbox,
  DataTable,
  Input,
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
import { fetchPagedItemsByEnvScope, resolveRequestNamespaceScope, useEnvNamespaceScope, useEnvScopePending } from '../../features/env/use-env-scope'
import ReasonDialog from './reason-dialog'
import IdentityDetailSheet from './identity-detail-sheet'

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
  // 观测范围仍在解析（env 选项未就绪）时显示骨架，不把「范围待解析」误报成空态。
  const envPending = useEnvScopePending()
  const requestScope = resolveRequestNamespaceScope(namespaceId, envScope)
  const [action, setAction] = useState<PendingAction | null>(null)
  // Q3 占用冲突强制解绑勾选
  const [forceUnbind, setForceUnbind] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)
  const [approvalRequestID, setApprovalRequestID] = useState<string | null>(null)
  const [serverIdDraft, setServerIdDraft] = useState('')
  const [detailIdentityId, setDetailIdentityId] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['identities', 'pending', requestScope],
    queryFn: () =>
      fetchPagedItemsByEnvScope(requestScope, (namespaceId) =>
        fetchIdentities({ status: 'pending', namespaceId, pageSize: 100 }),
      ),
    // 观测范围未就绪时不发请求，交回 react-query 原生 pending 态（骨架由此承接）
    enabled: !envPending,
  })
  const pendingRows = useMemo(() => query.data?.items ?? [], [query.data])

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['identities'] })
    await queryClient.invalidateQueries({ queryKey: ['servers'] })
  }

  const approveMutation = useMutation({
    mutationFn: ({ row, reason }: { row: AgentIdentityItem; reason: string }) =>
      approveIdentity(row.identityId, {
        reason,
        serverId: serverIdDraft.trim(),
        forceUnbindOccupier: row.conflictReason === 'server-id-occupied' ? forceUnbind : undefined,
      }),
    onSuccess: async (ticket) => {
      await invalidate()
      setApprovalRequestID(ticket.approvalRequestId)
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
              {row.serverId ?? '待分配服务器 ID'}
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
                setApprovalRequestID(null)
                setForceUnbind(false)
                setServerIdDraft(row.serverId ?? '')
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
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setDetailIdentityId(row.identityId)
              }}
            >
              查看身份详情
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
  const serverIdValidationError = validateServerId(serverIdDraft)

  return (
    <Sheet open={open} onOpenChange={onOpenChange} modal={false}>
      <SheetContent showOverlay={false} className="w-full gap-0 overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <UserPlus className="size-4 text-brand" />
            {t('cluster.servers.pending.title')}
          </SheetTitle>
          <SheetDescription>{t('cluster.servers.pending.sheetDesc')}</SheetDescription>
        </SheetHeader>
        <div className="px-4 pb-6">
          {approvalRequestID && (
            <div className="mb-4 grid gap-1 rounded-md border border-brand-100 bg-brand-50 px-3 py-2 text-sm text-ink-2" role="status">
              <span>确认接入审批申请已创建，等待审批中心执行。</span>
              <Link className="w-fit text-brand hover:underline" to={`/approvals/${encodeURIComponent(approvalRequestID)}`}>查看统一审批</Link>
            </div>
          )}
          <AsyncSection isLoading={query.isPending} isError={query.isError} error={query.error}>
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
          requireReason
          pending={approveMutation.isPending}
          errorText={errorText}
          confirmDisabled={serverIdValidationError !== null}
          impacts={approving ? [`serverId ${approving.serverId ?? '待分配服务器 ID'}`] : undefined}
          onConfirm={(reason) => {
            if (approving) {
              approveMutation.mutate({ row: approving, reason })
            }
          }}
        >
          <div className="grid gap-1.5">
            <Label htmlFor="pending-identity-server-id">服务器 ID</Label>
            <Input
              id="pending-identity-server-id"
              aria-label="服务器 ID"
              value={serverIdDraft}
              aria-invalid={serverIdValidationError !== null}
              onChange={(event) => {
                setServerIdDraft(event.target.value)
                setErrorText(null)
              }}
              placeholder="例如 lobby-1"
            />
            {serverIdValidationError && <p className="text-sm text-destructive">{serverIdValidationError}</p>}
          </div>
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
        <IdentityDetailSheet
          identityId={detailIdentityId}
          onOpenChange={(isOpen) => {
            if (!isOpen) {
              setDetailIdentityId(null)
            }
          }}
        />
      </SheetContent>
    </Sheet>
  )
}

// 与控制面 resolveApprovedServerID 对齐：去首尾空白后非空、最多 64 字符、不得含任意空白。
function validateServerId(value: string): string | null {
  const serverId = value.trim()
  if (serverId === '') {
    return '请填写服务器 ID'
  }
  if (serverId.length > 64) {
    return '服务器 ID 最多 64 个字符'
  }
  if (/\s/.test(serverId)) {
    return '服务器 ID 不能包含空白字符'
  }
  return null
}
