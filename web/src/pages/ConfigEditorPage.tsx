/**
 * 配置文件真详情多标签编辑器（FR-112，依赖 FR-111 / ADR-0050 决策 3）。
 *
 * /configs/:id 真子路由（取代旧重定向 / 工作台页内浮层恢复）：双击工作台文件进本页，
 * 聚焦编辑单个受管文件。形态为整页（非浮层），保留：
 *   - 多标签横切（同会话可开多个文件标签、切换即换 URL :id、可关标签）
 *   - Monaco 编辑 + 历史修订（点历史版本 → 当前 ⟷ 该版本 diff，FR-67/FR-111 已接真 revisions）
 *   - 保存确认（FR-67 ConfigSaveConfirmDialog：看 diff + 填备注，确认才调既有 publishFile 真保存）
 *   - 局部面包屑 / 返回（配置中心 / 环境·组 / 文件名）
 *
 * 数据：复用 useWorkbenchFile（FR-111 已接真 getFile + listFileRevisions）；保存接既有 publishFile（PUT /files/:id）。
 */

import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, History, RefreshCw, Save, X } from 'lucide-react'

import { publishFile } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { useMessage } from '@/components/useMessage'
import { usePageHeader } from '@/components/PageHeader'
import { cn } from '@/lib/utils'
import CodeEditor from '@/components/CodeEditor'

import ConfigSaveConfirmDialog from './configs/ConfigSaveConfirmDialog'
import { useWorkbenchFile } from './configs-workbench/useWorkbenchData'
import { tabLabel } from './configs-workbench/EditorOverlay'
import { SCOPE_META } from './configs-workbench/diffMeta'
import type { WorkbenchFile } from './configs-workbench/types'

// 已打开标签（同会话维护；活跃标签 = URL :id）
interface EditorTab {
  key: string
  name: string
}

