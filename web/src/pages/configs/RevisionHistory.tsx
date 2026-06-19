// 底部历史修订面板：可折叠，点击条目跳转到对应版本的 Diff 视图。

import { ChevronDown, ChevronRight } from 'lucide-react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { cn } from '@/lib/utils'
import { formatTime } from '../../api/format'
import type { RevisionView } from '../../api/types'

export default function RevisionHistory({
  revisions,
  collapsed,
  highlightRev,
  onToggleCollapse,
  onSelectRevision,
}: {
  revisions: RevisionView[]
  collapsed: boolean
  highlightRev?: number
  onToggleCollapse: () => void
  onSelectRevision: (rev: RevisionView) => void
}) {
  if (revisions.length === 0) return null
  return (
    <div className="flex-shrink-0 border-t border-border">
      <button
        type="button"
        className="flex w-full items-center justify-between px-3 py-1.5 text-xs font-medium text-muted-foreground hover:bg-muted/30 transition-colors"
        onClick={onToggleCollapse}
      >
        <span className="flex items-center gap-1">
          {collapsed ? <ChevronRight className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
          历史修订（共 {revisions.length} 条，点击条目查看 Diff）
        </span>
      </button>
      {!collapsed && (
        <ScrollArea className="h-[140px]">
          <div className="divide-y divide-border">
            {revisions.map((rev) => (
              <div
                key={rev.version}
                className={cn(
                  'flex items-center gap-3 px-3 py-1.5 cursor-pointer transition-colors text-xs',
                  highlightRev === rev.version ? 'bg-accent/50' : 'hover:bg-muted/30',
                )}
                onClick={() => onSelectRevision(rev)}
              >
                <span className="w-10 shrink-0 font-medium text-foreground">v{rev.version}</span>
                <span className="w-20 shrink-0 text-muted-foreground">{formatTime(rev.createdAt)}</span>
                <span className="w-20 shrink-0 text-foreground">{rev.operator}</span>
                <span className="flex-1 truncate text-muted-foreground">{rev.comment || '—'}</span>
                <span className="font-mono text-muted-foreground">{rev.md5.slice(0, 8)}</span>
                {rev.sourceRevision != null && (
                  <span className="text-xs text-blue-500">← v{rev.sourceRevision}</span>
                )}
              </div>
            ))}
          </div>
        </ScrollArea>
      )}
    </div>
  )
}
