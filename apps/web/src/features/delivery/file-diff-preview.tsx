// 变更项文件内容预览（懒加载）：按变更项 id 拉取文件前后内容，按变更类型渲染——
// modified 走行级双栏 diff（复用 TextDiff），added 展示新文件内容，removed 展示被删除内容，
// binary 项不回内容只展示元数据。
//
// 真实形态：后端对读源服文件内容恒回 409 operation_requires_approval（内容读取必须先走统一审批），
// 故此处按 409 展示「需审批」引导并给审批中心入口——不再按 403（敏感路径）/ 504（agent 离线）分流，
// 那两条是审批放行后才可能出现的下游错误，也不静默失败。
// 仅在调用方展开该行时才挂载，故 useQuery 天然懒执行（展开即取、收起即卸载）。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { useQuery } from '@tanstack/react-query'

import { AsyncSection, Button, cn } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import type { ChangeOrderItem, FileDiffResponse } from '../../api/delivery-changes'
import { fetchChangeItemFileDiff } from '../../api/delivery-changes'
import { formatBytes } from './format'
import TextDiff from './text-diff'

interface FileDiffPreviewProps {
  orderId: number
  item: ChangeOrderItem
}

// 需审批（真机恒 409）：内容读取必须先经统一审批放行
const NEEDS_APPROVAL_STATUS = 409

export default function FileDiffPreview({ orderId, item }: FileDiffPreviewProps) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['change-orders', 'file-diff', orderId, item.id],
    queryFn: () => fetchChangeItemFileDiff(orderId, item.id),
    // 409 是预期形态，自动重试无意义
    retry: false,
  })

  const status = query.error instanceof ApiClientError ? query.error.status : null
  if (status === NEEDS_APPROVAL_STATUS) {
    return <NeedsApprovalHint />
  }

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      loadingText={t('delivery.preview.fileDiff.loading')}
    >
      {query.data && <DiffBody data={query.data} item={item} />}
    </AsyncSection>
  )
}

// 成功态主体：对比目标 / 截断提示 + 二进制元数据或文本内容
function DiffBody({ data, item }: { data: FileDiffResponse; item: ChangeOrderItem }) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-1.5">
      {(data.serverId !== null || data.truncated) && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          {data.serverId !== null && (
            <span className="rounded-md bg-surface-2 px-1.5 py-0.5 font-mono text-ink-3 ring-1 ring-border">
              {t('delivery.preview.fileDiff.target', { serverId: data.serverId })}
            </span>
          )}
          {data.truncated && <span className="text-warn">{t('delivery.preview.fileDiff.truncated')}</span>}
        </div>
      )}
      {data.binary ? <BinaryMeta item={item} path={data.path} /> : <TextBody data={data} />}
    </div>
  )
}

// 文本项：按变更类型渲染双栏 diff / 单侧内容
function TextBody({ data }: { data: FileDiffResponse }) {
  const { t } = useTranslation()
  if (data.changeType === 'modified') {
    return (
      <TextDiff
        left={data.before ?? ''}
        right={data.after ?? ''}
        leftLabel={t('delivery.preview.fileDiff.beforeLabel')}
        rightLabel={t('delivery.preview.fileDiff.afterLabel')}
      />
    )
  }
  if (data.changeType === 'added') {
    return (
      <FileContentView label={t('delivery.preview.fileDiff.addedLabel')} content={data.after ?? ''} tone="ok" />
    )
  }
  return (
    <FileContentView label={t('delivery.preview.fileDiff.removedLabel')} content={data.before ?? ''} tone="crit" />
  )
}

// 二进制项：不支持内容对比，仅展示元数据（路径 / 大小 / 哈希）
function BinaryMeta({ item, path }: { item: ChangeOrderItem; path: string }) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-1 rounded-xl border border-border bg-surface-2 px-3 py-2">
      <p className="text-sm text-ink-2">{t('delivery.preview.fileDiff.binaryOnly')}</p>
      <span className="truncate font-mono text-xs text-ink-3">{path}</span>
      <span className="tnum text-xs text-ink-3">
        {t('delivery.preview.fileDiff.binaryMeta', {
          size: item.sizeBytes === null ? '-' : formatBytes(item.sizeBytes),
          hash: item.sha256 === null ? '-' : item.sha256.slice(0, 12),
        })}
      </span>
    </div>
  )
}

// 需审批（409 operation_requires_approval，真机恒此形态）：
// 文件内容读取必须先经统一审批放行，这里给明确引导与入口，不静默失败也不误报为「无权限」
function NeedsApprovalHint() {
  const { t } = useTranslation()
  return (
    <div className="grid gap-2 rounded-lg border border-warn-bd bg-warn-bg px-3 py-2">
      <p className="text-sm text-warn">{t('delivery.preview.fileDiff.needsApproval')}</p>
      <p className="text-xs text-ink-3">{t('delivery.preview.fileDiff.needsApprovalHint')}</p>
      <Button size="sm" variant="outline" className="w-fit" asChild>
        <Link to="/approvals">{t('delivery.preview.fileDiff.needsApprovalAction')}</Link>
      </Button>
    </div>
  )
}

// 单侧文件内容视图（新增 / 删除用）：等宽字体、可横向滚动，语义色标题条区分新增 / 删除
function FileContentView({ label, content, tone }: { label: string; content: string; tone: 'ok' | 'crit' }) {
  return (
    <div className="overflow-hidden rounded-xl border border-border">
      <div
        className={cn(
          'border-b border-border px-3 py-1.5 text-xs font-medium',
          tone === 'ok' ? 'bg-ok-bg text-ok' : 'bg-crit-bg text-crit',
        )}
      >
        {label}
      </div>
      <pre className="overflow-x-auto px-3 py-2 font-mono text-xs leading-relaxed text-ink-2">{content}</pre>
    </div>
  )
}