export default function ConfigEditorPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const params = useParams()
  const msg = useMessage()
  const queryClient = useQueryClient()

  // 活跃文件 key（URL :id 解码）
  const activeKey = params.id ? decodeURIComponent(params.id) : undefined

  // 已打开标签集合：随访问的 :id 累积（去重）；切换 / 关闭联动 URL
  const [tabs, setTabs] = useState<EditorTab[]>([])

  // 当前编辑态内容（按活跃 key 维护脏态）+ 脏标记集合
  const [content, setContent] = useState('')
  const [dirtyKeys, setDirtyKeys] = useState<Set<string>>(new Set())
  // 历史 diff：选中要对比的历史版本号（null=编辑模式，非 null=与该版本并排 diff）
  const [diffVersion, setDiffVersion] = useState<number | null>(null)
  // 保存确认对话框（FR-67）开合 + 备注
  const [saveConfirmOpen, setSaveConfirmOpen] = useState(false)
  const [saveComment, setSaveComment] = useState('')

  const file = useWorkbenchFile(activeKey)

  // 访问某 :id → 把它登记为标签（去重）
  useEffect(() => {
    if (!activeKey) return
    const name = activeKey.split('/').pop() ?? activeKey
    setTabs((prev) => (prev.some((p) => p.key === activeKey) ? prev : [...prev, { key: activeKey, name }]))
  }, [activeKey])

  // 活跃文件加载完成 → 回灌编辑器内容（切文件 / 刷新后回到编辑模式）
  useEffect(() => {
    if (file.data) {
      setContent(file.data.content)
      setDiffVersion(null)
    }
  }, [file.data])

  const isDirty = activeKey ? dirtyKeys.has(activeKey) : false

  // 上一已保存版本内容（diff 左侧基线）：取最新历史版本，无则用加载内容
  const lastSavedContent = file.data?.revisions[0]?.content ?? file.data?.content ?? ''

  function markDirty(next: string) {
    setContent(next)
    if (!activeKey) return
    setDirtyKeys((prev) => {
      if (prev.has(activeKey)) return prev
      const n = new Set(prev)
      n.add(activeKey)
      return n
    })
  }

  // 保存真发布（FR-67 确认后）：调既有 publishFile（PUT /files/:id）
  const saveMut = useMutation({
    mutationFn: (vars: { id: number; content: string; comment: string }) =>
      publishFile(vars.id, vars.content, vars.comment),
    onSuccess: () => {
      setSaveConfirmOpen(false)
      if (activeKey) {
        setDirtyKeys((prev) => {
          const n = new Set(prev)
          n.delete(activeKey)
          return n
        })
      }
      // 失效该文件查询，重拉最新版本 + 历史
      queryClient.invalidateQueries({ queryKey: ['wb-file'] })
      msg.showSuccess(t('configs.editorRoute.toastSaved'))
    },
    onError: (e) => msg.showError(e instanceof Error ? e.message : t('configs.editorRoute.saveFailed')),
  })

  // 点保存：不直接发布，先弹保存确认（看 diff + 填备注，FR-67）
  const openSaveConfirm = useCallback(() => {
    if (!file.data) return
    setSaveComment('')
    setSaveConfirmOpen(true)
  }, [file.data])

  // 确认保存：对话框确认才真正发布当前编辑态内容
  function doSave() {
    if (!file.data) return
    saveMut.mutate({ id: file.data.fileId, content, comment: saveComment })
  }

  // Ctrl/⌘+S：唤起保存确认
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault()
        openSaveConfirm()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [openSaveConfirm])

  // 切换标签：换 URL :id（活跃文件随之切）
  const activateTab = (key: string) => {
    if (key !== activeKey) navigate(`/configs/${encodeURIComponent(key)}`)
  }

  // 关闭标签：活跃标签关闭则跳邻标签；最后一个关闭则回工作台
  const closeTab = (key: string) => {
    setTabs((prev) => {
      const next = prev.filter((p) => p.key !== key)
      if (key === activeKey) {
        if (next.length > 0) navigate(`/configs/${encodeURIComponent(next[next.length - 1].key)}`)
        else navigate('/configs')
      }
      return next
    })
  }

  // 局部页眉：标题用面包屑（配置中心 / 环境·组 / 文件名）
  usePageHeader({
    title: <Breadcrumb file={file.data} />,
    envScoped: false,
  })

  return (
    <div className="relative flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
      {/* ===== 工具栏：保存 / 刷新 + 历史折叠（历史常驻右栏） ===== */}
      <div className="flex shrink-0 items-center gap-0.5 border-b border-border px-2 py-1">
        <TbBtn label={t('configs.editorRoute.save')} onClick={openSaveConfirm} primary disabled={!file.data}>
          <Save className="h-3.5 w-3.5" />
          <span className="ml-1 text-xs">{t('configs.editorRoute.save')}</span>
        </TbBtn>
        <TbBtn label={t('configs.editorRoute.refresh')} onClick={() => file.refetch()}>
          <RefreshCw className="h-3.5 w-3.5" />
        </TbBtn>
        {diffVersion !== null && (
          <button
            type="button"
            onClick={() => setDiffVersion(null)}
            className="ml-2 rounded bg-muted px-2 py-0.5 text-xs text-muted-foreground hover:text-foreground"
          >
            {t('configs.editorRoute.backToEdit')}
          </button>
        )}
      </div>

      {/* ===== 多标签栏 ===== */}
      <div className="flex h-8 shrink-0 overflow-x-auto border-b border-border scrollbar-hide">
        {tabs.map((tab) => {
          const active = tab.key === activeKey
          const tabDirty = dirtyKeys.has(tab.key)
          return (
            <button
              key={tab.key}
              type="button"
              onClick={() => activateTab(tab.key)}
              className={cn(
                'flex h-full select-none items-center gap-1.5 whitespace-nowrap border-r border-border px-3 text-xs transition-colors',
                active
                  ? 'bg-card font-medium text-foreground'
                  : 'bg-muted/30 text-muted-foreground hover:bg-muted/50 hover:text-foreground',
              )}
            >
              {tabDirty && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500" title={t('configs.editorRoute.dirty')} />}
              <span className="max-w-[160px] truncate">{tab.name}</span>
              <span
                className="ml-0.5 shrink-0 cursor-pointer text-muted-foreground/60 hover:text-destructive"
                onClick={(e) => {
                  e.stopPropagation()
                  closeTab(tab.key)
                }}
              >
                <X className="h-3 w-3" />
              </span>
            </button>
          )
        })}
      </div>

      {/* ===== 主体：编辑器 / 历史 diff + 右侧历史面板 ===== */}
      <div className="flex min-h-0 flex-1">
        <div className="min-w-0 flex-1">
          {file.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 8 }).map((_, i) => (
                <Skeleton key={i} className="h-4 w-full" />
              ))}
            </div>
          ) : !file.data ? (
            <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
              {t('configs.editorRoute.fileNotFound')}
            </div>
          ) : diffVersion !== null ? (
            // 历史 diff：左=选定历史版本，右=当前编辑态
            <CodeEditor
              original={revisionContent(file.data, diffVersion)}
              modified={content}
              language={file.data.format}
            />
          ) : (
            <CodeEditor value={content} language={file.data.format} onChange={markDirty} />
          )}
        </div>
        {/* 历史修订面板（点版本 → diff） */}
        <HistoryPanel file={file.data} diffVersion={diffVersion} onPick={setDiffVersion} />
      </div>

      {/* ===== 底部状态栏 ===== */}
      {file.data && <StatusBar file={file.data} dirty={isDirty} />}

      {/* ===== 保存确认对话框（FR-67：diff + 备注，确认才发布）===== */}
      {file.data && (
        <ConfigSaveConfirmDialog
          open={saveConfirmOpen}
          namespace={file.data.namespace}
          group={file.data.group}
          dataId={file.data.dataId}
          scopeLevel={file.data.scope}
          scopeTarget={file.data.targetServer}
          format={file.data.format}
          originalContent={lastSavedContent}
          currentContent={content}
          comment={saveComment}
          pending={saveMut.isPending}
          onCommentChange={setSaveComment}
          onConfirm={doSave}
          onCancel={() => setSaveConfirmOpen(false)}
        />
      )}
    </div>
  )
}

