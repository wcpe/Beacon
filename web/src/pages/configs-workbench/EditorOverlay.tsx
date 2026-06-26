/**
 * 悬浮覆盖编辑器（改进 3，参考 1Panel 在线文本编辑器）——纯前端 + mock。
 *
 * 双击工作台文件 → 编辑器浮层覆盖在工作台主区之上（非整页跳转）。
 * 浮层用「正常流内的 faux-viewport」实现：相对工作台根 relative 容器的 absolute 覆盖层 + min-height 撑住，
 * 不用 position:fixed（避免在某些容器塌缩）。
 *
 * 形态：默认覆盖工作台主区、四周留边见到下层；可最大化铺满（铺满工作台容器）、可最小化（缩为底部一条），可关闭。
 * 结构：顶部窗框（标题 + 最小化/最大化/关闭）→ 工具栏（保存/全部保存/刷新/搜索/替换/跳转行/字体/主题/设置）
 *      → 多标签栏 → 主体（Monaco + 右侧可折叠历史修订）→ 底部状态栏。
 */

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ChevronLeft,
  FileText,
  History,
  Maximize2,
  Minimize2,
  RefreshCw,
  Replace,
  Save,
  SaveAll,
  Search,
  Settings,
  SunMoon,
  Type as TypeIcon,
  X,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { useMessage } from '@/components/useMessage'
import { cn } from '@/lib/utils'
import CodeEditor from '@/components/CodeEditor'

import { useWorkbenchFile } from './useWorkbenchData'
import { SCOPE_META } from './diffMeta'
import type { WorkbenchFile } from '@/api/mock/workbench'

// 已打开标签（原型：仅记录 key + 文件名；内容按活跃标签拉取）
export interface EditorTab {
  key: string
  name: string
}

// 浮层窗体形态：常态(覆盖工作台主区留边) / 最大化(铺满) / 最小化(底部一条)
type WindowState = 'normal' | 'maximized' | 'minimized'

