// 工作台数据 hook（FR-111）：从既有真实端点取数并在客户端适配为工作台视图形状。
//
// FR-114 原型期这些 hook 直接 fetch mock `/admin/v1/workbench/*` 假端点；FR-111 接真后端后，
// 按 ADR-0050 决策 2 映射表改调 `api/client` 的既有真函数（不造聚合 BFF、不改后端契约），
// 把响应适配成 `./types` 里的视图形状——组件契约不变。映射见 docs/specs/config-xftp-workspace.md §3.1。

import { useQuery } from '@tanstack/react-query'

import {
  browse,
  effectiveFiles,
  getFile,
  getReverseFetchTask,
  impactPreview,
  listCommands,
  listFiles,
  listInstances,
  listFileRevisions,
  listReversibleOperations,
  type EffectiveFileItem,
} from '@/api/client'
import type {
  BrowseEntry,
  BrowseTreeResult,
  CommandMetaView,
  FileView,
  ImpactView,
  InstanceView,
  ReversibleOpView,
} from '@/api/types'
import { useEnvironment } from '@/state/environment'
import type {
  EffectiveFile,
  EffectiveLayer,
  IngestScanItem,
  ManagedNode,
  OverrideScope,
  PublishImpact,
  ScopeOption,
  ServerNode,
  ServerOption,
  SyncQueueRow,
  WorkbenchFile,
} from './types'

// 把后端覆盖层（global/group/zone/server）折叠到工作台三层视图（global/group/server）：
// 视图只有三层并排 diff，zone 视作「组级」定制呈现（保真简化，见 spec §3.1）。
function toViewScope(scope: string): OverrideScope {
  if (scope === 'global') return 'global'
  if (scope === 'server') return 'server'
  return 'group'
}

// 字节数转人类可读（Xftp 风「大小」列）
function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

// RFC3339 时间转 HH:mm:ss（同步队列行用）
function clockTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString('zh-CN', { hour12: false })
}

// ---- 受管配置树 ←  listFiles（FR-14）----
// 把扁平文件清单按 path 装配成 plugins/ 前缀树；同步状态以可得信息标注（完整交叉比对依赖右面板浏览，spec §6 partial）。
export function useManagedTree() {
  const namespace = useEnvironment()
  return useQuery({
    queryKey: ['wb-managed-tree', namespace],
    queryFn: () => listFiles({ namespace }).then(buildManagedTree),
    enabled: !!namespace,
  })
}

// 由扁平文件清单构建受管树：以 path 段建文件夹层级，文件挂叶子。
function buildManagedTree(files: FileView[]): ManagedNode[] {
  const root: ManagedNode = { key: 'plugins', name: 'plugins', type: 'folder', sync: 'managed-only', children: [] }
  const folderByPath = new Map<string, ManagedNode>([['plugins', root]])
  for (const f of files) {
    const segs = f.path.split('/').filter(Boolean)
    let parentKey = 'plugins'
    // 逐级建中间文件夹
    for (let i = 0; i < segs.length - 1; i++) {
      const key = `${parentKey}/${segs[i]}`
      if (!folderByPath.has(key)) {
        const node: ManagedNode = { key, name: segs[i], type: 'folder', sync: 'managed-only', children: [] }
        folderByPath.get(parentKey)!.children!.push(node)
        folderByPath.set(key, node)
      }
      parentKey = key
    }
    const leafName = segs[segs.length - 1] ?? f.path
    folderByPath.get(parentKey)!.children!.push({
      key: `plugins/${f.path}`,
      name: leafName,
      type: 'file',
      // 仅受管事实可得：默认「仅受管」；与服务器实况的差异由右面板浏览交叉判断（partial）
      sync: 'managed-only',
      scope: toViewScope(f.scopeLevel),
      version: f.version,
      modifiedAt: clockTime(f.updatedAt),
      fileId: f.id,
    })
  }
  return [root]
}

