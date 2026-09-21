import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from 'react-router-dom'
import { AlertTriangle, Clock3, ShieldAlert } from 'lucide-react'
import { Badge, Button, Input } from '@beacon/ui'

import type { ApprovalRequest, ApprovalStatus } from '@beacon/contracts'

import {
  approveApproval,
  fetchApprovalDetail,
  fetchApprovals,
  rejectApproval,
  withdrawApproval,
  type ApprovalListQuery,
} from '../api/approvals'
import ConfirmDialog from './changes/confirm-dialog'
import MasterDetail from '../features/shared/master-detail'
import Pager from '../features/observability/pager'
import { useTranslation } from 'react-i18next'

const PAGE_SIZE = 20
const statuses: (ApprovalStatus | 'all')[] = [
  'all',
  'pending',
  'executing',
  'succeeded',
  'failed',
  'rejected',
  'withdrawn',
  'expired',
]

type ApprovalAction = 'approve' | 'reject' | 'withdraw'

function displayStatus(status: ApprovalStatus): string {
  return {
    pending: '待审批',
    executing: '执行中',
    succeeded: '已完成',
    failed: '执行失败',
    rejected: '已拒绝',
    withdrawn: '已撤回',
    expired: '已过期',
  }[status]
}

function displayTimelineType(type: string): string {
  return type === 'requested' ? '已提交' : type === 'approved' ? '已批准' : displayStatus(type as ApprovalStatus)
}

function displayRisk(risk: ApprovalRequest['riskLevel']): string {
  return { low: '低', medium: '中', high: '高', critical: '严重' }[risk]
}

function displayPrincipal(type: ApprovalRequest['requesterType']): string {
  return { human: '人类', api_key: 'API 密钥', mcp: 'MCP', system: '系统' }[type]
}

function formatTime(value?: string | null): string {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value))
}

function toIso(value: string): string | undefined {
  return value === '' ? undefined : new Date(value).toISOString()
}

interface Filters {
  status: ApprovalStatus | 'all'
  operationKey: string
  riskLevel: string
  requesterType: string
  requesterId: string
  namespaceId: string
  keyword: string
  createdFrom: string
  createdTo: string
  expiresFrom: string
  expiresTo: string
}

const initialFilters: Filters = {
  status: 'pending',
  operationKey: '',
  riskLevel: '',
  requesterType: '',
  requesterId: '',
  namespaceId: 'all',
  keyword: '',
  createdFrom: '',
  createdTo: '',
  expiresFrom: '',
  expiresTo: '',
}

function isDefaultPendingFilter(filters: Filters): boolean {
  return (
    filters.status === 'pending' &&
    filters.namespaceId === 'all' &&
    filters.operationKey === '' &&
    filters.riskLevel === '' &&
    filters.requesterType === '' &&
    filters.requesterId === '' &&
    filters.keyword === '' &&
    filters.createdFrom === '' &&
    filters.createdTo === '' &&
    filters.expiresFrom === '' &&
    filters.expiresTo === ''
  )
}

function queryFromFilters(filters: Filters, page: number): ApprovalListQuery {
  return {
    status: filters.status,
    operationKey: filters.operationKey || undefined,
    riskLevel: filters.riskLevel || undefined,
    requesterType: filters.requesterType || undefined,
    requesterId: filters.requesterId || undefined,
    namespaceId:
      filters.namespaceId === 'all'
        ? undefined
        : filters.namespaceId === 'global'
          ? 'global'
          : Number(filters.namespaceId),
    keyword: filters.keyword || undefined,
    createdFrom: toIso(filters.createdFrom),
    createdTo: toIso(filters.createdTo),
    expiresFrom: toIso(filters.expiresFrom),
    expiresTo: toIso(filters.expiresTo),
    page,
    pageSize: PAGE_SIZE,
  }
}

interface ApprovalFiltersProps {
  filters: Filters
  onChange: (next: Partial<Filters>) => void
  onReset: () => void
}