export default function EditorOverlay({
  tabs,
  activeKey,
  onActivate,
  onClose,
  onCloseTab,
  // 最大化态把活跃文件 id 同步进 URL（刷新可恢复深链）；常态不改 URL
  onSyncUrl,
}: {
  tabs: EditorTab[]
  activeKey: string
  onActivate: (key: string) => void
  // 关闭整个浮层
  onClose: () => void
  // 关闭某标签（活跃标签关闭后跳邻；最后一个关闭则关浮层，由上层处理）
  onCloseTab: (key: string) => void
  onSyncUrl: (key: string | null) => void
}) {
  const { t } = useTranslation()
  const msg = useMessage()

  const [win, setWin] = useState<WindowState>('normal')
  const [historyCollapsed, setHistoryCollapsed] = useState(false)
  const [content, setContent] = useState('')
  // 脏标记集合：保存后清除当前标签脏态（原型示意 dirty）
  const [dirtyKeys, setDirtyKeys] = useState<Set<string>>(new Set())
  // 光标位置（状态栏展示，原型固定起点，编辑器变更不实时联动光标坐标）
  const [cursor] = useState({ line: 1, col: 1 })

  const file = useWorkbenchFile(activeKey)

  // 活跃文件加载完成 → 回灌编辑器内容
  useEffect(() => {
    if (file.data) setContent(file.data.content)
  }, [file.data])

  // 最大化态同步 URL；离开最大化态清掉深链
  useEffect(() => {
    onSyncUrl(win === 'maximized' ? activeKey : null)
  }, [win, activeKey, onSyncUrl])

  // Ctrl/⌘+S：标记当前标签已保存（清脏）+ toast
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault()
        doSave()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeKey])

  const isDirty = dirtyKeys.has(activeKey)

  function markDirty(next: string) {
    setContent(next)
    setDirtyKeys((prev) => {
      if (prev.has(activeKey)) return prev
      const n = new Set(prev)
      n.add(activeKey)
      return n
    })
  }

  function doSave() {
    setDirtyKeys((prev) => {
      const n = new Set(prev)
      n.delete(activeKey)
      return n
    })
    msg.showSuccess(t('configs.workbench.toastSaved'))
  }

  function doSaveAll() {
    setDirtyKeys(new Set())
    msg.showSuccess(t('configs.workbench.tbActionHint', { action: t('configs.workbench.tbSaveAll') }))
  }

  // 工具栏次要动作：原型仅 toast
  const tbHint = (action: string) => msg.showSuccess(t('configs.workbench.tbActionHint', { action }))

  // 最小化态：仅底部一条，点击恢复常态
  if (win === 'minimized') {
    return (
      <button
        type="button"
        onClick={() => setWin('normal')}
        className="absolute inset-x-0 bottom-0 z-30 flex items-center gap-2 border-t border-border bg-card px-3 py-2 text-left text-xs shadow-lg transition-colors hover:bg-muted/40"
      >
        <FileText className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="font-medium text-foreground">{t('configs.workbench.overlayRestoreBar')}</span>
        <span className="ml-2 truncate font-mono text-muted-foreground">{activeName(tabs, activeKey)}</span>
        <Maximize2 className="ml-auto h-3.5 w-3.5 text-muted-foreground" />
      </button>
    )
  }

  return (
    <div
      className={cn(
        'absolute z-30 flex flex-col overflow-hidden rounded-lg border border-border bg-card shadow-2xl',
        // 常态：四周留边见到下层工作台；最大化：铺满工作台容器
        win === 'maximized' ? 'inset-0' : 'inset-3',
      )}
      role="dialog"
      aria-modal="false"
    >
      {/* ===== 顶部窗框：标题（面包屑）+ 窗口控制 ===== */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-3 py-1.5">
        <Breadcrumb file={file.data} onBack={onClose} />
        <div className="ml-auto flex items-center gap-0.5">
          <WinBtn label={t('configs.workbench.overlayMinimize')} onClick={() => setWin('minimized')}>
            <Minimize2 className="h-3.5 w-3.5" />
          </WinBtn>
          <WinBtn
            label={t(win === 'maximized' ? 'configs.workbench.overlayRestore' : 'configs.workbench.overlayMaximize')}
            onClick={() => setWin((s) => (s === 'maximized' ? 'normal' : 'maximized'))}
          >
            <Maximize2 className="h-3.5 w-3.5" />
          </WinBtn>
          <WinBtn label={t('configs.workbench.overlayClose')} onClick={onClose} danger>
            <X className="h-3.5 w-3.5" />
          </WinBtn>
        </div>
      </div>

      {/* ===== 工具栏 ===== */}
      <div className="flex shrink-0 items-center gap-0.5 border-b border-border px-2 py-1">
        <TbBtn label={t('configs.workbench.tbSave')} onClick={doSave} primary>
          <Save className="h-3.5 w-3.5" />
        </TbBtn>
        <TbBtn label={t('configs.workbench.tbSaveAll')} onClick={doSaveAll}>
          <SaveAll className="h-3.5 w-3.5" />
        </TbBtn>
        <span className="mx-1 h-4 w-px bg-border" />
        <TbBtn label={t('configs.workbench.tbRefresh')} onClick={() => file.refetch()}>
          <RefreshCw className="h-3.5 w-3.5" />
        </TbBtn>
        <TbBtn label={t('configs.workbench.tbFind')} onClick={() => tbHint(t('configs.workbench.tbFind'))}>
          <Search className="h-3.5 w-3.5" />
        </TbBtn>
        <TbBtn label={t('configs.workbench.tbReplace')} onClick={() => tbHint(t('configs.workbench.tbReplace'))}>
          <Replace className="h-3.5 w-3.5" />
        </TbBtn>
        <TbBtn label={t('configs.workbench.tbGoto')} onClick={() => tbHint(t('configs.workbench.tbGoto'))}>
          <span className="text-[0.7rem] font-medium">行</span>
        </TbBtn>
        <span className="mx-1 h-4 w-px bg-border" />
        <TbBtn label={t('configs.workbench.tbFontSize')} onClick={() => tbHint(t('configs.workbench.tbFontSize'))}>
          <TypeIcon className="h-3.5 w-3.5" />
        </TbBtn>
        <TbBtn label={t('configs.workbench.tbTheme')} onClick={() => tbHint(t('configs.workbench.tbTheme'))}>
          <SunMoon className="h-3.5 w-3.5" />
        </TbBtn>
        <TbBtn label={t('configs.workbench.tbSettings')} onClick={() => tbHint(t('configs.workbench.tbSettings'))}>
          <Settings className="h-3.5 w-3.5" />
        </TbBtn>
        {/* 历史折叠切换（右对齐） */}
        <TbBtn
          label={t('configs.workbench.historyTitle')}
          onClick={() => setHistoryCollapsed((v) => !v)}
          className="ml-auto"
          active={!historyCollapsed}
        >
          <History className="h-3.5 w-3.5" />
        </TbBtn>
      </div>

      {/* ===== 多标签栏 ===== */}
      <div className="flex h-8 shrink-0 overflow-x-auto border-b border-border scrollbar-hide">
        {tabs.map((tab) => {
          const isActive = tab.key === activeKey
          const tabDirty = dirtyKeys.has(tab.key)
          return (
            <button
              key={tab.key}
              type="button"
              onClick={() => onActivate(tab.key)}
              className={cn(
                'flex h-full select-none items-center gap-1.5 whitespace-nowrap border-r border-border px-3 text-xs transition-colors',
                isActive
                  ? 'bg-card font-medium text-foreground'
                  : 'bg-muted/30 text-muted-foreground hover:bg-muted/50 hover:text-foreground',
              )}
            >
              {tabDirty && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500" title={t('configs.workbench.tbDirty')} />}
              <span className="max-w-[140px] truncate">{tab.name}</span>
              <span
                className="ml-0.5 shrink-0 cursor-pointer text-muted-foreground/60 hover:text-destructive"
                onClick={(e) => {
                  e.stopPropagation()
                  onCloseTab(tab.key)
                }}
              >
                <X className="h-3 w-3" />
              </span>
            </button>
          )
        })}
      </div>

      {/* ===== 主体：编辑器 + 历史面板 ===== */}
      <div className="flex min-h-0 flex-1">
        <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
          <div className="min-h-0 flex-1">
            {file.isLoading ? (
              <div className="space-y-2 p-4">
                {Array.from({ length: 8 }).map((_, i) => (
                  <Skeleton key={i} className="h-4 w-full" />
                ))}
              </div>
            ) : !file.data ? (
              <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
                {t('configs.workbench.fileNotFound')}
              </div>
            ) : (
              <CodeEditor value={content} language={file.data.format} onChange={markDirty} />
            )}
          </div>
        </div>
        {/* 历史修订面板（可折叠） */}
        {!historyCollapsed && <HistoryPanel file={file.data} />}
      </div>

      {/* ===== 底部状态栏 ===== */}
      {file.data && <StatusBar file={file.data} dirty={isDirty} cursor={cursor} />}
    </div>
  )
}

// 当前活跃标签名
function activeName(tabs: EditorTab[], key: string): string {
  return tabs.find((t) => t.key === key)?.name ?? ''
}

// 顶部面包屑：‹ 配置中心 / 环境 · 组 / 文件名
function Breadcrumb({ file, onBack }: { file: WorkbenchFile | undefined; onBack: () => void }) {
  const { t } = useTranslation()
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-xs">
      <button
        type="button"
        onClick={onBack}
        className="flex items-center gap-0.5 text-muted-foreground transition-colors hover:text-foreground"
      >
        <ChevronLeft className="h-3.5 w-3.5" />
        {t('configs.title')}
      </button>
      {file && (
        <>
          <span className="text-muted-foreground/50">/</span>
          <span className="truncate text-muted-foreground">
            {file.namespace} · {file.group}
          </span>
          <span className="text-muted-foreground/50">/</span>
          <span className="truncate font-medium text-foreground">{tabLabel(file)}</span>
        </>
      )}
    </span>
  )
}