// ---- 服务器实时 plugins 树 ← FR-110 浏览端点（op=tree）----
// 选定某在线服后懒展开其真实 plugins 子树；映射成 ServerNode（纳管标记默认 untracked，与受管交叉判断为 partial）。
export function useServerTree(serverId: string | undefined) {
  const namespace = useEnvironment()
  return useQuery({
    queryKey: ['wb-server-tree', namespace, serverId],
    queryFn: () =>
      browse(serverId!, namespace, { op: 'tree' }).then((r) => browseTreeToServerNodes(r as BrowseTreeResult)),
    enabled: !!namespace && !!serverId,
  })
}

// 浏览树结果 → ServerNode[]（根为 plugins）
function browseTreeToServerNodes(tree: BrowseTreeResult): ServerNode[] {
  const root: ServerNode = {
    key: 'srv/plugins',
    name: 'plugins',
    type: 'folder',
    mark: 'untracked',
    children: (tree.children ?? []).map((c) => browseEntryToServerNode(c, 'srv/plugins')),
  }
  return [root]
}

function browseEntryToServerNode(entry: BrowseEntry, parentKey: string): ServerNode {
  const key = `${parentKey}/${entry.name}`
  if (entry.dir) {
    return {
      key,
      name: entry.name,
      type: 'folder',
      mark: 'untracked',
      children: (entry.children ?? []).map((c) => browseEntryToServerNode(c, key)),
    }
  }
  return {
    key,
    name: entry.name,
    type: 'file',
    mark: 'untracked',
    size: humanSize(entry.size),
    fileType: fileTypeLabel(entry.name),
    modifiedAt: '',
  }
}

// 文件类型标签（Xftp 风「类型」列）：按扩展名给中文标签
function fileTypeLabel(name: string): string {
  const ext = name.includes('.') ? name.split('.').pop()!.toLowerCase() : ''
  const map: Record<string, string> = { yml: 'YAML 文件', yaml: 'YAML 文件', json: 'JSON 文件', properties: '属性文件', txt: '文本文件' }
  return map[ext] ?? '文件'
}

// ---- 同步队列 ← listCommands（FR-104 命令生命周期）----
// 取近期 agent 命令元数据映射成同步队列行（抓取 ingest-plugins → fetch；其余按类型推方向）。
export function useSyncQueue() {
  const namespace = useEnvironment()
  return useQuery({
    queryKey: ['wb-sync-queue', namespace],
    queryFn: () => listCommands({ namespace, page: 1, size: 50 }).then((r) => r.items.map(commandToQueueRow)),
    enabled: !!namespace,
  })
}

// agent 命令 → 同步队列行：状态收口到队列四态。
function commandToQueueRow(c: CommandMetaView): SyncQueueRow {
  // ingest-plugins 是反向抓取（server→managed），其余命令按下发处理（managed→server）
  const direction: 'fetch' | 'push' = c.type === 'ingest-plugins' ? 'fetch' : 'push'
  return {
    id: String(c.commandId),
    name: c.resultDetail || `${c.type} @ ${c.serverId}`,
    direction,
    status: commandStatusToQueueStatus(c.status, direction),
    scopeTarget: c.serverId,
    sourcePath: c.serverId,
    targetPath: c.namespace,
    time: clockTime(c.createdAt),
  }
}

// 命令状态 → 队列四态：done/failed→done；pending/fetched 按方向落到待审；ready→待拓印。
function commandStatusToQueueStatus(
  status: string,
  direction: 'fetch' | 'push',
): SyncQueueRow['status'] {
  if (status === 'done' || status === 'failed' || status === 'expired') return 'done'
  if (status === 'ready') return 'pending-imprint'
  // pending / fetched：执行中
  return direction === 'fetch' ? 'pending-ingest' : 'running'
}

// ---- 操作日志（撤回源）← listReversibleOperations（FR-116，见 ADR-0051）----
// 读真实可逆操作账目（下发 / 发布 / 反向抓取）映射成操作日志条目：撤回为真后端能力，
// id 即可逆账目 id（撤回端点据此撤回）；status=reversible 才可撤、已撤回置灰（undone）。
export function useOperationLog() {
  const namespace = useEnvironment()
  return useQuery({
    queryKey: ['wb-operation-log', namespace],
    queryFn: () => listReversibleOperations({ namespace, limit: 100 }).then((ops) => ops.map(reversibleOpToLogEntry)),
    enabled: !!namespace,
  })
}

