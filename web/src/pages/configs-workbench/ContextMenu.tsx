// 文件 / 文件夹自定义右键菜单（改进 5）：onContextMenu preventDefault 后在光标处弹出。
// 菜单项：编辑 / 重命名 / 删除 / 新建 / 抓取 or 下发 / 查看差异。点空白处或 ESC 关闭。
// 用相对工作台 relative 容器的 absolute 定位（坐标由父组件换算为容器内坐标），不用 position:fixed。

import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye, FilePlus, History, Pencil, Send, Trash2, Type as TypeIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

// 右键菜单动作标识
export type ContextAction = 'edit' | 'rename' | 'delete' | 'new' | 'transfer' | 'diff' | 'rollback'

export interface ContextMenuState {
  // 容器内坐标（相对工作台 relative 容器）
  x: number
  y: number
  // 来源面板：决定「抓取」(server) 还是「下发」(managed)
  side: 'managed' | 'server'
  // 目标节点名 + 是否文件夹
  name: string
  isFolder: boolean
}

export default function ContextMenu({
  state,
  onAction,
  onClose,
}: {
  state: ContextMenuState
  onAction: (action: ContextAction) => void
  onClose: () => void
}) {
  const { t } = useTranslation()

  // ESC 关闭
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  // 抓取（server→managed）/ 下发（managed→server）方向语义
  const transferKey = state.side === 'server' ? 'configs.workbench.ctxFetch' : 'configs.workbench.ctxPush'

  return (
    <>
      {/* 透明遮罩：点击空白处关闭（容器内 absolute 覆盖） */}
      <div className="absolute inset-0 z-40" onClick={onClose} onContextMenu={(e) => { e.preventDefault(); onClose() }} aria-hidden />
      <div
        className="absolute z-50 w-44 overflow-hidden rounded-lg border border-border bg-popover py-1 shadow-xl"
        style={{ left: state.x, top: state.y }}
        role="menu"
      >
        <div className="truncate border-b border-border px-3 py-1.5 font-mono text-[0.65rem] text-muted-foreground">
          {state.name}
        </div>
        {!state.isFolder && (
          <Item icon={<Pencil className="h-3.5 w-3.5" />} label={t('configs.workbench.ctxEdit')} onClick={() => onAction('edit')} />
        )}
        <Item icon={<TypeIcon className="h-3.5 w-3.5" />} label={t('configs.workbench.ctxRename')} onClick={() => onAction('rename')} />
        <Item icon={<FilePlus className="h-3.5 w-3.5" />} label={t('configs.workbench.ctxNew')} onClick={() => onAction('new')} />
        {!state.isFolder && (
          <Item icon={<Send className="h-3.5 w-3.5" />} label={t(transferKey)} onClick={() => onAction('transfer')} />
        )}
        {!state.isFolder && (
          <Item icon={<Eye className="h-3.5 w-3.5" />} label={t('configs.workbench.ctxDiff')} onClick={() => onAction('diff')} />
        )}
        {/* 回滚到历史版本（仅受管文件，复用编辑器 revisions） */}
        {!state.isFolder && state.side === 'managed' && (
          <Item icon={<History className="h-3.5 w-3.5" />} label={t('configs.workbench.ctxRollback')} onClick={() => onAction('rollback')} />
        )}
        <div className="my-1 h-px bg-border" />
        <Item
          icon={<Trash2 className="h-3.5 w-3.5" />}
          label={t('configs.workbench.ctxDelete')}
          onClick={() => onAction('delete')}
          danger
        />
      </div>
    </>
  )
}

function Item({
  icon,
  label,
  onClick,
  danger,
}: {
  icon: React.ReactNode
  label: string
  onClick: () => void
  danger?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      role="menuitem"
      className={cn(
        'flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors',
        danger
          ? 'text-destructive hover:bg-destructive/10'
          : 'text-foreground hover:bg-muted/60',
      )}
    >
      {icon}
      {label}
    </button>
  )
}
