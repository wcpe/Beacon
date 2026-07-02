/**
 * 配置中心新版页（批1，前端骨架 + 有状态 mock，路由 /configs）。
 *
 * - 服务器列表走真实 listInstances（与全站 1000 服同源）；配置内容为本地 mock（批0 行级合并 ADR 后接真）。
 * - 统一模型：每个文件都有 全局/大区/小区/单服 四层（每层文本）；不论扁平还是嵌套，四视角一致。
 * - 视角：生效（逐行来源染色）/ 层列 / 补丁栈（抽屉）/ 对盘（Monaco diff）。
 * - 浮窗编辑器：多标签 + 拖拽 + 调整大小 + 全屏 + 收缩到右下角 + diff；编辑某层文本，Ctrl+S 临时保存（非发布）。
 * - 同步队列：三类 tab，待审核可点开审核纳管。
 */

import { useEffect, useMemo, useReducer, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowRightLeft,
  Ban,
  Check,
  ChevronDown,
  Columns3,
  Database,
  Download,
  Eye,
  FileCode,
  FileText,
  FilterX,
  FlaskConical,
  FolderTree,
  Globe,
  GitCompare,
  History,
  Inbox,
  ListChecks,
  Lock,
  Maximize,
  Minimize2,
  Minus,
  Pencil,
  Plus,
  Rocket,
  RotateCcw,
  Save,
  Search,
  Server,
  SquarePen,
  Trash2,
  X,
} from 'lucide-react'

import { listInstances } from '@/api/client'
import type { InstanceView } from '@/api/types'
import Editor, { type OnMount } from '@monaco-editor/react'
import type { editor } from 'monaco-editor'
import { usePageHeader } from '@/components/PageHeader'
import CodeEditor from '@/components/CodeEditor'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useMessage } from '@/components/useMessage'
import { cn } from '@/lib/utils'

import {
  columnRows,
  deepSetText,
  DEFAULT_FILE_PATH,
  DEMO_SERVER,
  dirFileRows,
  diskText,
  driftCount,
  driftRows,
  effectiveSource,
  flatEffective,
  fileLang,
  initialExcludeRules,
  isBinaryName,
  isDiskFile,
  langOfName,
  lineSources,
  parseFlat,
  provenanceMap,
  pruneTree,
  topSourceLayer,
  initialFiles,
  initialQueue,
  isExcluded,
  layerText,
  LAYER_LABEL,
  LAYER_ORDER,
  REVISION_SNAPSHOTS,
  TREE,
  textDiff,
  unmanagedText,
  type ExcludeRule,
  type LayerLevel,
  type MockFile,
  type QueueItem,
  type QueueKind,
  type TreeNode,
} from './configCenterMock'

type Mode = 'effective' | 'columns' | 'drift'

const L: Record<LayerLevel, { dot: string; chip: string; barL: string; wrap: string; text: string }> = {
  global: { dot: 'bg-slate-400', chip: 'border-slate-300 text-slate-500 dark:border-slate-600 dark:text-slate-300', barL: 'border-l-slate-400', wrap: 'border-slate-300/70 bg-slate-500/5 dark:border-slate-600/60', text: 'text-slate-600 dark:text-slate-300' },
  area: { dot: 'bg-blue-500', chip: 'border-blue-400/60 text-blue-600 dark:text-blue-400', barL: 'border-l-blue-500', wrap: 'border-blue-400/50 bg-blue-500/5', text: 'text-blue-600 dark:text-blue-400' },
  zone: { dot: 'bg-amber-500', chip: 'border-amber-400/60 text-amber-600 dark:text-amber-400', barL: 'border-l-amber-500', wrap: 'border-amber-400/50 bg-amber-500/5', text: 'text-amber-600 dark:text-amber-400' },
  server: { dot: 'bg-emerald-500', chip: 'border-emerald-400/60 text-emerald-600 dark:text-emerald-400', barL: 'border-l-emerald-500', wrap: 'border-emerald-400/60 bg-emerald-500/10', text: 'text-emerald-600 dark:text-emerald-400' },
}

const MODES: { id: Mode; label: string; icon: typeof Globe }[] = [
  { id: 'effective', label: '编辑', icon: Pencil },
  { id: 'columns', label: '层列', icon: Columns3 },
  { id: 'drift', label: '对盘', icon: ArrowRightLeft },
]

interface SrvMeta {
  id: string
  group: string
  zone: string
  online: boolean
}

// 各层的作用范围（覆盖到多少台服务器）：用于给发布 / 写入层等操作显示「影响范围」
function layerScopeCounts(servers: SrvMeta[], group: string, zone: string): Record<LayerLevel, { total: number; online: number }> {
  const count = (pred: (s: SrvMeta) => boolean) => { const list = servers.filter(pred); return { total: list.length, online: list.filter((s) => s.online).length } }
  return {
    global: count(() => true),
    area: count((s) => s.group === group),
    zone: count((s) => s.group === group && s.zone === zone),
    server: { total: 1, online: 1 },
  }
}

interface EditorTab {
  id: string
  path: string
  title: string
  kind: 'layer' | 'diff' | 'super'
  layer?: LayerLevel
  panes?: string[]
  draft: string
  lang: string
  original?: string
  modified?: string
  diffVersion?: number
  dirty: boolean
}

interface State {
  files: Record<string, MockFile>
  excludeRules: ExcludeRule[]
  queue: QueueItem[]
  selectedPath: string
  lookServer: string
  group: string
  zone: string
  writeLayer: LayerLevel
  mode: Mode
  // 目录递归视图：非 null 时主区显示该目录的递归对盘（'' = 整个根目录）
  dirView: string | null
  // 目录级弹层（抓取 / 发布）作用于哪个目录
  dirTarget: string | null
  // 左侧树视图：受管库（只显已纳管）/ 磁盘（服务器真实磁盘全量）/ 暂存区（待发布草稿）
  treeView: 'managed' | 'disk' | 'staged'
  // 暂存区（待发布草稿）：抓取 / 编辑过、尚未发布的文件路径。整批统一发布到当前写入层（一次只发一个层）
  staged: string[]
  dialog: null | 'publish' | 'exclude' | 'scan-dir' | 'publish-dir' | 'publish-staged'
  tabs: EditorTab[]
  activeTab: string | null
  rollback: number | null
  review: number | null
  seqQueue: number
  seqRule: number
  seqTab: number
  // 外部编辑（浮窗保存 / 收编 / 回滚）后自增，用于刷新中间生效编辑器
  syncCounter: number
}

type Action =
  | { t: 'select'; path: string }
  | { t: 'selectDir'; path: string }
  | { t: 'treeView'; view: 'managed' | 'disk' | 'staged' }
  | { t: 'ingestDir'; dir: string; paths: string[] }
  | { t: 'publishDir'; dir: string; paths: string[]; gray: boolean; cohort: number }
  | { t: 'openDirDialog'; dialog: 'scan-dir' | 'publish-dir'; dir: string }
  | { t: 'publishStaged' }
  | { t: 'server'; id: string; group: string; zone: string }
  | { t: 'layer'; level: LayerLevel }
  | { t: 'mode'; mode: Mode }
  | { t: 'editPath'; path: string; value: string }
  | { t: 'editWhole'; layer: LayerLevel; text: string }
  | { t: 'ingest' }
  | { t: 'publish'; gray: boolean; cohort: number }
  | { t: 'rollbackAsk'; version: number }
  | { t: 'rollbackDo'; version: number }
  | { t: 'rollbackCancel' }
  | { t: 'dialog'; dialog: State['dialog'] }
  | { t: 'addExclude'; pattern: string }
  | { t: 'removeExclude'; id: string }
  | { t: 'toggleExclude'; id: string; field: 'scan' | 'sync' | 'manage' }
  | { t: 'openEdit'; path: string; layer?: LayerLevel }
  | { t: 'openDiff'; path: string; version: number }
  | { t: 'openSuper'; path: string }
  | { t: 'superToggle'; id: string; key: string }
  | { t: 'tabChange'; id: string; text: string }
  | { t: 'tabSave'; id: string }
  | { t: 'tabClose'; id: string }
  | { t: 'tabCloseOthers'; id: string }
  | { t: 'tabActivate'; id: string }
  | { t: 'editorClose' }
  | { t: 'reviewOpen'; id: number }
  | { t: 'reviewApprove'; id: number }
  | { t: 'reviewClose' }
  | { t: 'grayPromote'; id: number }
  | { t: 'grayAbort'; id: number }
  | { t: 'tick' }

function init(): State {
  return {
    files: initialFiles(),
    excludeRules: initialExcludeRules(),
    queue: initialQueue(),
    selectedPath: DEFAULT_FILE_PATH,
    lookServer: DEMO_SERVER,
    group: 'server-a',
    zone: 'zone-01',
    writeLayer: 'server',
    mode: 'effective',
    dirView: null,
    dirTarget: null,
    treeView: 'disk',
    staged: [],
    dialog: null,
    tabs: [],
    activeTab: null,
    rollback: null,
    review: null,
    seqQueue: 100,
    seqRule: 100,
    seqTab: 1,
    syncCounter: 0,
  }
}

function setLayerText(file: MockFile, layer: LayerLevel, server: string, text: string): MockFile {
  if (layer === 'server') return { ...file, server: { ...(file.server ?? {}), [server]: text } }
  return { ...file, [layer]: text }
}

function pushQueue(s: State, item: Omit<QueueItem, 'id' | 'time'>): QueueItem[] {
  return [{ id: s.seqQueue, time: '刚刚', ...item }, ...s.queue]
}

// 把某文件标记进暂存区（待发布草稿）
function stage(s: State, path: string): Pick<State, 'staged'> {
  return { staged: s.staged.includes(path) ? s.staged : [...s.staged, path] }
}

// 发布 / 放弃后把这些路径移出暂存区
function unstagePaths(s: State, paths: string[]): Pick<State, 'staged'> {
  const drop = new Set(paths)
  return { staged: s.staged.filter((p) => !drop.has(p)) }
}

