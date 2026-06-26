// 生效预览视图（改进 8，仿 IDEA 并排 diff）：受管面板「生效预览」模式下，
// 以「左=全局基线 / 右=本实例最终生效」的并排 diff 呈现覆盖/定制——
// 被覆盖的键左侧红底划除、右侧绿底高亮（带生效覆盖层徽标），未变的键作上下文灰行；
// 每文件给「N 处定制」计数、顶部给「本实例共 X 处覆盖，涉及 Y/Z 文件」总览，便于一眼判断覆盖面。
// 内嵌于受管面板体内（非浮层），随面板固定高度内部滚。纯前端 mock。

import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import { SCOPE_META } from './diffMeta'
import { useEffectivePreview } from './useWorkbenchData'
import type { EffectiveFile } from './types'

// 单文件统计：定制键数 / 总键数
function fileStats(f: EffectiveFile): { custom: number; total: number } {
  const custom = f.keys.filter((k) => k.chain.length > 1).length
  return { custom, total: f.keys.length }
}

export default function EffectivePreviewView({ serverId }: { serverId: string }) {
  const { t } = useTranslation()
  const eff = useEffectivePreview(serverId)
  const files = useMemo(() => eff.data ?? [], [eff.data])

  // 总览统计：总定制键数 + 含定制的文件数
  const summary = useMemo(() => {
    let custom = 0
    let touched = 0
    for (const f of files) {
      const s = fileStats(f)
      custom += s.custom
      if (s.custom > 0) touched += 1
    }
    return { custom, touched, total: files.length }
  }, [files])

  if (eff.isLoading) {
    return (
      <div className="space-y-2 px-3 py-2">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-4 w-full" />
        ))}
      </div>
    )
  }

  if (files.length === 0) {
    return (
      <div className="px-3 py-6 text-center text-xs text-muted-foreground">
        {t('configs.workbench.effectiveEmpty')}
      </div>
    )
  }

  return (
    <div className="py-1">
      {/* 总览条：本实例覆盖面一眼判断 */}
      <div className="mx-2 mb-1 flex items-center gap-2 rounded-md border border-border bg-muted/30 px-2.5 py-1.5 text-[0.7rem]">
        <span className="font-medium text-foreground">{t('configs.workbench.effectiveTargetLabelShort', { server: serverId })}</span>
        <Badge variant="outline" className="h-4 border-primary/40 px-1 text-[0.6rem] text-primary">
          {t('configs.workbench.effectiveDiffSummary', { count: summary.custom, files: summary.touched, total: summary.total })}
        </Badge>
        <span className="ml-auto text-[0.6rem] text-muted-foreground/70">{t('configs.workbench.effectiveChainLegend')}</span>
      </div>

      {files.map((f) => {
        const s = fileStats(f)
        return (
          <div key={f.name} className="mb-2">
            {/* 文件名 + 定制计数 */}
            <div className="flex items-center gap-2 px-2 py-1 text-xs font-medium text-foreground">
              <span className="truncate">{f.name}</span>
              {s.custom > 0 ? (
                <Badge variant="outline" className="h-4 shrink-0 border-amber-500/40 px-1 text-[0.55rem] text-amber-600 dark:text-amber-400">
                  {t('configs.workbench.effectiveDiffFileCustom', { count: s.custom })}
                </Badge>
              ) : (
                <Badge variant="outline" className="h-4 shrink-0 border-muted-foreground/30 px-1 text-[0.55rem] text-muted-foreground/70">
                  {t('configs.workbench.effectiveDiffNoCustom')}
                </Badge>
              )}
              <span className="shrink-0 text-[0.55rem] text-muted-foreground/60">{t('configs.workbench.effectiveDiffFileKeys', { count: s.total })}</span>
            </div>

            {/* 并排 diff：左 全局基线 / 右 本实例生效 */}
            <div className="mx-2 overflow-hidden rounded-md border border-border">
              {/* 两栏列头 */}
              <div className="grid grid-cols-2 border-b border-border bg-muted/30 text-[0.6rem] font-medium text-muted-foreground">
                <div className="border-r border-border px-2 py-0.5">{t('configs.workbench.effectiveDiffBaseCol')}</div>
                <div className="px-2 py-0.5">{t('configs.workbench.effectiveDiffEffCol')}</div>
              </div>
              {/* 逐键行 */}
              {f.keys.map((k, idx) => {
                const baseValue = k.chain[0]?.value ?? ''
                const effLayer = k.chain[k.chain.length - 1]
                const changed = k.chain.length > 1
                const meta = SCOPE_META[effLayer.scope]
                return (
                  <div
                    key={k.key}
                    className={cn(
                      'grid grid-cols-2 border-b border-border/40 font-mono text-[0.65rem] last:border-b-0',
                    )}
                  >
                    {/* 左：全局基线（被覆盖则红底划除） */}
                    <div
                      className={cn(
                        'flex items-center gap-1 border-r border-border/40 px-2 py-0.5',
                        changed ? 'bg-red-500/10' : 'bg-transparent',
                      )}
                    >
                      <span className="w-5 shrink-0 select-none text-right text-muted-foreground/40">{idx + 1}</span>
                      <span className="min-w-0 flex-1 truncate">
                        <span className="text-muted-foreground/70">{k.key}: </span>
                        <span className={cn(changed ? 'text-red-600 line-through dark:text-red-400' : 'text-muted-foreground')}>
                          {baseValue}
                        </span>
                      </span>
                    </div>
                    {/* 右：本实例生效（被覆盖则绿底高亮 + 生效覆盖层徽标） */}
                    <div
                      className={cn(
                        'flex items-center gap-1 px-2 py-0.5',
                        changed ? 'bg-emerald-500/10' : 'bg-transparent',
                      )}
                    >
                      <span className="w-5 shrink-0 select-none text-right text-muted-foreground/40">{idx + 1}</span>
                      <span className="min-w-0 flex-1 truncate">
                        <span className="text-muted-foreground/70">{k.key}: </span>
                        <span className={cn(changed ? 'font-medium text-emerald-700 dark:text-emerald-400' : 'text-muted-foreground')}>
                          {effLayer.value}
                        </span>
                      </span>
                      {changed && (
                        <Badge variant="outline" className={cn('h-4 shrink-0 px-1 text-[0.5rem]', meta.badgeClass)}>
                          {t(meta.labelKey)}
                        </Badge>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          </div>
        )
      })}
    </div>
  )
}
