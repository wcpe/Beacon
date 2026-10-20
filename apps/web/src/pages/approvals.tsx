// 审批中心（/approvals）：汇总所有范围待审批申请的全局收件箱与详情主从视图。
// 本页是唯一审批入口，不继承页眉环境 / 命名空间筛选；保留申请自身的命名空间信息供辨识。
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from 'react-router-dom'
import { AlertTriangle, ChevronDown, ChevronRight, ClipboardCheck, Clock3, ShieldAlert, SlidersHorizontal } from 'lucide-react'
import {
  Badge,
  Button,
  Combobox,
  Input,
  PageHeader,
} from '@beacon/ui'

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
import DocHelp from '../features/delivery/doc-help'
import MasterDetail from '../features/shared/master-detail'
import ListCard from '../features/shared/list-card'
import Pager from '../features/observability/pager'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'

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

// 风险级别 / 申请主体的「不筛选」在筛选状态里存空串，控件边界用 'all' 哨兵显示「全部」并映射回空串。
const SELECT_ALL = 'all'
const riskLevels: ApprovalRequest['riskLevel'][] = ['low', 'medium', 'high', 'critical']
const requesterTypes: ApprovalRequest['requesterType'][] = ['human', 'api_key', 'mcp', 'system']

type ApprovalAction = 'approve' | 'reject' | 'withdraw'

// 状态 / 风险 / 主体一律「值 → i18n key」翻译；未知值回退原串，避免把 key 渲进界面。
function statusLabel(t: TFunction, status: ApprovalStatus): string {
  return t(`delivery.approvals.status.${status}`, { defaultValue: status })
}

// 时间线条目类型：requested / approved 有专属文案，其余条目类型本身就是申请状态。
function timelineTypeLabel(t: TFunction, type: string): string {
  if (type === 'requested' || type === 'approved') {
    return t(`delivery.approvals.timeline.${type}`, { defaultValue: type })
  }
  return statusLabel(t, type as ApprovalStatus)
}

function riskLabel(t: TFunction, risk: ApprovalRequest['riskLevel']): string {
  return t(`delivery.approvals.risk.${risk}`, { defaultValue: risk })
}

// 风险徽标：语序由两侧译文决定（中文「高风险」/ 英文「High risk」）
function riskBadgeLabel(t: TFunction, risk: ApprovalRequest['riskLevel']): string {
  return t('delivery.approvals.riskBadge', { risk: riskLabel(t, risk) })
}

function principalLabel(t: TFunction, type: ApprovalRequest['requesterType']): string {
  return t(`delivery.approvals.principal.${type}`, { defaultValue: type })
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
  keyword: '',
  createdFrom: '',
  createdTo: '',
  expiresFrom: '',
  expiresTo: '',
}