function reducer(s: State, a: Action): State {
  switch (a.t) {
    case 'select':
      return { ...s, selectedPath: a.path, dirView: null }
    case 'selectDir':
      return { ...s, dirView: a.path }
    case 'treeView':
      return { ...s, treeView: a.view }
    case 'ingestDir': {
      // 整目录递归抓取入库 / 收编：已纳管且有漂移 → 磁盘内容收编为单服补丁；未纳管 → 以磁盘内容纳入受管库（全局基线）
      const files = { ...s.files }
      const staged = [...s.staged]
      const addStage = (p: string) => { if (!staged.includes(p)) staged.push(p) }
      let managed = 0
      let onboarded = 0
      for (const p of a.paths) {
        const f = files[p]
        if (f) {
          if (f.disk?.[s.lookServer] === undefined) continue
          const nf = setLayerText(f, 'server', s.lookServer, f.disk[s.lookServer])
          const disk = { ...(nf.disk ?? {}) }
          delete disk[s.lookServer]
          files[p] = { ...nf, disk }
          addStage(p)
          managed++
        } else {
          const text = unmanagedText(p, s.lookServer)
          if (text === null) continue
          const name = p.split('/').pop() ?? p
          files[p] = { path: p, name, kind: name.endsWith('.json') ? 'config' : langOfName(name) === 'plaintext' ? 'text' : 'config', global: text, version: 1 }
          addStage(p)
          onboarded++
        }
      }
      const dirLabel = a.dir === '' ? '根目录' : a.dir
      // 抓取后落地处：暂存区（待发布草稿）。左侧自动切到暂存区树，整批统一发布到当前写入层
      const focus = a.paths.find((p) => files[p]) ?? s.selectedPath
      return { ...s, files, staged, dialog: null, dirView: null, treeView: 'staged', selectedPath: focus, queue: pushQueue(s, { kind: 'fetch', title: `整目录抓取 ${dirLabel}`, detail: `暂存 ${a.paths.length} 个（新纳管 ${onboarded} · 收编 ${managed}）`, state: '已暂存', done: true, operator: 'admin', target: `${a.paths.length} 个文件` }), seqQueue: s.seqQueue + 1, syncCounter: s.syncCounter + 1 }
    }
    case 'publishDir': {
      // 整目录批量发布：把选中的已纳管文件统一升版热推
      const files = { ...s.files }
      for (const p of a.paths) { const f = files[p]; if (f) files[p] = { ...f, version: (f.version ?? 1) + 1 } }
      const dirLabel = a.dir === '' ? '根目录' : a.dir
      const item: Omit<QueueItem, 'id' | 'time'> = a.gray
        ? { kind: 'publish', title: `整目录灰度 ${dirLabel}`, detail: `${a.paths.length} 个文件 · cohort ${a.cohort} 台先收 · 校验通过`, state: '灰度中', operator: 'admin', target: `cohort ${a.cohort} 台` }
        : { kind: 'publish', title: `整目录发布 ${dirLabel} → ${LAYER_LABEL[s.writeLayer].label}`, detail: `${a.paths.length} 个文件 · 热推在线服 · 校验通过`, state: '推送中', progress: 5, operator: 'admin', target: LAYER_LABEL[s.writeLayer].label }
      return { ...s, files, ...unstagePaths(s, a.paths), dialog: null, queue: pushQueue(s, item), seqQueue: s.seqQueue + 1 }
    }
    case 'openDirDialog':
      return { ...s, dialog: a.dialog, dirTarget: a.dir }
    case 'publishStaged': {
      // 一键发布全部暂存：整批统一发布到当前写入层（一次只发一个层），升版热推
      const paths = s.staged.filter((p) => s.files[p])
      if (paths.length === 0) return s
      const files = { ...s.files }
      for (const p of paths) { const f = files[p]; if (f) files[p] = { ...f, version: (f.version ?? 1) + 1 } }
      return { ...s, files, ...unstagePaths(s, paths), dialog: null, queue: pushQueue(s, { kind: 'publish', title: `发布暂存区 ${paths.length} 个文件 → ${LAYER_LABEL[s.writeLayer].label}`, detail: `统一发布到 ${LAYER_LABEL[s.writeLayer].label} 层 · 热推在线服 · 校验通过`, state: '推送中', progress: 5, operator: 'admin', target: LAYER_LABEL[s.writeLayer].label }), seqQueue: s.seqQueue + 1 }
    }
    case 'server':
      return { ...s, lookServer: a.id, group: a.group, zone: a.zone }
    case 'layer':
      return { ...s, writeLayer: a.level }
    case 'mode':
      return { ...s, mode: a.mode }
    case 'editPath': {
      const f = s.files[s.selectedPath]
      if (!f) return s
      const src = flatEffective(f, s.group, s.zone, s.lookServer).find((e) => e.path === a.path)?.source ?? s.writeLayer
      const cur = layerText(f, src, s.group, s.zone, s.lookServer) ?? ''
      const nf = setLayerText(f, src, s.lookServer, deepSetText(cur, a.path, a.value, fileLang(f)))
      return { ...s, files: { ...s.files, [f.path]: nf }, ...stage(s, f.path) }
    }
    case 'editWhole': {
      const f = s.files[s.selectedPath]
      if (!f) return s
      return { ...s, files: { ...s.files, [f.path]: setLayerText(f, a.layer, s.lookServer, a.text) }, ...stage(s, f.path) }
    }
    case 'ingest': {
      const f = s.files[s.selectedPath]
      if (!f || f.disk?.[s.lookServer] === undefined) return s
      const n = driftCount(f, s.group, s.zone, s.lookServer)
      const nf = setLayerText(f, 'server', s.lookServer, f.disk[s.lookServer])
      const disk = { ...(nf.disk ?? {}) }
      delete disk[s.lookServer]
      const cleared = { ...nf, disk }
      return { ...s, files: { ...s.files, [f.path]: cleared }, ...stage(s, f.path), treeView: 'staged', queue: pushQueue(s, { kind: 'fetch', title: `收编 ${s.lookServer} / ${f.name}`, detail: `${n} 处磁盘改动 → 暂存（待发布）`, state: '已暂存', done: true, operator: 'admin', target: `单服 ${s.lookServer}` }), seqQueue: s.seqQueue + 1, syncCounter: s.syncCounter + 1 }
    }
    case 'publish': {
      const f = s.files[s.selectedPath]
      if (!f) return s
      const nf = { ...f, version: (f.version ?? 1) + 1 }
      const item: Omit<QueueItem, 'id' | 'time'> = a.gray
        ? { kind: 'publish', title: `灰度 ${f.name} → cohort`, detail: `v${f.version}→v${nf.version} · cohort ${a.cohort} 台先收 · 校验通过`, state: '灰度中', operator: 'admin', target: `cohort ${a.cohort} 台` }
        : { kind: 'publish', title: `发布 ${f.name} → ${LAYER_LABEL[s.writeLayer].label}`, detail: `v${f.version}→v${nf.version} · 热推在线服 · 校验通过`, state: '推送中', progress: 5, operator: 'admin', target: LAYER_LABEL[s.writeLayer].label }
      return { ...s, files: { ...s.files, [f.path]: nf }, ...unstagePaths(s, [f.path]), queue: pushQueue(s, item), seqQueue: s.seqQueue + 1, dialog: null }
    }
    case 'rollbackAsk':
      return { ...s, rollback: a.version }
    case 'rollbackCancel':
      return { ...s, rollback: null }
    case 'rollbackDo': {
      const f = s.files[s.selectedPath]
      if (!f) return s
      // 回滚：读目标版本内容发为新版本。有历史快照则真实还原全局层内容；无快照文件回滚到全局基线
      const target = REVISION_SNAPSHOTS[f.path]?.[a.version]
      const nf = { ...f, version: (f.version ?? 1) + 1, ...(target !== undefined ? { global: target } : {}) }
      return { ...s, files: { ...s.files, [f.path]: nf }, rollback: null, queue: pushQueue(s, { kind: 'audit', title: `回滚 ${f.name} → v${a.version}`, detail: `读 v${a.version} 内容发为 v${nf.version} · 热推在线服`, state: '已回滚', done: true, operator: 'admin', target: f.path }), seqQueue: s.seqQueue + 1, syncCounter: s.syncCounter + 1 }
    }
    case 'dialog':
      return { ...s, dialog: a.dialog }
    case 'addExclude':
      return { ...s, excludeRules: [...s.excludeRules, { id: `r${s.seqRule}`, pattern: a.pattern, scope: '全局', scan: true, sync: true, manage: true }], seqRule: s.seqRule + 1 }
    case 'removeExclude':
      return { ...s, excludeRules: s.excludeRules.filter((r) => r.id !== a.id) }
    case 'toggleExclude':
      return { ...s, excludeRules: s.excludeRules.map((r) => (r.id === a.id ? { ...r, [a.field]: !r[a.field] } : r)) }
    case 'openEdit': {
      const f = s.files[a.path]
      if (!f) return s
      const layer = a.layer ?? s.writeLayer
      const existing = s.tabs.find((tb) => tb.path === a.path && tb.kind === 'layer' && tb.layer === layer)
      if (existing) return { ...s, selectedPath: a.path, activeTab: existing.id }
      const draft = layerText(f, layer, s.group, s.zone, s.lookServer) ?? ''
      const tab: EditorTab = { id: `tab-${s.seqTab}`, path: a.path, title: `${f.name} · ${LAYER_LABEL[layer].short}`, kind: 'layer', layer, draft, lang: fileLang(f), dirty: false }
      return { ...s, selectedPath: a.path, tabs: [...s.tabs, tab], activeTab: tab.id, seqTab: s.seqTab + 1 }
    }
    case 'openDiff': {
      const f = s.files[a.path]
      if (!f) return s
      const snaps = REVISION_SNAPSHOTS[a.path] ?? {}
      const cur = snaps[f.version ?? 0] ?? f.global
      const old = snaps[a.version] ?? ''
      const tab: EditorTab = { id: `tab-${s.seqTab}`, path: a.path, title: `${f.name} v${a.version} diff`, kind: 'diff', draft: old, lang: fileLang(f), original: cur, modified: old, diffVersion: a.version, dirty: false }
      return { ...s, tabs: [...s.tabs, tab], activeTab: tab.id, seqTab: s.seqTab + 1 }
    }
    case 'openSuper': {
      const f = s.files[a.path]
      if (!f) return s
      const tab: EditorTab = { id: `tab-${s.seqTab}`, path: a.path, title: `${f.name} 超级diff`, kind: 'super', draft: '', lang: fileLang(f), panes: ['global', 'server'], dirty: false }
      return { ...s, tabs: [...s.tabs, tab], activeTab: tab.id, seqTab: s.seqTab + 1 }
    }
    case 'superToggle':
      return { ...s, tabs: s.tabs.map((tb) => { if (tb.id !== a.id) return tb; const set = new Set(tb.panes ?? []); if (set.has(a.key)) set.delete(a.key); else if (set.size < 4) set.add(a.key); return { ...tb, panes: [...set] } }) }
    case 'tabChange':
      return { ...s, tabs: s.tabs.map((tb) => (tb.id === a.id ? { ...tb, draft: a.text, dirty: true } : tb)) }
    case 'tabSave': {
      const tab = s.tabs.find((tb) => tb.id === a.id)
      if (!tab || tab.kind !== 'layer' || !tab.layer) return s
      const f = s.files[tab.path]
      if (!f) return s
      const nf = setLayerText(f, tab.layer, s.lookServer, tab.draft)
      return { ...s, files: { ...s.files, [f.path]: nf }, ...stage(s, f.path), tabs: s.tabs.map((tb) => (tb.id === a.id ? { ...tb, dirty: false } : tb)), queue: pushQueue(s, { kind: 'audit', title: `临时保存 ${f.name}（${LAYER_LABEL[tab.layer].short}层）`, detail: '存入暂存区草稿，未发布', state: '已暂存', done: true, operator: 'admin', target: f.path }), seqQueue: s.seqQueue + 1, syncCounter: s.syncCounter + 1 }
    }
    case 'tabClose': {
      const tabs = s.tabs.filter((tb) => tb.id !== a.id)
      const activeTab = s.activeTab === a.id ? (tabs.length ? tabs[tabs.length - 1].id : null) : s.activeTab
      return { ...s, tabs, activeTab }
    }
    case 'tabCloseOthers':
      return { ...s, tabs: s.tabs.filter((tb) => tb.id === a.id), activeTab: a.id }
    case 'tabActivate':
      return { ...s, activeTab: a.id }
    case 'editorClose':
      return { ...s, tabs: [], activeTab: null }
    case 'reviewOpen':
      return { ...s, review: a.id }
    case 'reviewClose':
      return { ...s, review: null }
    case 'reviewApprove': {
      const item = s.queue.find((q) => q.id === a.id)
      return { ...s, review: null, queue: s.queue.map((q) => (q.id === a.id ? { ...q, state: '已纳管', done: true, detail: `${item?.review?.length ?? 0} 个文件已纳管` } : q)) }
    }
    case 'grayPromote': {
      const it = s.queue.find((q) => q.id === a.id)
      return { ...s, queue: s.queue.map((q) => (q.id === a.id ? { ...q, state: '推送中', progress: 5, detail: `${it?.detail ?? ''} · 晋升全量` } : q)) }
    }
    case 'grayAbort':
      return { ...s, queue: s.queue.map((q) => (q.id === a.id ? { ...q, state: '已中止', done: true } : q)) }
    case 'tick': {
      if (!s.queue.some((q) => q.progress !== undefined && q.progress < 100)) return s
      return {
        ...s,
        queue: s.queue.map((q) => {
          if (q.progress === undefined || q.progress >= 100) return q
          const p = Math.min(100, q.progress + 18)
          return p >= 100 ? { ...q, progress: 100, state: '完成', done: true } : { ...q, progress: p }
        }),
      }
    }
    default:
      return s
  }
}

// ===== 主页 =====
// 跨页面切换缓存：离开 /configs 再回来仍保留打开的标签 / 编辑（整页刷新才重置）
let persisted: State | null = null