// 可逆操作账目 → 操作日志条目：opType 与 OpAction 的 fetch/push/publish 同名直映；
// undone = 已撤回（status=reversed），其余非 reversible 状态（expired/superseded）亦不可再撤回故也置灰。
function reversibleOpToLogEntry(op: ReversibleOpView): import('./types').OpLogEntry {
  return {
    id: String(op.id),
    time: clockTime(op.createdAt),
    action: op.opType,
    operator: op.operator,
    files: [op.summary],
    target: op.scopeTarget ? `${op.scope} ${op.scopeTarget}` : op.scope,
    detail: op.summary,
    undone: !op.reversible,
  }
}

// ---- scope / server 候选 ← listInstances（FR-3）----
// server 候选取在线实例；scope 候选给固定三层 + 各实例所属组（覆盖层选择）。
export function useWorkbenchOptions() {
  const namespace = useEnvironment()
  return useQuery({
    queryKey: ['wb-options', namespace],
    queryFn: () => listInstances({ namespace }).then((insts) => buildOptions(insts)),
    enabled: !!namespace,
  })
}

function buildOptions(insts: InstanceView[]): { scopes: ScopeOption[]; servers: ServerOption[] } {
  const servers: ServerOption[] = insts.map((i) => ({ serverId: i.serverId, label: i.serverId, online: true }))
  // 覆盖层候选：全局 + 各唯一组 + 各实例（实例层定向覆盖）
  const scopes: ScopeOption[] = [{ value: 'global', label: '全局', scope: 'global' }]
  const seenGroups = new Set<string>()
  for (const i of insts) {
    if (i.group && !seenGroups.has(i.group)) {
      seenGroups.add(i.group)
      scopes.push({ value: `group:${i.group}`, label: `组 ${i.group}`, scope: 'group' })
    }
  }
  for (const i of insts) scopes.push({ value: `server:${i.serverId}`, label: `实例 ${i.serverId}`, scope: 'server' })
  return { scopes, servers }
}

// ---- 单个受管文件内容 + 历史修订 ← getFile + listFileRevisions（FR-14/67）----
// key 形如 plugins/<path>；通过 listFiles 解析 fileId 后拉内容 + revisions。
export function useWorkbenchFile(key: string | undefined) {
  const namespace = useEnvironment()
  return useQuery({
    queryKey: ['wb-file', namespace, key],
    queryFn: () => loadWorkbenchFile(namespace, key!),
    enabled: !!namespace && !!key,
  })
}

