/**
 * 配置中心「双面板 Xftp 工作台」v2 (FR-111) + 悬浮覆盖编辑器 (FR-112) — style 预览原型，纯前端 + mock 数据驱动。
 *
 * 布局（固定高度、内部滚，relative 容器内）：
 *   ┌── 图例行（同步状态四态 + 覆盖层徽标说明）───────────────┐
 *   ├── 选中驱动动作条（任一面板有选中时出现）─────────────────┤
 *   ├── 双面板 ────────────┬─ 分隔 ─┬──────────────────────────┤
 *   │ 左：受管配置          │       │ 右：服务器                 │
 *   │ (配置树/生效预览切换)  │       │ (全量 plugins，含未纳管)   │
 *   ├── 同步队列·实时（固定高度，待审核项可点开审核浮层）────────┤
 *   └──────────────────────────────────────────────────────────┘
 *   └─── 浮层：右键菜单 / 二次确认 / ingest 审核 / 拓印 diff 审核 / 悬浮编辑器 ───┘
 *
 * 交互要点：
 *  - 多选：行内复选框 + ctrl/shift 点选；任一面板有选中 → 顶部动作条（抓取 / 下发选中 N 项）。
 *  - 危险操作（抓取 / 下发 / 批量 / 删除 / 重命名 / 新建）全走二次确认（DestructiveConfirmDialog，FR-76）。
 *  - 右键文件 / 文件夹 → 自定义菜单；Del=删除选中、F2=重命名（均拦截浏览器默认）。
 *  - 拖拽=快捷路径，触发同样的确认流。
 *  - 队列「待审核」项点开：抓取→ingest 审核清单（FR-58~60）、下发→拓印 diff 审核（FR-46）。
 *  - 双击文件 → 进真详情多标签编辑器路由 /configs/:id（FR-112）；右键「编辑/diff/回滚」仍走页内悬浮编辑器（轻量快捷）。
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useParams } from 'react-router-dom'
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useDroppable,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
} from '@dnd-kit/core'
import { FolderTree, ListTree, Server } from 'lucide-react'

import { useAuth } from '@/state/auth'
import { usePageHeader } from '@/components/PageHeader'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { useMessage } from '@/components/useMessage'
import DestructiveConfirmDialog from '@/components/DestructiveConfirmDialog'
import { cn } from '@/lib/utils'

import { useManagedTree, useOperationLog, useServerTree, useSyncQueue, useWorkbenchOptions } from './configs-workbench/useWorkbenchData'
import PanelTree, { flattenVisibleFiles, type ContextMenuPayload, type PanelNode } from './configs-workbench/PanelTree'
import PanelToolbar, { type ToolbarAction } from './configs-workbench/PanelToolbar'
import BottomDock from './configs-workbench/BottomDock'
import SelectionStatusBar from './configs-workbench/SelectionStatusBar'
import EditorOverlay, { type EditorTab } from './configs-workbench/EditorOverlay'
import ChipSelect from './configs-workbench/ChipSelect'
import ContextMenu, { type ContextAction, type ContextMenuState } from './configs-workbench/ContextMenu'
import IngestReviewOverlay from './configs-workbench/IngestReviewOverlay'
import ImprintReviewOverlay from './configs-workbench/ImprintReviewOverlay'
import PublishPanel from './configs-workbench/PublishPanel'
import BatchReviewOverlay from './configs-workbench/BatchReviewOverlay'
import EffectivePreviewView from './configs-workbench/EffectivePreviewView'
import { SCOPE_META, SERVER_MARK_META, SYNC_LEGEND_META, SYNC_META, type DotMeta } from './configs-workbench/diffMeta'
import type { ManagedNode, OpAction, OpLogEntry, ServerNode, SyncQueueRow } from './configs-workbench/types'

// 覆盖层徽标图例
const SCOPE_LEGEND = ['global', 'group', 'server'] as const

// 受管面板视图模式：配置树 / 生效预览
type ManagedView = 'tree' | 'effective'

// 二次确认弹窗承载的操作描述（驱动 DestructiveConfirmDialog）
interface ConfirmState {
  kind: 'transfer-fetch' | 'transfer-push' | 'delete' | 'rename' | 'new' | 'move'
  // 涉及的文件名列表
  names: string[]
  // 落点说明（覆盖层 / 目标服 / 目标目录）
  target: string
}

export default function ConfigWorkbenchPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const msg = useMessage()
  const params = useParams()

  const { operator } = useAuth()

  // scope / server chip 选择：scope 决定抓取 / 发布落的覆盖层；serverId 决定右面板浏览哪台在线服
  const [scope, setScope] = useState('group:main')
  const [serverId, setServerId] = useState('')

  const managed = useManagedTree()
  // 右面板实时浏览选定在线服（FR-110）；未选服时不浏览
  const server = useServerTree(serverId || undefined)
  const queue = useSyncQueue()
  const opLog = useOperationLog()
  const options = useWorkbenchOptions()

  // 受管面板视图：配置树 / 生效预览
  const [managedView, setManagedView] = useState<ManagedView>('tree')

  // 本地追加 / 完结的同步队列行（拖拽 / 状态栏触发叠加在 mock 队列之上；审核完结改其状态）
  const [extraQueue, setExtraQueue] = useState<SyncQueueRow[]>([])
  const [doneIds, setDoneIds] = useState<Set<string>>(new Set())

  // 操作日志：运行期新增条目叠加在 mock 种子之上；undoneLogIds 命中的标记为已撤回
  const [extraLog, setExtraLog] = useState<OpLogEntry[]>([])
  const [undoneLogIds, setUndoneLogIds] = useState<Set<string>>(new Set())
  // 操作日志多选（批量撤回用）
  const [logSel, setLogSel] = useState<Set<string>>(new Set())

  // 拖拽中的节点名（DragOverlay 展示）
  const [draggingName, setDraggingName] = useState<string | null>(null)

  // 多选 + 展开 + 锚点（受管 / 服务器各一套）
  const [selManaged, setSelManaged] = useState<Set<string>>(new Set())
  const [selServer, setSelServer] = useState<Set<string>>(new Set())
  const [expManaged, setExpManaged] = useState<Set<string>>(new Set(['plugins']))
  const [expServer, setExpServer] = useState<Set<string>>(new Set(['srv/plugins']))
  const anchorManaged = useRef<string | null>(null)
  const anchorServer = useRef<string | null>(null)

  // 右键菜单 / 二次确认 / 审核浮层状态
  const [ctxMenu, setCtxMenu] = useState<ContextMenuState | null>(null)
  const [confirm, setConfirm] = useState<ConfirmState | null>(null)
  // 队列待审核浮层：抓取→ingest / 下发→拓印
  const [reviewRow, setReviewRow] = useState<SyncQueueRow | null>(null)
  // 发布面板（改进 1）：受管选中 → 发布 + 影响面；持有待发布文件名（null=未开）
  const [publishNames, setPublishNames] = useState<string[] | null>(null)
  // 队列批量审核（改进 4）：选中的待审队列行 id 集合 + 批量审核浮层开关
  const [queueSel, setQueueSel] = useState<Set<string>>(new Set())
  const [batchReview, setBatchReview] = useState<SyncQueueRow[] | null>(null)

  // 悬浮编辑器：已打开标签集合 + 活跃标签 key（null=浮层未开）
  const [tabs, setTabs] = useState<EditorTab[]>([])
  const [activeKey, setActiveKey] = useState<string | null>(null)

  // 工作台根容器引用（右键菜单坐标换算为容器内坐标）
  const rootRef = useRef<HTMLDivElement>(null)

  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }))

  // 队列展示行：本地新增在前 + mock 行；doneIds 命中的改为已完成态
  const queueRows = useMemo(() => {
    const base = [...extraQueue, ...(queue.data ?? [])]
    if (doneIds.size === 0) return base
    return base.map((r) => (doneIds.has(r.id) ? { ...r, status: 'done' as const } : r))
  }, [extraQueue, queue.data, doneIds])

  // 操作日志展示行：本地新增在前 + mock 种子；undoneLogIds 命中的标记已撤回
  const logRows = useMemo(() => {
    const base = [...extraLog, ...(opLog.data ?? [])]
    if (undoneLogIds.size === 0) return base
    return base.map((e) => (undoneLogIds.has(e.id) ? { ...e, undone: true } : e))
  }, [extraLog, opLog.data, undoneLogIds])

  // 记一条操作日志（每次大操作调用）：返回新条目 id，便于关联其产生的队列行
  const logOp = useCallback(
    (action: OpAction, files: string[], target: string, detail: string, queueRowIds?: string[]) => {
      const entry: OpLogEntry = {
        id: `op-${Date.now()}-${Math.floor(performance.now())}`,
        time: nowTime(),
        action,
        operator: operator || 'admin',
        files,
        target,
        detail,
        undone: false,
        queueRowIds,
      }
      setExtraLog((prev) => [entry, ...prev])
      return entry.id
    },
    [operator],
  )

  // 撤回一批操作：移除其产生的队列行 + 标记已撤回 + toast（下发/发布的撤回即回滚到操作前）
  const undoOps = useCallback(
    (ids: string[]) => {
      const entries = logRows.filter((e) => ids.includes(e.id) && !e.undone)
      if (entries.length === 0) return
      const rowIds = entries.flatMap((e) => e.queueRowIds ?? [])
      if (rowIds.length > 0) setExtraQueue((prev) => prev.filter((r) => !rowIds.includes(r.id)))
      setUndoneLogIds((prev) => {
        const next = new Set(prev)
        for (const e of entries) next.add(e.id)
        return next
      })
      setLogSel(new Set())
      if (entries.length === 1) msg.showSuccess(t('configs.workbench.toastUndoneOne', { detail: entries[0].detail }))
      else msg.showSuccess(t('configs.workbench.toastUndoneBatch', { count: entries.length }))
    },
    [logRows, msg, t],
  )

  // 打开文件到浮层（去重）：双击工作台文件 / 深链恢复 / 右键编辑共用
  const openFile = useCallback((key: string, name: string) => {
    setTabs((prev) => (prev.some((p) => p.key === key) ? prev : [...prev, { key, name }]))
    setActiveKey(key)
  }, [])

  // 默认选中首个在线服（右面板浏览目标）：options 加载完成且未选服时落到第一台
  useEffect(() => {
    if (!serverId && options.data?.servers?.length) {
      setServerId(options.data.servers[0].serverId)
    }
  }, [serverId, options.data])

  // 双击工作台文件 → 进真详情多标签编辑器（FR-112，/configs/:id 真路由，不再开页内浮层）
  const onDoubleClickFile = (node: PanelNode) => navigate(`/configs/${encodeURIComponent(node.key)}`)

  // 关闭整个浮层：清空标签 + 回 /configs（去掉深链 id）
  const closeOverlay = () => {
    setTabs([])
    setActiveKey(null)
    if (params.id) navigate('/configs')
  }

  // 关闭某标签：活跃标签关闭则跳邻；最后一个关闭则关浮层
  const closeTab = (key: string) => {
    setTabs((prev) => {
      const next = prev.filter((p) => p.key !== key)
      if (key === activeKey) {
        if (next.length > 0) setActiveKey(next[next.length - 1].key)
        else closeOverlay()
      }
      return next
    })
  }

  // 浮层最大化态把活跃 id 同步进 URL（刷新可恢复深链）；离开最大化清掉
  const syncUrl = useCallback(
    (key: string | null) => {
      if (key) {
        if (params.id !== encodeURIComponent(key)) navigate(`/configs/${encodeURIComponent(key)}`, { replace: true })
      } else if (params.id) {
        navigate('/configs', { replace: true })
      }
    },
    [params.id, navigate],
  )

  // ---- 多选：单选 / ctrl 切换 / shift 范围（按可见文件序） ----
  const selectFile = useCallback(
    (side: 'managed' | 'server', key: string, mods: { ctrl: boolean; shift: boolean }) => {
      const setSel = side === 'managed' ? setSelManaged : setSelServer
      const anchorRef = side === 'managed' ? anchorManaged : anchorServer
      const visible =
        side === 'managed'
          ? flattenVisibleFiles((managed.data ?? []) as PanelNode[], expManaged)
          : flattenVisibleFiles((server.data ?? []) as PanelNode[], expServer)

      setSel((prev) => {
        const next = new Set(prev)
        if (mods.shift && anchorRef.current) {
          // 范围选：锚点到当前点之间的可见文件
          const a = visible.indexOf(anchorRef.current)
          const b = visible.indexOf(key)
          if (a >= 0 && b >= 0) {
            const [lo, hi] = a < b ? [a, b] : [b, a]
            for (let i = lo; i <= hi; i++) next.add(visible[i])
            return next
          }
        }
        if (mods.ctrl) {
          // 切换单项
          if (next.has(key)) next.delete(key)
          else next.add(key)
          anchorRef.current = key
          return next
        }
        // 普通点：单选
        anchorRef.current = key
        return new Set([key])
      })
      // 选中某侧时清空另一侧（动作条方向单一）
      if (side === 'managed') setSelServer(new Set())
      else setSelManaged(new Set())
    },
    [managed.data, server.data, expManaged, expServer],
  )

  const toggleExpand = useCallback((side: 'managed' | 'server', key: string) => {
    const setExp = side === 'managed' ? setExpManaged : setExpServer
    setExp((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [])

  // ---- 动作条 / 确认 → 批量入队 ----
  // 选中方向：受管有选中=下发；服务器有选中=抓取
  const selSide: 'managed' | 'server' | null = selManaged.size > 0 ? 'managed' : selServer.size > 0 ? 'server' : null
  const selNames = useMemo(() => {
    if (selSide === 'managed') return [...selManaged].map((k) => k.split('/').pop() ?? k)
    if (selSide === 'server') return [...selServer].map((k) => k.split('/').pop() ?? k)
    return []
  }, [selSide, selManaged, selServer])

  // 打开「发布选中」面板（改进 1）：受管层发布 → 热推到所有受影响在线服（不再是逐台下发确认）
  function askPublishSelected() {
    setPublishNames(selNames)
  }
  // 打开「抓取选中」确认（落当前 scope 覆盖层）
  function askFetchSelected() {
    const scopeLabel = options.data?.scopes.find((s) => s.value === scope)?.label ?? scope
    setConfirm({ kind: 'transfer-fetch', names: selNames, target: scopeLabel })
  }

  // 确认弹窗「确认」回调：按 kind 批量入队 / 删除 / 提示
  function onConfirmDone() {
    if (!confirm) return
    const c = confirm
    const filesLabel = c.names.join('、')
    if (c.kind === 'transfer-push') {
      const ids = enqueue(c.names, 'push', c.target)
      logOp('push', c.names, c.target, t('configs.workbench.logDetailPush', { files: filesLabel, target: c.target }), ids)
      msg.showSuccess(t('configs.workbench.toastPushBatch', { count: c.names.length, target: c.target }))
      setSelManaged(new Set())
    } else if (c.kind === 'transfer-fetch') {
      const ids = enqueue(c.names, 'fetch', c.target)
      logOp('fetch', c.names, c.target, t('configs.workbench.logDetailFetch', { files: filesLabel, target: c.target }), ids)
      msg.showSuccess(t('configs.workbench.toastFetchBatch', { count: c.names.length, scope: c.target }))
      setSelServer(new Set())
    } else if (c.kind === 'delete') {
      logOp('delete', c.names, c.target, t('configs.workbench.logDetailDelete', { files: filesLabel, where: c.target }))
      msg.showSuccess(t('configs.workbench.toastDeleted', { count: c.names.length }))
      setSelManaged(new Set())
      setSelServer(new Set())
    } else if (c.kind === 'rename') {
      logOp('rename', c.names, t('configs.workbench.managedTitle'), t('configs.workbench.logDetailRename', { name: c.names[0] ?? '' }))
      msg.showSuccess(t('configs.workbench.toastRenamed', { name: c.names[0] ?? '' }))
    } else if (c.kind === 'new') {
      logOp('new', c.names, t('configs.workbench.managedTitle'), t('configs.workbench.logDetailNew', { name: c.names[0] ?? '' }))
      msg.showSuccess(t('configs.workbench.toastCreated'))
    } else if (c.kind === 'move') {
      // 改进 3：移动改目录（原型仅 toast 示意，不真改 mock 树）
      logOp('move', c.names, c.target, t('configs.workbench.logDetailMove', { name: c.names[0] ?? '', target: c.target }))
      msg.showSuccess(t('configs.workbench.toastMoved', { name: c.names[0] ?? '', target: c.target }))
    }
    setConfirm(null)
  }

  // 批量入队：按文件逐条建队列行，方向决定状态（抓取→待 ingest / 下发→待拓印）；返回新建行 id（供操作日志撤回）
  function enqueue(names: string[], direction: 'fetch' | 'push', target: string): string[] {
    const base = Date.now()
    const rows: SyncQueueRow[] = names.map((name, i) => ({
      id: `local-${base}-${i}`,
      name,
      direction,
      status: direction === 'fetch' ? 'pending-ingest' : 'pending-imprint',
      scopeTarget: target,
      sourcePath: direction === 'fetch' ? `${serverId}:/plugins/${name}` : `prod/main/${name}`,
      targetPath: direction === 'fetch' ? `prod/main/${name}` : `${serverId}:/plugins/${name}`,
      time: nowTime(),
    }))
    setExtraQueue((prev) => [...rows, ...prev])
    return rows.map((r) => r.id)
  }

  // ---- 右键菜单动作分发 ----
  function onContextMenu(payload: ContextMenuPayload) {
    const rect = rootRef.current?.getBoundingClientRect()
    setCtxMenu({
      x: payload.x - (rect?.left ?? 0),
      y: payload.y - (rect?.top ?? 0),
      side: payload.side,
      name: payload.node.name,
      isFolder: payload.node.type === 'folder',
    })
    // 右键的节点临时记到锚点，供菜单动作引用
    if (payload.side === 'managed') anchorManaged.current = payload.node.key
    else anchorServer.current = payload.node.key
    // 右键文件即选中它（单选），让删除 / 传输有明确目标
    if (payload.node.type === 'file') selectFile(payload.side, payload.node.key, { ctrl: false, shift: false })
  }

  function onCtxAction(action: ContextAction) {
    if (!ctxMenu) return
    const m = ctxMenu
    setCtxMenu(null)
    const key = (m.side === 'managed' ? anchorManaged.current : anchorServer.current) ?? ''
    const name = m.name
    switch (action) {
      case 'edit':
        openFile(key, name)
        break
      case 'rename':
        setConfirm({ kind: 'rename', names: [name], target: '' })
        break
      case 'delete':
        setConfirm({ kind: 'delete', names: [name], target: m.side === 'managed' ? t('configs.workbench.managedTitle') : `实例 ${serverId}` })
        break
      case 'new':
        setConfirm({ kind: 'new', names: [name], target: '' })
        break
      case 'transfer':
        if (m.side === 'server') setConfirm({ kind: 'transfer-fetch', names: [name], target: options.data?.scopes.find((s) => s.value === scope)?.label ?? scope })
        else setConfirm({ kind: 'transfer-push', names: [name], target: `实例 ${serverId}` })
        break
      case 'diff':
        openFile(key, name)
        break
      case 'rollback':
        // 回滚到历史版本：打开编辑器（含 revisions 面板）并提示选择历史版本
        openFile(key, name)
        msg.showSuccess(t('configs.workbench.toastRolledBack', { name }))
        break
    }
  }

  // ---- 键盘拦截：Del=删除选中、F2=重命名（均阻止浏览器默认）----
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      // 编辑器浮层开启或聚焦输入框时不接管
      if (activeKey) return
      const tag = (e.target as HTMLElement | null)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA') return
      const names = selSide ? selNames : []
      if (e.key === 'Delete' && names.length > 0) {
        e.preventDefault()
        setConfirm({ kind: 'delete', names, target: selSide === 'managed' ? t('configs.workbench.managedTitle') : `实例 ${serverId}` })
      } else if (e.key === 'F2' && names.length === 1) {
        e.preventDefault()
        setConfirm({ kind: 'rename', names, target: '' })
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [activeKey, selSide, selNames, serverId, t])

  // ---- 拖拽落点（改进 3 收窄语义）：
  //   同面板 拖文件落文件夹 = 移动（改目录），走「移动」二次确认；
  //   跨面板 右→左 = 抓取（走 ingest 审核）；左→右 = 给这一台建实例层覆盖（定向单服，走二次确认）。
  function onDragStart(e: DragStartEvent) {
    setDraggingName((e.active.data.current?.name as string) ?? null)
  }
  function onDragEnd(e: DragEndEvent) {
    setDraggingName(null)
    const { active, over } = e
    if (!over) return
    const fromSide = active.data.current?.side as 'managed' | 'server' | undefined
    const name = (active.data.current?.name as string) ?? '文件'
    const fromKey = active.data.current?.key as string | undefined
    if (!fromSide) return

    // 落到文件夹（folder::<side>::<key>）→ 同面板移动改目录（文件 / 整个目录皆可拖入）
    const overData = over.data.current as { side?: string; folderName?: string; folderKey?: string } | undefined
    if (overData?.folderName) {
      // 仅同面板移动；跨面板落到对侧文件夹不触发同步（避免误判），忽略
      if (overData.side !== fromSide) return
      // 文件夹拖到自身上，忽略（自身既可拖又是放置目标）
      if (overData.folderKey === fromKey) return
      setConfirm({ kind: 'move', names: [name], target: overData.folderName })
      return
    }

    // 落到面板体（drop-managed / drop-server）→ 跨面板同步
    const overSide = over.id === 'drop-managed' ? 'managed' : over.id === 'drop-server' ? 'server' : undefined
    if (!overSide || fromSide === overSide) return
    if (fromSide === 'server') {
      setConfirm({ kind: 'transfer-fetch', names: [name], target: options.data?.scopes.find((s) => s.value === scope)?.label ?? scope })
    } else {
      setConfirm({ kind: 'transfer-push', names: [name], target: `实例 ${serverId}` })
    }
  }

  // ---- 队列待审核行点开 → 审核浮层；审核确认 → 该行转完成 ----
  function onReviewConfirm() {
    if (reviewRow) setDoneIds((prev) => new Set(prev).add(reviewRow.id))
    setReviewRow(null)
  }

  // ---- 发布面板确认（改进 1）：往队列加该批「下发·已完成」行 + toast ----
  function onPublishConfirm(onlineCount: number) {
    const names = publishNames ?? []
    const ids = enqueuePublished(names)
    logOp(
      'publish',
      names,
      t('configs.workbench.publishScopeTarget'),
      t('configs.workbench.logDetailPublish', { files: names.join('、'), online: onlineCount }),
      ids,
    )
    msg.showSuccess(t('configs.workbench.toastPublished', { count: names.length, online: onlineCount }))
    setPublishNames(null)
    setSelManaged(new Set())
  }

  // 发布入队：按文件逐条建「下发·已完成」队列行（发布即热推，无需再过逐台拓印门）；返回新建行 id
  function enqueuePublished(names: string[]): string[] {
    const base = Date.now()
    const rows: SyncQueueRow[] = names.map((name, i) => ({
      id: `pub-${base}-${i}`,
      name,
      direction: 'push',
      status: 'done',
      scopeTarget: t('configs.workbench.publishScopeTarget'),
      sourcePath: `prod/main/${name}`,
      targetPath: t('configs.workbench.publishTargetPath'),
      time: nowTime(),
    }))
    setExtraQueue((prev) => [...rows, ...prev])
    return rows.map((r) => r.id)
  }

  // ---- 队列批量审核（改进 4）：选中待审行 → 批量审核浮层；全部通过 → 一并转完成 ----
  function onBatchReviewConfirm() {
    if (batchReview) {
      setDoneIds((prev) => {
        const next = new Set(prev)
        for (const r of batchReview) next.add(r.id)
        return next
      })
    }
    setBatchReview(null)
    setQueueSel(new Set())
  }

  // 第二层页眉：标题 + 图例（同步状态 / 覆盖层，小字）+ 主操作（导入 / 反向抓取 / 新建，mock 触发）
  usePageHeader({
    title: t('configs.title'),
    envScoped: true,
    subtitle: <WorkbenchLegend />,
    actions: (
      <div className="flex items-center gap-2">
        <Button variant="outline" size="xs" className="h-7 text-xs" onClick={() => msg.showSuccess(t('configs.workbench.actImport'))}>
          {t('configs.importBtn')}
        </Button>
        <Button variant="outline" size="xs" className="h-7 text-xs" onClick={() => msg.showSuccess(t('configs.workbench.actReverseFetch'))}>
          {t('configs.reverseFetchBtn')}
        </Button>
        <Button size="xs" className="h-7 text-xs" onClick={() => msg.showSuccess(t('configs.workbench.actCreate'))}>
          {t('configs.createBtn')}
        </Button>
      </div>
    ),
  })

  const loading = managed.isLoading || server.isLoading
  const onToolbar = (action: ToolbarAction) => {
    const hintKey: Record<ToolbarAction, string> = {
      up: 'configs.workbench.toolbarUpHint',
      refresh: 'configs.workbench.toolbarRefreshHint',
      new: 'configs.workbench.toolbarNewHint',
      search: 'configs.workbench.toolbarSearchHint',
    }
    msg.showSuccess(t(hintKey[action]))
  }

  return (
    // relative：作为浮层（absolute）的定位包含块；h-full + flex-col 固定整体高度（改进 1）
    <div ref={rootRef} className="relative flex h-full min-h-0 flex-col gap-2 overflow-hidden">
      {/* ===== 顶部固定状态栏（常驻、固定高度）：选中态 + 发布/抓取选中 + 撤回上一步；
              未选中时显操作提示。常驻不改布局，避免挤动面板选错文件。 ===== */}
      <SelectionStatusBar
        selSide={selSide}
        count={selSide === 'managed' ? selManaged.size : selServer.size}
        fetchScopeLabel={options.data?.scopes.find((s) => s.value === scope)?.label ?? scope}
        onPublish={askPublishSelected}
        onFetch={askFetchSelected}
        onClear={() => {
          setSelManaged(new Set())
          setSelServer(new Set())
        }}
        canUndoLast={logRows.some((e) => !e.undone)}
        onUndoLast={() => {
          const last = logRows.find((e) => !e.undone)
          if (last) undoOps([last.id])
        }}
      />

      {/* ===== 双面板（选中驱动操作在顶部固定状态栏，不在面板上方插入动态条、避免挤动布局选错文件）===== */}
      <DndContext sensors={sensors} onDragStart={onDragStart} onDragEnd={onDragEnd}>
        <div className="flex min-h-0 flex-1 gap-0">
          {/* 左：受管配置 */}
          <Panel
            side="managed"
            icon={<FolderTree className="h-4 w-4 text-muted-foreground" />}
            title={t('configs.workbench.managedTitle')}
            headerRight={
              <div className="flex items-center gap-2">
                {/* 视图切换：配置树 / 生效预览 */}
                <ViewToggle value={managedView} onChange={setManagedView} />
                <ChipSelect
                  value={scope}
                  options={(options.data?.scopes ?? []).map((s) => ({ value: s.value, label: s.label }))}
                  onChange={setScope}
                />
              </div>
            }
            segments={['plugins']}
            showNew
            onToolbar={onToolbar}
            cols={
              managedView === 'tree'
                ? [
                    { label: t('configs.workbench.colOverride'), width: 'w-12' },
                    { label: t('configs.workbench.colVersion'), width: 'w-10' },
                    { label: t('configs.workbench.colModified'), width: 'w-20' },
                  ]
                : []
            }
            loading={loading}
          >
            {managedView === 'effective' ? (
              <EffectivePreviewView serverId={serverId} />
            ) : (
              managed.data && (
                <PanelTree
                  nodes={managed.data as PanelNode[]}
                  side="managed"
                  onOpenFile={onDoubleClickFile}
                  getDot={(n) => managedDot(n as ManagedNode)}
                  colWidths={['w-12', 'w-10', 'w-20']}
                  renderCols={(n) => managedCols(n as ManagedNode, t)}
                  expanded={expManaged}
                  onToggleExpand={(k) => toggleExpand('managed', k)}
                  selected={selManaged}
                  onSelectFile={(k, mods) => selectFile('managed', k, mods)}
                  onContextMenu={onContextMenu}
                />
              )
            )}
          </Panel>

          {/* 中间视觉分隔（不再放操作按钮，改进 2）*/}
          <div className="w-3 shrink-0" />

          {/* 右：服务器 */}
          <Panel
            side="server"
            icon={<Server className="h-4 w-4 text-muted-foreground" />}
            title={t('configs.workbench.serverTitle')}
            headerRight={
              <ChipSelect
                value={serverId}
                options={(options.data?.servers ?? []).map((s) => ({
                  value: s.serverId,
                  label: (
                    <span className="flex items-center gap-1.5">
                      <span className={cn('h-1.5 w-1.5 rounded-full', s.online ? 'bg-emerald-500' : 'bg-muted-foreground/40')} />
                      {s.label}
                    </span>
                  ),
                }))}
                onChange={setServerId}
                leading={<span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />}
              />
            }
            segments={['plugins']}
            onToolbar={onToolbar}
            cols={[
              { label: t('configs.workbench.colSize'), width: 'w-16' },
              { label: t('configs.workbench.colType'), width: 'w-16' },
              { label: t('configs.workbench.colModified'), width: 'w-20' },
            ]}
            loading={loading}
          >
            {server.data && (
              <PanelTree
                nodes={server.data as PanelNode[]}
                side="server"
                onOpenFile={onDoubleClickFile}
                getDot={(n) => serverDot(n as ServerNode)}
                colWidths={['w-16', 'w-16', 'w-20']}
                renderCols={(n) => serverCols(n as ServerNode)}
                expanded={expServer}
                onToggleExpand={(k) => toggleExpand('server', k)}
                selected={selServer}
                onSelectFile={(k, mods) => selectFile('server', k, mods)}
                onContextMenu={onContextMenu}
              />
            )}
          </Panel>
        </div>

        {/* 拖拽浮层 */}
        <DragOverlay>
          {draggingName ? (
            <div className="rounded-md border border-primary/50 bg-card px-2 py-1 text-xs font-mono shadow-lg">
              {draggingName}
            </div>
          ) : null}
        </DragOverlay>
      </DndContext>

      {/* ===== 底部 dock（固定高度、内部滚）：tab 切换 同步队列 / 操作日志 ===== */}
      <BottomDock
        queueRows={queueRows}
        onReview={(row) => setReviewRow(row)}
        queueSel={queueSel}
        onToggleQueueSel={(id) =>
          setQueueSel((prev) => {
            const next = new Set(prev)
            if (next.has(id)) next.delete(id)
            else next.add(id)
            return next
          })
        }
        onBatchReview={() => {
          // 取当前队列里仍待审且被选中的行，传入批量审核浮层
          const picked = queueRows.filter((r) => queueSel.has(r.id) && r.status !== 'done')
          if (picked.length > 0) setBatchReview(picked)
        }}
        logEntries={logRows}
        logSel={logSel}
        onToggleLogSel={(id) =>
          setLogSel((prev) => {
            const next = new Set(prev)
            if (next.has(id)) next.delete(id)
            else next.add(id)
            return next
          })
        }
        onUndo={(id) => undoOps([id])}
        onBatchUndo={() => undoOps([...logSel])}
      />

      {/* ===== 右键菜单（容器内 absolute 定位）===== */}
      {ctxMenu && <ContextMenu state={ctxMenu} onAction={onCtxAction} onClose={() => setCtxMenu(null)} />}

      {/* ===== 二次确认弹窗（DestructiveConfirmDialog，FR-76 复用）===== */}
      {confirm && (
        <DestructiveConfirmDialog
          open
          onOpenChange={(o) => !o && setConfirm(null)}
          title={confirmTitle(confirm, t)}
          description={confirmDesc(confirm, t)}
          confirmLabel={confirmLabel(confirm, t)}
          impacts={confirmImpacts(confirm, t)}
          onConfirm={onConfirmDone}
        />
      )}

      {/* ===== 发布 + 影响面板（改进 1）：受管选中 → 按覆盖层发布热推到受影响在线服 ===== */}
      {publishNames && (
        <PublishPanel names={publishNames} onPublish={onPublishConfirm} onCancel={() => setPublishNames(null)} />
      )}

      {/* ===== 队列批量审核浮层（改进 4）：选中多个待审项 → 批量看 diff/清单 + 全部通过 ===== */}
      {batchReview && (
        <BatchReviewOverlay rows={batchReview} onConfirm={onBatchReviewConfirm} onCancel={() => setBatchReview(null)} />
      )}

      {/* ===== 队列待审核浮层：抓取→ingest 审核 / 下发→拓印 diff 审核 ===== */}
      {reviewRow && reviewRow.direction === 'fetch' && (
        <IngestReviewOverlay queueName={reviewRow.name} onConfirm={onReviewConfirm} onCancel={() => setReviewRow(null)} />
      )}
      {reviewRow && reviewRow.direction === 'push' && (
        <ImprintReviewOverlay queueName={reviewRow.name} onConfirm={onReviewConfirm} onCancel={() => setReviewRow(null)} />
      )}

      {/* ===== 悬浮覆盖编辑器（双击文件后绝对覆盖在本容器之上）===== */}
      {activeKey && tabs.length > 0 && (
        <EditorOverlay
          tabs={tabs}
          activeKey={activeKey}
          onActivate={setActiveKey}
          onClose={closeOverlay}
          onCloseTab={closeTab}
          onSyncUrl={syncUrl}
        />
      )}
    </div>
  )
}

