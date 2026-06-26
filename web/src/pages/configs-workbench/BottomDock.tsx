// 底部 dock（固定高度内部滚）：tab 切换「同步队列 / 操作日志」。
// 队列：实时同步任务 + 待审核批量审核；日志：大操作留痕 + 逐条/批量撤回。
// 卡片外壳 + tab 头 + 右侧上下文操作（批量审核 / 批量撤回）统一在此，两个列表只渲染内容。

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ClipboardCheck, ListChecks, ScrollText, Undo2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { OpLogEntry, SyncQueueRow } from '@/api/mock/workbench'
import { QueueList, countPendingSelected } from './SyncQueuePanel'
import { OperationLogList, countUndoableSelected } from './OperationLogPanel'

type DockTab = 'queue' | 'log'

export default function BottomDock({
  // 同步队列
  queueRows,
  onReview,
  queueSel,
  onToggleQueueSel,
  onBatchReview,
  // 操作日志
  logEntries,
  logSel,
  onToggleLogSel,
  onUndo,
  onBatchUndo,
}: {
  queueRows: SyncQueueRow[]
  onReview: (row: SyncQueueRow) => void
  queueSel: Set<string>
  onToggleQueueSel: (id: string) => void
  onBatchReview: () => void
  logEntries: OpLogEntry[]
  logSel: Set<string>
  onToggleLogSel: (id: string) => void
  onUndo: (id: string) => void
  onBatchUndo: () => void
}) {
  const { t } = useTranslation()
  const [tab, setTab] = useState<DockTab>('queue')
  const pendingSelected = countPendingSelected(queueRows, queueSel)
  const undoableSelected = countUndoableSelected(logEntries, logSel)

  return (
    // 固定高度：h-48，内部滚，不撑高页面
    <div className="flex h-48 shrink-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
      {/* 头部：tab + 上下文操作 */}
      <div className="flex shrink-0 items-center gap-1 border-b border-border bg-muted/30 px-2 py-1.5">
        <TabBtn active={tab === 'queue'} onClick={() => setTab('queue')} icon={<ListChecks className="h-3.5 w-3.5" />}>
          {t('configs.workbench.queueTitle')}
        </TabBtn>
        <TabBtn active={tab === 'log'} onClick={() => setTab('log')} icon={<ScrollText className="h-3.5 w-3.5" />}>
          {t('configs.workbench.logTitle')}
        </TabBtn>
        {tab === 'queue' && (
          <>
            <span className="ml-1 inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-500" />
            <span className="text-[0.65rem] text-muted-foreground">{t('configs.workbench.queueLive')}</span>
          </>
        )}
        {/* 右侧上下文操作 */}
        {tab === 'queue' && pendingSelected > 0 && (
          <Button size="xs" className="ml-auto h-6 text-[0.65rem]" onClick={onBatchReview}>
            <ClipboardCheck className="mr-1 h-3 w-3" />
            {t('configs.workbench.queueBatchReview', { count: pendingSelected })}
          </Button>
        )}
        {tab === 'log' && undoableSelected > 0 && (
          <Button variant="outline" size="xs" className="ml-auto h-6 text-[0.65rem]" onClick={onBatchUndo}>
            <Undo2 className="mr-1 h-3 w-3" />
            {t('configs.workbench.logBatchUndo', { count: undoableSelected })}
          </Button>
        )}
        <span
          className={cn(
            'text-[0.65rem] text-muted-foreground',
            (tab === 'queue' && pendingSelected > 0) || (tab === 'log' && undoableSelected > 0) ? 'ml-3' : 'ml-auto',
          )}
        >
          {tab === 'queue'
            ? t('configs.workbench.queueCount', { count: queueRows.length })
            : t('configs.workbench.logCount', { count: logEntries.length })}
        </span>
      </div>
      {/* 内容：当前 tab 列表 */}
      {tab === 'queue' ? (
        <QueueList rows={queueRows} onReview={onReview} selected={queueSel} onToggleSelect={onToggleQueueSel} />
      ) : (
        <OperationLogList entries={logEntries} selected={logSel} onToggleSelect={onToggleLogSel} onUndo={onUndo} />
      )}
    </div>
  )
}

function TabBtn({
  active,
  onClick,
  icon,
  children,
}: {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium transition-colors',
        active ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {icon}
      {children}
    </button>
  )
}