// 标签 / 面包屑文件名：取 dataId 末段
export function tabLabel(file: WorkbenchFile): string {
  const parts = file.dataId.split('/')
  return parts[parts.length - 1]
}

// 底部状态栏：文件位置 | LF | 行,列 | 编码 | 语言 | 历史版本 N 份
function StatusBar({
  file,
  dirty,
  cursor,
}: {
  file: WorkbenchFile
  dirty: boolean
  cursor: { line: number; col: number }
}) {
  const { t } = useTranslation()
  const meta = SCOPE_META[file.scope]
  return (
    <div className="flex shrink-0 items-center gap-3 border-t border-border bg-muted/30 px-3 py-1 text-[0.7rem] text-muted-foreground">
      {/* 文件位置（路径） */}
      <span className="truncate font-mono">
        {file.namespace}/{file.group}/{file.dataId}
      </span>
      {dirty && <span className="shrink-0 text-amber-600 dark:text-amber-400">●{t('configs.workbench.tbDirty')}</span>}
      <span className="ml-auto flex shrink-0 items-center gap-3">
        <Badge variant="outline" className={cn('h-4 px-1 text-[0.6rem]', meta.badgeClass)}>
          {t(meta.labelKey)}
        </Badge>
        <span>{t('configs.workbench.sbEol')}</span>
        <span className="tabular-nums">{t('configs.workbench.sbCursor', { line: cursor.line, col: cursor.col })}</span>
        <span>{t('configs.workbench.sbEncoding')}</span>
        <span>{t('configs.workbench.sbLanguage', { lang: file.format })}</span>
        <span>{t('configs.workbench.sbRevisions', { count: file.revisions.length })}</span>
      </span>
    </div>
  )
}

