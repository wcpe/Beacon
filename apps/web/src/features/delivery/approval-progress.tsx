// 变更单详情审批进度视图（FR-255）：展示本单关联的统一审批申请状态、审批人与驳回理由，
// 填补「提审后详情页没有进度」的断层。
//
// 数据源：GET /admin/v2/approval-requests?keyword=<单号> + 客户端按 resourceType/resourceId 精确过滤。
// 为什么不用详情里的 approvalRequestId：变更单表不落该列（真机详情响应不带申请号），
// 后端 keyword 会命中 resource_id，故按单号反查是唯一可行的读法。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { ClipboardCheck, ExternalLink } from 'lucide-react'

import { AsyncSection, Badge, SectionHeader } from '@beacon/ui'
import type { ApprovalRequest, ApprovalStatus } from '@beacon/contracts'

import { fetchApprovals } from '../../api/approvals'
import { formatTime } from './format'
import type { PillVariant } from './status-badges'

// 审批状态 → 语义药丸（与 /approvals 页的状态语义一致）
const APPROVAL_VARIANT: Record<ApprovalStatus, PillVariant> = {
  pending: 'warn',
  executing: 'brand',
  succeeded: 'ok',
  failed: 'crit',
  rejected: 'crit',
  withdrawn: 'off',
  expired: 'off',
}

// 真机交付申请的操作键 → 文案键（未知键回退展示原始键，不吞信息）
const OPERATION_LABEL_KEYS: Record<string, string | undefined> = {
  'delivery.approve': 'submit',
  'delivery.draft_delete': 'draftDelete',
  'delivery.resume': 'resume',
  'delivery.confirm_batch': 'confirmBatch',
  'delivery.rollback': 'rollback',
  'delivery.rollback_finish': 'rollbackFinish',
}

/** 关联申请的展示上限（超出只提示条数，避免详情页被历史申请撑开） */
const MAX_ROWS = 5

/** 审批列表拉取页大小（接口无按资源过滤的参数，只按单号关键词拉一页后本地精确过滤） */
const FETCH_PAGE_SIZE = 50

interface ApprovalProgressProps {
  orderId: number
}

export default function ApprovalProgress({ orderId }: ApprovalProgressProps) {
  const { t } = useTranslation()

  const query = useQuery({
    queryKey: ['approvals', 'list', { changeOrderId: orderId }],
    queryFn: () => fetchApprovals({ keyword: String(orderId), pageSize: FETCH_PAGE_SIZE }),
    // 审批在审批中心推进，详情页需保持可见进度：轻量轮询（页面停留期间）
    refetchInterval: 5000,
  })

  const items = query.data?.items ?? []
  const rows = items
    .filter((row) => isChangeOrderOf(row, orderId))
    .sort((left, right) => Date.parse(right.updatedAt ?? '') - Date.parse(left.updatedAt ?? ''))
  // 两层口径分开，避免互相灌水：
  // ① 「本单超过展示上限」= 本地精确过滤后的条数（准确）；
  // ② 「服务端分页截断」= 响应 total > 本页条数（keyword 命中含无关行，故只提示审批中心命中数）
  const serverTruncated = (query.data?.total ?? items.length) > items.length
  // 本页没有任何本单申请、但服务端可能还有下一页（响应 total 大于本页条数，或本页正好拉满一页）：
  // 不能静默——用户无法区分「本单没有申请」与「申请在下一页」
  const maybeUnmatched = rows.length === 0 && (serverTruncated || items.length >= FETCH_PAGE_SIZE)

  // 确无关联申请且无分页截断：不渲染空卡（也不占版面）
  if (!query.isLoading && !query.isError && rows.length === 0 && !maybeUnmatched) {
    return null
  }

  return (
    <div className="grid gap-2 rounded-xl border border-border bg-surface-2 px-3 py-2.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <SectionHeader icon={<ClipboardCheck className="size-4" />} title={t('delivery.changes.approval.title')} />
        <Link className="text-xs text-brand hover:underline" to="/approvals">
          {t('delivery.changes.approval.openCenter')}
        </Link>
      </div>
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <ul className="grid gap-1.5">
          {rows.slice(0, MAX_ROWS).map((row) => (
            <ApprovalProgressRow key={row.requestId} row={row} />
          ))}
        </ul>
        {rows.length > MAX_ROWS && (
          <p className="text-xs text-ink-3">
            {t('delivery.changes.approval.truncatedOrder', { total: rows.length, shown: MAX_ROWS })}
          </p>
        )}
        {maybeUnmatched && (
          <p className="text-xs text-ink-3">{t('delivery.changes.approval.unmatched')}</p>
        )}
        {serverTruncated && (
          <p className="text-xs text-ink-3">
            {t('delivery.changes.approval.truncatedServer', {
              count: query.data?.total ?? items.length,
            })}
          </p>
        )}
      </AsyncSection>
    </div>
  )
}

// 单条申请：操作 + 申请号 + 状态 + 审批人 + 时间；驳回 / 失败时补原因行
function ApprovalProgressRow({ row }: { row: ApprovalRequest }) {
  const { t } = useTranslation()
  const labelKey = OPERATION_LABEL_KEYS[row.operationKey]
  const reason = row.rejectReason ?? (row.status === 'failed' ? row.decisionReason : null)
  return (
    <li className="grid gap-1 rounded-lg border border-border bg-surface px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-ink-1">
          {labelKey === undefined
            ? row.operationKey
            : t(`delivery.changes.approval.operation.${labelKey}`)}
        </span>
        <Badge variant={APPROVAL_VARIANT[row.status]} className="gap-1.5">
          <span className="size-1.5 rounded-full bg-current" aria-hidden />
          {t(`delivery.changes.approval.status.${row.status}`)}
        </Badge>
        <span className="font-mono text-xs text-ink-3">{row.requestId}</span>
        <Link
          className="ml-auto flex items-center gap-1 text-xs text-brand hover:underline"
          to={`/approvals/${encodeURIComponent(row.requestId)}`}
        >
          {t('delivery.changes.approval.open')}
          <ExternalLink className="size-3" aria-hidden />
        </Link>
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">
        <span>
          {t('delivery.changes.approval.approver')}：{row.deciderId ?? t('delivery.changes.approval.approverPending')}
        </span>
        <span>
          {t('delivery.changes.approval.updatedAt')}：{formatTime(row.updatedAt ?? null)}
        </span>
        <span>
          {t('delivery.changes.approval.reason')}：{row.requestReason === '' ? '-' : row.requestReason}
        </span>
      </div>
      {reason !== null && reason !== '' && (
        <p className="text-xs text-crit">
          {t('delivery.changes.approval.rejectReason')}：{reason}
        </p>
      )}
    </li>
  )
}

// 关联判定：真机的交付申请资源类型为 change-order、资源号即变更单号
function isChangeOrderOf(row: ApprovalRequest, orderId: number): boolean {
  return row.resourceType === 'change-order' && row.resourceId === String(orderId)
}
