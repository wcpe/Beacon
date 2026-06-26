// 反向抓取「待审核 ingest」浮层（改进 8，FR-113 语义，仿 FR-58~60）：
// 右→左抓取确认后队列项进入「待审核」，点开此浮层展示扫描清单全量 + 勾选要纳管的 + 忽略规则，
// 确认 ingest 后队列项转完成。纯前端 mock，用相对工作台容器的 absolute 覆盖层（非 fixed）。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, FileSearch, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import { useIngestScanList } from './useWorkbenchData'

export default function IngestReviewOverlay({
  // 待审核的队列项名（标题展示）
  queueName,
  // 关联的反向抓取受管任务 id（FR-58~60）：取其扫描清单；原型期无任务时为空态
  taskId,
  onConfirm,
  onCancel,
}: {
  queueName: string
  taskId?: number
  // 确认 ingest（传纳管文件数）
  onConfirm: (count: number) => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const scan = useIngestScanList(taskId)
  const items = scan.data?.items ?? []
  const ignoreRules = scan.data?.ignoreRules ?? []

  // 勾选纳管集合：扫描加载完成后按 defaultPick 初始化
  const [picked, setPicked] = useState<Set<string>>(new Set())
  useEffect(() => {
    if (scan.data) setPicked(new Set(items.filter((i) => i.defaultPick).map((i) => i.path)))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scan.data])

  const pickedCount = picked.size
  const total = items.length
  const allPicked = useMemo(() => total > 0 && pickedCount === total, [total, pickedCount])

  function toggle(path: string) {
    setPicked((prev) => {
      const n = new Set(prev)
      if (n.has(path)) n.delete(path)
      else n.add(path)
      return n
    })
  }

  function toggleAll() {
    setPicked(allPicked ? new Set() : new Set(items.map((i) => i.path)))
  }

  return (
    <div className="absolute inset-0 z-50 flex items-center justify-center p-6">
      {/* 半透明遮罩 */}
      <div className="absolute inset-0 bg-background/70 backdrop-blur-sm" onClick={onCancel} aria-hidden />
      {/* 浮层卡 */}
      <div className="relative flex max-h-full w-full max-w-2xl flex-col overflow-hidden rounded-lg border border-border bg-card shadow-2xl">
        {/* 头 */}
        <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-4 py-3">
          <FileSearch className="h-4 w-4 text-muted-foreground" />
          <div className="min-w-0">
            <div className="text-sm font-medium text-foreground">{t('configs.workbench.ingestTitle')}</div>
            <div className="truncate font-mono text-[0.65rem] text-muted-foreground">{queueName}</div>
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

        {/* 工具行：全选 + 计数 */}
        <div className="flex shrink-0 items-center gap-3 border-b border-border px-4 py-2 text-xs">
          <label className="flex cursor-pointer items-center gap-1.5 text-muted-foreground">
            <input type="checkbox" checked={allPicked} onChange={toggleAll} className="h-3 w-3 accent-primary" />
            {t('configs.workbench.ingestSelectAll')}
          </label>
          <span className="ml-auto text-muted-foreground">
            {t('configs.workbench.ingestPickedCount', { picked: pickedCount, total })}
          </span>
        </div>

        {/* 扫描清单 */}
        <div className="min-h-0 flex-1 overflow-y-auto scrollbar-hide">
          {scan.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-5 w-full" />
              ))}
            </div>
          ) : (
            items.map((it) => {
              const isPicked = picked.has(it.path)
              return (
                <label
                  key={it.path}
                  className={cn(
                    'flex cursor-pointer items-center gap-2 border-b border-border/50 px-4 py-1.5 text-xs transition-colors last:border-b-0 hover:bg-muted/40',
                    it.ignored && 'opacity-70',
                  )}
                >
                  <input
                    type="checkbox"
                    checked={isPicked}
                    onChange={() => toggle(it.path)}
                    className="h-3 w-3 shrink-0 accent-primary"
                  />
                  <span className="min-w-0 flex-1 truncate font-mono text-foreground">{it.path}</span>
                  {it.ignored && (
                    <Badge variant="outline" className="h-4 shrink-0 border-muted-foreground/40 px-1 text-[0.55rem] text-muted-foreground">
                      {t('configs.workbench.ingestIgnored')}
                    </Badge>
                  )}
                  <span className="w-16 shrink-0 text-right tabular-nums text-muted-foreground">{it.size}</span>
                </label>
              )
            })
          )}
        </div>

        {/* 忽略规则说明 */}
        <div className="shrink-0 border-t border-border bg-muted/20 px-4 py-2 text-[0.65rem] text-muted-foreground">
          <span className="font-medium">{t('configs.workbench.ingestIgnoreRules')}：</span>
          <span className="font-mono">{ignoreRules.join('  ·  ')}</span>
        </div>

        {/* 底部操作 */}
        <div className="flex shrink-0 items-center justify-end gap-2 border-t border-border px-4 py-3">
          <Button variant="outline" size="sm" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
          <Button size="sm" disabled={pickedCount === 0} onClick={() => onConfirm(pickedCount)}>
            <CheckCircle2 className="mr-1 h-3.5 w-3.5" />
            {t('configs.workbench.ingestConfirm', { count: pickedCount })}
          </Button>
        </div>
      </div>
    </div>
  )
}