function ApprovalFilters({ filters, onChange, onReset }: ApprovalFiltersProps) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-2 rounded-xl border border-border bg-card p-3 shadow-card">
      <div className="grid gap-2 md:grid-cols-4">
        <label className="grid gap-1 text-xs text-ink-3">
          状态
          <select
            aria-label="审批状态"
            className="h-8 rounded-md border border-input bg-background px-2 text-sm text-ink-1"
            value={filters.status}
            onChange={(event) => {
              onChange({ status: event.target.value as Filters['status'] })
            }}
          >
            {statuses.map((status) => (
              <option key={status} value={status}>
                {status === 'all' ? '全部状态' : displayStatus(status)}
              </option>
            ))}
          </select>
        </label>
        <label className="grid gap-1 text-xs text-ink-3">
          operation / 领域
          <Input
            aria-label="operation / 领域"
            value={filters.operationKey}
            onChange={(event) => {
              onChange({ operationKey: event.target.value })
            }}
            placeholder="如 config.publish"
          />
        </label>
        <label className="grid gap-1 text-xs text-ink-3">
          风险
          <select
            aria-label="风险级别"
            className="h-8 rounded-md border border-input bg-background px-2 text-sm text-ink-1"
            value={filters.riskLevel}
            onChange={(event) => {
              onChange({ riskLevel: event.target.value })
            }}
          >
            <option value="">全部风险</option>
            <option value="low">低</option>
            <option value="medium">中</option>
            <option value="high">高</option>
            <option value="critical">严重</option>
          </select>
        </label>
        <label className="grid gap-1 text-xs text-ink-3">
          申请主体
          <select
            aria-label="申请主体类型"
            className="h-8 rounded-md border border-input bg-background px-2 text-sm text-ink-1"
            value={filters.requesterType}
            onChange={(event) => {
              onChange({ requesterType: event.target.value })
            }}
          >
            <option value="">全部主体</option>
            <option value="human">人类</option>
            <option value="api_key">API 密钥</option>
            <option value="mcp">MCP</option>
            <option value="system">系统</option>
          </select>
        </label>
      </div>
      <div className="grid gap-2 md:grid-cols-4">
        <label className="grid gap-1 text-xs text-ink-3">
          申请人
          <Input
            aria-label="申请人"
            value={filters.requesterId}
            onChange={(event) => {
              onChange({ requesterId: event.target.value })
            }}
          />
        </label>
        <label className="grid gap-1 text-xs text-ink-3">
          目标 namespace
          <select
            aria-label="目标命名空间"
            className="h-8 rounded-md border border-input bg-background px-2 text-sm text-ink-1"
            value={filters.namespaceId}
            onChange={(event) => {
              onChange({ namespaceId: event.target.value })
            }}
          >
            <option value="all">全部命名空间</option>
            <option value="global">全局操作</option>
            <option value="1">namespace 1</option>
            <option value="2">namespace 2</option>
          </select>
        </label>
        <label className="grid gap-1 text-xs text-ink-3">
          关键字
          <Input
            aria-label="审批关键字"
            value={filters.keyword}
            onChange={(event) => {
              onChange({ keyword: event.target.value })
            }}
          />
        </label>
        <div className="flex items-end justify-end">
          <Button type="button" variant="ghost" onClick={onReset}>
            {t('observability.common.filterReset')}
          </Button>
        </div>
      </div>
      <div className="grid gap-2 md:grid-cols-4">
        <label className="grid gap-1 text-xs text-ink-3">
          创建时间起
          <input
            aria-label="创建时间起"
            type="datetime-local"
            className="h-8 rounded-md border border-input bg-background px-2 text-sm text-ink-1"
            value={filters.createdFrom}
            onChange={(event) => {
              onChange({ createdFrom: event.target.value })
            }}
          />
        </label>
        <label className="grid gap-1 text-xs text-ink-3">
          创建时间止
          <input
            aria-label="创建时间止"
            type="datetime-local"
            className="h-8 rounded-md border border-input bg-background px-2 text-sm text-ink-1"
            value={filters.createdTo}
            onChange={(event) => {
              onChange({ createdTo: event.target.value })
            }}
          />
        </label>
        <label className="grid gap-1 text-xs text-ink-3">
          过期时间起
          <input
            aria-label="过期时间起"
            type="datetime-local"
            className="h-8 rounded-md border border-input bg-background px-2 text-sm text-ink-1"
            value={filters.expiresFrom}
            onChange={(event) => {
              onChange({ expiresFrom: event.target.value })
            }}
          />
        </label>
        <label className="grid gap-1 text-xs text-ink-3">
          过期时间止
          <input
            aria-label="过期时间止"
            type="datetime-local"
            className="h-8 rounded-md border border-input bg-background px-2 text-sm text-ink-1"
            value={filters.expiresTo}
            onChange={(event) => {
              onChange({ expiresTo: event.target.value })
            }}
          />
        </label>
      </div>
    </div>
  )
}

