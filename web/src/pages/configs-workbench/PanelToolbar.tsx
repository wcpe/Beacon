// 面板工具栏行（Xftp 风）：左侧若干小图标按钮（上级 / 刷新 / 新建 / 搜索）+ 右侧可点逐级面包屑地址栏。
// 纯展示原型：按钮点击经 onAction 上抛触发 toast；面包屑逐级可点（原型仅 toast，不真切目录）。

import { useTranslation } from 'react-i18next'
import { ArrowUp, FilePlus, RefreshCw, Search } from 'lucide-react'
import { cn } from '@/lib/utils'

// 工具栏动作标识：上级 / 刷新 / 新建（仅左面板）/ 搜索
export type ToolbarAction = 'up' | 'refresh' | 'new' | 'search'

export default function PanelToolbar({
  // 面包屑各段（如 ['plugins', 'Essentials']）
  segments,
  // 是否显示「新建」按钮（仅受管侧）
  showNew,
  onAction,
  onCrumb,
}: {
  segments: string[]
  showNew?: boolean
  onAction: (action: ToolbarAction) => void
  // 点击某级面包屑（传该级索引）
  onCrumb: (index: number) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex shrink-0 items-center gap-1 border-b border-border px-2 py-1">
      {/* 左侧图标按钮组 */}
      <IconBtn label={t('configs.workbench.toolbarUp')} onClick={() => onAction('up')}>
        <ArrowUp className="h-3.5 w-3.5" />
      </IconBtn>
      <IconBtn label={t('configs.workbench.toolbarRefresh')} onClick={() => onAction('refresh')}>
        <RefreshCw className="h-3.5 w-3.5" />
      </IconBtn>
      {showNew && (
        <IconBtn label={t('configs.workbench.toolbarNew')} onClick={() => onAction('new')}>
          <FilePlus className="h-3.5 w-3.5" />
        </IconBtn>
      )}
      <IconBtn label={t('configs.workbench.toolbarSearch')} onClick={() => onAction('search')}>
        <Search className="h-3.5 w-3.5" />
      </IconBtn>
      {/* 地址面包屑栏（逐级可点） */}
      <div className="ml-2 flex min-w-0 items-center gap-0.5 overflow-x-auto scrollbar-hide font-mono text-[0.7rem]">
        <span className="text-muted-foreground/60">/</span>
        {segments.map((seg, i) => (
          <span key={i} className="flex items-center gap-0.5">
            {i > 0 && <span className="text-muted-foreground/40">/</span>}
            <button
              type="button"
              onClick={() => onCrumb(i)}
              className={cn(
                'truncate rounded px-1 py-0.5 transition-colors hover:bg-muted/60',
                i === segments.length - 1 ? 'text-foreground' : 'text-muted-foreground',
              )}
            >
              {seg}
            </button>
          </span>
        ))}
      </div>
    </div>
  )
}

// 工具栏小图标按钮
function IconBtn({ label, onClick, children }: { label: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={label}
      aria-label={label}
      className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
    >
      {children}
    </button>
  )
}