async function loadWorkbenchFile(namespace: string, key: string): Promise<WorkbenchFile> {
  // key = plugins/<相对 path>；剥前缀得文件树 path
  const path = key.replace(/^plugins\//, '')
  // 解析 fileId：按 path 在受管文件清单里定位
  const files = await listFiles({ namespace, path })
  const match = files.find((f) => f.path === path) ?? files[0]
  if (!match) throw new Error(`受管文件不存在: ${path}`)
  const [detail, revisions] = await Promise.all([getFile(match.id), listFileRevisions(match.id)])
  return {
    key,
    fileId: match.id,
    namespace: detail.namespace,
    group: detail.group,
    dataId: detail.path,
    scope: toViewScope(detail.scopeLevel),
    targetServer: detail.scopeTarget || '全局',
    format: formatFromPath(detail.path),
    content: detail.content ?? '',
    revisions: revisions.map((r) => ({
      version: r.version,
      author: r.operator,
      time: clockTime(r.createdAt),
      comment: r.comment,
      content: r.content ?? '',
    })),
  }
}

// 由 path 扩展名推编辑器语言（文件树对象无独立 format 字段）
function formatFromPath(path: string): string {
  const ext = path.includes('.') ? path.split('.').pop()!.toLowerCase() : ''
  const map: Record<string, string> = { yml: 'yaml', yaml: 'yaml', json: 'json', properties: 'properties', txt: 'text' }
  return map[ext] ?? 'yaml'
}

// ---- 反向抓取扫描清单 ← getReverseFetchTask（FR-58~60）----
// 取某受管任务的扫描清单映射成 ingest 审核项；无任务 id 时返回空（审核浮层据此空态）。
export function useIngestScanList(taskId: number | undefined) {
  return useQuery({
    queryKey: ['wb-ingest-scan', taskId],
    queryFn: () =>
      getReverseFetchTask(taskId!).then((task) => ({
        items: task.files.map<IngestScanItem>((f) => ({
          path: f.path,
          size: humanSize(f.size),
          ignored: f.ignoredByRule || f.overThreshold,
          defaultPick: !f.ignoredByRule && !f.overThreshold,
        })),
        ignoreRules: [] as string[],
      })),
    enabled: !!taskId,
  })
}

// ---- 生效预览 ← effectiveFiles（FR-45/68 逐键来源）----
// 某实例合并后有效文件树 + 逐键来源 → 工作台覆盖链视图（基线 ⟷ 生效并排 diff 的数据源）。
export function useEffectivePreview(serverId: string | undefined) {
  const namespace = useEnvironment()
  return useQuery({
    queryKey: ['wb-effective', namespace, serverId],
    queryFn: () => effectiveFiles({ namespace, serverId }).then((tree) => tree.files.map(effectiveFileToView)),
    enabled: !!namespace && !!serverId,
  })
}

// 有效文件（含逐键 sources）→ 工作台生效预览文件：每键给覆盖链（基线 + 生效层）。
function effectiveFileToView(file: EffectiveFileItem): EffectiveFile {
  // 整文件覆盖模式：单条以 winner 层呈现（无逐键链）
  if (file.wholeFile) {
    const winner = file.sources[0]
    const scope = toViewScope(winner?.scope ?? 'global')
    return {
      name: file.path,
      keys: [{ key: file.path, chain: layersFor(scope) }],
    }
  }
  // 逐键模式：每条 source 给一个键，chain 含基线（global）+ 生效层（若非 global）
  return {
    name: file.path,
    keys: file.sources.map((s) => {
      const keyLabel = s.path.length > 0 ? s.path.join('.') : file.path
      return { key: keyLabel, chain: layersFor(toViewScope(s.scope)) }
    }),
  }
}

// 给定生效层构造覆盖链：global 层只有自身；非 global 层 = 基线 global + 生效层（呈现「被覆盖」）。
function layersFor(scope: OverrideScope): EffectiveLayer[] {
  if (scope === 'global') return [{ scope: 'global', value: '基线值' }]
  return [
    { scope: 'global', value: '基线值' },
    { scope, value: '生效值' },
  ]
}

// ---- 发布影响面 ← impactPreview（FR-79）----
// 按选中文件解析受影响在线服清单；本 FR 以首个选中文件的覆盖层算一组影响面（多文件按需扩展）。
export function usePublishImpact(names: string[], enabled: boolean, scopeLevel = 'group', group?: string) {
  const namespace = useEnvironment()
  return useQuery({
    queryKey: ['wb-publish-impact', namespace, names, scopeLevel, group],
    queryFn: () =>
      impactPreview({ namespace, scopeLevel, group }).then((impact) => impactToView(names, impact, scopeLevel)),
    enabled: enabled && !!namespace && names.length > 0,
  })
}

function impactToView(names: string[], impact: ImpactView, scopeLevel: string): PublishImpact {
  const scope = toViewScope(scopeLevel)
  const servers = impact.affected.map((serverId) => ({ serverId, online: true, changed: true }))
  return {
    files: names.map((name) => ({ name, scope, fromVersion: 1, toVersion: 2 })),
    groups: [{ scope, label: impact.group ? `组 ${impact.group}` : '全局', servers }],
    // 受影响在线服皆视为有差异（拓印门据此提示）；精确差异由拓印 diff 逐台判断
    driftCount: servers.length,
  }
}