function isDefaultPendingFilter(filters: Filters): boolean {
  return (
    filters.status === 'pending' &&
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

// 审批列表始终跨命名空间查询；这里只保留审批中心自己的筛选和分页参数。
function queryFromFilters(filters: Filters, page: number): ApprovalListQuery {
  return {
    status: filters.status,
    operationKey: filters.operationKey || undefined,
    riskLevel: filters.riskLevel || undefined,
    requesterType: filters.requesterType || undefined,
    requesterId: filters.requesterId || undefined,
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
  countLabel: string
}

function ApprovalFilters({ filters, onChange, onReset, countLabel }: ApprovalFiltersProps) {
  const { t } = useTranslation()
  // 高级筛选默认收起：审批日常只需「状态 + 关键字」，其余字段（领域/风险/主体/时间窗）
  // 属于排查场景。收起后筛选区从约 380px 压到 56px，把纵向空间还给列表。
  const [advancedOpen, setAdvancedOpen] = useState(false)
  // 高级区里是否有已生效的条件：有则即使收起也要给出可视提示，避免"隐藏着却仍在过滤"
  const advancedActive =
    filters.operationKey !== '' ||
    filters.riskLevel !== '' ||
    filters.requesterType !== '' ||
    filters.requesterId !== '' ||
    filters.createdFrom !== '' ||
    filters.createdTo !== '' ||
    filters.expiresFrom !== '' ||
    filters.expiresTo !== ''

  // 命名空间信息只用于申请上下文辨识，不参与本页筛选。
  return (
    // 筛选表单作为列表卡的工具栏，与其他交付列表共享卡面、滚动区与分页布局。
    <div className="grid gap-2.5">
      {/* 常显行：状态 / 关键字 / 高级开关 / 重置，所有控件单行横排 */}
      <div className="flex flex-wrap items-center gap-2">
        <Combobox
          id="approval-status"
          className="w-36"
          aria-label={t('delivery.approvals.filters.status')}
          value={filters.status}
          onChange={(value) => {
            onChange({ status: value as Filters['status'] })
          }}
          options={statuses.map((status) => ({
            value: status,
            label:
              status === 'all'
                ? t('delivery.approvals.filters.statusAll')
                : statusLabel(t, status),
          }))}
          allowCustom={false}
          emptyText={t('common.combobox.noMatch')}
        />
        <Input
          id="approval-keyword"
          aria-label={t('delivery.approvals.filters.keyword')}
          placeholder={t('delivery.approvals.filters.keywordPlaceholder')}
          value={filters.keyword}
          onChange={(event) => {
            onChange({ keyword: event.target.value })
          }}
          className="w-56"
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-9"
          aria-expanded={advancedOpen}
          onClick={() => {
            setAdvancedOpen((prev) => !prev)
          }}
        >
          <SlidersHorizontal className="size-3.5" aria-hidden />
          {t('delivery.approvals.filters.advanced')}
          {advancedActive && !advancedOpen ? (
            <span className="size-1.5 rounded-full bg-brand" aria-hidden />
          ) : null}
          <ChevronDown
            className={[
              'size-3.5 transition-transform duration-200',
              advancedOpen ? 'rotate-180' : 'rotate-0',
            ].join(' ')}
            aria-hidden
          />
        </Button>
        <span className="tnum ml-auto text-xs text-ink-3">{countLabel}</span>
        <Button type="button" variant="ghost" size="sm" className="h-9" onClick={onReset}>
          {t('observability.common.filterReset')}
        </Button>
      </div>

      {/* 高级筛选：字段用占位符代替竖排 Label，网格自适应 */}
      {advancedOpen && (
        <div className="grid gap-2 border-t border-border pt-3 sm:grid-cols-2 xl:grid-cols-4">
          <Input
            id="approval-operation"
            aria-label={t('delivery.approvals.filters.operation')}
            placeholder={t('delivery.approvals.filters.operationPlaceholder')}
            value={filters.operationKey}
            onChange={(event) => {
              onChange({ operationKey: event.target.value })
            }}
            className="h-9"
          />
          <Combobox
            id="approval-risk"
            className="w-full"
            aria-label={t('delivery.approvals.filters.risk')}
            value={filters.riskLevel === '' ? SELECT_ALL : filters.riskLevel}
            onChange={(value) => {
              onChange({ riskLevel: value === SELECT_ALL ? '' : value })
            }}
            options={[
              { value: SELECT_ALL, label: t('delivery.approvals.filters.riskAll') },
              ...riskLevels.map((risk) => ({ value: risk, label: riskLabel(t, risk) })),
            ]}
            allowCustom={false}
            emptyText={t('common.combobox.noMatch')}
          />
          <Combobox
            id="approval-requester-type"
            className="w-full"
            aria-label={t('delivery.approvals.filters.requesterType')}
            value={filters.requesterType === '' ? SELECT_ALL : filters.requesterType}
            onChange={(value) => {
              onChange({ requesterType: value === SELECT_ALL ? '' : value })
            }}
            options={[
              { value: SELECT_ALL, label: t('delivery.approvals.filters.requesterTypeAll') },
              ...requesterTypes.map((type) => ({ value: type, label: principalLabel(t, type) })),
            ]}
            allowCustom={false}
            emptyText={t('common.combobox.noMatch')}
          />
          <Input
            id="approval-requester"
            aria-label={t('delivery.approvals.filters.requester')}
            placeholder={t('delivery.approvals.filters.requesterPlaceholder')}
            value={filters.requesterId}
            onChange={(event) => {
              onChange({ requesterId: event.target.value })
            }}
            className="h-9"
          />
          <Input
            id="approval-created-from"
            aria-label={t('delivery.approvals.filters.createdFrom')}
            type="datetime-local"
            value={filters.createdFrom}
            onChange={(event) => {
              onChange({ createdFrom: event.target.value })
            }}
            className="h-9"
          />
          <Input
            id="approval-created-to"
            aria-label={t('delivery.approvals.filters.createdTo')}
            type="datetime-local"
            value={filters.createdTo}
            onChange={(event) => {
              onChange({ createdTo: event.target.value })
            }}
            className="h-9"
          />
          <Input
            id="approval-expires-from"
            aria-label={t('delivery.approvals.filters.expiresFrom')}
            type="datetime-local"
            value={filters.expiresFrom}
            onChange={(event) => {
              onChange({ expiresFrom: event.target.value })
            }}
            className="h-9"
          />
          <Input
            id="approval-expires-to"
            aria-label={t('delivery.approvals.filters.expiresTo')}
            type="datetime-local"
            value={filters.expiresTo}
            onChange={(event) => {
              onChange({ expiresTo: event.target.value })
            }}
            className="h-9"
          />
        </div>
      )}
    </div>
  )
}

interface ApprovalRowProps {
  row: ApprovalRequest
  selected: boolean
  onOpen: () => void
}

/** 审批列表列宽（表头与行共用，保证列对齐）：
 *  编号 / 摘要 / 领域 / 申请主体 / 状态 / 创建时间 / 行尾箭头 */
const APPROVAL_GRID = 'md:grid-cols-[11rem_minmax(0,1.6fr)_minmax(0,1fr)_8rem_5.5rem_7rem_1.5rem]'

/** 表头：桌面显示列名，小屏隐藏（小屏行内自带语义） */
function ApprovalTableHead() {
  const { t } = useTranslation()
  return (
    <div
      // 与 @beacon/ui 的 TableHead 保持同一套视觉参数：底色 surface-2/70、行高 h-9、
      // 水平内边距 px-2.5、字号 11px + semibold + 字距。此前这里是 bg-surface-2（不透明）
      // 加 px-3/py-2/font-medium，所以和其余页面的 DataTable 表头一眼就能看出不同。
      className={[
        'sticky top-0 z-10 hidden h-9 items-center gap-3 border-b border-border bg-surface-2/70 px-2.5 text-[11px] font-semibold tracking-[0.02em] whitespace-nowrap text-ink-4 md:grid',
        APPROVAL_GRID,
      ].join(' ')}
    >
      <span>{t('delivery.approvals.columns.requestId')}</span>
      <span>{t('delivery.approvals.columns.summary')}</span>
      <span>{t('delivery.approvals.columns.domain')}</span>
      <span>{t('delivery.approvals.columns.requester')}</span>
      <span>{t('delivery.approvals.columns.status')}</span>
      <span className="text-right">{t('delivery.approvals.columns.createdAt')}</span>
      <span aria-hidden />
    </div>
  )
}

function ApprovalRow({ row, selected, onOpen }: ApprovalRowProps) {
  const { t } = useTranslation()
  const namespace =
    row.namespaceId === null || row.namespaceId === undefined
      ? t('delivery.approvals.list.namespaceGlobal')
      : t('delivery.approvals.list.namespaceRef', { id: String(row.namespaceId) })
  return (
    <Button
      type="button"
      variant="ghost"
      className={[
        // 单行等高表格行：列对齐、点击区整行，去掉原来三行堆叠造成的纵向浪费。
        // 水平内边距与表头保持 px-2.5 一致，否则列起点会错开。
        'grid h-auto w-full justify-start gap-1 whitespace-normal rounded-none border-b border-border px-2.5 py-2 text-left font-normal hover:bg-surface-2',
        'md:items-center md:gap-3',
        APPROVAL_GRID,
        selected ? 'bg-brand-50 hover:bg-brand-50' : '',
      ].join(' ')}
      onClick={onOpen}
      aria-current={selected ? 'true' : undefined}
    >
      {/* 桌面：每列一格；小屏：同一 DOM 顺序流式堆叠，靠 mq 控制显隐 */}
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="truncate font-mono text-xs text-ink-3" title={row.requestId}>{row.requestId}</span>
      </span>
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="truncate text-sm font-medium text-ink-1" title={row.safeSummary}>{row.safeSummary}</span>
        <Badge
          variant={row.riskLevel === 'critical' ? 'destructive' : 'secondary'}
          className="shrink-0 text-[10px]"
        >
          {riskBadgeLabel(t, row.riskLevel)}
        </Badge>
      </span>
      <span className="flex min-w-0 flex-col md:flex-row md:items-center md:gap-1.5">
        <span className="truncate text-xs text-ink-2">{row.operationKey}</span>
        <span className="shrink-0 text-[11px] text-ink-4 md:hidden">{namespace}</span>
      </span>
      <span className="truncate text-xs text-ink-3">
        {principalLabel(t, row.requesterType)} · {row.requesterId}
      </span>
      <span>
        <Badge variant="outline" className="text-[10px]">
          {statusLabel(t, row.status)}
        </Badge>
      </span>
      <span className="text-[11px] text-ink-4 md:text-right">{formatTime(row.createdAt)}</span>
      {/* 可进入暗示：与「创建时间」同处第一行第 7 列。必须排在理由行之前，
          否则会被理由的 col-span 挤到第二行去 */}
      <span className="hidden items-center justify-end text-ink-4 md:flex" aria-hidden>
        <ChevronRight className="size-3.5" />
      </span>
      {/* 申请理由：第二行整行次要说明（跨全部 7 列） */}
      {row.requestReason ? (
        <span className="col-span-full truncate text-[11px] text-ink-4" title={row.requestReason}>
          {row.requestReason}
        </span>
      ) : null}
    </Button>
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
  const { t } = useTranslation()
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
      ? t('delivery.approvals.detail.blocked.drift')
      : evidenceStatus !== 'available'
        ? t('delivery.approvals.detail.blocked.evidence')
        : detail.expiresAt !== null && Date.parse(detail.expiresAt) <= Date.now()
          ? t('delivery.approvals.detail.blocked.expired')
          : null

  return (
    <div className="flex min-h-full flex-col gap-4">
      <div className="grid gap-1">
        <div className="font-mono text-xs text-ink-3">{detail.requestId}</div>
        <div className="text-sm font-semibold text-ink-1">{detail.safeSummary}</div>
        <div className="flex flex-wrap gap-2">
          <Badge variant="outline">{statusLabel(t, detail.status)}</Badge>
          <Badge variant={detail.riskLevel === 'critical' ? 'destructive' : 'secondary'}>
            {riskBadgeLabel(t, detail.riskLevel)}
          </Badge>
        </div>
      </div>
      <DetailSection title={t('delivery.approvals.detail.sections.riskImpact')}>
        <p className="text-sm text-ink-2">{detail.riskSummary}</p>
        <p className="text-sm text-ink-2">{detail.impactSummary}</p>
        <p className="text-sm text-ink-2">{detail.securitySummary}</p>
      </DetailSection>
      <DetailSection title={t('delivery.approvals.detail.sections.snapshot')}>
        <dl className="grid gap-1 text-sm">
          {(detail.frozenPayloadSummary ?? []).map((line) => (
            <div className="grid grid-cols-[7rem_1fr] gap-2" key={line.label}>
              <dt className="text-ink-3">{line.label}</dt>
              <dd className="text-ink-1">{line.value}</dd>
            </div>
          ))}
        </dl>
      </DetailSection>
      <DetailSection title={t('delivery.approvals.detail.sections.currentFacts')}>
        <dl className="grid gap-1 text-sm">
          {(detail.currentFactsSummary ?? []).map((line) => (
            <div className="grid grid-cols-[7rem_1fr] gap-2" key={line.label}>
              <dt className="text-ink-3">{line.label}</dt>
              <dd className="text-ink-1">{line.value}</dd>
            </div>
          ))}
        </dl>
      </DetailSection>
      <DetailSection title={t('delivery.approvals.detail.sections.diff')}>
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
      <DetailSection title={t('delivery.approvals.detail.sections.timeline')}>
        <ol className="grid gap-2 text-sm">
          {(detail.timeline ?? []).map((item, index) => (
            <li className="grid grid-cols-[auto_1fr] gap-2" key={`${item.at}-${String(index)}`}>
              <Clock3 className="mt-0.5 size-4 text-ink-4" aria-hidden />
              <span>
                <strong>{timelineTypeLabel(t, item.type)}</strong> · {formatTime(item.at)}
                {item.note ? ` · ${item.note}` : ''}
              </span>
            </li>
          ))}
        </ol>
      </DetailSection>
      {detail.resultRef ? (
        <a className="text-sm text-brand underline-offset-2 hover:underline" href={detail.resultRef}>
          {t('delivery.approvals.detail.resultLink')}
        </a>
      ) : null}
      {blockedReason ? (
        <p className="flex items-start gap-2 rounded-md bg-muted px-2.5 py-2 text-sm text-ink-2">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {blockedReason}
        </p>
      ) : null}
      {isPending && !detail.canApprove && !detail.canReject && !detail.canWithdraw ? (
        <p className="text-sm text-ink-3">{t('delivery.approvals.detail.noActions')}</p>
      ) : null}
      {isPending ? (
        <div data-slot="detail-actions-footer" className="sticky bottom-0 z-10 mt-auto -mx-4 flex flex-wrap gap-2 border-t border-border bg-card/95 px-4 py-3 backdrop-blur">
          {detail.canApprove ? (
            <Button
              type="button"
              disabled={!canApprove || actionPending}
              onClick={() => {
                onAction('approve')
              }}
            >
              {t('delivery.approvals.actions.approve')}
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
              {t('delivery.approvals.actions.reject')}
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
              {t('delivery.approvals.actions.withdraw')}
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
  const { t } = useTranslation()
  const { requestId } = useParams<{ requestId: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [filters, setFilters] = useState<Filters>(initialFilters)
  const [page, setPage] = useState(1)
  const [action, setAction] = useState<ApprovalAction | null>(null)
  const listParams = useMemo(
    () => queryFromFilters(filters, page),
    [filters, page],
  )
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
        throw new Error(t('delivery.approvals.errors.missingRequestId'))
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
    // 页内筛选变化时回到第 1 页，避免停在已越界的高页码上。
    setPage(1)
  }, [filters])

  const selected = detailQuery.data
  const list = listQuery.data?.items ?? []
  const total = listQuery.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const actionTitle =
    action === 'approve'
      ? t('delivery.approvals.confirm.approveTitle')
      : action === 'reject'
        ? t('delivery.approvals.confirm.rejectTitle')
        : t('delivery.approvals.confirm.withdrawTitle')
  const actionDescription =
    action === 'approve'
      ? t('delivery.approvals.confirm.approveDesc')
      : action === 'reject'
        ? t('delivery.approvals.confirm.rejectDesc')
        : t('delivery.approvals.confirm.withdrawDesc')

  return (
    <section className="grid gap-4 md:flex md:h-full md:min-h-0 md:flex-col">
      {/* 页面身份标识：标题与说明统一由 PageHeader 承担，文档入口挂在右侧操作区。 */}
      <PageHeader
        icon={<ClipboardCheck />}
        title={t('nav.approvals')}
        description={t('delivery.approvals.pageDesc')}
        actions={<DocHelp topic="approvals" />}
      />
      <MasterDetail
        fill
        master={
          <ListCard
            fill
            toolbar={
              <ApprovalFilters
                filters={filters}
                onChange={(next) => {
                  setFilters((current) => ({ ...current, ...next }))
                }}
                onReset={() => {
                  setFilters(initialFilters)
                }}
                countLabel={
                  listQuery.data
                    ? t('delivery.approvals.list.total', { count: total })
                    : t('delivery.approvals.list.loading')
                }
              />
            }
            footer={
              listQuery.data && total > 0 ? (
                <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} />
              ) : undefined
            }
          >
            {listQuery.isLoading ? (
              <div className="grid gap-2 p-4">
                <div className="h-12 animate-pulse rounded bg-muted" />
                <div className="h-12 animate-pulse rounded bg-muted" />
                <div className="h-12 animate-pulse rounded bg-muted" />
              </div>
            ) : listQuery.isError ? (
              <div className="grid gap-2 p-6 text-sm">
                <p className="text-destructive">{t('delivery.approvals.list.loadFailed')}</p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    void listQuery.refetch()
                  }}
                >
                  {t('delivery.approvals.list.retry')}
                </Button>
              </div>
            ) : list.length === 0 ? (
              <div className="grid justify-items-center gap-4 px-4 py-10 text-center text-sm text-ink-3">
                <AlertTriangle className="mx-auto size-6" aria-hidden />
                <p>
                  {/* 默认 pending 筛选下的全局空态；本页不继承页眉命名空间范围。 */}
                  {isDefaultPendingFilter(filters)
                    ? t('delivery.approvals.list.emptyAll')
                    : t('delivery.approvals.list.emptyFiltered')}
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setFilters(initialFilters)
                  }}
                >
                  {t('delivery.approvals.list.clearFilters')}
                </Button>
              </div>
            ) : (
              <>
                <ApprovalTableHead />
                {list.map((row) => (
                  <ApprovalRow
                    key={row.requestId}
                    row={row}
                    selected={row.requestId === requestId}
                    onOpen={() => {
                      void navigate(`/approvals/${encodeURIComponent(row.requestId)}`)
                    }}
                  />
                ))}
              </>
            )}
          </ListCard>
        }
        detail={
          requestId === undefined ? null : detailQuery.isLoading ? (
            <div className="grid gap-3">
              <div className="h-5 animate-pulse rounded bg-muted" />
              <div className="h-32 animate-pulse rounded bg-muted" />
            </div>
          ) : detailQuery.isError ? (
            <div className="grid gap-2 text-sm">
              <p className="text-destructive">{t('delivery.approvals.detail.loadFailed')}</p>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  void detailQuery.refetch()
                }}
              >
                {t('delivery.approvals.detail.retry')}
              </Button>
            </div>
          ) : selected ? (
            <ApprovalDetail
              detail={selected}
              onAction={(next) => {
                mutation.reset()
                setAction(next)
              }}
              actionPending={mutation.isPending}
            />
          ) : null
        }
        detailTitle={selected?.safeSummary ?? t('delivery.approvals.detail.title')}
        onClose={() => {
          void navigate('/approvals')
        }}
        closeLabel={t('delivery.approvals.detail.close')}
      />
      <ConfirmDialog
        open={action !== null}
        onOpenChange={(open) => {
          if (!open && !mutation.isPending) {
            setAction(null)
            mutation.reset()
          }
        }}
        title={actionTitle}
        description={actionDescription}
        confirmLabel={
          action === 'approve'
            ? t('delivery.approvals.actions.approve')
            : action === 'reject'
              ? t('delivery.approvals.actions.reject')
              : t('delivery.approvals.actions.withdraw')
        }
        confirmVariant={action === 'reject' || action === 'withdraw' ? 'destructive' : 'default'}
        requireReason={action === 'reject'}
        pending={mutation.isPending}
        errorText={mutation.isError && mutation.error instanceof Error ? mutation.error.message : null}
        onConfirm={({ reason }) => {
          if (action !== null) {
            mutation.mutate({ type: action, reason })
          }
        }}
      />
    </section>
  )
}