// 右侧历史修订面板（只读列表）
function HistoryPanel({ file }: { file: WorkbenchFile | undefined }) {
  const { t } = useTranslation()
  const revisions = file?.revisions ?? []
  return (
    <div className="flex w-56 shrink-0 flex-col overflow-hidden border-l border-border bg-card">
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-3 py-2">
        <History className="h-4 w-4 text-muted-foreground" />
        <span className="text-xs font-medium text-foreground">{t('configs.workbench.historyTitle')}</span>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto scrollbar-hide p-1.5">
        {revisions.map((r, idx) => (
          <div
            key={r.version}
            className="mb-1 w-full rounded-md border border-transparent px-2 py-1.5 text-left text-xs hover:border-border hover:bg-muted/50"
          >
            <div className="flex items-center gap-1.5">
              <span className="font-medium tabular-nums text-foreground">v{r.version}</span>
              {idx === 0 && (
                <Badge
                  variant="outline"
                  className="h-4 border-emerald-500/40 px-1 text-[0.55rem] text-emerald-600 dark:text-emerald-400"
                >
                  {t('configs.workbench.historyCurrent')}
                </Badge>
              )}
            </div>
            <div className="mt-0.5 truncate text-muted-foreground">{r.comment}</div>
            <div className="mt-0.5 text-[0.65rem] text-muted-foreground/70">
              {r.author} · {r.time}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

// 窗口控制按钮（最小化 / 最大化 / 关闭）
function WinBtn({
  label,
  onClick,
  danger,
  children,
}: {
  label: string
  onClick: () => void
  danger?: boolean
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={label}
      aria-label={label}
      className={cn(
        'flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors',
        danger ? 'hover:bg-destructive/10 hover:text-destructive' : 'hover:bg-muted/60 hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}

// 工具栏按钮
function TbBtn({
  label,
  onClick,
  primary,
  active,
  className,
  children,
}: {
  label: string
  onClick: () => void
  primary?: boolean
  active?: boolean
  className?: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={label}
      aria-label={label}
      className={cn(
        'flex h-6 items-center gap-1 rounded px-1.5 text-xs transition-colors',
        primary
          ? 'text-primary hover:bg-primary/10'
          : active
            ? 'bg-muted text-foreground'
            : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground',
        className,
      )}
    >
      {children}
    </button>
  )
}