export default function ConfigCenterPage() {
  const [s, dispatch] = useReducer(reducer, undefined, () => persisted ?? init())
  const msg = useMessage()
  const [leftOpen, setLeftOpen] = useState(true)
  const [rightOpen, setRightOpen] = useState(true)
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(['plugins', 'plugins/Essentials']))
  const [ctx, setCtx] = useState<{ x: number; y: number; path: string; isDir: boolean } | null>(null)
  // 文件多选（ctrl 点选 / shift 连选）→ 批量抓取
  const [multiSel, setMultiSel] = useState<Set<string>>(() => new Set())
  const lastClickRef = useRef<string | null>(null)
  // 可拖拽抽屉尺寸：左右栏宽 / 底部队列高
  const [leftW, setLeftW] = useState(220)
  const [rightW, setRightW] = useState(200)
  const [queueH, setQueueH] = useState(196)
  const [queueOpen, setQueueOpen] = useState(true)
  const rowRef = useRef<HTMLDivElement | null>(null)
  const colRef = useRef<HTMLDivElement | null>(null)

  const { data: instances = [] } = useQuery({ queryKey: ['instances', {}], queryFn: () => listInstances({}) })
  const servers: SrvMeta[] = useMemo(() => instances.map((i: InstanceView) => ({ id: i.serverId, group: i.group, zone: i.zone ?? '—', online: i.status === 'online' })), [instances])
  // 各层影响范围（覆盖多少台服务器），供写入层 / 发布等处显示
  const scope = useMemo(() => layerScopeCounts(servers, s.group, s.zone), [servers, s.group, s.zone])

  const file = s.files[s.selectedPath]
  const lb = L[s.writeLayer]

  const stateRef = useRef(s)
  stateRef.current = s
  useEffect(() => {
    persisted = s
  }, [s])
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        const cur = stateRef.current
        const tab = cur.tabs.find((tb) => tb.id === cur.activeTab)
        if (tab && tab.kind === 'layer' && tab.dirty) {
          dispatch({ t: 'tabSave', id: tab.id })
          msg.showSuccess('已存入暂存区（受管草稿，未发布）')
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [msg])

  // 进度推进：有"推送中"项时每 0.7s tick 一次直到完成
  useEffect(() => {
    const id = window.setInterval(() => {
      if (stateRef.current.queue.some((q) => q.progress !== undefined && q.progress < 100)) dispatch({ t: 'tick' })
    }, 700)
    return () => window.clearInterval(id)
  }, [])

  // 受管库 / 暂存区视图下选中文件时，自动展开其各级父目录，让选中项在树里可见（抓取后跳转尤其需要）
  useEffect(() => {
    if (s.treeView === 'disk' || !s.selectedPath.includes('/')) return
    setExpanded((prev) => {
      const next = new Set(prev)
      const segs = s.selectedPath.split('/')
      let acc = ''
      for (let i = 0; i < segs.length - 1; i++) {
        acc = acc ? `${acc}/${segs[i]}` : segs[i]
        next.add(acc)
      }
      return next
    })
  }, [s.treeView, s.selectedPath])

  usePageHeader({
    title: '配置中心',
    subtitle: (
      <span className="flex items-center gap-2">
        <Badge variant="outline" className="h-5 gap-1 px-1.5 text-[0.65rem]"><Globe aria-hidden className="size-3" />prod</Badge>
        <span className="font-mono text-xs text-muted-foreground">{file ? file.path : '/'}</span>
      </span>
    ),
    actions: (
      <div className="flex items-center gap-2">
        <Button variant="outline" size="xs" className="h-7 text-xs" onClick={() => dispatch({ t: 'openDirDialog', dialog: 'scan-dir', dir: s.dirView ?? '' })}><Download aria-hidden className="size-3.5" />抓取入库</Button>
        <Button variant="outline" size="xs" className="h-7 text-xs" onClick={() => dispatch({ t: 'dialog', dialog: 'exclude' })}><FilterX aria-hidden className="size-3.5" />排除名单</Button>
      </div>
    ),
  })

  function toggleDir(path: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  // 受管库视图只显已纳管文件（裁掉空目录）；磁盘视图显服务器真实磁盘全量
  const treeNodes = useMemo(() => {
    if (s.treeView === 'disk') return TREE
    if (s.treeView === 'staged') return pruneTree(TREE, (p) => s.staged.includes(p))
    return pruneTree(TREE, (p) => !!s.files[p])
  }, [s.treeView, s.files, s.staged])
  // 当前展开可见的文件顺序，供 shift 连选
  const visiblePaths = useMemo(() => {
    const out: string[] = []
    const walk = (ns: TreeNode[]) => { for (const n of ns) { if (n.type === 'file') out.push(n.path); else if (expanded.has(n.path)) walk(n.children ?? []) } }
    walk(treeNodes)
    return out
  }, [treeNodes, expanded])

  // 点文件：ctrl 加/减选、shift 连选、普通单选打开
  function pickFile(path: string, mods: { ctrl: boolean; shift: boolean }) {
    if (mods.shift && lastClickRef.current) {
      const a = visiblePaths.indexOf(lastClickRef.current)
      const b = visiblePaths.indexOf(path)
      if (a >= 0 && b >= 0) {
        const [lo, hi] = a < b ? [a, b] : [b, a]
        setMultiSel(new Set(visiblePaths.slice(lo, hi + 1)))
        return
      }
    }
    if (mods.ctrl) {
      setMultiSel((cur) => { const n = new Set(cur); if (n.has(path)) n.delete(path); else n.add(path); return n })
      lastClickRef.current = path
      return
    }
    setMultiSel(new Set())
    lastClickRef.current = path
    dispatch({ t: 'select', path })
  }

  // 批量抓取：选中里「未纳管 / 有漂移」的文件一次性入库
  const selFetchable = useMemo(() => [...multiSel].filter((p) => { const f = s.files[p]; return f ? driftCount(f, s.group, s.zone, s.lookServer) > 0 : unmanagedText(p, s.lookServer) !== null }), [multiSel, s.files, s.group, s.zone, s.lookServer])

  // 抽屉拖拽改尺寸（左右栏宽 / 队列高），拖动期间禁选中文本
  function startResize(which: 'left' | 'right' | 'queue', e: React.MouseEvent) {
    e.preventDefault()
    const move = (ev: MouseEvent) => {
      if (which === 'queue') {
        const r = colRef.current?.getBoundingClientRect()
        if (r) setQueueH(Math.min(520, Math.max(90, r.bottom - ev.clientY - 8)))
      } else {
        const r = rowRef.current?.getBoundingClientRect()
        if (!r) return
        if (which === 'left') setLeftW(Math.min(480, Math.max(170, ev.clientX - r.left)))
        else setRightW(Math.min(480, Math.max(170, r.right - ev.clientX)))
      }
    }
    const up = () => {
      window.removeEventListener('mousemove', move)
      window.removeEventListener('mouseup', up)
      document.body.style.userSelect = ''
    }
    document.body.style.userSelect = 'none'
    window.addEventListener('mousemove', move)
    window.addEventListener('mouseup', up)
  }

  return (
    <div ref={colRef} className="flex h-full min-h-0 flex-col gap-2 overflow-hidden" onClick={() => ctx && setCtx(null)}>
      <div className={cn('flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 rounded-lg border px-3 py-1.5 text-xs transition-colors', lb.wrap)}>
        <span className="flex items-center gap-1.5"><Eye aria-hidden className="size-3.5 text-muted-foreground" /><span className="text-muted-foreground">看生效</span><ServerPicker servers={servers} value={s.lookServer} onChange={(m) => dispatch({ t: 'server', id: m.id, group: m.group, zone: m.zone })} /></span>
        <span className="h-4 w-px bg-border" />
        <span className="flex items-center gap-1.5">
          {s.writeLayer === 'server' ? <Lock aria-hidden className={cn('size-3.5', lb.text)} /> : <SquarePen aria-hidden className="size-3.5 text-muted-foreground" />}
          <span className="text-muted-foreground">写入层</span>
          {LAYER_ORDER.map((lvl) => { const m = L[lvl]; const active = lvl === s.writeLayer; return <button key={lvl} type="button" onClick={() => dispatch({ t: 'layer', level: lvl })} title={`覆盖 ${scope[lvl].total} 台（在线 ${scope[lvl].online}）`} className={cn('flex items-center gap-1 rounded-md border px-2 py-0.5 transition-colors', active ? cn('bg-background font-medium', m.chip) : 'border-transparent text-muted-foreground hover:bg-background/60')}><span className={cn('size-1.5 rounded-full', m.dot)} />{LAYER_LABEL[lvl].label}<span className="text-[0.6rem] opacity-70">·{scope[lvl].total}</span></button> })}
        </span>
        <span className={cn('ml-auto flex items-center gap-1.5 text-[0.7rem]', lb.text)}>{s.writeLayer === 'server' ? <><Lock aria-hidden className="size-3" />正在为 单服 {s.lookServer} 打补丁 —— 只覆盖这 1 台，其余继承上层</> : <><SquarePen aria-hidden className="size-3" />正在写 {LAYER_LABEL[s.writeLayer].label} 层 —— 覆盖该层 {scope[s.writeLayer].total} 台（在线 {scope[s.writeLayer].online}）中未单独覆盖的服</>}</span>
      </div>

      <div ref={rowRef} className="flex min-h-0 flex-1 gap-0 overflow-hidden">
        {leftOpen ? (
          <>
            <aside style={{ width: leftW }} className="flex shrink-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
              <div className="flex shrink-0 items-center gap-1.5 border-b border-border px-2.5 py-1.5">
                <FolderTree aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
                <div className="flex overflow-hidden rounded-md border border-border text-[0.65rem]">
                  <button type="button" onClick={() => dispatch({ t: 'treeView', view: 'managed' })} className={cn('px-1.5 py-0.5', s.treeView === 'managed' ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground hover:bg-muted')}>受管库</button>
                  <button type="button" onClick={() => dispatch({ t: 'treeView', view: 'disk' })} className={cn('border-l border-border px-1.5 py-0.5', s.treeView === 'disk' ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground hover:bg-muted')}>磁盘</button>
                  <button type="button" onClick={() => dispatch({ t: 'treeView', view: 'staged' })} className={cn('flex items-center gap-1 border-l border-border px-1.5 py-0.5', s.treeView === 'staged' ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground hover:bg-muted')}><Inbox aria-hidden className="size-3" />暂存{s.staged.length > 0 && <span className={cn('rounded-full px-1 text-[0.55rem]', s.treeView === 'staged' ? 'bg-primary/20 text-primary' : 'bg-amber-500/15 text-amber-600 dark:text-amber-400')}>{s.staged.length}</span>}</button>
                </div>
                <button type="button" onClick={() => setLeftOpen(false)} title="收起" className="ml-auto rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"><X aria-hidden className="size-3.5" /></button>
              </div>
              <div className="shrink-0 border-b border-border p-1.5"><div className="flex items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1"><Search aria-hidden className="size-3.5 text-muted-foreground" /><input type="text" disabled placeholder="搜索文件 / 目录" className="w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground" /></div></div>
              <div className="scrollbar-hide min-h-0 flex-1 overflow-y-auto p-1"><div className="px-1.5 py-1 text-[0.65rem] text-muted-foreground">{s.treeView === 'disk' ? '服务器磁盘（全量）' : s.treeView === 'staged' ? '暂存区 · 待发布草稿' : '受管配置库'} · {s.lookServer}{s.treeView === 'disk' && <span className="ml-1 text-[0.6rem]">· Ctrl 点选 / Shift 连选可多选</span>}{s.treeView === 'staged' && <span className="ml-1 text-[0.6rem]">· 双击编辑 · 整批发到写入层</span>}</div>{s.treeView === 'staged' && treeNodes.length === 0 ? <div className="flex flex-col items-center justify-center gap-1 px-2 py-8 text-center text-[0.7rem] text-muted-foreground"><Inbox aria-hidden className="size-6 opacity-40" />暂存区为空<span className="text-[0.65rem]">去「磁盘」抓取文件，或编辑受管文件</span></div> : <TreeView nodes={treeNodes} depth={0} expanded={expanded} state={s} selectedPath={s.selectedPath} diskMode={s.treeView === 'disk'} stagedMode={s.treeView === 'staged'} multiSel={multiSel} onToggle={toggleDir} onDir={(p) => dispatch({ t: 'selectDir', path: p })} onPick={pickFile} onOpen={(p) => dispatch({ t: 'openEdit', path: p })} onCtx={(x, y, p, isDir) => setCtx({ x, y, path: p, isDir })} />}</div>
              {multiSel.size > 0 && (
                <div className="flex shrink-0 items-center gap-1.5 border-t border-border px-2 py-1.5 text-[0.7rem]">
                  <span className="text-muted-foreground">已选 <span className="font-medium text-foreground">{multiSel.size}</span></span>
                  <button type="button" onClick={() => setMultiSel(new Set())} className="rounded border border-border px-1.5 py-px text-[0.65rem] hover:bg-muted">清空</button>
                  <Button size="xs" className="ml-auto h-6 text-[0.7rem]" disabled={selFetchable.length === 0} onClick={() => { dispatch({ t: 'ingestDir', dir: `选中 ${selFetchable.length} 个`, paths: selFetchable }); msg.showSuccess(`已抓入「暂存区」${selFetchable.length} 个（待发布草稿）· 去暂存区编辑 / 定层后发布`); setMultiSel(new Set()) }}><Download aria-hidden className="size-3" />抓取选中 {selFetchable.length}</Button>
                </div>
              )}
              {s.treeView === 'staged' && multiSel.size === 0 && s.staged.length > 0 && (
                <div className="flex shrink-0 flex-col gap-1 border-t border-border px-2 py-1.5 text-[0.7rem]">
                  {/* 一次只发一个层：整批发到当前写入层（改层用上方写入层选择器） */}
                  <div className="flex items-center gap-1"><span className="text-muted-foreground">{s.staged.length} 个 · 发到</span><span className={cn('rounded border px-1.5 py-px text-[0.6rem]', L[s.writeLayer].chip)}>{LAYER_LABEL[s.writeLayer].label}</span><span className="text-[0.6rem] text-muted-foreground">· 覆盖 {scope[s.writeLayer].total} 台（在线 {scope[s.writeLayer].online}）</span></div>
                  <Button size="xs" className="h-6 w-full text-[0.7rem]" onClick={() => dispatch({ t: 'dialog', dialog: 'publish-staged' })}><Rocket aria-hidden className="size-3" />发布全部暂存 → {LAYER_LABEL[s.writeLayer].short}</Button>
                </div>
              )}
            </aside>
            <div onMouseDown={(e) => startResize('left', e)} title="拖动调整宽度" className="w-1.5 shrink-0 cursor-col-resize rounded transition-colors hover:bg-primary/30" />
          </>
        ) : (
          <CollapsedStrip onExpand={() => setLeftOpen(true)} icon={<FolderTree className="size-4" />} label="配置库" />
        )}

        <section className="flex min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border bg-card" onDragOver={(e) => { if (e.dataTransfer.types.includes('text/cc-path')) e.preventDefault() }} onDrop={(e) => { const p = e.dataTransfer.getData('text/cc-path'); if (p) dispatch({ t: 'openEdit', path: p }) }}>{s.dirView !== null ? <DirectoryView state={s} dir={s.dirView} dispatch={dispatch} /> : file ? <Workspace state={s} file={file} dispatch={dispatch} msg={msg} /> : isDiskFile(s.selectedPath, s.lookServer) ? <DiskFilePanel state={s} path={s.selectedPath} dispatch={dispatch} msg={msg} /> : <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">从左侧选一个文件 / 目录</div>}</section>

        {rightOpen ? (
          <>
            <div onMouseDown={(e) => startResize('right', e)} title="拖动调整宽度" className="w-1.5 shrink-0 cursor-col-resize rounded transition-colors hover:bg-primary/30" />
            <aside style={{ width: rightW }} className="flex shrink-0 flex-col overflow-hidden rounded-lg border border-border bg-card"><div className="flex shrink-0 items-center justify-between border-b border-border px-2.5 py-1.5"><span className="text-xs font-medium text-foreground">上下文</span><button type="button" onClick={() => setRightOpen(false)} title="收起" className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"><X aria-hidden className="size-3.5" /></button></div><div className="scrollbar-hide min-h-0 flex-1 overflow-y-auto"><RightRail state={s} file={file} dispatch={dispatch} /></div></aside>
          </>
        ) : (
          <CollapsedStrip onExpand={() => setRightOpen(true)} icon={<History className="size-4" />} label="上下文" />
        )}
      </div>

      {queueOpen ? (
        <div className="shrink-0">
          <div onMouseDown={(e) => startResize('queue', e)} title="拖动调整高度" className="mx-auto mb-0.5 h-1.5 w-full cursor-row-resize rounded transition-colors hover:bg-primary/30" />
          <QueueDock queue={s.queue} dispatch={dispatch} height={queueH} onCollapse={() => setQueueOpen(false)} />
        </div>
      ) : (
        <button type="button" onClick={() => setQueueOpen(true)} className="flex shrink-0 items-center gap-2 rounded-lg border border-border bg-card px-3 py-1.5 text-xs text-muted-foreground hover:bg-muted"><ArrowRightLeft aria-hidden className="size-3.5" /><span className="font-medium text-foreground">同步队列</span><span className="rounded-full bg-muted px-1.5 text-[0.6rem]">{s.queue.length}</span><span className="ml-auto text-[0.7rem]">展开 ▴</span></button>
      )}

      {s.dialog === 'publish' && file && <PublishDialog state={s} file={file} servers={servers} dispatch={dispatch} msg={msg} />}
      {s.dialog === 'exclude' && <ExcludeDialog state={s} dispatch={dispatch} />}
      {s.dialog === 'scan-dir' && s.dirTarget !== null && <DirScanDialog state={s} dir={s.dirTarget} dispatch={dispatch} msg={msg} />}
      {s.dialog === 'publish-dir' && s.dirTarget !== null && <DirPublishDialog state={s} dir={s.dirTarget} servers={servers} dispatch={dispatch} msg={msg} />}
      {s.dialog === 'publish-staged' && <PublishStagedDialog state={s} servers={servers} dispatch={dispatch} msg={msg} />}
      {s.rollback !== null && file && <RollbackDialog state={s} file={file} version={s.rollback} dispatch={dispatch} />}
      {s.review !== null && <ReviewDialog item={s.queue.find((q) => q.id === s.review)} dispatch={dispatch} />}

      {s.tabs.length > 0 && s.activeTab && <EditorDock state={s} dispatch={dispatch} msg={msg} />}

      {ctx && <ContextMenu x={ctx.x} y={ctx.y} path={ctx.path} isDir={ctx.isDir} onClose={() => setCtx(null)} dispatch={dispatch} />}
    </div>
  )
}

// ===== 固定宽度可搜索服选择器 =====
function ServerPicker({ servers, value, onChange }: { servers: SrvMeta[]; value: string; onChange: (m: SrvMeta) => void }) {
  const [open, setOpen] = useState(false)
  const [q, setQ] = useState('')
  const cur = servers.find((s) => s.id === value)
  const list = useMemo(() => { const f = q.trim().toLowerCase(); return servers.filter((s) => !f || s.id.toLowerCase().includes(f) || s.group.includes(f) || s.zone.includes(f)).slice(0, 80) }, [q, servers])
  return (
    <span className="relative">
      <button type="button" onClick={(e) => { e.stopPropagation(); setOpen((o) => !o) }} className="flex w-[208px] items-center gap-1.5 rounded-md border border-primary bg-background px-2 py-0.5 font-mono text-foreground"><span className="truncate">{value}</span><span className="ml-auto shrink-0 text-[0.6rem] text-muted-foreground">{cur ? `${cur.group}/${cur.online ? '在线' : '离线'}` : ''}</span><ChevronDown className="size-3 shrink-0" /></button>
      {open && (
        <div className="absolute left-0 top-full z-30 mt-1 w-72 rounded-lg border border-border bg-card shadow-lg" onClick={(e) => e.stopPropagation()}>
          <div className="flex items-center gap-1.5 border-b border-border px-2 py-1.5"><Search className="size-3.5 text-muted-foreground" /><input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={`搜索 ${servers.length} 台实例`} className="w-full bg-transparent text-xs outline-none" /></div>
          <div className="scrollbar-hide max-h-64 overflow-y-auto py-1">{list.map((sv) => <button key={sv.id} type="button" onClick={() => { onChange(sv); setOpen(false); setQ('') }} className={cn('flex w-full items-center gap-2 px-2.5 py-1 text-left text-xs hover:bg-muted', sv.id === value && 'bg-accent text-accent-foreground')}><span className={cn('size-1.5 shrink-0 rounded-full', sv.online ? 'bg-emerald-500' : 'bg-muted-foreground/40')} /><span className="flex-1 truncate font-mono">{sv.id}</span><span className="shrink-0 text-[0.6rem] text-muted-foreground">{sv.group}/{sv.zone}</span></button>)}{list.length === 0 && <div className="px-2.5 py-2 text-xs text-muted-foreground">无匹配实例</div>}</div>
        </div>
      )}
    </span>
  )
}

function CollapsedStrip({ onExpand, icon, label }: { onExpand: () => void; icon: React.ReactNode; label: string }) {
  return <button type="button" onClick={onExpand} title={`展开${label}`} className="flex w-8 shrink-0 flex-col items-center gap-3 rounded-lg border border-border bg-card py-2.5 text-muted-foreground hover:bg-muted hover:text-foreground">{icon}<span className="text-[0.7rem] tracking-wide [writing-mode:vertical-rl]">{label}</span></button>
}

// ===== 文件树 =====
// diskMode：磁盘视图——可见服务器真实磁盘全量（含未纳管 / 运行期），被排除目录仍可展开浏览；支持 Ctrl/Shift 多选
function TreeView({ nodes, depth, expanded, state, selectedPath, diskMode, stagedMode, multiSel, onToggle, onDir, onPick, onOpen, onCtx }: { nodes: TreeNode[]; depth: number; expanded: Set<string>; state: State; selectedPath: string; diskMode: boolean; stagedMode: boolean; multiSel: Set<string>; onToggle: (p: string) => void; onDir: (p: string) => void; onPick: (p: string, mods: { ctrl: boolean; shift: boolean }) => void; onOpen: (p: string) => void; onCtx: (x: number, y: number, p: string, isDir: boolean) => void }) {
  return (
    <ul className="flex flex-col gap-0.5">
      {nodes.map((node) => {
        const pad = { paddingLeft: `${depth * 12 + 6}px` }
        const excluded = isExcluded(node.path, state.excludeRules)
        if (node.type === 'dir') {
          const open = expanded.has(node.path)
          const dirActive = state.dirView === node.path
          // 磁盘视图下被排除目录也可展开浏览（运维要能看到磁盘真实文件）；受管视图保持排除目录收起灰显
          const canEnter = diskMode || !excluded
          return (
            <li key={node.path}>
              <button type="button" onClick={() => { onToggle(node.path); if (canEnter) onDir(node.path) }} onContextMenu={(e) => { e.preventDefault(); onCtx(e.clientX, e.clientY, node.path, true) }} style={pad} className={cn('flex w-full items-center gap-1.5 rounded-md py-1 pr-1.5 text-xs', excluded && 'opacity-60', dirActive ? 'bg-accent font-medium text-accent-foreground' : 'text-foreground hover:bg-muted')}><span className="inline-block w-3 shrink-0 text-center text-[0.6rem] text-muted-foreground">{!canEnter ? '·' : open ? '▾' : '▸'}</span>{excluded ? <Ban aria-hidden className="size-3.5 shrink-0 text-muted-foreground" /> : <FolderTree aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />}<span className="truncate">{node.name}/</span>{excluded && <span className="ml-auto shrink-0 rounded border border-dashed border-border px-1 text-[0.55rem]">排除</span>}</button>
              {open && canEnter && node.children && node.children.length > 0 && <TreeView nodes={node.children} depth={depth + 1} expanded={expanded} state={state} selectedPath={selectedPath} diskMode={diskMode} stagedMode={stagedMode} multiSel={multiSel} onToggle={onToggle} onDir={onDir} onPick={onPick} onOpen={onOpen} onCtx={onCtx} />}
            </li>
          )
        }
        const f = state.files[node.path]
        const unmanaged = !f && unmanagedText(node.path, state.lookServer) !== null
        const checked = multiSel.has(node.path)
        const active = node.path === selectedPath && state.dirView === null && !checked
        const drift = f ? driftCount(f, state.group, state.zone, state.lookServer) : 0
        const fdirty = state.tabs.some((tb) => tb.path === node.path && tb.dirty)
        const isStaged = state.staged.includes(node.path)
        const binary = isBinaryName(node.name)
        return (
          <li key={node.path}>
            <button type="button" draggable={!!f} onDragStart={(e) => { if (!f) { e.preventDefault(); return } e.dataTransfer.setData('text/cc-path', node.path); e.dataTransfer.effectAllowed = 'copy' }} onClick={(e) => onPick(node.path, { ctrl: e.ctrlKey || e.metaKey, shift: e.shiftKey })} onDoubleClick={() => f && onOpen(node.path)} onContextMenu={(e) => { e.preventDefault(); onPick(node.path, { ctrl: false, shift: false }); onCtx(e.clientX, e.clientY, node.path, false) }} style={pad} className={cn('flex w-full items-center gap-1.5 rounded-md py-1 pr-1.5 text-xs', excluded && 'opacity-60', checked ? 'bg-primary/10 ring-1 ring-inset ring-primary/40 text-foreground' : active ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground')}>
              <span className="inline-block w-3 shrink-0 text-center">{checked && <Check aria-hidden className="size-3 text-primary" />}</span>
              {node.name.endsWith('.json') ? <FileCode aria-hidden className="size-3.5 shrink-0 text-muted-foreground" /> : binary || f?.kind === 'binary' ? <Database aria-hidden className="size-3.5 shrink-0 text-muted-foreground" /> : <FileText aria-hidden className={cn('size-3.5 shrink-0', unmanaged ? 'text-sky-500' : 'text-muted-foreground')} />}
              <span className={cn('truncate', unmanaged && 'text-sky-600 dark:text-sky-400')}>{node.name}</span>
              <span className="ml-auto flex shrink-0 items-center gap-1">{fdirty && <span title="未保存" className="size-1.5 rounded-full bg-primary" />}{isStaged && !stagedMode && <span title="在暂存区 · 待发布" className="rounded border border-amber-400/60 px-1 text-[0.55rem] text-amber-600 dark:text-amber-400">暂存</span>}{f?.sensitive && <Lock aria-hidden className="size-3 text-rose-500" />}{unmanaged && <span title="磁盘上存在，尚未纳管" className="rounded border border-sky-400/60 px-1 text-[0.55rem] text-sky-600 dark:text-sky-400">未纳管</span>}{excluded && <span className="rounded border border-dashed border-border px-1 text-[0.55rem]">排除</span>}{drift > 0 && <AlertTriangle aria-hidden className="size-3 text-amber-500" />}</span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}

// ===== 工作区 =====
function Workspace({ state, file, dispatch, msg }: { state: State; file: MockFile; dispatch: React.Dispatch<Action>; msg: ReturnType<typeof useMessage> }) {
  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-3 py-1.5 text-xs">
        <FileText aria-hidden className="size-3.5 text-muted-foreground" />
        <span className="font-mono font-medium text-foreground">{file.path}</span>
        <span className="rounded-full border border-border px-1.5 py-px text-[0.6rem] text-muted-foreground">{file.nested ? '嵌套 · 四层' : '配置 · 四层'}</span>
        {file.sensitive && <span className="flex items-center gap-1 rounded-full border border-rose-400/60 px-1.5 py-px text-[0.6rem] text-rose-600 dark:text-rose-400"><Lock aria-hidden className="size-2.5" /> 敏感</span>}
        <span className="rounded-full border border-border px-1.5 py-px text-[0.6rem] text-muted-foreground">{fileLang(file).toUpperCase()}</span>
        <div className="ml-auto flex items-center gap-1.5">
          <Button variant="outline" size="xs" className="h-6 text-[0.7rem]" onClick={() => dispatch({ t: 'openEdit', path: file.path })}><Pencil aria-hidden className="size-3" />编辑 {LAYER_LABEL[state.writeLayer].short}层</Button>
          <Button variant="outline" size="xs" className="h-6 text-[0.7rem]" onClick={() => dispatch({ t: 'openSuper', path: file.path })}><GitCompare aria-hidden className="size-3" />超级diff</Button>
          <Button size="xs" className="h-6 text-[0.7rem]" onClick={() => dispatch({ t: 'dialog', dialog: 'publish' })}><Rocket aria-hidden className="size-3" />发布</Button>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-2.5 py-1">{MODES.map((m) => { const Icon = m.icon; const active = m.id === state.mode; return <button key={m.id} type="button" onClick={() => dispatch({ t: 'mode', mode: m.id })} className={cn('flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs', active ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground')}><Icon aria-hidden className="size-3.5" />{m.label}</button> })}</div>
      {state.mode === 'effective' ? <EffectiveView state={state} file={file} dispatch={dispatch} msg={msg} /> : state.mode === 'columns' ? <ColumnsView state={state} file={file} dispatch={dispatch} /> : <DriftView state={state} file={file} dispatch={dispatch} />}
    </div>
  )
}

// ===== 生效 Monaco 编辑器（左侧行号处显来源层标签 + 彩条；改值写回该层） =====
// 稳定的编辑器选项（模块常量，避免每次 render 新对象触发 Monaco updateOptions 抖动 → 查找框闪烁）
const EFF_OPTIONS: editor.IStandaloneEditorConstructionOptions = {
  fontSize: 12,
  fontFamily: 'var(--font-mono)',
  minimap: { enabled: false },
  scrollBeyondLastLine: false,
  automaticLayout: true,
  fixedOverflowWidgets: true,
  lineNumbers: 'off',
  lineDecorationsWidth: 54,
  glyphMargin: false,
  folding: true,
  padding: { top: 6 },
  scrollbar: { verticalScrollbarSize: 8, horizontalScrollbarSize: 8 },
}

// 行装饰 gutter：每行左侧显来源层中文标签（::before）+ 彩条
const GUTTER_STYLE = `.cc-src-global::before,.cc-src-area::before,.cc-src-zone::before,.cc-src-server::before{display:inline-block;width:48px;padding-left:6px;font-size:10px}
.cc-src-global::before{content:'全局';color:#64748b;box-shadow:inset 3px 0 0 #94a3b8}
.cc-src-area::before{content:'大区';color:#2563eb;box-shadow:inset 3px 0 0 #3b82f6}
.cc-src-zone::before{content:'小区';color:#d97706;box-shadow:inset 3px 0 0 #f59e0b}
.cc-src-server::before{content:'单服';color:#059669;box-shadow:inset 3px 0 0 #10b981}`

function EffectiveMonaco({ initial, lang, prov, baseline, wholeSource, fallback, onEditPath, onEditWhole, onSaved }: { initial: string; lang: string; prov: Map<string, LayerLevel>; baseline: Map<string, string>; wholeSource: LayerLevel; fallback: LayerLevel; onEditPath: (p: string, v: string) => void; onEditWhole: (text: string) => void; onSaved: () => void }) {
  const edRef = useRef<editor.IStandaloneCodeEditor | null>(null)
  const monRef = useRef<Parameters<OnMount>[1] | null>(null)
  const decoRef = useRef<string[]>([])
  const provRef = useRef(prov)
  provRef.current = prov
  const baseRef = useRef(new Map(baseline))
  const debRef = useRef<number | null>(null)
  const monacoLang = lang === 'json' ? 'json' : lang === 'yaml' ? 'yaml' : 'plaintext'

  function recolor(text: string) {
    const ed = edRef.current
    const mon = monRef.current
    if (!ed || !mon) return
    const src = lang === 'plaintext' ? text.split('\n').map((l) => (l.trim() ? wholeSource : null)) : lineSources(text, provRef.current, lang, fallback)
    const decos = src
      .map((s, i) => (s ? { range: new mon.Range(i + 1, 1, i + 1, 1), options: { isWholeLine: true, linesDecorationsClassName: `cc-src-${s}` } } : null))
      .filter((d): d is NonNullable<typeof d> => d !== null)
    decoRef.current = ed.deltaDecorations(decoRef.current, decos)
  }

  const handleMount: OnMount = (ed, mon) => {
    edRef.current = ed
    monRef.current = mon
    recolor(ed.getModel()?.getValue() ?? initial)
  }

  function handleChange(v?: string) {
    const text = v ?? ''
    recolor(text)
    if (debRef.current) window.clearTimeout(debRef.current)
    debRef.current = window.setTimeout(() => {
      if (lang === 'plaintext') {
        onEditWhole(text)
        onSaved()
        return
      }
      let changed = false
      for (const { path, value } of parseFlat(text, lang)) {
        if (baseRef.current.get(path) !== value) {
          onEditPath(path, value)
          baseRef.current.set(path, value)
          changed = true
        }
      }
      if (changed) onSaved()
    }, 450)
  }

  return (
    <>
      <style>{GUTTER_STYLE}</style>
      <Editor defaultValue={initial} language={monacoLang} theme="vs" onMount={handleMount} onChange={handleChange} options={EFF_OPTIONS} loading={<div className="flex h-full items-center justify-center text-xs text-muted-foreground">加载编辑器…</div>} />
    </>
  )
}

// ===== 视角：编辑（生效 Monaco，左侧来源层标签，改值写回该层） =====
function EffectiveView({ state, file, dispatch, msg }: { state: State; file: MockFile; dispatch: React.Dispatch<Action>; msg: ReturnType<typeof useMessage> }) {
  const lang = fileLang(file)
  const eff = flatEffective(file, state.group, state.zone, state.lookServer)
  const prov = provenanceMap(file, state.group, state.zone, state.lookServer)
  const baseline = new Map(eff.map((e) => [e.path, e.value] as const))
  const whole = topSourceLayer(file, state.group, state.zone, state.lookServer)
  const initial = effectiveSource(file, state.group, state.zone, state.lookServer)
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1.5 text-[0.7rem] text-muted-foreground">
        生效编辑器 → <span className="font-mono">{state.lookServer}</span>（{state.group}/{state.zone}）· 真实合并源文件 · 左侧行号处=来源层 · 改即写回受管草稿该层（非磁盘，入暂存区）
        <span className="ml-auto flex items-center gap-2">{LAYER_ORDER.map((lvl) => <span key={lvl} className="flex items-center gap-1"><span className={cn('size-2 rounded-sm', L[lvl].dot)} />{LAYER_LABEL[lvl].short}</span>)}</span>
      </div>
      <div className="min-h-0 flex-1">
        <EffectiveMonaco key={`${file.path}|${state.lookServer}|${state.syncCounter}`} initial={initial} lang={lang} prov={prov} baseline={baseline} wholeSource={whole} fallback="global" onEditPath={(p, v) => dispatch({ t: 'editPath', path: p, value: v })} onEditWhole={(text) => dispatch({ t: 'editWhole', layer: whole, text })} onSaved={() => msg.showSuccess('已写回受管草稿 · 入暂存区（未发布）')} />
      </div>
      <div className="flex shrink-0 items-center gap-1.5 border-t border-border px-3 py-1.5">
        <span className="text-[0.7rem] text-muted-foreground">整层原文（含注释/折叠）用浮窗编辑器：</span>
        {LAYER_ORDER.map((lvl) => <button key={lvl} type="button" onClick={() => { dispatch({ t: 'layer', level: lvl }); dispatch({ t: 'openEdit', path: file.path, layer: lvl }) }} className={cn('rounded-md border px-2 py-0.5 text-[0.7rem] hover:bg-muted', L[lvl].chip)}>{LAYER_LABEL[lvl].short}</button>)}
      </div>
    </div>
  )
}

// ===== 视角：层列（按键层级折叠树） =====
interface CNode {
  key: string
  full: string
  children: CNode[]
  leaf?: { path: string; cells: Record<LayerLevel, string | null>; winner: LayerLevel }
}

function buildKeyTree(rows: { path: string; cells: Record<LayerLevel, string | null>; winner: LayerLevel }[]): CNode[] {
  const root: CNode = { key: '', full: '', children: [] }
  for (const r of rows) {
    let cur = root
    let acc = ''
    const segs = r.path.split('.')
    segs.forEach((seg, i) => {
      acc = acc ? `${acc}.${seg}` : seg
      let child = cur.children.find((c) => c.key === seg)
      if (!child) {
        child = { key: seg, full: acc, children: [] }
        cur.children.push(child)
      }
      cur = child
      if (i === segs.length - 1) cur.leaf = r
    })
  }
  return root.children
}

function ColNode({ node, depth, collapsed, onToggle, onEdit, widths }: { node: CNode; depth: number; collapsed: Set<string>; onToggle: (full: string) => void; onEdit: () => void; widths: number[] }) {
  const pad = { paddingLeft: `${depth * 12 + 8}px` }
  if (node.children.length > 0) {
    const open = !collapsed.has(node.full)
    return (
      <>
        <div className="flex items-center border-b border-border/40 text-[0.7rem] hover:bg-muted/30">
          <button type="button" onClick={() => onToggle(node.full)} style={pad} className="flex min-w-0 flex-1 items-center gap-1 py-1 text-left">
            <span className="w-3 shrink-0 text-[0.6rem] text-muted-foreground">{open ? '▾' : '▸'}</span>
            <span className="truncate font-mono font-medium text-foreground">{node.key}</span>
            <span className="shrink-0 text-[0.6rem] text-muted-foreground">{node.children.length}</span>
          </button>
          {LAYER_ORDER.map((lvl, i) => <span key={lvl} style={{ width: widths[i] }} className="shrink-0" />)}
          <span className="w-6 shrink-0" />
        </div>
        {open && node.children.map((c) => <ColNode key={c.full} node={c} depth={depth + 1} collapsed={collapsed} onToggle={onToggle} onEdit={onEdit} widths={widths} />)}
      </>
    )
  }
  const r = node.leaf!
  return (
    <div className="flex items-center border-b border-border/40 text-[0.7rem] hover:bg-muted/30">
      <span style={pad} className="flex min-w-0 flex-1 items-center gap-1 py-1"><span className="w-3 shrink-0" /><span className="truncate font-mono text-foreground" title={r.path}>{node.key}</span></span>
      {LAYER_ORDER.map((lvl, i) => {
        const v = r.cells[lvl]
        const over = v !== null && lvl !== r.winner
        return <span key={lvl} style={{ width: widths[i] }} className="shrink-0 truncate px-1 font-mono" title={v ?? ''}>{v === null ? <span className="text-muted-foreground/40">–</span> : <span className={cn(over ? 'text-muted-foreground line-through' : L[lvl].text)}>{v}</span>}</span>
      })}
      <button type="button" onClick={onEdit} title="在编辑器改写入层" className="w-6 shrink-0 text-muted-foreground hover:text-foreground"><Pencil className="size-3" /></button>
    </div>
  )
}

function ColumnsView({ state, file, dispatch }: { state: State; file: MockFile; dispatch: React.Dispatch<Action> }) {
  const rows = columnRows(file, state.group, state.zone, state.lookServer)
  const tree = buildKeyTree(rows)
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [widths, setWidths] = useState<number[]>([120, 120, 110, 110])
  const writeLayer = state.writeLayer
  const toggle = (full: string) => setCollapsed((p) => { const n = new Set(p); if (n.has(full)) n.delete(full); else n.add(full); return n })
  const rz = useRef<{ col: number; x: number; w: number } | null>(null)
  useEffect(() => {
    function onMove(e: MouseEvent) { const r = rz.current; if (!r) return; setWidths((ws) => { const n = [...ws]; n[r.col] = Math.max(56, r.w + (e.clientX - r.x)); return n }) }
    function onUp() { rz.current = null; document.body.style.userSelect = '' }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => { window.removeEventListener('mousemove', onMove); window.removeEventListener('mouseup', onUp) }
  }, [])
  function startCol(col: number, e: React.MouseEvent) { e.preventDefault(); e.stopPropagation(); rz.current = { col, x: e.clientX, w: widths[col] }; document.body.style.userSelect = 'none' }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center border-b border-border bg-card text-[0.65rem] text-muted-foreground">
        <span className="min-w-0 flex-1 px-2 py-1.5 font-medium">键路径（折叠树）· 拖列边调宽</span>
        {LAYER_ORDER.map((lvl, i) => (
          <span key={lvl} style={{ width: widths[i] }} className="relative shrink-0 px-1 py-1.5">
            <button type="button" onClick={() => dispatch({ t: 'layer', level: lvl })} className={cn('rounded-full border px-1.5 py-px text-[0.6rem]', L[lvl].chip, lvl === writeLayer && 'ring-1 ring-primary/40')}>{LAYER_LABEL[lvl].short}</button>
            <span onMouseDown={(e) => startCol(i, e)} className="absolute right-0 top-0 h-full w-1.5 cursor-col-resize hover:bg-primary/30" />
          </span>
        ))}
        <span className="w-6 shrink-0" />
      </div>
      <div className="scrollbar-hide min-h-0 flex-1 overflow-y-auto">
        {tree.map((n) => <ColNode key={n.full} node={n} depth={0} collapsed={collapsed} onToggle={toggle} onEdit={() => dispatch({ t: 'openEdit', path: file.path, layer: writeLayer })} widths={widths} />)}
      </div>
      <p className="shrink-0 border-t border-border px-3 py-1.5 text-[0.65rem] text-muted-foreground">按键层级折叠 · 叶子行显各层值（划掉=被覆盖 · 彩色=胜出 · –=未覆盖）· 拖列头右缘调宽 · 点 ✎ 在编辑器改写入层</p>
    </div>
  )
}


// ===== 视角：对盘（叶子路径 diff：中心生效 ↔ 磁盘） =====
function DriftView({ state, file, dispatch }: { state: State; file: MockFile; dispatch: React.Dispatch<Action> }) {
  const hasDisk = file.disk?.[state.lookServer] !== undefined
  const rows = driftRows(file, state.group, state.zone, state.lookServer)
  const n = rows.filter((r) => r.kind !== 'same').length
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 border-b border-border text-[0.7rem] font-medium text-muted-foreground"><div className="w-1/4 border-r border-border px-3 py-1">键路径</div><div className="flex-1 border-r border-border px-3 py-1">中心生效（应生效）</div><div className="flex-1 px-3 py-1">{state.lookServer} 磁盘实际 · {hasDisk ? (n > 0 ? `${n} 处漂移` : '一致') : '无磁盘快照'}</div></div>
      <div className="scrollbar-hide min-h-0 flex-1 overflow-y-auto">
        {!hasDisk ? (
          <div className="flex h-full items-center justify-center text-xs text-muted-foreground">该服暂无磁盘快照</div>
        ) : (
          rows.map((r) => (
            <div key={r.path} className={cn('flex border-b border-border/60 text-xs', r.kind !== 'same' && 'bg-amber-500/[0.04]')}>
              <div className="w-1/4 truncate border-r border-border px-3 py-1 font-mono text-muted-foreground" title={r.path}>{r.path}</div>
              <div className="flex-1 truncate border-r border-border px-3 py-1 font-mono">{r.effective !== null ? <span className={cn(r.kind === 'changed' && 'rounded bg-rose-500/10 px-1')}>{r.effective}</span> : <span className="text-muted-foreground/40">—</span>}</div>
              <div className="flex-1 truncate px-3 py-1 font-mono">{r.disk !== null ? <span className={cn((r.kind === 'changed' || r.kind === 'added') && 'rounded bg-emerald-500/10 px-1')}>{r.disk}</span> : <span className="text-muted-foreground/40">—</span>}</div>
            </div>
          ))
        )}
      </div>
      <div className="flex shrink-0 items-center gap-2 border-t border-border px-3 py-1.5"><Button size="xs" className="h-7 text-xs" disabled={!hasDisk || n === 0} onClick={() => dispatch({ t: 'ingest' })}><Download aria-hidden className="size-3.5" />收编磁盘改动为单服补丁</Button><span className={cn('ml-auto text-xs font-medium', n > 0 ? 'text-amber-600 dark:text-amber-400' : 'text-emerald-600 dark:text-emerald-400')}>{hasDisk ? (n > 0 ? `${n} 处漂移` : '无漂移') : ''}</span></div>
    </div>
  )
}

// ===== 右栏：当前上下文（看哪个服 / 生效来源构成 / 层栈 / 漂移 / 版本回滚） =====
function RightRail({ state, file, dispatch }: { state: State; file: MockFile | undefined; dispatch: React.Dispatch<Action> }) {
  const cur = file?.version ?? 1
  // 生效配置的来源构成：每个叶子键最终取自哪一层，按层计数 → 直观看出「这台服的配置由各层贡献多少」
  const eff = file ? flatEffective(file, state.group, state.zone, state.lookServer) : []
  const counts: Record<LayerLevel, number> = { global: 0, area: 0, zone: 0, server: 0 }
  for (const e of eff) counts[e.source]++
  const total = eff.length || 1
  const drift = file ? driftCount(file, state.group, state.zone, state.lookServer) : 0
  return (
    <div className="flex flex-col">
      {/* 本服：当前在看哪台服务器 + 归属大区/小区（锁定单服视角时高亮绿色） */}
      <Section title="当前在看">
        <div className="flex items-center gap-1.5 text-xs">
          <Server aria-hidden className="size-3.5 text-emerald-500" />
          <span className="font-mono font-medium text-foreground">{state.lookServer}</span>
        </div>
        <div className="mt-1.5 flex flex-wrap gap-1">
          <span className={cn('rounded-full border px-1.5 py-px text-[0.6rem]', L.area.chip)}>{state.group}</span>
          <span className={cn('rounded-full border px-1.5 py-px text-[0.6rem]', L.zone.chip)}>{state.zone}</span>
        </div>
      </Section>
      {file && eff.length > 0 && (
        <Section title="生效来源构成">
          <div className="flex h-2 overflow-hidden rounded-full">{LAYER_ORDER.map((lvl) => counts[lvl] > 0 ? <span key={lvl} className={cn('h-full', L[lvl].dot)} style={{ width: `${(counts[lvl] / total) * 100}%` }} title={`${LAYER_LABEL[lvl].label} ${counts[lvl]} 项`} /> : null)}</div>
          <div className="mt-1.5 flex flex-col gap-0.5 text-[0.7rem]">{LAYER_ORDER.map((lvl) => <span key={lvl} className="flex items-center gap-1.5"><span className={cn('size-1.5 rounded-full', counts[lvl] > 0 ? L[lvl].dot : 'bg-muted-foreground/30')} />{LAYER_LABEL[lvl].label}<span className="ml-auto font-mono text-muted-foreground">{counts[lvl]}</span></span>)}</div>
        </Section>
      )}
      {file && (
        <Section title="本文件层栈 · 点击切换写入层">
          <div className="flex flex-col gap-1 text-[0.7rem]">{LAYER_ORDER.map((lvl) => { const t = layerText(file, lvl, state.group, state.zone, state.lookServer); const has = t !== null && t.trim() !== ''; const m = L[lvl]; const active = state.writeLayer === lvl; return <button key={lvl} type="button" onClick={() => dispatch({ t: 'layer', level: lvl })} className={cn('flex items-center gap-1.5 rounded-md border px-1.5 py-1 text-left', active ? cn('border-current', m.text) : 'border-transparent hover:bg-muted')}><span className={cn('size-1.5 rounded-full', has ? m.dot : 'bg-muted-foreground/30')} />{LAYER_LABEL[lvl].label}<span className="ml-auto text-muted-foreground">{has ? '✓ 有覆盖' : '继承'}{lvl === 'server' && ' · 最高'}</span></button> })}</div>
        </Section>
      )}
      {file && (
        <Section title="与磁盘漂移">
          <button type="button" onClick={() => { dispatch({ t: 'select', path: file.path }); dispatch({ t: 'mode', mode: 'drift' }) }} className="flex w-full items-center gap-1.5 rounded-md px-1 py-0.5 text-xs hover:bg-muted">
            {drift > 0 ? <span className="flex items-center gap-1.5 text-amber-600 dark:text-amber-400"><AlertTriangle aria-hidden className="size-3.5" /> {drift} 处差异</span> : <span className="flex items-center gap-1.5 text-emerald-600 dark:text-emerald-400"><Check aria-hidden className="size-3.5" /> 一致</span>}
            <span className="ml-auto text-[0.6rem] text-muted-foreground">查看对盘 →</span>
          </button>
        </Section>
      )}
      <Section title="版本 · 回滚"><div className="flex flex-col gap-1">{[cur, cur - 1, cur - 2].filter((v) => v >= 1).map((v, i) => <div key={v} className={cn('flex items-center justify-between rounded-md border px-2 py-1 text-xs', i === 0 ? 'border-primary bg-primary/10 text-primary' : 'border-border bg-background text-muted-foreground')}><span className="font-mono">v{v}</span>{i === 0 ? <span className="text-[0.65rem]">当前</span> : <button type="button" onClick={() => file && dispatch({ t: 'openDiff', path: file.path, version: v })} className="flex items-center gap-1 rounded px-1 hover:bg-muted hover:text-foreground" title="编辑器里看 diff 并回滚"><GitCompare className="size-3" /> diff/回滚</button>}</div>)}</div></Section>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return <div className="border-b border-border px-2.5 py-2"><div className="mb-1.5 text-[0.65rem] text-muted-foreground">{title}</div>{children}</div>
}

// ===== 底部同步队列（更高 + 信息更全 + 待审核可点） =====
const QUEUE_TABS: { id: QueueKind; label: string }[] = [{ id: 'fetch', label: '抓取 / 收编' }, { id: 'publish', label: '发布 / 灰度' }, { id: 'audit', label: '操作日志' }]

function QueueDock({ queue, dispatch, height, onCollapse }: { queue: QueueItem[]; dispatch: React.Dispatch<Action>; height: number; onCollapse: () => void }) {
  const [tab, setTab] = useState<QueueKind>('fetch')
  const rows = queue.filter((q) => q.kind === tab)
  return (
    <div style={{ height }} className="flex shrink-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-2 py-1"><ArrowRightLeft aria-hidden className="size-3.5 text-muted-foreground" /><span className="mr-2 text-xs font-medium text-foreground">同步队列</span>{QUEUE_TABS.map((t) => { const cnt = queue.filter((q) => q.kind === t.id).length; const pending = queue.filter((q) => q.kind === t.id && q.state === '待审核').length; return <button key={t.id} type="button" onClick={() => setTab(t.id)} className={cn('flex items-center gap-1 rounded-md px-2 py-0.5 text-xs', tab === t.id ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground hover:bg-muted')}>{t.label}<span className="rounded-full bg-muted px-1 text-[0.6rem]">{cnt}</span>{pending > 0 && <span className="rounded-full bg-amber-500/20 px-1 text-[0.6rem] text-amber-600 dark:text-amber-400">{pending} 待审</span>}</button> })}<button type="button" onClick={onCollapse} title="收起队列" className="ml-auto rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"><Minus aria-hidden className="size-3.5" /></button></div>
      <div className="scrollbar-hide min-h-0 flex-1 overflow-y-auto">
        {rows.length === 0 ? (
          <div className="flex h-full items-center justify-center text-[0.7rem] text-muted-foreground">暂无{QUEUE_TABS.find((t) => t.id === tab)?.label}消息</div>
        ) : (
          rows.map((q) => {
            const pending = q.state === '待审核'
            const gray = q.state === '灰度中'
            const running = q.progress !== undefined && q.progress < 100
            const dot = pending ? 'bg-amber-500' : gray ? 'bg-violet-500' : running ? 'bg-blue-500' : q.done ? 'bg-emerald-500' : 'bg-muted-foreground/40'
            const stateColor = pending ? 'text-amber-600 dark:text-amber-400' : gray ? 'text-violet-600 dark:text-violet-400' : running ? 'text-blue-600 dark:text-blue-400' : q.done ? 'text-emerald-600 dark:text-emerald-400' : 'text-muted-foreground'
            return (
              <div key={q.id} className="relative flex items-center gap-2 border-b border-border/60 px-3 py-1.5 text-[0.7rem] hover:bg-muted/40">
                <span className={cn('size-1.5 shrink-0 rounded-full', dot)} />
                <span className="max-w-[24%] shrink-0 truncate font-medium text-foreground" title={q.title}>{q.title}</span>
                <span className="min-w-0 flex-1 truncate text-muted-foreground" title={q.detail}>{q.detail}</span>
                <span className="hidden shrink-0 rounded bg-muted px-1 text-[0.6rem] text-muted-foreground sm:inline">{q.operator}</span>
                <span className="hidden max-w-[16%] shrink-0 truncate text-[0.6rem] text-muted-foreground md:inline" title={q.target}>{q.target}</span>
                <span className="hidden shrink-0 text-[0.6rem] text-muted-foreground lg:inline">{q.time}</span>
                <span className="ml-auto flex shrink-0 items-center gap-1.5">
                  {running && <span className="text-[0.6rem] text-blue-600 dark:text-blue-400">{q.progress}%</span>}
                  <span className={cn('font-medium', stateColor)}>{q.state}</span>
                  {pending && q.review && <button type="button" onClick={() => dispatch({ t: 'reviewOpen', id: q.id })} className="rounded border border-amber-400/60 bg-amber-500/10 px-1.5 py-px text-[0.6rem] text-amber-700 hover:bg-amber-500/20 dark:text-amber-300">审核</button>}
                  {gray && (
                    <>
                      <button type="button" onClick={() => dispatch({ t: 'grayPromote', id: q.id })} className="rounded border border-emerald-400/60 px-1.5 py-px text-[0.6rem] text-emerald-600 hover:bg-emerald-500/10 dark:text-emerald-400">晋升</button>
                      <button type="button" onClick={() => dispatch({ t: 'grayAbort', id: q.id })} className="rounded border border-border px-1.5 py-px text-[0.6rem] text-muted-foreground hover:bg-muted">中止</button>
                    </>
                  )}
                </span>
                {running && <span className="absolute bottom-0 left-0 h-0.5 bg-blue-500 transition-all" style={{ width: `${q.progress}%` }} />}
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}

// ===== 浮窗编辑器 =====
function EditorDock({ state, dispatch, msg }: { state: State; dispatch: React.Dispatch<Action>; msg: ReturnType<typeof useMessage> }) {
  const active = state.tabs.find((tb) => tb.id === state.activeTab) ?? state.tabs[state.tabs.length - 1]
  const [full, setFull] = useState(false)
  const [min, setMin] = useState(false)
  const [tabCtx, setTabCtx] = useState<{ x: number; y: number; id: string } | null>(null)
  type Geo = { left: number; top: number; w: number; h: number }
  const [box, setBox] = useState<Geo>(() => ({ left: Math.max(16, window.innerWidth - 644), top: Math.max(16, window.innerHeight - 484), w: 620, h: 460 }))
  const boxRef = useRef(box)
  boxRef.current = box
  const lastRef = useRef(box)
  const winRef = useRef<HTMLDivElement>(null)
  type Mode = 'move' | 'nw' | 'ne' | 'sw' | 'se'
  const drag = useRef<{ mode: Mode; x: number; y: number; b: Geo } | null>(null)
  useEffect(() => {
    function onMove(e: MouseEvent) {
      const d = drag.current
      const el = winRef.current
      if (!d || !el) return
      const dx = e.clientX - d.x
      const dy = e.clientY - d.y
      let { left, top, w, h } = d.b
      if (d.mode === 'move') {
        left = Math.min(Math.max(0, d.b.left + dx), window.innerWidth - 120)
        top = Math.min(Math.max(0, d.b.top + dy), window.innerHeight - 40)
      } else {
        if (d.mode === 'se' || d.mode === 'ne') w = Math.max(360, d.b.w + dx)
        if (d.mode === 'sw' || d.mode === 'nw') { w = Math.max(360, d.b.w - dx); left = d.b.left + (d.b.w - w) }
        if (d.mode === 'se' || d.mode === 'sw') h = Math.max(240, d.b.h + dy)
        if (d.mode === 'ne' || d.mode === 'nw') { h = Math.max(240, d.b.h - dy); top = d.b.top + (d.b.h - h) }
      }
      el.style.left = `${left}px`
      el.style.top = `${top}px`
      el.style.width = `${w}px`
      el.style.height = `${h}px`
      lastRef.current = { left, top, w, h }
    }
    function onUp() {
      if (drag.current) {
        setBox(lastRef.current)
        drag.current = null
        document.body.style.userSelect = ''
      }
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => { window.removeEventListener('mousemove', onMove); window.removeEventListener('mouseup', onUp) }
  }, [])
  function startDrag(mode: Mode, e: React.MouseEvent) {
    if (full) return
    e.stopPropagation()
    drag.current = { mode, x: e.clientX, y: e.clientY, b: boxRef.current }
    lastRef.current = boxRef.current
    document.body.style.userSelect = 'none'
  }
  function saveActive() { if (active && active.kind === 'layer' && active.dirty) { dispatch({ t: 'tabSave', id: active.id }); msg.showSuccess('已存入暂存区（受管草稿，未发布）') } }

  const dirtyCount = state.tabs.filter((tb) => tb.dirty).length
  if (min) {
    return (
      <button type="button" onClick={() => setMin(false)} className="fixed bottom-4 right-4 z-40 flex items-center gap-2 rounded-lg border border-border bg-card px-3 py-2 shadow-xl hover:bg-muted">
        <Pencil className="size-3.5 text-primary" />
        <span className="text-xs font-medium text-foreground">编辑器</span>
        <span className="rounded-full bg-muted px-1.5 text-[0.6rem] text-muted-foreground">{state.tabs.length} 标签</span>
        {dirtyCount > 0 && <span className="size-1.5 rounded-full bg-primary" />}
        <Maximize className="size-3.5 text-muted-foreground" />
      </button>
    )
  }

  const style: React.CSSProperties = full ? { inset: 16 } : { left: box.left, top: box.top, width: box.w, height: box.h }
  return (
    <div ref={winRef} className="fixed z-40 flex flex-col overflow-hidden rounded-xl border border-border bg-card shadow-2xl" style={style} onDragOver={(e) => { if (e.dataTransfer.types.includes('text/cc-path')) e.preventDefault() }} onDrop={(e) => { const p = e.dataTransfer.getData('text/cc-path'); if (p) dispatch({ t: 'openEdit', path: p }) }}>
      <div className="flex shrink-0 items-center gap-1 border-b border-border bg-muted/40 px-1 py-1">
        {/* 仅左上角手柄可拖动窗口 */}
        <div onMouseDown={(e) => startDrag('move', e)} title="拖动窗口" className="flex shrink-0 cursor-move items-center rounded px-1 py-0.5 text-primary hover:bg-background/60"><Pencil className="size-3.5" /></div>
        {/* 页签区铺满并可横向滚动（鼠标滚轮 / 触控板）；超过可视宽度也能滚到后面 */}
        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto scrollbar-hide" onWheel={(e) => { if (e.deltaY) e.currentTarget.scrollLeft += e.deltaY }}>
          {state.tabs.map((tb) => (
            <div key={tb.id} onContextMenu={(e) => { e.preventDefault(); e.stopPropagation(); setTabCtx({ x: e.clientX, y: e.clientY, id: tb.id }) }} className={cn('flex shrink-0 items-center gap-1 rounded-md border py-0.5 pl-2 pr-1 text-[0.7rem]', tb.id === active?.id ? 'border-primary bg-background text-foreground' : 'border-transparent text-muted-foreground hover:bg-background/60')}>
              <button type="button" onClick={() => dispatch({ t: 'tabActivate', id: tb.id })} className="flex max-w-[150px] items-center gap-1">{(tb.kind === 'diff' || tb.kind === 'super') && <GitCompare className="size-3" />}{tb.dirty && <span className="size-1.5 rounded-full bg-primary" />}<span className="truncate">{tb.title}</span></button>
              <button type="button" onClick={() => dispatch({ t: 'tabClose', id: tb.id })} title="关闭标签" className="rounded p-0.5 hover:bg-muted"><X className="size-3" /></button>
            </div>
          ))}
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          <button type="button" onClick={() => setMin(true)} title="收缩到右下角" className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"><Minus className="size-3.5" /></button>
          <button type="button" onClick={() => setFull((f) => !f)} title={full ? '还原' : '全屏'} className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground">{full ? <Minimize2 className="size-3.5" /> : <Maximize className="size-3.5" />}</button>
          <button type="button" onClick={() => dispatch({ t: 'editorClose' })} title="关闭全部" className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"><X className="size-4" /></button>
        </div>
      </div>

      {active && (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1 text-[0.7rem] text-muted-foreground"><span className="font-mono">{active.path}</span>{active.kind === 'layer' && active.layer && <span title="编辑的是受管配置库的草稿（不是服务器磁盘文件）；保存进暂存区，发布才下发" className={cn('rounded-full border px-1.5 py-px text-[0.6rem]', L[active.layer].chip)}>改受管草稿 · {LAYER_LABEL[active.layer].label} 层{active.layer === 'server' ? ` @ ${state.lookServer}` : ''}</span>}{active.kind === 'diff' && <span className="rounded-full border border-border px-1.5 py-px text-[0.6rem]">diff：当前 ↔ v{active.diffVersion}（只读）</span>}{active.kind === 'super' && <span className="rounded-full border border-border px-1.5 py-px text-[0.6rem]">超级diff · {(active.panes ?? []).length} 来源并排（只读）</span>}<span className="ml-auto">{active.lang.toUpperCase()}</span></div>
          <div className="min-h-0 flex-1">{active.kind === 'super' ? <SuperDiff file={state.files[active.path]} group={state.group} zone={state.zone} server={state.lookServer} panes={active.panes ?? []} onToggle={(k) => dispatch({ t: 'superToggle', id: active.id, key: k })} /> : active.kind === 'diff' ? <CodeEditor original={active.original ?? ''} modified={active.modified ?? ''} language={active.lang} sideBySide /> : <CodeEditor value={active.draft} language={active.lang} onChange={(v) => dispatch({ t: 'tabChange', id: active.id, text: v })} />}</div>
          <div className="flex shrink-0 items-center gap-2 border-t border-border px-3 py-1.5 text-xs">
            {active.kind === 'super' ? (
              <><span className="text-muted-foreground">勾选 2-4 个来源横向对比 · 2 个走差异高亮、3-4 个并排只读</span><div className="ml-auto"><Button variant="outline" size="xs" className="h-7" onClick={() => dispatch({ t: 'tabClose', id: active.id })}>关闭</Button></div></>
            ) : active.kind === 'diff' ? (
              <><span className="text-muted-foreground">查看历史版本 diff</span><div className="ml-auto flex items-center gap-1.5"><Button variant="outline" size="xs" className="h-7" onClick={() => dispatch({ t: 'tabClose', id: active.id })}>关闭</Button><Button size="xs" className="h-7" onClick={() => dispatch({ t: 'rollbackAsk', version: active.diffVersion! })}><RotateCcw className="size-3.5" />回滚到 v{active.diffVersion}</Button></div></>
            ) : (
              <><span className="text-muted-foreground">编辑受管库草稿（不改服务器磁盘）· Ctrl+S 存入暂存区，发布才下发</span><div className="ml-auto flex items-center gap-1.5"><Button variant="outline" size="xs" className="h-7" onClick={() => dispatch({ t: 'tabClose', id: active.id })}>关闭</Button><Button size="xs" className="h-7" disabled={!active.dirty} onClick={saveActive}><Save className="size-3.5" />临时保存</Button></div></>
            )}
          </div>
        </div>
      )}
      {!full && (
        <>
          <div onMouseDown={(e) => startDrag('nw', e)} className="absolute left-0 top-0 z-10 size-3 cursor-nwse-resize" />
          <div onMouseDown={(e) => startDrag('ne', e)} className="absolute right-0 top-0 z-10 size-3 cursor-nesw-resize" />
          <div onMouseDown={(e) => startDrag('sw', e)} className="absolute bottom-0 left-0 z-10 size-3 cursor-nesw-resize" />
          <div onMouseDown={(e) => startDrag('se', e)} className="absolute bottom-0 right-0 z-10 size-4 cursor-nwse-resize" style={{ background: 'linear-gradient(135deg, transparent 50%, var(--border-strong, #ccc) 50%)' }} />
        </>
      )}
      {tabCtx && (
        <>
          <div className="fixed inset-0 z-[60]" onClick={() => setTabCtx(null)} onContextMenu={(e) => { e.preventDefault(); setTabCtx(null) }} />
          <div className="fixed z-[61] w-28 rounded-lg border border-border bg-card py-1 text-xs shadow-lg" style={{ left: tabCtx.x, top: tabCtx.y }}>
            <button type="button" onClick={() => { dispatch({ t: 'tabClose', id: tabCtx.id }); setTabCtx(null) }} className="flex w-full px-3 py-1 text-left hover:bg-muted">关闭</button>
            <button type="button" onClick={() => { dispatch({ t: 'tabCloseOthers', id: tabCtx.id }); setTabCtx(null) }} className="flex w-full px-3 py-1 text-left hover:bg-muted">关闭其他</button>
            <button type="button" onClick={() => { dispatch({ t: 'editorClose' }); setTabCtx(null) }} className="flex w-full px-3 py-1 text-left hover:bg-muted">关闭全部</button>
          </div>
        </>
      )}
    </div>
  )
}

// ===== 超级 diff：选 2-4 个来源横向并排对比 =====
// 只读 Monaco 选项（模块常量，避免每帧新对象触发 updateOptions 抖动）
const READ_OPTIONS: editor.IStandaloneEditorConstructionOptions = {
  readOnly: true,
  fontSize: 11,
  fontFamily: 'var(--font-mono)',
  minimap: { enabled: false },
  scrollBeyondLastLine: false,
  automaticLayout: true,
  fixedOverflowWidgets: true,
  lineNumbers: 'on',
  lineNumbersMinChars: 3,
  folding: true,
  padding: { top: 6 },
  scrollbar: { verticalScrollbarSize: 6, horizontalScrollbarSize: 6 },
}

// 可选来源：四层覆盖 + 合并生效 + 磁盘快照
const PANE_OPTS: { key: string; label: string }[] = [
  { key: 'global', label: '全局' },
  { key: 'area', label: '大区' },
  { key: 'zone', label: '小区' },
  { key: 'server', label: '单服' },
  { key: 'effective', label: '生效' },
  { key: 'disk', label: '磁盘' },
]

// 取某来源的文本：生效=合并后源文件；磁盘=该服磁盘快照；其余=对应层原始覆盖文本
function paneText(file: MockFile, key: string, group: string, zone: string, server: string): string {
  if (key === 'effective') return effectiveSource(file, group, zone, server)
  if (key === 'disk') return file.disk?.[server] ?? '# 该服无磁盘快照'
  const t = layerText(file, key as LayerLevel, group, zone, server)
  return t ?? '# 本层无覆盖（继承上层）'
}

function ReadPane({ text, lang }: { text: string; lang: string }) {
  const monacoLang = lang === 'json' ? 'json' : lang === 'yaml' ? 'yaml' : 'plaintext'
  return <Editor value={text} language={monacoLang} theme="vs" options={READ_OPTIONS} loading={<div className="flex h-full items-center justify-center text-xs text-muted-foreground">加载编辑器…</div>} />
}

function SuperDiff({ file, group, zone, server, panes, onToggle }: { file: MockFile | undefined; group: string; zone: string; server: string; panes: string[]; onToggle: (key: string) => void }) {
  if (!file) return <div className="flex h-full items-center justify-center text-xs text-muted-foreground">文件不存在</div>
  const lang = fileLang(file)
  const sel = PANE_OPTS.filter((o) => panes.includes(o.key)).map((o) => o.key)
  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-border px-3 py-1.5 text-[0.7rem]">
        <span className="text-muted-foreground">放一起对比（2-4 个）：</span>
        {PANE_OPTS.map((o) => { const on = sel.includes(o.key); return <button key={o.key} type="button" onClick={() => onToggle(o.key)} className={cn('rounded-md border px-2 py-0.5', on ? 'border-primary bg-primary/5 text-foreground' : 'border-border text-muted-foreground hover:bg-muted')}>{o.label}</button> })}
        <span className="ml-auto text-muted-foreground">已选 {sel.length} / 4</span>
      </div>
      <div className="min-h-0 flex-1">
        {sel.length < 2 ? (
          <div className="flex h-full items-center justify-center text-xs text-muted-foreground">至少选 2 个来源开始对比</div>
        ) : sel.length === 2 ? (
          <CodeEditor original={paneText(file, sel[0], group, zone, server)} modified={paneText(file, sel[1], group, zone, server)} language={lang} sideBySide />
        ) : (
          <div className="flex h-full">
            {sel.map((k) => (
              <div key={k} className="flex min-w-0 flex-1 flex-col border-r border-border last:border-r-0">
                <div className="shrink-0 border-b border-border bg-muted/40 px-2 py-1 text-[0.65rem] font-medium text-muted-foreground">{PANE_OPTS.find((o) => o.key === k)?.label}</div>
                <div className="min-h-0 flex-1"><ReadPane text={paneText(file, k, group, zone, server)} lang={lang} /></div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// ===== 右键菜单 =====
function ContextMenu({ x, y, path, isDir, onClose, dispatch }: { x: number; y: number; path: string; isDir: boolean; onClose: () => void; dispatch: React.Dispatch<Action> }) {
  const items: { label: string; icon: typeof Globe; run: () => void }[] = isDir
    ? [
        { label: '递归对盘 diff', icon: ArrowRightLeft, run: () => dispatch({ t: 'selectDir', path }) },
        { label: '整目录抓取入库', icon: Download, run: () => dispatch({ t: 'openDirDialog', dialog: 'scan-dir', dir: path }) },
        { label: '整目录批量发布', icon: Rocket, run: () => dispatch({ t: 'openDirDialog', dialog: 'publish-dir', dir: path }) },
        { label: '整目录加入排除', icon: FilterX, run: () => dispatch({ t: 'addExclude', pattern: `${path}/**` }) },
      ]
    : [
        { label: '打开编辑器', icon: Pencil, run: () => dispatch({ t: 'openEdit', path }) },
        { label: '对盘对比', icon: ArrowRightLeft, run: () => { dispatch({ t: 'select', path }); dispatch({ t: 'mode', mode: 'drift' }) } },
        { label: '层列对比', icon: Columns3, run: () => { dispatch({ t: 'select', path }); dispatch({ t: 'mode', mode: 'columns' }) } },
        { label: '加入排除名单', icon: FilterX, run: () => dispatch({ t: 'addExclude', pattern: path }) },
      ]
  return (
    <div className="fixed z-50 w-44 rounded-lg border border-border bg-card py-1 shadow-lg" style={{ left: x, top: y }} onClick={(e) => e.stopPropagation()}>
      <div className="border-b border-border px-3 py-1 font-mono text-[0.65rem] text-muted-foreground">{isDir ? `${path || '根目录'}/` : path.split('/').pop()}</div>
      {items.map((it) => { const Icon = it.icon; return <button key={it.label} type="button" onClick={() => { it.run(); onClose() }} className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-foreground hover:bg-muted"><Icon className="size-3.5 text-muted-foreground" />{it.label}</button> })}
    </div>
  )
}

// ===== 弹层壳 =====
function Modal({ title, icon, onClose, children, width = 'w-[460px]' }: { title: string; icon: React.ReactNode; onClose: () => void; children: React.ReactNode; width?: string }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div className={cn('max-w-full overflow-hidden rounded-xl border border-border bg-card shadow-lg', width)} onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-2 border-b border-border px-4 py-2.5">{icon}<span className="text-sm font-medium text-foreground">{title}</span><button type="button" onClick={onClose} className="ml-auto rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"><X className="size-4" /></button></div>
        <div className="p-4">{children}</div>
      </div>
    </div>
  )
}

// 危险操作勾选框二次确认（发布 / 批量发布共用）：未勾选则确认按钮禁用
function DangerConfirm({ checked, onCheck, label }: { checked: boolean; onCheck: (v: boolean) => void; label: string }) {
  return (
    <label className="flex cursor-pointer items-start gap-2 rounded-md border border-amber-400/50 bg-amber-500/10 px-3 py-2 text-amber-700 dark:text-amber-300">
      <input type="checkbox" checked={checked} onChange={(e) => onCheck(e.target.checked)} className="mt-0.5 size-3.5 shrink-0" />
      <span className="flex items-start gap-1"><AlertTriangle aria-hidden className="mt-px size-3.5 shrink-0" />{label}</span>
    </label>
  )
}

// 灰度 cohort 服务器多选（文件发布 / 整目录发布共用）
function CohortPicker({ servers, sel, setSel }: { servers: SrvMeta[]; sel: Set<string>; setSel: React.Dispatch<React.SetStateAction<Set<string>>> }) {
  const [q, setQ] = useState('')
  const filtered = useMemo(() => { const f = q.trim().toLowerCase(); return servers.filter((s) => !f || s.id.toLowerCase().includes(f) || s.group.includes(f) || s.zone.includes(f)).slice(0, 100) }, [q, servers])
  const groups = useMemo(() => Array.from(new Set(servers.map((s) => s.group))), [servers])
  function toggle(id: string) { setSel((p) => { const n = new Set(p); if (n.has(id)) n.delete(id); else n.add(id); return n }) }
  function addGroup(g: string) { setSel((p) => { const n = new Set(p); servers.filter((s) => s.group === g).forEach((s) => n.add(s.id)); return n }) }
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2"><span className="text-muted-foreground">cohort 已选 <span className="font-medium text-foreground">{sel.size}</span> 台</span><button type="button" onClick={() => setSel(new Set())} className="rounded border border-border px-1.5 py-px text-[0.65rem] hover:bg-muted">清空</button><div className="ml-auto flex items-center gap-1"><span className="text-[0.65rem] text-muted-foreground">快速加大区</span><select onChange={(e) => { if (e.target.value) addGroup(e.target.value); e.target.value = '' }} className="h-6 rounded border border-border bg-background text-[0.65rem]" defaultValue=""><option value="" disabled>选大区…</option>{groups.map((g) => <option key={g} value={g}>{g}</option>)}</select></div></div>
      <div className="flex items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1"><Search className="size-3.5 text-muted-foreground" /><input value={q} onChange={(e) => setQ(e.target.value)} placeholder={`搜索 ${servers.length} 台实例`} className="w-full bg-transparent text-xs outline-none" /></div>
      <div className="scrollbar-hide max-h-44 overflow-y-auto rounded-md border border-border">{filtered.map((sv) => <label key={sv.id} className="flex cursor-pointer items-center gap-2 border-b border-border/60 px-2 py-1 last:border-0 hover:bg-muted"><input type="checkbox" checked={sel.has(sv.id)} onChange={() => toggle(sv.id)} className="size-3.5" /><span className={cn('size-1.5 rounded-full', sv.online ? 'bg-emerald-500' : 'bg-muted-foreground/40')} /><span className="flex-1 truncate font-mono">{sv.id}</span><span className="text-[0.6rem] text-muted-foreground">{sv.group}/{sv.zone}</span></label>)}</div>
      <p className="text-[0.65rem] text-muted-foreground">灰度仅这 {sel.size} 台先拿新版，其余 {servers.length - sel.size} 台维持当前版本，可随后晋升 / 中止。</p>
    </div>
  )
}

// ===== 弹层：发布（单文件）=====
function PublishDialog({ state, file, servers, dispatch, msg }: { state: State; file: MockFile; servers: SrvMeta[]; dispatch: React.Dispatch<Action>; msg: ReturnType<typeof useMessage> }) {
  const [gray, setGray] = useState(false)
  const [sel, setSel] = useState<Set<string>>(() => new Set([state.lookServer]))
  const [ack, setAck] = useState(false)
  const cnt = layerScopeCounts(servers, state.group, state.zone)[state.writeLayer]
  const scope = gray ? `灰度 ${sel.size} 台` : `${LAYER_LABEL[state.writeLayer].label} 层 · 覆盖 ${cnt.total} 台（在线 ${cnt.online}）`
  return (
    <Modal title={`发布 · ${file.name}`} icon={<Rocket className="size-4 text-primary" />} onClose={() => dispatch({ t: 'dialog', dialog: null })} width="w-[540px]">
      <div className="flex flex-col gap-3 text-xs">
        <div className="flex items-center gap-2"><span className="text-muted-foreground">发布范围</span><div className="flex overflow-hidden rounded-md border border-border"><button type="button" onClick={() => { setGray(false); setAck(false) }} className={cn('px-3 py-1', !gray ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground')}>全量</button><button type="button" onClick={() => { setGray(true); setAck(false) }} className={cn('flex items-center gap-1 px-3 py-1', gray ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground')}><FlaskConical className="size-3" /> 灰度</button></div><span className="ml-auto flex items-center gap-1 text-emerald-600 dark:text-emerald-400"><Check className="size-3" /> 校验通过</span></div>
        {!gray && <div className="rounded-md bg-muted/50 px-3 py-2 text-[0.7rem] text-muted-foreground">发布到 <span className="font-medium text-foreground">{LAYER_LABEL[state.writeLayer].label}</span> 层 —— 覆盖 <span className="font-medium text-foreground">{cnt.total}</span> 台（在线 {cnt.online}）中未单独覆盖的服。</div>}
        {gray && <CohortPicker servers={servers} sel={sel} setSel={setSel} />}
        <DangerConfirm checked={ack} onCheck={setAck} label={`我确认将 ${file.name} 发布到「${scope}」，将立即热推到在线服务器。`} />
        <div className="flex items-center gap-2"><span className="ml-auto" /><Button variant="outline" size="xs" className="h-7" onClick={() => dispatch({ t: 'dialog', dialog: null })}>取消</Button><Button size="xs" className="h-7" disabled={!ack || (gray && sel.size === 0)} onClick={() => { dispatch({ t: 'publish', gray, cohort: sel.size }); msg.showSuccess(gray ? `已灰度发布给 ${sel.size} 台` : '已发布并热更') }}>{gray ? '灰度发布' : '发布并热更'}</Button></div>
      </div>
    </Modal>
  )
}

// ===== 弹层：排除名单 =====
function ExcludeDialog({ state, dispatch }: { state: State; dispatch: React.Dispatch<Action> }) {
  const [pattern, setPattern] = useState('')
  return (
    <Modal title="排除名单（通配符）" icon={<FilterX className="size-4 text-primary" />} onClose={() => dispatch({ t: 'dialog', dialog: null })} width="w-[520px]">
      <div className="flex flex-col gap-3 text-xs">
        <div className="overflow-hidden rounded-md border border-border"><table className="w-full"><tbody>{state.excludeRules.map((r) => <tr key={r.id} className="border-b border-border/60"><td className="px-2 py-1.5 font-mono">{r.pattern}</td><td className="px-2 py-1.5 text-muted-foreground">{r.scope}</td><td className="px-2 py-1.5"><div className="flex gap-1">{(['scan', 'sync', 'manage'] as const).map((a) => <button key={a} type="button" onClick={() => dispatch({ t: 'toggleExclude', id: r.id, field: a })} className={cn('rounded px-1.5 py-px text-[0.6rem]', r[a] ? 'border border-emerald-400/60 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400' : 'border border-border text-muted-foreground')}>{a === 'scan' ? '扫描' : a === 'sync' ? '同步' : '管理'}</button>)}</div></td><td className="px-2 py-1.5 text-right"><button type="button" onClick={() => dispatch({ t: 'removeExclude', id: r.id })} className="text-muted-foreground hover:text-destructive"><Trash2 className="size-3.5" /></button></td></tr>)}</tbody></table></div>
        <div className="flex items-center gap-2"><Plus className="size-3.5 text-muted-foreground" /><input value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder="新模式  例如  *.log  或  world/**" className="h-8 flex-1 rounded-md border border-border bg-background px-2 font-mono text-xs outline-none focus:border-primary" /><Button size="xs" className="h-8" disabled={!pattern.trim()} onClick={() => { dispatch({ t: 'addExclude', pattern: pattern.trim() }); setPattern('') }}>添加</Button></div>
        <p className="text-[0.7rem] text-muted-foreground">通配符 <span className="font-mono">*</span> 单层 · <span className="font-mono">**</span> 多层 · <span className="font-mono">?</span> 单字符。命中路径在文件树灰显「排除」。</p>
      </div>
    </Modal>
  )
}

// ===== 目录递归视图：整目录对盘 + 目录级操作 =====
function DirectoryView({ state, dir, dispatch }: { state: State; dir: string; dispatch: React.Dispatch<Action> }) {
  const [open, setOpen] = useState<Set<string>>(() => new Set())
  const rows = dirFileRows(dir, state.files, state.excludeRules, state.group, state.zone, state.lookServer)
  const active = rows.filter((r) => !r.excluded)
  const drifted = active.filter((r) => r.managed && r.drift > 0)
  const unman = active.filter((r) => r.unmanaged)
  const label = dir === '' ? '整个根目录' : dir
  function toggle(p: string) { setOpen((s) => { const n = new Set(s); if (n.has(p)) n.delete(p); else n.add(p); return n }) }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-3 py-1.5 text-xs">
        <FolderTree aria-hidden className="size-3.5 text-muted-foreground" />
        <span className="font-mono font-medium text-foreground">{label}</span>
        <span className="rounded-full border border-border px-1.5 py-px text-[0.6rem] text-muted-foreground">{active.length} 文件</span>
        {drifted.length > 0 && <span className="rounded-full border border-amber-400/60 px-1.5 py-px text-[0.6rem] text-amber-600 dark:text-amber-400">{drifted.length} 有漂移</span>}
        {unman.length > 0 && <span className="rounded-full border border-sky-400/60 px-1.5 py-px text-[0.6rem] text-sky-600 dark:text-sky-400">{unman.length} 未纳管</span>}
        <div className="ml-auto flex items-center gap-1.5">
          <Button variant="outline" size="xs" className="h-6 text-[0.7rem]" onClick={() => dispatch({ t: 'openDirDialog', dialog: 'scan-dir', dir })}><Download aria-hidden className="size-3" />抓取入库</Button>
          <Button variant="outline" size="xs" className="h-6 text-[0.7rem]" onClick={() => dispatch({ t: 'openDirDialog', dialog: 'publish-dir', dir })}><Rocket aria-hidden className="size-3" />批量发布</Button>
          <Button variant="outline" size="xs" className="h-6 text-[0.7rem]" onClick={() => dispatch({ t: 'addExclude', pattern: `${dir}/**` })}><FilterX aria-hidden className="size-3" />加入排除</Button>
        </div>
      </div>
      <div className="shrink-0 px-3 py-1.5 text-[0.7rem] text-muted-foreground">递归对盘：{state.lookServer} 磁盘实际 vs 中心生效 · 点有漂移的行展开看逐行差异</div>
      <div className="scrollbar-hide min-h-0 flex-1 overflow-y-auto">
        {active.length === 0 ? (
          <div className="flex h-full items-center justify-center text-xs text-muted-foreground">该目录下无可管理文件</div>
        ) : (
          active.map((r) => {
            const expandable = r.managed && r.hasDisk && r.drift > 0
            const isOpen = open.has(r.path)
            return (
              <div key={r.path} className="border-b border-border/60">
                <div className={cn('flex items-center gap-2 px-3 py-1.5 text-xs', expandable && 'cursor-pointer hover:bg-muted/40')} onClick={() => expandable && toggle(r.path)}>
                  <span className="inline-block w-3 text-center text-[0.6rem] text-muted-foreground">{expandable ? (isOpen ? '▾' : '▸') : ''}</span>
                  {r.name.endsWith('.json') ? <FileCode aria-hidden className="size-3.5 shrink-0 text-muted-foreground" /> : <FileText aria-hidden className={cn('size-3.5 shrink-0', r.unmanaged ? 'text-sky-500' : 'text-muted-foreground')} />}
                  <span className="truncate font-mono text-foreground" title={r.path}>{r.path}</span>
                  <span className="ml-auto flex shrink-0 items-center gap-2">
                    {r.unmanaged ? <span className="rounded-full border border-sky-400/60 px-1.5 py-px text-[0.6rem] text-sky-600 dark:text-sky-400">未纳管 · {r.drift} 行</span> : !r.hasDisk ? <span className="text-[0.6rem] text-muted-foreground">无磁盘快照</span> : r.drift > 0 ? <span className="rounded-full border border-amber-400/60 px-1.5 py-px text-[0.6rem] text-amber-600 dark:text-amber-400">{r.drift} 处漂移</span> : <span className="flex items-center gap-1 text-[0.6rem] text-emerald-600 dark:text-emerald-400"><Check aria-hidden className="size-3" /> 一致</span>}
                    {r.managed && <button type="button" onClick={(e) => { e.stopPropagation(); dispatch({ t: 'select', path: r.path }) }} className="rounded border border-border px-1.5 py-px text-[0.6rem] text-muted-foreground hover:bg-muted hover:text-foreground">打开</button>}
                  </span>
                </div>
                {expandable && isOpen && <DirDriftRows file={state.files[r.path]!} state={state} />}
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}

// 目录视图里单个文件展开的逐行对盘差异（只列有差异的行）
function DirDriftRows({ file, state }: { file: MockFile; state: State }) {
  const rows = driftRows(file, state.group, state.zone, state.lookServer).filter((r) => r.kind !== 'same')
  return (
    <div className="border-t border-border/60 bg-muted/20 px-3 py-1.5">
      {rows.map((r) => (
        <div key={r.path} className="flex items-center gap-2 py-0.5 text-[0.7rem]">
          <span className="w-1/3 truncate font-mono text-muted-foreground" title={r.path}>{r.path}</span>
          <span className="flex-1 truncate font-mono">{r.effective !== null ? <span className="rounded bg-rose-500/10 px-1">{r.effective}</span> : <span className="text-muted-foreground/40">—</span>}</span>
          <span className="text-muted-foreground">→</span>
          <span className="flex-1 truncate font-mono">{r.disk !== null ? <span className="rounded bg-emerald-500/10 px-1">{r.disk}</span> : <span className="text-muted-foreground/40">—</span>}</span>
        </div>
      ))}
    </div>
  )
}

// ===== 磁盘文件面板（服务器磁盘上的非受管文件：未纳管配置 / 运行期文件）=====
function DiskFilePanel({ state, path, dispatch, msg }: { state: State; path: string; dispatch: React.Dispatch<Action>; msg: ReturnType<typeof useMessage> }) {
  const text = diskText(path, state.files, state.group, state.zone, state.lookServer)
  const unmanaged = unmanagedText(path, state.lookServer) !== null
  const excluded = isExcluded(path, state.excludeRules)
  const binary = isBinaryName(path)
  const dir = path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : ''
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1.5 text-xs">
        {binary ? <Database aria-hidden className="size-3.5 text-muted-foreground" /> : <FileText aria-hidden className={cn('size-3.5', unmanaged ? 'text-sky-500' : 'text-muted-foreground')} />}
        <span className="font-mono font-medium text-foreground">{path}</span>
        {unmanaged ? <span className="rounded-full border border-sky-400/60 px-1.5 py-px text-[0.6rem] text-sky-600 dark:text-sky-400">未纳管</span> : <span className="rounded-full border border-border px-1.5 py-px text-[0.6rem] text-muted-foreground">仅磁盘</span>}
        {excluded && <span className="rounded-full border border-dashed border-border px-1.5 py-px text-[0.6rem] text-muted-foreground">排除</span>}
        {/* 仅未纳管配置可抓取；运行期 / 二进制 / 排除文件不纳管 */}
        {unmanaged && !binary && <Button size="xs" className="ml-auto h-6 text-[0.7rem]" onClick={() => { dispatch({ t: 'ingestDir', dir, paths: [path] }); msg.showSuccess('已抓入「暂存区」· 全局层草稿（未发布）· 去暂存区编辑 / 定层后发布才下发') }}><Download aria-hidden className="size-3" />抓取入库</Button>}
      </div>
      <div className="shrink-0 px-3 py-1.5 text-[0.7rem] text-muted-foreground">{state.lookServer} 磁盘实际内容（只读）。{unmanaged ? '尚未纳管，抓取后进「暂存区」（全局层草稿），在那里编辑 / 定层，发布才下发。' : excluded ? '该文件被排除名单命中，不纳管。' : '运行期文件，通常不纳管。'}</div>
      <pre className="scrollbar-hide min-h-0 flex-1 overflow-auto px-3 py-2 font-mono text-xs text-foreground">{text}</pre>
    </div>
  )
}

// ===== 弹层：整目录递归抓取入库 =====
function DirScanDialog({ state, dir, dispatch, msg }: { state: State; dir: string; dispatch: React.Dispatch<Action>; msg: ReturnType<typeof useMessage> }) {
  const rows = dirFileRows(dir, state.files, state.excludeRules, state.group, state.zone, state.lookServer)
  const actionable = rows.filter((r) => !r.excluded && (r.unmanaged || (r.managed && r.hasDisk && r.drift > 0)))
  const excludedCount = rows.filter((r) => r.excluded).length
  const [sel, setSel] = useState<Set<string>>(() => new Set(actionable.map((r) => r.path)))
  const label = dir === '' ? '整个根目录' : dir
  function toggle(p: string) { setSel((s) => { const n = new Set(s); if (n.has(p)) n.delete(p); else n.add(p); return n }) }
  return (
    <Modal title={`抓取入库 · ${label}`} icon={<Download className="size-4 text-primary" />} onClose={() => dispatch({ t: 'dialog', dialog: null })} width="w-[560px]">
      <div className="flex flex-col gap-3 text-xs">
        <div className="text-muted-foreground">递归扫描 {label}（{state.lookServer}），列出「未纳管」与「有漂移」的文件，勾选后一次性抓取入库。{excludedCount > 0 && `已排除 ${excludedCount} 个文件，跳过。`}</div>
        {actionable.length === 0 ? (
          <div className="rounded-md border border-border px-3 py-4 text-center text-muted-foreground">该目录下无未纳管 / 漂移文件，与中心生效全部一致。</div>
        ) : (
          <>
            <div className="flex items-center gap-2"><button type="button" onClick={() => setSel(new Set(actionable.map((r) => r.path)))} className="rounded border border-border px-1.5 py-px text-[0.65rem] hover:bg-muted">全选</button><button type="button" onClick={() => setSel(new Set())} className="rounded border border-border px-1.5 py-px text-[0.65rem] hover:bg-muted">清空</button><span className="ml-auto text-muted-foreground">已选 {sel.size} / {actionable.length}</span></div>
            <div className="scrollbar-hide max-h-72 overflow-y-auto rounded-md border border-border">{actionable.map((r) => <label key={r.path} className="flex cursor-pointer items-center gap-2 border-b border-border/60 px-2 py-1.5 last:border-0 hover:bg-muted"><input type="checkbox" checked={sel.has(r.path)} onChange={() => toggle(r.path)} className="size-3.5" />{r.name.endsWith('.json') ? <FileCode className="size-3.5 shrink-0 text-muted-foreground" /> : <FileText className={cn('size-3.5 shrink-0', r.unmanaged ? 'text-sky-500' : 'text-muted-foreground')} />}<span className="flex-1 truncate font-mono">{r.path}</span>{r.unmanaged ? <span className="rounded-full border border-sky-400/60 px-1.5 py-px text-[0.6rem] text-sky-600 dark:text-sky-400">未纳管 · {r.drift} 行</span> : <span className="text-amber-600 dark:text-amber-400">{r.drift} 处漂移</span>}</label>)}</div>
            <p className="text-[0.65rem] text-muted-foreground">未纳管文件以磁盘内容作为全局基线纳入；有漂移的已纳管文件按磁盘收编为单服补丁。均入受管库（未发布）。</p>
          </>
        )}
        <div className="flex items-center gap-2"><span className="ml-auto" /><Button variant="outline" size="xs" className="h-7" onClick={() => dispatch({ t: 'dialog', dialog: null })}>取消</Button><Button size="xs" className="h-7" disabled={sel.size === 0} onClick={() => { dispatch({ t: 'ingestDir', dir, paths: [...sel] }); msg.showSuccess(`已抓入「暂存区」${sel.size} 个待发布草稿 · 在暂存区编辑 / 定层后发布`) }}><Download className="size-3.5" />抓取 {sel.size} 个入库</Button></div>
      </div>
    </Modal>
  )
}

// ===== 弹层：整目录批量发布 =====
function DirPublishDialog({ state, dir, servers, dispatch, msg }: { state: State; dir: string; servers: SrvMeta[]; dispatch: React.Dispatch<Action>; msg: ReturnType<typeof useMessage> }) {
  const rows = dirFileRows(dir, state.files, state.excludeRules, state.group, state.zone, state.lookServer)
  const managed = rows.filter((r) => r.managed && !r.excluded)
  const [gray, setGray] = useState(false)
  const [sel, setSel] = useState<Set<string>>(() => new Set([state.lookServer]))
  const [ack, setAck] = useState(false)
  const cnt = layerScopeCounts(servers, state.group, state.zone)[state.writeLayer]
  const label = dir === '' ? '整个根目录' : dir
  return (
    <Modal title={`批量发布 · ${label}`} icon={<Rocket className="size-4 text-primary" />} onClose={() => dispatch({ t: 'dialog', dialog: null })} width="w-[560px]">
      <div className="flex flex-col gap-3 text-xs">
        <div className="text-muted-foreground">把 {label} 下 <span className="font-medium text-foreground">{managed.length}</span> 个受管文件统一发布到 {LAYER_LABEL[state.writeLayer].label} 层。</div>
        <div className="scrollbar-hide max-h-40 overflow-y-auto rounded-md border border-border">{managed.length === 0 ? <div className="px-3 py-3 text-center text-muted-foreground">该目录下无受管文件可发布</div> : managed.map((r) => <div key={r.path} className="flex items-center gap-2 border-b border-border/60 px-2 py-1 last:border-0">{r.name.endsWith('.json') ? <FileCode className="size-3.5 shrink-0 text-muted-foreground" /> : <FileText className="size-3.5 shrink-0 text-muted-foreground" />}<span className="flex-1 truncate font-mono">{r.path}</span><span className="text-[0.6rem] text-muted-foreground">v{state.files[r.path]?.version ?? 1}</span></div>)}</div>
        <div className="flex items-center gap-2"><span className="text-muted-foreground">发布范围</span><div className="flex overflow-hidden rounded-md border border-border"><button type="button" onClick={() => setGray(false)} className={cn('px-3 py-1', !gray ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground')}>全量</button><button type="button" onClick={() => setGray(true)} className={cn('flex items-center gap-1 px-3 py-1', gray ? 'bg-accent font-medium text-accent-foreground' : 'text-muted-foreground')}><FlaskConical className="size-3" /> 灰度</button></div><span className="ml-auto flex items-center gap-1 text-emerald-600 dark:text-emerald-400"><Check className="size-3" /> 校验通过</span></div>
        {!gray && <div className="rounded-md bg-muted/50 px-3 py-2 text-[0.7rem] text-muted-foreground">发布到 <span className="font-medium text-foreground">{LAYER_LABEL[state.writeLayer].label}</span> 层 —— 覆盖 <span className="font-medium text-foreground">{cnt.total}</span> 台（在线 {cnt.online}）中未单独覆盖的服。</div>}
        {gray && <CohortPicker servers={servers} sel={sel} setSel={setSel} />}
        {managed.length > 0 && <DangerConfirm checked={ack} onCheck={setAck} label={`我确认把 ${label} 下 ${managed.length} 个文件发布到「${gray ? `灰度 ${sel.size} 台` : `${LAYER_LABEL[state.writeLayer].label} 层 · 覆盖 ${cnt.total} 台`}」，将立即下发。`} />}
        <div className="flex items-center gap-2"><span className="ml-auto" /><Button variant="outline" size="xs" className="h-7" onClick={() => dispatch({ t: 'dialog', dialog: null })}>取消</Button><Button size="xs" className="h-7" disabled={!ack || managed.length === 0 || (gray && sel.size === 0)} onClick={() => { dispatch({ t: 'publishDir', dir, paths: managed.map((r) => r.path), gray, cohort: sel.size }); msg.showSuccess(gray ? `已灰度发布 ${managed.length} 个文件给 ${sel.size} 台` : `已发布 ${managed.length} 个文件并热更`) }}>{gray ? '灰度发布' : `发布 ${managed.length} 个并热更`}</Button></div>
      </div>
    </Modal>
  )
}

// ===== 弹层：发布全部暂存（二次确认发布位置）=====
function PublishStagedDialog({ state, servers, dispatch, msg }: { state: State; servers: SrvMeta[]; dispatch: React.Dispatch<Action>; msg: ReturnType<typeof useMessage> }) {
  const items = state.staged.filter((p) => state.files[p])
  const [ack, setAck] = useState(false)
  const layer = state.writeLayer
  const cnt = layerScopeCounts(servers, state.group, state.zone)[layer]
  return (
    <Modal title="发布全部暂存" icon={<Rocket className="size-4 text-primary" />} onClose={() => dispatch({ t: 'dialog', dialog: null })} width="w-[560px]">
      <div className="flex flex-col gap-3 text-xs">
        {/* 一次只发一个层：整批统一发到当前写入层，明确显示影响范围 */}
        <div className="flex items-center gap-2 rounded-md bg-muted/50 px-3 py-2"><span className="text-muted-foreground">共 <span className="font-medium text-foreground">{items.length}</span> 个文件，统一发布到</span><span className={cn('rounded-full border px-2 py-0.5 text-[0.65rem]', L[layer].chip)}>{LAYER_LABEL[layer].label}</span><span className="text-muted-foreground">层 · 覆盖 <span className="font-medium text-foreground">{cnt.total}</span> 台（在线 {cnt.online}）</span></div>
        <div className="text-[0.7rem] text-muted-foreground">一次只发布到一个层。要改层请先在上方「写入层」切换，再发布。发布后按覆盖链热推到 {LAYER_LABEL[layer].label} 范围内未单独覆盖的服。</div>
        <div className="scrollbar-hide max-h-56 overflow-y-auto rounded-md border border-border">
          {items.map((p) => <div key={p} className="flex items-center gap-2 border-b border-border/50 px-3 py-1 last:border-0">{p.endsWith('.json') ? <FileCode className="size-3 shrink-0 text-muted-foreground" /> : <FileText className="size-3 shrink-0 text-muted-foreground" />}<span className="flex-1 truncate font-mono">{p}</span><span className="text-[0.6rem] text-muted-foreground">v{state.files[p]?.version ?? 1}</span></div>)}
        </div>
        <DangerConfirm checked={ack} onCheck={setAck} label={`我确认把这 ${items.length} 个文件发布到「${LAYER_LABEL[layer].label}」层（覆盖 ${cnt.total} 台），将立即热推下发。`} />
        <div className="flex items-center gap-2"><span className="ml-auto" /><Button variant="outline" size="xs" className="h-7" onClick={() => dispatch({ t: 'dialog', dialog: null })}>取消</Button><Button size="xs" className="h-7" disabled={!ack || items.length === 0} onClick={() => { dispatch({ t: 'publishStaged' }); msg.showSuccess(`已发布暂存区 ${items.length} 个文件 → ${LAYER_LABEL[layer].label}`) }}><Rocket className="size-3.5" />确认发布 {items.length} 个 → {LAYER_LABEL[layer].short}</Button></div>
      </div>
    </Modal>
  )
}

// ===== 弹层：待审核纳管 =====
function ReviewDialog({ item, dispatch }: { item: QueueItem | undefined; dispatch: React.Dispatch<Action> }) {
  const [sel, setSel] = useState<Set<string>>(() => new Set(item?.review?.map((r) => r.path) ?? []))
  if (!item) return null
  function toggle(p: string) { setSel((x) => { const n = new Set(x); if (n.has(p)) n.delete(p); else n.add(p); return n }) }
  return (
    <Modal title={`审核纳管 · ${item.title}`} icon={<ListChecks className="size-4 text-primary" />} onClose={() => dispatch({ t: 'reviewClose' })} width="w-[520px]">
      <div className="flex flex-col gap-3 text-xs">
        <div className="text-muted-foreground">{item.operator} 发起 · 目标 {item.target}。勾选要纳管的文件，通过后入受管库（未发布）。</div>
        <div className="overflow-hidden rounded-md border border-border">{(item.review ?? []).map((rf) => <label key={rf.path} className="flex cursor-pointer items-center gap-2 border-b border-border/60 px-3 py-1.5 last:border-0 hover:bg-muted"><input type="checkbox" checked={sel.has(rf.path)} onChange={() => toggle(rf.path)} className="size-3.5" /><FileText className="size-3.5 text-muted-foreground" /><span className="flex-1 truncate font-mono">{rf.path}</span><span className="text-amber-600 dark:text-amber-400">{rf.lines} 行</span></label>)}</div>
        <div className="flex items-center gap-2"><span className="text-[0.7rem] text-muted-foreground">已选 {sel.size} / {item.review?.length ?? 0} 个文件</span><span className="ml-auto" /><Button variant="outline" size="xs" className="h-7 text-destructive hover:text-destructive" onClick={() => dispatch({ t: 'reviewApprove', id: item.id })}>驳回</Button><Button size="xs" className="h-7" disabled={sel.size === 0} onClick={() => dispatch({ t: 'reviewApprove', id: item.id })}><Check className="size-3.5" />通过纳管 {sel.size} 个</Button></div>
      </div>
    </Modal>
  )
}

// ===== 弹层：回滚再三确认 =====
function RollbackDialog({ state, file, version, dispatch }: { state: State; file: MockFile; version: number; dispatch: React.Dispatch<Action> }) {
  const [ack, setAck] = useState(false)
  const [confirm2, setConfirm2] = useState(false)
  const snaps = REVISION_SNAPSHOTS[file.path] ?? {}
  // 无历史快照的文件也可回滚：当前取真实生效、目标回滚到全局基线，保证 diff 有意义
  const cur = snaps[file.version ?? 0] ?? effectiveSource(file, state.group, state.zone, state.lookServer)
  const target = snaps[version] ?? file.global
  const rows = textDiff(cur, target)
  const changes = rows.filter((r) => r.changed).length
  return (
    <Modal title={`回滚到 v${version}`} icon={<RotateCcw className="size-4 text-primary" />} onClose={() => dispatch({ t: 'rollbackCancel' })} width="w-[520px]">
      <div className="flex flex-col gap-3 text-xs">
        <div className="flex border-b border-border text-[0.7rem] font-medium text-muted-foreground"><div className="flex-1 border-r border-border px-2 py-1">当前 v{file.version ?? 1}</div><div className="flex-1 px-2 py-1">回滚目标 v{version}</div></div>
        <div className="max-h-44 overflow-y-auto font-mono text-[0.7rem]">{rows.map((r, i) => <div key={i} className="flex border-b border-border/60"><div className="flex-1 whitespace-pre border-r border-border px-2 py-0.5">{r.left !== null ? <span className={cn(r.changed && 'bg-rose-500/10')}>{r.left || ' '}</span> : <span className="text-muted-foreground/40">（无）</span>}</div><div className="flex-1 whitespace-pre px-2 py-0.5">{r.right !== null ? <span className={cn(r.changed && 'bg-emerald-500/10')}>{r.right || ' '}</span> : <span className="text-muted-foreground/40">（无）</span>}</div></div>)}</div>
        <div className="rounded-md border border-amber-400/50 bg-amber-500/10 px-3 py-2 text-amber-700 dark:text-amber-300"><AlertTriangle className="mr-1 inline size-3.5 align-[-2px]" />回滚将以 v{version} 内容发为新版本，覆盖当前生效（{changes} 处变化），并热推在线服。</div>
        <label className="flex cursor-pointer items-center gap-2"><input type="checkbox" checked={ack} onChange={(e) => { setAck(e.target.checked); setConfirm2(false) }} className="size-3.5" />我已审阅以上 diff，确认回滚到 v{version}</label>
        <div className="flex items-center gap-2"><span className="ml-auto" /><Button variant="outline" size="xs" className="h-7" onClick={() => dispatch({ t: 'rollbackCancel' })}>取消</Button>{!confirm2 ? <Button size="xs" className="h-7" disabled={!ack} onClick={() => setConfirm2(true)}>回滚…</Button> : <Button size="xs" className="h-7 border-destructive bg-destructive/10 text-destructive hover:bg-destructive/20" onClick={() => dispatch({ t: 'rollbackDo', version })}>再次确认：回滚到 v{version}</Button>}</div>
      </div>
    </Modal>
  )
}