// 现在时间（同步队列行用，HH:mm:ss）
function nowTime(): string {
  return new Date().toLocaleTimeString('zh-CN', { hour12: false })
}

// 受管侧行首点：同步状态（文件用自身、文件夹用聚合）
function managedDot(node: ManagedNode): DotMeta {
  return SYNC_META[node.sync]
}
// 服务器侧行首点：纳管标记
function serverDot(node: ServerNode): DotMeta {
  return SERVER_MARK_META[node.mark]
}

// 受管侧三列：覆盖层 badge | 版本 | 修改时间（仅文件，文件夹返回空列占位）
function managedCols(node: ManagedNode, t: (k: string) => string) {
  if (node.type !== 'file' || !node.scope) return [null, null, null]
  const meta = SCOPE_META[node.scope]
  return [
    <Badge key="ov" variant="outline" className={cn('h-4 px-1 text-[0.6rem]', meta.badgeClass)}>
      {t(meta.labelKey)}
    </Badge>,
    <span key="ver" className="text-[0.65rem]">v{node.version}</span>,
    <span key="mod" className="text-[0.65rem]">{node.modifiedAt}</span>,
  ]
}

// 服务器侧三列：大小 | 类型 | 修改时间（仅文件）
function serverCols(node: ServerNode) {
  if (node.type !== 'file') return [null, null, null]
  return [
    <span key="size" className="text-[0.65rem]">{node.size}</span>,
    <span key="type" className="text-[0.65rem]">{node.fileType}</span>,
    <span key="mod" className="text-[0.65rem]">{node.modifiedAt}</span>,
  ]
}

