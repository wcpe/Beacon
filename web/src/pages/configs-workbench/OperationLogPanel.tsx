// 操作日志列表（供底部 dock 的「操作日志」tab 渲染）：每次大操作留一条带上下文的记录，
// 支持逐条撤回（已撤回置灰）+ 多选批量撤回（复选框，仅未撤回项可选）。
// 列：操作(badge) | 文件(mono) | 覆盖层·目标 | 详情 | 操作人 | 时间 | 撤回。
// 卡片外壳 / 标题 / 批量撤回按钮在 BottomDock；本组件仅列头 + 可滚行列表。

import { useTranslation } from 'react-i18next'
import { Undo2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { OpAction, OpLogEntry } from './types'

// 列宽模板（表头与行共用）
const COLS = {
  action: 'w-16',
  target: 'w-28',
  detail: 'flex-[2]',
  operator: 'w-16',
  time: 'w-16',
  undo: 'w-16',
} as const

// 操作类型 → 文案键 + 着色
const OP_META: Record<OpAction, { labelKey: string; cls: string }> = {
  fetch: { labelKey: 'configs.workbench.dirFetch', cls: 'border-emerald-500/40 text-emerald-600 dark:text-emerald-400' },
  push: { labelKey: 'configs.workbench.dirPush', cls: 'border-sky-500/40 text-sky-600 dark:text-sky-400' },
  publish: { labelKey: 'configs.workbench.opPublish', cls: 'border-primary/40 text-primary' },
  delete: { labelKey: 'configs.workbench.opDelete', cls: 'border-destructive/40 text-destructive' },
  rename: { labelKey: 'configs.workbench.opRename', cls: 'border-amber-500/40 text-amber-600 dark:text-amber-400' },
  new: { labelKey: 'configs.workbench.opNew', cls: 'border-border text-foreground' },
  move: { labelKey: 'configs.workbench.opMove', cls: 'border-amber-500/40 text-amber-600 dark:text-amber-400' },
}

export function OperationLogList({
  entries,
  selected,
  onToggleSelect,
  onUndo,
}: {
  entries: OpLogEntry[]
  selected: Set<string>
  onToggleSelect: (id: string) => void
  onUndo: (id: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      {/* 列头 */}
      <div className="flex shrink-0 items-center gap-3 border-b border-border bg-muted/20 px-3 py-1 text-[0.65rem] font-medium text-muted-foreground">
        <span className="w-3 shrink-0" />
        <span className={cn('shrink-0', COLS.action)}>{t('configs.workbench.logColAction')}</span>
        <span className="min-w-0 flex-1">{t('configs.workbench.logColFiles')}</span>
        <span className={cn('shrink-0', COLS.target)}>{t('configs.workbench.logColTarget')}</span>
        <span className={cn('min-w-0', COLS.detail)}>{t('configs.workbench.logColDetail')}</span>
        <span className={cn('shrink-0', COLS.operator)}>{t('configs.workbench.logColOperator')}</span>
        <span className={cn('shrink-0 text-right', COLS.time)}>{t('configs.workbench.logColTime')}</span>
        <span className={cn('shrink-0 text-right', COLS.undo)} />
      </div>
      {/* 列表（内部滚） */}
      <div className="min-h-0 flex-1 overflow-y-auto scrollbar-hide">
        {entries.length === 0 ? (
          <div className="px-3 py-4 text-center text-xs text-muted-foreground">{t('configs.workbench.logEmpty')}</div>
        ) : (
          entries.map((e) => (
            <LogRow key={e.id} entry={e} selected={selected.has(e.id)} onToggleSelect={onToggleSelect} onUndo={onUndo} />
          ))
        )}
      </div>
    </div>
  )
}

function LogRow({
  entry,
  selected,
  onToggleSelect,
  onUndo,
}: {
  entry: OpLogEntry
  selected: boolean
  onToggleSelect: (id: string) => void
  onUndo: (id: string) => void
}) {
  const { t } = useTranslation()
  const meta = OP_META[entry.action]
  return (
    <div
      className={cn(
        'flex items-center gap-3 border-b border-border/50 px-3 py-1.5 text-xs last:border-b-0',
        entry.undone && 'opacity-50',
      )}
    >
      {/* 复选框（仅未撤回项可批量撤回） */}
      {entry.undone ? (
        <span className="w-3 shrink-0" />
      ) : (
        <input
          type="checkbox"
          checked={selected}
          onChange={() => onToggleSelect(entry.id)}
          className="h-3 w-3 shrink-0 cursor-pointer accent-primary"
          aria-label={entry.detail}
        />
      )}
      {/* 操作 badge */}
      <span className={cn('shrink-0', COLS.action)}>
        <Badge variant="outline" className={cn('h-4 px-1 text-[0.6rem]', meta.cls)}>
          {t(meta.labelKey)}
        </Badge>
      </span>
      {/* 文件（mono，多文件以、连接） */}
      <span className="min-w-0 flex-1 truncate font-mono text-foreground" title={entry.files.join('、')}>
        {entry.files.join('、')}
      </span>
      {/* 覆盖层·目标 */}
      <span className={cn('shrink-0 truncate text-muted-foreground', COLS.target)}>{entry.target}</span>
      {/* 详情 */}
      <span className={cn('min-w-0 truncate text-muted-foreground/80', COLS.detail)} title={entry.detail}>
        {entry.detail}
      </span>
      {/* 操作人 */}
      <span className={cn('shrink-0 truncate text-muted-foreground', COLS.operator)}>{entry.operator}</span>
      {/* 时间 */}
      <span className={cn('shrink-0 text-right tabular-nums text-muted-foreground/70', COLS.time)}>{entry.time}</span>
      {/* 撤回 / 已撤回 */}
      <span className={cn('flex shrink-0 justify-end', COLS.undo)}>
        {entry.undone ? (
          <span className="text-[0.65rem] text-muted-foreground/60">{t('configs.workbench.logUndone')}</span>
        ) : (
          <Button
            variant="ghost"
            size="xs"
            className="h-6 px-1.5 text-[0.65rem] text-muted-foreground hover:text-foreground"
            onClick={() => onUndo(entry.id)}
          >
            <Undo2 className="mr-1 h-3 w-3" />
            {t('configs.workbench.logUndo')}
          </Button>
        )}
      </span>
    </div>
  )
}

// 选中且未撤回（可批量撤回）的条数
export function countUndoableSelected(entries: OpLogEntry[], selected: Set<string>): number {
  return entries.filter((e) => selected.has(e.id) && !e.undone).length
}
