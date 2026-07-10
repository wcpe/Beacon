// 变更项文件内容预览（懒加载）：按变更项 id 拉取文件前后内容，按变更类型渲染——
// modified 走行级双栏 diff（复用 TextDiff），added 展示新文件内容，removed 展示被删除内容。
// 仅在调用方展开该行时才挂载，故 useQuery 天然懒执行（展开即取、收起即卸载）。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, cn } from '@beacon/ui'

import type { ChangeOrderItem } from '../../api/delivery-changes'
import { fetchChangeItemFileDiff } from '../../api/delivery-changes'
import TextDiff from './text-diff'

interface FileDiffPreviewProps {
  orderId: number
  item: ChangeOrderItem
}

export default function FileDiffPreview({ orderId, item }: FileDiffPreviewProps) {
  const { t } = useTranslation()

  const query = useQuery({
    queryKey: ['change-orders', 'file-diff', orderId, item.id],
    queryFn: () => fetchChangeItemFileDiff(orderId, item.id),
  })

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      loadingText={t('delivery.preview.fileDiff.loading')}
    >
      {query.data && (
        <div className="grid gap-1.5">
          {query.data.truncated && (
            <p className="text-xs text-warn">{t('delivery.preview.fileDiff.truncated')}</p>
          )}
          {query.data.changeType === 'modified' ? (
            <TextDiff
              left={query.data.before ?? ''}
              right={query.data.after ?? ''}
              leftLabel={t('delivery.preview.fileDiff.beforeLabel')}
              rightLabel={t('delivery.preview.fileDiff.afterLabel')}
            />
          ) : query.data.changeType === 'added' ? (
            <FileContentView
              label={t('delivery.preview.fileDiff.addedLabel')}
              content={query.data.after ?? ''}
              tone="ok"
            />
          ) : (
            <FileContentView
              label={t('delivery.preview.fileDiff.removedLabel')}
              content={query.data.before ?? ''}
              tone="crit"
            />
          )}
        </div>
      )}
    </AsyncSection>
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