// ---- 二次确认文案 ----
function confirmTitle(c: ConfirmState, t: (k: string, o?: Record<string, unknown>) => string): string {
  switch (c.kind) {
    case 'transfer-fetch':
      return t('configs.workbench.confirmFetchTitle', { count: c.names.length })
    case 'transfer-push':
      return t('configs.workbench.confirmPushTitle', { count: c.names.length })
    case 'delete':
      return t('configs.workbench.confirmDeleteTitle', { count: c.names.length })
    case 'rename':
      return t('configs.workbench.confirmRenameTitle', { name: c.names[0] ?? '' })
    case 'new':
      return t('configs.workbench.confirmNewTitle')
    case 'move':
      return t('configs.workbench.confirmMoveTitle', { name: c.names[0] ?? '', target: c.target })
  }
}
function confirmDesc(c: ConfirmState, t: (k: string) => string): string {
  switch (c.kind) {
    case 'transfer-fetch':
      return t('configs.workbench.confirmFetchDesc')
    case 'transfer-push':
      return t('configs.workbench.confirmPushDesc')
    case 'delete':
      return t('configs.workbench.confirmDeleteDesc')
    case 'rename':
      return t('configs.workbench.confirmRenameDesc')
    case 'new':
      return t('configs.workbench.confirmNewDesc')
    case 'move':
      return t('configs.workbench.confirmMoveDesc')
  }
}
function confirmLabel(c: ConfirmState, t: (k: string) => string): string {
  switch (c.kind) {
    case 'transfer-fetch':
      return t('configs.workbench.confirmFetchBtn')
    case 'transfer-push':
      return t('configs.workbench.confirmPushBtn')
    case 'delete':
      return t('configs.workbench.confirmDeleteBtn')
    case 'rename':
      return t('configs.workbench.confirmRenameBtn')
    case 'new':
      return t('configs.workbench.confirmNewBtn')
    case 'move':
      return t('configs.workbench.confirmMoveBtn')
  }
}
// 影响摘要：哪些文件 + 落哪覆盖层/哪服 + 拓印门提示
function confirmImpacts(c: ConfirmState, t: (k: string, o?: Record<string, unknown>) => string): string[] {
  const fileLine = t('configs.workbench.impactFiles', { files: c.names.join('、') })
  switch (c.kind) {
    case 'transfer-fetch':
      return [fileLine, t('configs.workbench.impactFetchScope', { scope: c.target }), t('configs.workbench.impactFetchIngest')]
    case 'transfer-push':
      return [fileLine, t('configs.workbench.impactPushTarget', { target: c.target }), t('configs.workbench.impactPushImprint')]
    case 'delete':
      return [fileLine, t('configs.workbench.impactDeleteWhere', { where: c.target })]
    case 'rename':
      return [fileLine]
    case 'new':
      return []
    case 'move':
      return [fileLine, t('configs.workbench.impactMoveTarget', { target: c.target })]
  }
}