interface ApprovalRowProps {
  row: ApprovalRequest
  selected: boolean
  onOpen: () => void
}

function ApprovalRow({ row, selected, onOpen }: ApprovalRowProps) {
  const namespace = row.namespaceId === null || row.namespaceId === undefined ? '全局' : `namespace ${String(row.namespaceId)}`
  return (
    <button
      type="button"
      className={[
        'grid w-full gap-2 border-b border-border px-3 py-3 text-left transition-colors hover:bg-surface-2',
        selected ? 'bg-brand-50' : '',
      ].join(' ')}
      onClick={onOpen}
      aria-current={selected ? 'true' : undefined}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-xs text-ink-3">{row.requestId}</span>
        <Badge variant={row.riskLevel === 'critical' ? 'destructive' : 'secondary'}>
          {displayRisk(row.riskLevel)}风险
        </Badge>
        <Badge variant="outline">{displayStatus(row.status)}</Badge>
        <span className="ml-auto text-xs text-ink-4">{formatTime(row.createdAt)}</span>
      </div>
      <div className="grid gap-1 text-sm text-ink-1 md:grid-cols-[1.4fr_1fr_0.8fr]">
        <span className="font-medium">{row.safeSummary}</span>
        <span>{row.operationKey}</span>
        <span>{namespace}</span>
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">
        <span>
          {displayPrincipal(row.requesterType)} · {row.requesterId}
        </span>
        <span>{row.requestReason}</span>
        <span>过期：{formatTime(row.expiresAt)}</span>
      </div>
    </button>
  )
}

function DetailSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-2 border-b border-border pb-3">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-ink-3">{title}</h3>
      {children}
    </section>
  )
}

interface ApprovalDetailProps {
  detail: ApprovalRequest
  onAction: (action: ApprovalAction) => void
  actionPending: boolean
}

