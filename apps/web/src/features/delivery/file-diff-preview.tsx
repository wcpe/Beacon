// 变更项文件内容预览（懒加载）：按变更项 id 拉取文件前后内容，按变更类型渲染——
// modified 走行级双栏 diff（复用 TextDiff），added 展示新文件内容，removed 展示被删除内容，
// binary 项不回内容只展示元数据。错误形态按 HTTP status 分流（经 ApiClientError，不耦合错误码）：
// 403 = 敏感路径，内联填写原因后带 reason 重试；504 = before 侧 agent 离线，可手动重试。
// 仅在调用方展开该行时才挂载，故 useQuery 天然懒执行（展开即取、收起即卸载）。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, Button, Input, cn } from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import type { ChangeOrderItem, FileDiffResponse } from '../../api/delivery-changes'
import { fetchChangeItemFileDiff } from '../../api/delivery-changes'
import { formatBytes } from './format'
import TextDiff from './text-diff'

interface FileDiffPreviewProps {
  orderId: number
  item: ChangeOrderItem
}

export default function FileDiffPreview({ orderId, item }: FileDiffPreviewProps) {
  const { t } = useTranslation()
  // 敏感放行原因：403 后由内联表单提交，进 queryKey 触发带原因重取
  const [reason, setReason] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['change-orders', 'file-diff', orderId, item.id, reason],
    queryFn: () => fetchChangeItemFileDiff(orderId, item.id, reason === null ? undefined : { reason }),
    // 403（需原因）/ 504（agent 离线）是预期错误形态，自动重试无意义，交给内联表单 / 重试按钮处理
    retry: false,
  })

  const status = query.error instanceof ApiClientError ? query.error.status : null
  if (status === 403) {
    return <SensitiveReasonForm pending={query.isFetching} onSubmit={setReason} />
  }
  if (status === 504) {
    return (
      <OfflineRetry
        message={query.error instanceof Error ? query.error.message : String(query.error)}
        pending={query.isFetching}
        onRetry={() => {
          void query.refetch()
        }}
      />
    )
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

// 敏感路径（403）：内联填写原因后带 reason 重试（原因将记入审计）
function SensitiveReasonForm({ pending, onSubmit }: { pending: boolean; onSubmit: (reason: string) => void }) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState('')
  return (
    <div className="grid gap-2">
      <p className="rounded-lg border border-warn-bd bg-warn-bg px-3 py-2 text-sm text-warn">
        {t('delivery.preview.fileDiff.sensitiveHint')}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('delivery.preview.fileDiff.reasonLabel')}
          placeholder={t('delivery.preview.fileDiff.reasonPlaceholder')}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
          }}
          className="h-8 w-64"
        />
        <Button
          size="sm"
          disabled={draft.trim() === '' || pending}
          onClick={() => {
            onSubmit(draft.trim())
          }}
        >
          {t('delivery.preview.fileDiff.sensitiveConfirm')}
        </Button>
      </div>
    </div>
  )
}

// agent 离线（504）：展示后端脱敏真因 + 手动重试
function OfflineRetry({ message, pending, onRetry }: { message: string; pending: boolean; onRetry: () => void }) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-2">
      <p className="text-sm text-destructive">{message}</p>
      <Button size="sm" variant="outline" className="w-fit" disabled={pending} onClick={onRetry}>
        {t('delivery.preview.fileDiff.retry')}
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