// ---- 面板容器（受管 / 服务器共用骨架，含工具栏 + 地址栏 + 列头 + 可放置区） ----

interface ColDef {
  label: string
  width: string
}

function Panel({
  side,
  icon,
  title,
  headerRight,
  segments,
  showNew,
  onToolbar,
  cols,
  loading,
  children,
}: {
  side: 'managed' | 'server'
  icon: React.ReactNode
  title: string
  headerRight: React.ReactNode
  segments: string[]
  showNew?: boolean
  onToolbar: (action: ToolbarAction) => void
  cols: ColDef[]
  loading: boolean
  children: React.ReactNode
}) {
  const { t } = useTranslation()
  const nameLabel = t('configs.workbench.colName')
  // 整个面板体作为 @dnd-kit 放置区（drop-managed / drop-server）
  const { setNodeRef, isOver } = useDroppable({ id: `drop-${side}` })
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border bg-card">
      {/* 头部：图标 + 标题 + 右侧 chip */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-3 py-2">
        {icon}
        <span className="text-xs font-medium text-foreground">{title}</span>
        <div className="ml-auto">{headerRight}</div>
      </div>
      {/* 工具栏行（上级 / 刷新 / 新建 / 搜索）+ 地址面包屑栏 */}
      <PanelToolbar segments={segments} showNew={showNew} onAction={onToolbar} onCrumb={() => onToolbar('up')} />
      {/* 列头（名称 + 元信息列，与行等宽对齐）；生效预览模式下无元信息列 */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/20 px-2 py-1 text-[0.65rem] font-medium text-muted-foreground">
        <span className="min-w-0 flex-1">{nameLabel}</span>
        {cols.map((c, i) => (
          <span key={i} className={cn('shrink-0 text-right', c.width)}>
            {c.label}
          </span>
        ))}
      </div>
      {/* 树（放置区，固定高度内部滚） */}
      <div
        ref={setNodeRef}
        className={cn('min-h-0 flex-1 overflow-y-auto scrollbar-hide px-1', isOver && 'bg-primary/5 ring-1 ring-inset ring-primary/30')}
      >
        {loading ? (
          <div className="space-y-2 px-3 py-2">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className={`h-4 ${i % 3 === 0 ? 'w-32' : 'w-24 ml-3'}`} />
            ))}
          </div>
        ) : (
          children
        )}
      </div>
    </div>
  )
}