function ApprovalDetail({ detail, onAction, actionPending }: ApprovalDetailProps) {
  const evidenceStatus = detail.evidenceStatus ?? 'available'
  const driftStatus = detail.driftStatus ?? 'none'
  const isPending = detail.status === 'pending'
  const canApprove =
    detail.canApprove === true &&
    driftStatus === 'none' &&
    evidenceStatus === 'available' &&
    (detail.expiresAt === null || Date.parse(detail.expiresAt) > Date.now())
  const blockedReason =
    driftStatus === 'detected'
      ? '当前事实已漂移，需按当前事实重新申请'
      : evidenceStatus !== 'available'
        ? '快照或当前证据不可用/已过期'
        : detail.expiresAt !== null && Date.parse(detail.expiresAt) <= Date.now()
          ? '申请已过期'
          : null

  return (
    <div className="grid gap-4">
      <div className="grid gap-1">
        <div className="font-mono text-xs text-ink-3">{detail.requestId}</div>
        <div className="text-sm font-semibold text-ink-1">{detail.safeSummary}</div>
        <div className="flex flex-wrap gap-2">
          <Badge variant="outline">{displayStatus(detail.status)}</Badge>
          <Badge variant={detail.riskLevel === 'critical' ? 'destructive' : 'secondary'}>
            {displayRisk(detail.riskLevel)}风险
          </Badge>
        </div>
      </div>
      <DetailSection title="风险与影响">
        <p className="text-sm text-ink-2">{detail.riskSummary}</p>
        <p className="text-sm text-ink-2">{detail.impactSummary}</p>
        <p className="text-sm text-ink-2">{detail.securitySummary}</p>
      </DetailSection>
      <DetailSection title="申请快照">
        <dl className="grid gap-1 text-sm">
          {(detail.frozenPayloadSummary ?? []).map((line) => (
            <div className="grid grid-cols-[7rem_1fr] gap-2" key={line.label}>
              <dt className="text-ink-3">{line.label}</dt>
              <dd className="text-ink-1">{line.value}</dd>
            </div>
          ))}
        </dl>
      </DetailSection>
      <DetailSection title="当前事实">
        <dl className="grid gap-1 text-sm">
          {(detail.currentFactsSummary ?? []).map((line) => (
            <div className="grid grid-cols-[7rem_1fr] gap-2" key={line.label}>
              <dt className="text-ink-3">{line.label}</dt>
              <dd className="text-ink-1">{line.value}</dd>
            </div>
          ))}
        </dl>
      </DetailSection>
      <DetailSection title="快照 / 当前差异">
        <div className="grid gap-2 text-sm">
          {(detail.currentDiff ?? []).map((line) => (
            <div
              className={[
                'grid grid-cols-[7rem_1fr_1fr] gap-2 rounded-md px-2 py-1',
                line.changed ? 'bg-destructive/10 text-destructive' : 'bg-muted/50',
              ].join(' ')}
              key={line.label}
            >
              <span>{line.label}</span>
              <span>{line.snapshot}</span>
              <span>{line.current}</span>
            </div>
          ))}
        </div>
      </DetailSection>
      <DetailSection title="时间线">
        <ol className="grid gap-2 text-sm">
          {(detail.timeline ?? []).map((item, index) => (
            <li className="grid grid-cols-[auto_1fr] gap-2" key={`${item.at}-${String(index)}`}>
              <Clock3 className="mt-0.5 size-4 text-ink-4" aria-hidden />
              <span>
                <strong>{displayTimelineType(item.type)}</strong> · {formatTime(item.at)}
                {item.note ? ` · ${item.note}` : ''}
              </span>
            </li>
          ))}
        </ol>
      </DetailSection>
      {detail.resultRef ? (
        <a className="text-sm text-brand underline-offset-2 hover:underline" href={detail.resultRef}>
          查看领域结果与审计
        </a>
      ) : null}
      {blockedReason ? (
        <p className="flex items-start gap-2 rounded-md bg-muted px-2.5 py-2 text-sm text-ink-2">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {blockedReason}
        </p>
      ) : null}
      {isPending && !detail.canApprove && !detail.canReject && !detail.canWithdraw ? (
        <p className="text-sm text-ink-3">当前主体没有可执行的审批操作。</p>
      ) : null}
      {isPending ? (
        <div className="flex flex-wrap gap-2">
          {detail.canApprove ? (
            <Button
              type="button"
              disabled={!canApprove || actionPending}
              onClick={() => {
                onAction('approve')
              }}
            >
              批准并执行
            </Button>
          ) : null}
          {detail.canReject ? (
            <Button
              type="button"
              variant="outline"
              disabled={actionPending}
              onClick={() => {
                onAction('reject')
              }}
            >
              拒绝
            </Button>
          ) : null}
          {detail.canWithdraw ? (
            <Button
              type="button"
              variant="ghost"
              disabled={actionPending}
              onClick={() => {
                onAction('withdraw')
              }}
            >
              撤回
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

function isConflictError(error: unknown): boolean {
  return error instanceof Error && 'status' in error && (error as { status?: number }).status === 409
}

export default function ApprovalsPage() {
  const { requestId } = useParams<{ requestId: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [filters, setFilters] = useState<Filters>(initialFilters)
  const [page, setPage] = useState(1)
  const [action, setAction] = useState<ApprovalAction | null>(null)
  const listParams = useMemo(() => queryFromFilters(filters, page), [filters, page])
  const listQuery = useQuery({
    queryKey: ['approvals', 'list', listParams],
    queryFn: () => fetchApprovals(listParams),
  })
  const detailQuery = useQuery({
    queryKey: ['approvals', 'detail', requestId],
    queryFn: () => fetchApprovalDetail(requestId ?? ''),
    enabled: requestId !== undefined,
    refetchInterval: (query) => (query.state.data?.status === 'executing' ? 1000 : false),
  })
  const mutation = useMutation({
    mutationFn: async ({ type, reason }: { type: ApprovalAction; reason: string }) => {
      if (requestId === undefined) {
        throw new Error('缺少审批申请编号')
      }
      if (type === 'approve') return approveApproval(requestId)
      if (type === 'reject') return rejectApproval(requestId, reason)
      return withdrawApproval(requestId)
    },
    onSuccess: async () => {
      setAction(null)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['approvals', 'list'] }),
        detailQuery.refetch(),
      ])
    },
    onError: async (error: unknown) => {
      if (isConflictError(error)) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ['approvals', 'list'] }),
          detailQuery.refetch(),
        ])
      }
    },
  })

  useEffect(() => {
    setPage(1)
  }, [filters])

  const selected = detailQuery.data
  const list = listQuery.data?.items ?? []
  const total = listQuery.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const actionTitle =
    action === 'approve' ? '确认批准并执行' : action === 'reject' ? '确认拒绝审批' : '确认撤回申请'
  const actionDescription =
    action === 'approve'
      ? '批准只调用统一审批 API，服务端将自动创建执行任务。'
      : action === 'reject'
        ? '拒绝后申请进入终态，必须填写原因。'
        : '撤回后申请进入终态，原因可选。'

  return (
    <section className="grid gap-4">
      <div
        data-slot="global-approval-scope"
        className="flex items-start gap-2 rounded-lg border border-brand-100 bg-brand-50 px-3 py-2 text-sm text-brand"
      >
        <ShieldAlert className="mt-0.5 size-4" aria-hidden />
        全局审批不继承页眉环境或 namespace scope；以下 namespace 仅为页面筛选。
      </div>
      <ApprovalFilters
        filters={filters}
        onChange={(next) => {
          setFilters((current) => ({ ...current, ...next }))
        }}
        onReset={() => {
          setFilters(initialFilters)
        }}
      />
      <MasterDetail
        master={
          <div className="grid gap-3">
            <div className="flex items-center justify-between">
              <h2 className="text-sm font-semibold text-ink-1">审批申请</h2>
              <span className="text-xs text-ink-3">
                {listQuery.data ? `共 ${String(total)} 条` : '加载中…'}
              </span>
            </div>
            <div className="overflow-hidden rounded-xl border border-border bg-card shadow-card">
              {listQuery.isLoading ? (
                <div className="grid gap-2 p-4">
                  <div className="h-12 animate-pulse rounded bg-muted" />
                  <div className="h-12 animate-pulse rounded bg-muted" />
                  <div className="h-12 animate-pulse rounded bg-muted" />
                </div>
              ) : listQuery.isError ? (
                <div className="grid gap-2 p-6 text-sm">
                  <p className="text-destructive">审批列表加载失败</p>
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => {
                      void listQuery.refetch()
                    }}
                  >
                    重试
                  </Button>
                </div>
              ) : list.length === 0 ? (
                <div className="grid gap-2 p-8 text-center text-sm text-ink-3">
                  <AlertTriangle className="mx-auto size-6" aria-hidden />
                  <p>
                    {isDefaultPendingFilter(filters)
                      ? '全局暂无待审批'
                      : '当前筛选无结果'}
                  </p>
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => {
                      setFilters(initialFilters)
                    }}
                  >
                    清除筛选
                  </Button>
                </div>
              ) : (
                list.map((row) => (
                  <ApprovalRow
                    key={row.requestId}
                    row={row}
                    selected={row.requestId === requestId}
                    onOpen={() => {
                      void navigate(`/approvals/${encodeURIComponent(row.requestId)}`)
                    }}
                  />
                ))
              )}
            </div>
            {listQuery.data && total > 0 ? (
              <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />
            ) : null}
          </div>
        }
        detail={
          requestId === undefined ? null : detailQuery.isLoading ? (
            <div className="grid gap-3">
              <div className="h-5 animate-pulse rounded bg-muted" />
              <div className="h-32 animate-pulse rounded bg-muted" />
            </div>
          ) : detailQuery.isError ? (
            <div className="grid gap-2 text-sm">
              <p className="text-destructive">审批详情加载失败</p>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  void detailQuery.refetch()
                }}
              >
                重试
              </Button>
            </div>
          ) : selected ? (
            <ApprovalDetail
              detail={selected}
              onAction={setAction}
              actionPending={mutation.isPending}
            />
          ) : null
        }
        detailTitle={selected?.safeSummary ?? '审批详情'}
        onClose={() => {
          void navigate('/approvals')
        }}
        closeLabel="关闭详情"
      />
      <ConfirmDialog
        open={action !== null}
        onOpenChange={(open) => {
          if (!open && !mutation.isPending) {
            setAction(null)
          }
        }}
        title={actionTitle}
        description={actionDescription}
        confirmLabel={action === 'approve' ? '批准并执行' : action === 'reject' ? '拒绝' : '撤回'}
        requireReason={action === 'reject'}
        pending={mutation.isPending}
        errorText={mutation.error instanceof Error ? mutation.error.message : null}
        onConfirm={({ reason }) => {
          if (action !== null) {
            mutation.mutate({ type: action, reason })
          }
        }}
      />
    </section>
  )
}
