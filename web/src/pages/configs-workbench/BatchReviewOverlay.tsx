// 队列批量审核浮层（改进 4）：同步队列里多选「待审核」项（待审·ingest / 待审·拓印）后，
// 一次性审阅 —— 左侧列待审文件（逐个可看 diff/清单）+「我已审阅全部」批量门 +「全部通过」一并转完成。
// 告别一条条点。纯前端 mock，用相对工作台容器的 absolute 覆盖层（非 fixed）。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, ArrowRight, ClipboardCheck, FileSearch, GitCompare, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import CodeEditor from '@/components/CodeEditor'
import { cn } from '@/lib/utils'
import { imprintDiffs } from './sampleData'
import type { SyncQueueRow } from './types'

export default function BatchReviewOverlay({
  // 选中的待审队列行（ingest / imprint 混合）
  rows,
  // 全部通过：选中项一并转完成
  onConfirm,
  onCancel,
}: {
  rows: SyncQueueRow[]
  onConfirm: () => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  // 批量审阅门：勾选才放行「全部通过」
  const [reviewed, setReviewed] = useState(false)
  // 当前在右侧详情区查看的行
  const [active, setActive] = useState<SyncQueueRow>(rows[0])

  const fetchCount = useMemo(() => rows.filter((r) => r.direction === 'fetch').length, [rows])
  const pushCount = rows.length - fetchCount

  return (
    <div className="absolute inset-0 z-50 flex items-center justify-center p-6">
      {/* 半透明遮罩 */}
      <div className="absolute inset-0 bg-background/70 backdrop-blur-sm" onClick={onCancel} aria-hidden />
      {/* 浮层卡 */}
      <div className="relative flex max-h-full w-full max-w-4xl flex-col overflow-hidden rounded-lg border border-border bg-card shadow-2xl">
        {/* 头 */}
        <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-4 py-3">
          <ClipboardCheck className="h-4 w-4 text-muted-foreground" />
          <div className="min-w-0">
            <div className="text-sm font-medium text-foreground">{t('configs.workbench.batchReviewTitle')}</div>
            <div className="text-[0.65rem] text-muted-foreground">
              {t('configs.workbench.batchReviewSubtitle', { count: rows.length, fetch: fetchCount, push: pushCount })}
            </div>
          </div>
          <button
            type="button"
            onClick={onCancel}
            aria-label={t('common.cancel')}
            className="ml-auto flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>

        {/* 主体：左待审文件列 + 右详情（diff / 清单提示） */}
        <div className="flex min-h-0 flex-1">
          {/* 左：待审文件列 */}
          <div className="w-52 shrink-0 overflow-y-auto scrollbar-hide border-r border-border py-1">
            {rows.map((r) => {
              const isFetch = r.direction === 'fetch'
              return (
                <button
                  key={r.id}
                  type="button"
                  onClick={() => setActive(r)}
                  className={cn(
                    'flex w-full items-center gap-1.5 px-3 py-1.5 text-left text-xs transition-colors',
                    r.id === active?.id
                      ? 'bg-primary/10 font-medium text-foreground'
                      : 'text-muted-foreground hover:bg-muted/50',
                  )}
                >
                  {isFetch ? (
                    <ArrowLeft className="h-3 w-3 shrink-0 text-emerald-600 dark:text-emerald-400" />
                  ) : (
                    <ArrowRight className="h-3 w-3 shrink-0 text-sky-600 dark:text-sky-400" />
                  )}
                  <span className="truncate font-mono">{r.name}</span>
                </button>
              )
            })}
          </div>

          {/* 右：详情区 */}
          <div className="flex min-w-0 flex-1 flex-col">
            {active && <ReviewDetail row={active} />}
          </div>
        </div>

        {/* 底部：批量审阅门 + 全部通过 */}
        <div className="flex shrink-0 items-center gap-3 border-t border-border px-4 py-3">
          <label className="flex cursor-pointer select-none items-center gap-1.5 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={reviewed}
              onChange={(e) => setReviewed(e.target.checked)}
              className="h-3 w-3 accent-primary"
            />
            {t('configs.workbench.batchReviewedCheckbox')}
          </label>
          <div className="ml-auto flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={onCancel}>
              {t('common.cancel')}
            </Button>
            <Button size="sm" disabled={!reviewed} onClick={onConfirm}>
              <ClipboardCheck className="mr-1 h-3.5 w-3.5" />
              {t('configs.workbench.batchReviewApprove', { count: rows.length })}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

// 单行详情：拓印（push）→ diff；ingest（fetch）→ 纳管清单提示
function ReviewDetail({ row }: { row: SyncQueueRow }) {
  const { t } = useTranslation()
  if (row.direction === 'push') {
    const base = row.name.split('/').pop() ?? row.name
    const diff = imprintDiffs[base] ?? { expected: '', current: '' }
    return (
      <>
        <div className="flex shrink-0 items-center gap-1.5 border-b border-border bg-muted/20 px-4 py-1.5 text-[0.65rem] font-medium text-muted-foreground">
          <GitCompare className="h-3.5 w-3.5" />
          <span className="flex-1">{t('configs.workbench.imprintExpected')}</span>
          <span className="flex-1">{t('configs.workbench.imprintCurrent')}</span>
        </div>
        <div className="min-h-0 flex-1">
          <CodeEditor original={diff.current} modified={diff.expected} language="yaml" />
        </div>
      </>
    )
  }
  // fetch（ingest）：原型给纳管清单提示（详细勾选仍可单项点开 IngestReviewOverlay）
  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 py-8 text-center">
      <FileSearch className="h-8 w-8 text-muted-foreground/50" />
      <Badge variant="secondary" className="h-4 px-1.5 text-[0.6rem]">
        {t('configs.workbench.statusPendingIngest')}
      </Badge>
      <p className="max-w-xs text-xs text-muted-foreground">{t('configs.workbench.batchReviewIngestHint')}</p>
      <p className="font-mono text-[0.65rem] text-muted-foreground/70">{row.sourcePath}</p>
    </div>
  )
}
