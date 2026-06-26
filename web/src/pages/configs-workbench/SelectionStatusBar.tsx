// 顶部固定选中状态栏（常驻、固定高度）：展示当前选中态 + 选中驱动操作（发布 / 抓取选中）+ 撤回上一步。
// 取代原先「选中即插入的动态动作条」——动态条会把双面板挤下去、导致选错文件；本状态栏常驻不改布局。
// 未选中时显操作提示（勾选 / 拖拽 / 右键用法），让面板上方始终有一条固定高度的提示/操作带。

import { useTranslation } from 'react-i18next'
import { ArrowLeft, MousePointerClick, Rocket, Undo2, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export default function SelectionStatusBar({
  // 选中方向：受管=下发/发布，服务器=抓取，null=未选中
  selSide,
  count,
  // 受管发布提示 / 服务器抓取目标覆盖层
  fetchScopeLabel,
  onPublish,
  onFetch,
  onClear,
  // 撤回上一步（最近一条未撤回的操作）
  canUndoLast,
  onUndoLast,
}: {
  selSide: 'managed' | 'server' | null
  count: number
  fetchScopeLabel: string
  onPublish: () => void
  onFetch: () => void
  onClear: () => void
  canUndoLast: boolean
  onUndoLast: () => void
}) {
  const { t } = useTranslation()
  return (
    <div
      className={cn(
        'flex h-9 shrink-0 items-center gap-3 rounded-lg border px-3 text-xs transition-colors',
        selSide ? 'border-primary/40 bg-primary/5' : 'border-border bg-card',
      )}
    >
      {selSide === 'server' ? (
        <>
          <ArrowLeft className="h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
          <span className="font-medium text-foreground">{t('configs.workbench.barFetch', { count })}</span>
          <span className="text-muted-foreground">{t('configs.workbench.barFetchTarget', { scope: fetchScopeLabel })}</span>
        </>
      ) : selSide === 'managed' ? (
        <>
          <Rocket className="h-4 w-4 shrink-0 text-primary" />
          <span className="font-medium text-foreground">{t('configs.workbench.barPublish', { count })}</span>
          <span className="text-muted-foreground">{t('configs.workbench.barPublishHint')}</span>
        </>
      ) : (
        <>
          <MousePointerClick className="h-4 w-4 shrink-0 text-muted-foreground/60" />
          <span className="min-w-0 truncate text-muted-foreground">{t('configs.workbench.legendHint')}</span>
        </>
      )}

      {/* 右侧：选中驱动操作 + 撤回上一步 */}
      <div className="ml-auto flex shrink-0 items-center gap-2">
        {selSide === 'server' && (
          <Button size="xs" className="h-6 text-[0.65rem]" onClick={onFetch}>
            {t('configs.workbench.barFetchBtn', { count })}
          </Button>
        )}
        {selSide === 'managed' && (
          <Button size="xs" className="h-6 text-[0.65rem]" onClick={onPublish}>
            {t('configs.workbench.barPublishBtn', { count })}
          </Button>
        )}
        {selSide && (
          <button
            type="button"
            onClick={onClear}
            aria-label={t('common.cancel')}
            className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
        {/* 撤回上一步：常驻可见，无可撤回操作时禁用 */}
        <Button
          variant="ghost"
          size="xs"
          className="h-6 text-[0.65rem] text-muted-foreground hover:text-foreground"
          disabled={!canUndoLast}
          onClick={onUndoLast}
        >
          <Undo2 className="mr-1 h-3 w-3" />
          {t('configs.workbench.barUndoLast')}
        </Button>
      </div>
    </div>
  )
}
