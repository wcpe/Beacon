// 拓印审核「待审核确认」浮层（改进 8，FR-113 语义，仿 FR-46）：
// 左→右下发的队列项点开此浮层展示 diff（期望合并值 ⟷ 服务器现状），单人自审「看过才能确认」，
// 勾「我已审阅」才放行确认按钮，确认才真下发。纯前端 mock，用相对工作台容器的 absolute 覆盖层（非 fixed）。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { GitCompare, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import CodeEditor from '@/components/CodeEditor'
import { imprintDiffs } from './sampleData'

export default function ImprintReviewOverlay({
  // 待审核的队列项名（含路径，如 motd.yml）；用末段匹配 mock diff
  queueName,
  onConfirm,
  onCancel,
}: {
  queueName: string
  onConfirm: () => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  // 显式审阅闸：勾选才放行确认
  const [reviewed, setReviewed] = useState(false)

  // 按文件名末段取 mock diff（取不到给空 diff 占位）
  const fileName = queueName.split('/').pop() ?? queueName
  const diff = useMemo(
    () => imprintDiffs[fileName] ?? { expected: '', current: '' },
    [fileName],
  )
  const differs = diff.expected.trim() !== diff.current.trim()

  return (
    <div className="absolute inset-0 z-50 flex items-center justify-center p-6">
      {/* 半透明遮罩 */}
      <div className="absolute inset-0 bg-background/70 backdrop-blur-sm" onClick={onCancel} aria-hidden />
      {/* 浮层卡 */}
      <div className="relative flex max-h-full w-full max-w-3xl flex-col overflow-hidden rounded-lg border border-border bg-card shadow-2xl">
        {/* 头 */}
        <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-4 py-3">
          <GitCompare className="h-4 w-4 text-muted-foreground" />
          <div className="min-w-0">
            <div className="text-sm font-medium text-foreground">{t('configs.workbench.imprintTitle')}</div>
            <div className="truncate font-mono text-[0.65rem] text-muted-foreground">{queueName}</div>
          </div>
          {differs ? (
            <Badge variant="destructive" className="ml-2 h-4 px-1.5 text-[0.6rem]">
              {t('configs.workbench.imprintDiffers')}
            </Badge>
          ) : (
            <Badge variant="secondary" className="ml-2 h-4 px-1.5 text-[0.6rem]">
              {t('configs.workbench.imprintNoDiff')}
            </Badge>
          )}
          <button
            type="button"
            onClick={onCancel}
            aria-label={t('common.cancel')}
            className="ml-auto flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>

        {/* diff 列头：左期望合并值 / 右服务器现状 */}
        <div className="flex shrink-0 items-center border-b border-border bg-muted/20 text-[0.65rem] font-medium text-muted-foreground">
          <span className="flex-1 px-4 py-1.5">{t('configs.workbench.imprintExpected')}</span>
          <span className="flex-1 px-4 py-1.5">{t('configs.workbench.imprintCurrent')}</span>
        </div>

        {/* diff 主体（Monaco DiffEditor）：original=服务器现状、modified=期望合并值 */}
        <div className="h-72 min-h-0 shrink-0 border-b border-border">
          <CodeEditor original={diff.current} modified={diff.expected} language="yaml" />
        </div>

        {/* 自审说明 */}
        <div className="shrink-0 border-b border-border bg-muted/20 px-4 py-2 text-[0.65rem] text-muted-foreground">
          {t('configs.workbench.imprintReviewHint')}
        </div>

        {/* 底部：审阅闸 + 操作 */}
        <div className="flex shrink-0 items-center gap-3 border-t border-border px-4 py-3">
          <label className="flex cursor-pointer select-none items-center gap-1.5 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={reviewed}
              onChange={(e) => setReviewed(e.target.checked)}
              className="h-3 w-3 accent-primary"
            />
            {t('configs.workbench.imprintReviewedCheckbox')}
          </label>
          <div className="ml-auto flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={onCancel}>
              {t('common.cancel')}
            </Button>
            <Button size="sm" disabled={!reviewed} onClick={onConfirm}>
              {t('configs.workbench.imprintConfirm')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
