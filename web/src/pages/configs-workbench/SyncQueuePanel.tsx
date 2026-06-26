// 同步队列列表（Xftp 风传输面板内容，供底部 dock 的「同步队列」tab 渲染）：表头对齐 + 等宽列。
// 列：名称(mono) | 方向 | 状态 | 进度(条+%) | 覆盖层·目标 | 源→目标 | 时间。
// 方向：← 抓取(绿) / → 下发(蓝)；状态：已完成(绿) / 进行中 / 待审核(琥珀，可点开审核浮层)。
// 卡片外壳 / 标题 / 批量审核按钮已上移至 BottomDock；本组件仅渲染列头 + 可滚行列表。

import { useTranslation } from 'react-i18next'
import { ArrowLeft, ArrowRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SyncQueueRow } from '@/api/mock/workbench'

// 列宽模板（表头与行共用，保证 Xftp 风对齐）
const COLS = {
  direction: 'w-16',
  status: 'w-24',
  progress: 'w-28',
  scopeTarget: 'w-28',
  path: 'flex-[2]',
  time: 'w-16',
} as const

// 是否待审核状态（抓取待 ingest / 下发待拓印）
export function isPendingReview(s: SyncQueueRow['status']): boolean {
  return s === 'pending-ingest' || s === 'pending-imprint'
}