// 取某历史版本的内容（用于 diff 左侧）
function revisionContent(file: WorkbenchFile, version: number): string {
  return file.revisions.find((r) => r.version === version)?.content ?? ''
}

// 局部面包屑：配置中心 / 环境·组 / 文件名（注入页眉标题槽）
function Breadcrumb({ file }: { file: WorkbenchFile | undefined }) {
  const { t } = useTranslation()
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-sm">
      <Link
        to="/configs"
        className="flex items-center gap-0.5 text-muted-foreground transition-colors hover:text-foreground"
      >
        <ChevronLeft className="h-3.5 w-3.5" />
        {t('configs.title')}
      </Link>
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

// 底部状态栏：文件位置 | 覆盖层 | 语言 | 历史版本 N 份
function StatusBar({ file, dirty }: { file: WorkbenchFile; dirty: boolean }) {
  const { t } = useTranslation()
  const meta = SCOPE_META[file.scope]
  return (
    <div className="flex shrink-0 items-center gap-3 border-t border-border bg-muted/30 px-3 py-1 text-[0.7rem] text-muted-foreground">
      <span className="truncate font-mono">
        {file.namespace}/{file.group}/{file.dataId}
      </span>
      {dirty && <span className="shrink-0 text-amber-600 dark:text-amber-400">●{t('configs.editorRoute.dirty')}</span>}
      <span className="ml-auto flex shrink-0 items-center gap-3">
        <Badge variant="outline" className={cn('h-4 px-1 text-[0.6rem]', meta.badgeClass)}>
          {t(meta.labelKey)}
        </Badge>
        <span>{t('configs.editorRoute.language', { lang: file.format })}</span>
        <span>{t('configs.editorRoute.revisions', { count: file.revisions.length })}</span>
      </span>
    </div>
  )
}

// 右侧历史修订面板（点版本 → 与当前编辑态 diff）
function HistoryPanel({
  file,
  diffVersion,
  onPick,
}: {
  file: WorkbenchFile | undefined
  diffVersion: number | null
  onPick: (v: number | null) => void
}) {
  const { t } = useTranslation()
  const revisions = file?.revisions ?? []
  return (
    <div className="flex w-56 shrink-0 flex-col overflow-hidden border-l border-border bg-card">
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-3 py-2">
        <History className="h-4 w-4 text-muted-foreground" />
        <span className="text-xs font-medium text-foreground">{t('configs.editorRoute.historyTitle')}</span>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto scrollbar-hide p-1.5">
        {revisions.map((r, idx) => (
          <button
            key={r.version}
            type="button"
            onClick={() => onPick(diffVersion === r.version ? null : r.version)}
            className={cn(
              'mb-1 block w-full rounded-md border px-2 py-1.5 text-left text-xs transition-colors',
              diffVersion === r.version ? 'border-primary/50 bg-primary/5' : 'border-transparent hover:border-border hover:bg-muted/50',
            )}
          >
            <div className="flex items-center gap-1.5">
              <span className="font-medium tabular-nums text-foreground">v{r.version}</span>
              {idx === 0 && (
                <Badge
                  variant="outline"
                  className="h-4 border-emerald-500/40 px-1 text-[0.55rem] text-emerald-600 dark:text-emerald-400"
                >
                  {t('configs.editorRoute.historyCurrent')}
                </Badge>
              )}
            </div>
            <div className="mt-0.5 truncate text-muted-foreground">{r.comment}</div>
            <div className="mt-0.5 text-[0.65rem] text-muted-foreground/70">
              {r.author} · {r.time}
            </div>
          </button>
        ))}
      </div>
    </div>
  )
}

// 工具栏按钮
function TbBtn({
  label,
  onClick,
  primary,
  disabled,
  children,
}: {
  label: string
  onClick: () => void
  primary?: boolean
  disabled?: boolean
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={label}
      aria-label={label}
      disabled={disabled}
      className={cn(
        'flex h-6 items-center gap-0.5 rounded px-1.5 text-xs transition-colors disabled:opacity-40',
        primary ? 'text-primary hover:bg-primary/10' : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}