// 页眉小字图例（同步状态四态 + 覆盖层徽标）：随「配置中心」标题后用小字展示（PageHeader subtitle 槽）。
// 仅用 span（subtitle 外层为 span，避免 div 嵌套不合法）；操作用法提示移至顶部状态栏未选中态。
function WorkbenchLegend() {
  const { t } = useTranslation()
  return (
    <span className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-[0.7rem] text-muted-foreground">
      <span className="font-medium">{t('configs.workbench.legendSyncTitle')}</span>
      {SYNC_LEGEND_META.map((m) => (
        <span key={m.labelKey} className="flex items-center gap-1">
          <m.icon className={cn('h-3 w-3', m.iconClass, m.spin && 'animate-spin')} />
          {t(m.labelKey)}
        </span>
      ))}
      <span className="ml-1 font-medium">{t('configs.workbench.legendScopeTitle')}</span>
      {SCOPE_LEGEND.map((s) => (
        <span key={s} className={cn('inline-flex h-4 items-center rounded border px-1 text-[0.6rem]', SCOPE_META[s].badgeClass)}>
          {t(SCOPE_META[s].labelKey)}
        </span>
      ))}
    </span>
  )
}

// 受管面板视图切换：配置树 / 生效预览
function ViewToggle({ value, onChange }: { value: ManagedView; onChange: (v: ManagedView) => void }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center rounded-md border border-border bg-background p-0.5 text-[0.7rem]">
      <ToggleBtn active={value === 'tree'} onClick={() => onChange('tree')} icon={<FolderTree className="h-3 w-3" />}>
        {t('configs.workbench.viewTree')}
      </ToggleBtn>
      <ToggleBtn active={value === 'effective'} onClick={() => onChange('effective')} icon={<ListTree className="h-3 w-3" />}>
        {t('configs.workbench.viewEffective')}
      </ToggleBtn>
    </div>
  )
}

function ToggleBtn({
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
        'flex items-center gap-1 rounded px-1.5 py-0.5 transition-colors',
        active ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {icon}
      {children}
    </button>
  )
}