export function QueueList({
  rows,
  // 点开待审核行 → 上层弹审核浮层（抓取=ingest 清单 / 下发=拓印 diff）
  onReview,
  // 批量审核（改进 4）：选中的待审行 id 集合 + 切换
  selected,
  onToggleSelect,
}: {
  rows: SyncQueueRow[]
  onReview: (row: SyncQueueRow) => void
  selected: Set<string>
  onToggleSelect: (id: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      {/* 列头（与行等宽对齐）；首列复选框占位 */}
      <div className="flex shrink-0 items-center gap-3 border-b border-border bg-muted/20 px-3 py-1 text-[0.65rem] font-medium text-muted-foreground">
        <span className="w-3 shrink-0" />
        <span className="min-w-0 flex-1">{t('configs.workbench.qColName')}</span>
        <span className={cn('shrink-0', COLS.direction)}>{t('configs.workbench.qColDirection')}</span>
        <span className={cn('shrink-0', COLS.status)}>{t('configs.workbench.qColStatus')}</span>
        <span className={cn('shrink-0', COLS.progress)}>{t('configs.workbench.qColProgress')}</span>
        <span className={cn('shrink-0', COLS.scopeTarget)}>{t('configs.workbench.qColScopeTarget')}</span>
        <span className={cn('min-w-0', COLS.path)}>{t('configs.workbench.qColPath')}</span>
        <span className={cn('shrink-0 text-right', COLS.time)}>{t('configs.workbench.qColTime')}</span>
      </div>
      {/* 列表（内部滚） */}
      <div className="min-h-0 flex-1 overflow-y-auto scrollbar-hide">
        {rows.length === 0 ? (
          <div className="px-3 py-4 text-center text-xs text-muted-foreground">{t('configs.workbench.queueEmpty')}</div>
        ) : (
          rows.map((r) => (
            <QueueRow
              key={r.id}
              row={r}
              onReview={onReview}
              selected={selected.has(r.id)}
              onToggleSelect={onToggleSelect}
            />
          ))
        )}
      </div>
    </div>
  )
}

// 队列里仍待审且被选中的数量（dock 头部批量审核按钮据此显隐）
export function countPendingSelected(rows: SyncQueueRow[], selected: Set<string>): number {
  return rows.filter((r) => isPendingReview(r.status) && selected.has(r.id)).length
}

function QueueRow({
  row,
  onReview,
  selected,
  onToggleSelect,
}: {
  row: SyncQueueRow
  onReview: (row: SyncQueueRow) => void
  selected: boolean
  onToggleSelect: (id: string) => void
}) {
  const { t } = useTranslation()
  const isFetch = row.direction === 'fetch'
  const pending = isPendingReview(row.status)
  return (
    <div
      className={cn(
        'flex items-center gap-3 border-b border-border/50 px-3 py-1.5 text-xs last:border-b-0',
        pending && 'cursor-pointer hover:bg-amber-500/5',
      )}
      onClick={pending ? () => onReview(row) : undefined}
      title={pending ? t('configs.workbench.queueReviewHint') : undefined}
    >
      {/* 复选框（仅待审核行可多选，改进 4）；非待审行占位对齐 */}
      {pending ? (
        <input
          type="checkbox"
          checked={selected}
          onClick={(e) => e.stopPropagation()}
          onChange={() => onToggleSelect(row.id)}
          className="h-3 w-3 shrink-0 cursor-pointer accent-primary"
          aria-label={row.name}
        />
      ) : (
        <span className="w-3 shrink-0" />
      )}
      {/* 名称（mono） */}
      <span className="min-w-0 flex-1 truncate font-mono text-foreground">{row.name}</span>
      {/* 方向 */}
      <span
        className={cn(
          'flex shrink-0 items-center gap-1',
          COLS.direction,
          isFetch ? 'text-emerald-600 dark:text-emerald-400' : 'text-sky-600 dark:text-sky-400',
        )}
      >
        {isFetch ? <ArrowLeft className="h-3 w-3" /> : <ArrowRight className="h-3 w-3" />}
        {isFetch ? t('configs.workbench.dirFetch') : t('configs.workbench.dirPush')}
      </span>
      {/* 状态 */}
      <span className={cn('shrink-0', COLS.status)}>
        {row.status === 'done' && (
          <span className="text-emerald-600 dark:text-emerald-400">{t('configs.workbench.statusDone')}</span>
        )}
        {row.status === 'running' && (
          <span className="text-sky-600 dark:text-sky-400">{t('configs.workbench.statusRunning')}</span>
        )}
        {row.status === 'pending-ingest' && (
          <span className="text-amber-600 underline decoration-dotted dark:text-amber-400">
            {t('configs.workbench.statusPendingIngest')}
          </span>
        )}
        {row.status === 'pending-imprint' && (
          <span className="text-amber-600 underline decoration-dotted dark:text-amber-400">
            {t('configs.workbench.statusPendingImprint')}
          </span>
        )}
      </span>
      {/* 进度（条 + %） */}
      <span className={cn('flex shrink-0 items-center gap-2', COLS.progress)}>
        {row.status === 'running' ? (
          <>
            <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
              <span className="block h-full rounded-full bg-sky-500" style={{ width: `${row.progress ?? 0}%` }} />
            </span>
            <span className="w-8 text-right tabular-nums text-muted-foreground">{row.progress ?? 0}%</span>
          </>
        ) : row.status === 'done' ? (
          <>
            <span className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
              <span className="block h-full w-full rounded-full bg-emerald-500" />
            </span>
            <span className="w-8 text-right tabular-nums text-muted-foreground">100%</span>
          </>
        ) : (
          <span className="text-muted-foreground/50">—</span>
        )}
      </span>
      {/* 覆盖层·目标 */}
      <span className={cn('shrink-0 truncate text-muted-foreground', COLS.scopeTarget)}>{row.scopeTarget}</span>
      {/* 源 → 目标 */}
      <span className={cn('flex min-w-0 items-center gap-1 text-muted-foreground/80', COLS.path)}>
        <span className="truncate font-mono text-[0.65rem]">{row.sourcePath}</span>
        <span className="shrink-0 text-muted-foreground/50">→</span>
        <span className="truncate font-mono text-[0.65rem]">{row.targetPath}</span>
      </span>
      {/* 时间 */}
      <span className={cn('shrink-0 text-right tabular-nums text-muted-foreground/70', COLS.time)}>{row.time}</span>
    </div>
  )
}
