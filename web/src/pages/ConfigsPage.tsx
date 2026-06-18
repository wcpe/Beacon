/**
 * 配置中心 — VS Code 风格编辑器
 *
 * 布局：
 *   ┌────────┬──────────────────────────────────────┐
 *   │ 左侧    │ 标签栏（右键菜单关闭）                  │
 *   │ 面板    ├──────────────────────────────────────┤
 *   │ 上：    │ [编辑] [Diff]                          │
 *   │ 文件树  │┌────────────────────────────────────┐│
 *   │ 下：    ││  Monaco Editor / DiffEditor         ││
 *   │ 实例/   │└────────────────────────────────────┘│
 *   │ 分组    │ ▼ 历史修订面板（可折叠）               │
 *   └────────┴──────────────────────────────────────┘
 */

import { useState, useMemo, useCallback, useEffect } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { createConfig, diffConfig, effectiveConfig, getConfig, listConfigs, listInstances, listRevisions, publishConfig } from '../api/client'
import type { CreateConfigParams } from '../api/client'
import { useMessage } from '../components/useMessage'
import CodeEditor from '../components/CodeEditor'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger,
} from '@/components/ui/dialog'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { formatTime } from '../api/format'
import { cn } from '@/lib/utils'

// ---- 类型 ----

interface OpenTab {
  configId: number
  dataId: string
  namespace: string
  group: string
  scopeLevel: string
  scopeTarget: string
  format: string
  content: string
}

type ViewMode = 'edit' | 'diff' | 'effective'

// ---- 主组件 ----

export default function ConfigsPage() {
  const qc = useQueryClient()
  const msg = useMessage()

  // 左侧：文件树选中的 dataId
  const [selectedFile, setSelectedFile] = useState<string | null>(null)
  // 左侧：选中的目标（实例 serverId 或分组 group）
  const [selectedTarget, setSelectedTarget] = useState<{ type: 'server' | 'group'; value: string } | null>(null)

  // 右侧：打开的标签
  const [openTabs, setOpenTabs] = useState<OpenTab[]>([])
  const [activeTabKey, setActiveTabKey] = useState<string | null>(null)

  // 视图模式（每个 tab 独立）
  const [viewModes, setViewModes] = useState<Record<number, ViewMode>>({})

  // Diff 版本选择
  const [diffVersions, setDiffVersions] = useState<Record<number, { from: string; to: string }>>({})

  // 历史面板折叠状态
  const [historyCollapsed, setHistoryCollapsed] = useState<Record<number, boolean>>({})

  // 高亮的修订版本
  const [highlightRev, setHighlightRev] = useState<number | undefined>()

  // 右键菜单
  const [contextTabId, setContextTabId] = useState<number | null>(null)
  const [showTabMenu, setShowTabMenu] = useState(false)

  // 新建表单
  const [form, setForm] = useState<CreateConfigParams>({
    namespace: 'prod', group: '__GLOBAL__', dataId: '', scopeLevel: 'global',
    scopeTarget: '', format: 'yaml', content: '', comment: '',
  })
  const [createOpen, setCreateOpen] = useState(false)

  // 查询配置列表
  const list = useQuery({ queryKey: ['configs'], queryFn: () => listConfigs({}) })

  // 查询实例列表（用于左侧目标选择器）
  const instancesQuery = useQuery({ queryKey: ['instances-all'], queryFn: () => listInstances({}) })

  // 当前活跃标签
  const activeTab = openTabs.find((t) => t.configId === Number(activeTabKey))

  // 配置详情
  const detail = useQuery({
    queryKey: ['config', activeTab?.configId],
    queryFn: () => activeTab ? getConfig(activeTab.configId) : Promise.reject(new Error('no tab')),
    enabled: !!activeTab,
  })

  // 当 detail 加载完成时，同步到 editContent
  useEffect(() => {
    if (detail.data?.content) {
      setOpenTabs((prev) => prev.map((t) =>
        t.configId === activeTab?.configId ? { ...t, content: detail.data?.content ?? t.content } : t
      ))
    }
  }, [detail.data?.content])

  // revision 列表（用于 Diff 模式）
  const revisions = useQuery({
    queryKey: ['config-revisions', activeTab?.configId],
    queryFn: () => activeTab ? listRevisions(activeTab.configId) : Promise.resolve([]),
    enabled: !!activeTab,
  })

  // diff 数据
  const activeDiff = diffVersions[Number(activeTabKey)] ?? { from: '', to: '' }
  const diff = useQuery({
    queryKey: ['config-diff', activeTab?.configId, activeDiff.from, activeDiff.to],
    queryFn: () => (activeTab && activeDiff.from && activeDiff.to)
      ? diffConfig(activeTab.configId, Number(activeDiff.from), Number(activeDiff.to))
      : Promise.resolve(null),
    enabled: !!activeTab && activeDiff.from !== '' && activeDiff.to !== '',
  })

  // 生效预览：选中的 serverId / group
  const [effectiveTarget, setEffectiveTarget] = useState<{ serverId?: string; group?: string }>({})

  // 生效预览数据
  const effectiveQuery = useQuery({
    queryKey: ['config-effective', effectiveTarget],
    queryFn: () => effectiveConfig({
      namespace: 'prod',
      serverId: effectiveTarget.serverId,
      group: effectiveTarget.group,
    }),
    enabled: !!(effectiveTarget.serverId || effectiveTarget.group),
  })

  // 保存 mutation
  const saveMut = useMutation({
    mutationFn: (params: { id: number; content: string; comment: string }) =>
      publishConfig(params.id, params.content, params.comment),
    onSuccess: (r) => {
      msg.showSuccess(`已保存版本 ${r.version}`)
      qc.invalidateQueries({ queryKey: ['configs'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 新建 mutation
  const createMut = useMutation({
    mutationFn: (params: CreateConfigParams) => createConfig(params),
    onSuccess: (c) => {
      msg.showSuccess(`已新建配置 #${c.id}`)
      setForm({ namespace: 'prod', group: '__GLOBAL__', dataId: '', scopeLevel: 'global', scopeTarget: '', format: 'yaml', content: '', comment: '' })
      setCreateOpen(false)
      qc.invalidateQueries({ queryKey: ['configs'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 保存当前标签
  const saveCurrentTab = useCallback(() => {
    if (!activeTab) return
    saveMut.mutate({ id: activeTab.configId, content: activeTab.content, comment: '管理台保存' })
  }, [activeTab, saveMut])

  // Ctrl+S 全局快捷键
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault()
        saveCurrentTab()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [saveCurrentTab])

  // 从配置列表构建文件树（受 selectedTarget 过滤）
  const treeData = useMemo(() => {
    let items = list.data ?? []

    // 如果选中了目标，只展示该目标相关的配置
    if (selectedTarget) {
      if (selectedTarget.type === 'server') {
        items = items.filter((c) => c.scopeTarget === selectedTarget.value)
      } else if (selectedTarget.type === 'group') {
        items = items.filter((c) => c.group === selectedTarget.value)
      }
    }

    const nsMap = new Map<string, Map<string, Map<string, boolean>>>()
    for (const item of items) {
      if (!nsMap.has(item.namespace)) nsMap.set(item.namespace, new Map())
      const groupMap = nsMap.get(item.namespace)!
      if (!groupMap.has(item.group)) groupMap.set(item.group, new Map())
      groupMap.get(item.group)!.set(item.dataId, true)
    }
    const nodes: TreeNode[] = []
    for (const [ns, groupMap] of nsMap) {
      const groupNodes: TreeNode[] = []
      for (const [group, dataIdMap] of groupMap) {
        const dataIdNodes: TreeNode[] = Array.from(dataIdMap.keys()).map((dataId) => ({
          key: `${ns}/${group}/${dataId}`,
          label: dataId,
          type: 'file' as const,
          data: { namespace: ns, group, dataId },
        }))
        groupNodes.push({ key: `${ns}/${group}`, label: group, type: 'folder', children: dataIdNodes })
      }
      nodes.push({ key: ns, label: ns, type: 'folder', children: groupNodes })
    }
    return nodes
  }, [list.data, selectedTarget])

  // 点击资源管理器节点 → 打开标签
  const handleFileSelect = useCallback((node: TreeNode) => {
    if (node.type !== 'file' || !node.data) return
    const { namespace, group, dataId } = node.data as { namespace: string; group: string; dataId: string }
    setSelectedFile(node.key)

    const items = list.data ?? []
    const matched = items.filter((c) => c.namespace === namespace && c.group === group && c.dataId === dataId)
    if (matched.length === 0) return

    setOpenTabs((prev) => {
      const existingIds = new Set(prev.map((t) => t.configId))
      const fresh = matched.filter((c) => !existingIds.has(c.id)).map((c) => ({
        configId: c.id, dataId: c.dataId, namespace: c.namespace,
        group: c.group, scopeLevel: c.scopeLevel, scopeTarget: c.scopeTarget,
        format: c.format, content: c.content ?? '',
      }))
      return [...prev, ...fresh]
    })

    setActiveTabKey(String(matched[0].id))
    setViewModes((prev) => ({ ...prev, [matched[0].id]: 'edit' }))
  }, [list.data])

  // 关闭标签
  const closeTab = useCallback((configId: number) => {
    setOpenTabs((prev) => {
      const next = prev.filter((t) => t.configId !== configId)
      if (activeTabKey === String(configId)) {
        setActiveTabKey(next.length > 0 ? String(next[next.length - 1].configId) : null)
      }
      return next
    })
  }, [activeTabKey])

  // 右键菜单操作
  const handleTabContextAction = useCallback((action: 'close' | 'closeOthers' | 'closeAll') => {
    const tabId = contextTabId
    if (tabId === null) return
    if (action === 'close') closeTab(tabId)
    else if (action === 'closeOthers') {
      setOpenTabs((prev) => {
        const keep = prev.find((t) => t.configId === tabId)
        if (keep) { setActiveTabKey(String(keep.configId)); return [keep] }
        return prev
      })
    } else if (action === 'closeAll') {
      setOpenTabs([]); setActiveTabKey(null)
    }
    setShowTabMenu(false); setContextTabId(null)
  }, [contextTabId, closeTab])

  // 新建
  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!form.dataId.trim()) { msg.showError('dataId 为必填'); return }
    createMut.mutate(form)
  }

  const versionNumbers = (revisions.data ?? []).map((r) => r.version)
  const activeView = viewModes[Number(activeTabKey)] ?? 'edit'

  return (
    <div className="flex flex-col h-screen overflow-hidden gap-2">
      {/* ===== 顶部工具栏 ===== */}
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">配置中心</h1>
        <div className="flex items-center gap-2">
          <Badge variant="outline" className="text-xs">{list.data?.length ?? 0} 条配置</Badge>
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild><Button size="sm">新建配置</Button></DialogTrigger>
            <DialogContent className="sm:max-w-md">
              <DialogHeader><DialogTitle>新建配置</DialogTitle></DialogHeader>
              <form id="create-config" onSubmit={onCreate} className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <Label>环境</Label>
                  <select className="h-8 w-full rounded border border-input bg-background px-2 text-sm" value={form.namespace} onChange={(e) => setForm({ ...form, namespace: e.target.value })}>
                    <option value="prod">prod</option><option value="test">test</option>
                  </select>
                </div>
                <div className="space-y-1.5">
                  <Label>大区</Label>
                  <select className="h-8 w-full rounded border border-input bg-background px-2 text-sm" value={form.group} onChange={(e) => setForm({ ...form, group: e.target.value })}>
                    <option value="__GLOBAL__">__GLOBAL__</option>
                    <option value="server-a">server-a</option><option value="server-b">server-b</option>
                  </select>
                </div>
                <div className="space-y-1.5"><Label>dataId</Label><Input value={form.dataId} onChange={(e) => setForm({ ...form, dataId: e.target.value })} /></div>
                <div className="space-y-1.5">
                  <Label>覆盖层</Label>
                  <select className="h-8 w-full rounded border border-input bg-background px-2 text-sm" value={form.scopeLevel} onChange={(e) => setForm({ ...form, scopeLevel: e.target.value })}>
                    <option value="global">global</option><option value="group">group</option><option value="zone">zone</option><option value="server">server</option>
                  </select>
                </div>
                <div className="space-y-1.5"><Label>覆盖目标</Label><Input value={form.scopeTarget} onChange={(e) => setForm({ ...form, scopeTarget: e.target.value })} /></div>
                <div className="space-y-1.5">
                  <Label>格式</Label>
                  <select className="h-8 w-full rounded border border-input bg-background px-2 text-sm" value={form.format} onChange={(e) => setForm({ ...form, format: e.target.value })}>
                    <option value="yaml">yaml</option><option value="properties">properties</option><option value="json">json</option>
                  </select>
                </div>
                <div className="col-span-2 space-y-1.5"><Label>初始内容</Label><Input value={form.content} onChange={(e) => setForm({ ...form, content: e.target.value })} placeholder="可选" /></div>
              </form>
              <DialogFooter><Button type="submit" form="create-config" disabled={createMut.isPending}>创建</Button></DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      {/* ===== 主体 ===== */}
      <div className="flex flex-1 min-h-0 gap-2">

        {/* ===== 左侧面板（两段式） ===== */}
        <aside className="w-56 shrink-0 flex flex-col rounded-lg border border-border overflow-hidden bg-card">
          {/* 上半：配置文件树 */}
          <div className="flex-1 flex flex-col min-h-0 border-b border-border">
            <div className="flex-shrink-0 px-3 py-2 text-xs font-medium text-muted-foreground border-b border-border bg-muted/30 flex items-center justify-between">
              <span>配置文件</span>
              {selectedTarget && (
                <button className="text-blue-500 hover:text-blue-600 text-[0.65rem]" onClick={() => setSelectedTarget(null)}>
                  清除
                </button>
              )}
            </div>
            <div className="flex-1 overflow-y-auto py-1">
              {treeData.map((node) => (
                <ResourceTreeNode key={node.key} node={node} depth={0} selectedKey={selectedFile} onSelect={handleFileSelect} />
              ))}
            </div>
          </div>

          {/* 下半：实例/分组选择器（树形） */}
          <TargetSelector
            instances={instancesQuery.data ?? []}
            selectedTarget={selectedTarget}
            onSelectTarget={(t) => { setSelectedTarget(t) }}
          />
        </aside>

        {/* ===== 右侧编辑器区域 ===== */}
        <div className="flex-1 flex flex-col min-w-0 gap-2">

          {/* 标签栏 */}
          <div className="flex-shrink-0 flex rounded-lg border border-border overflow-hidden bg-card h-9">
            {openTabs.length === 0 ? (
              <div className="px-4 py-2 text-sm text-muted-foreground">从左侧选择配置文件</div>
            ) : (
              openTabs.map((tab) => {
                const isActive = String(tab.configId) === activeTabKey
                return (
                  <DropdownMenu key={tab.configId}>
                    <DropdownMenuTrigger asChild>
                      <button
                        type="button"
                        className={cn(
                          'flex items-center gap-1.5 px-3 text-sm whitespace-nowrap border-b-2 transition-colors select-none h-full',
                          isActive ? 'border-b-primary bg-muted/50 font-medium text-foreground'
                            : 'border-b-transparent text-muted-foreground hover:text-foreground hover:bg-muted/30',
                        )}
                        onClick={() => setActiveTabKey(String(tab.configId))}
                        onContextMenu={(e) => { e.preventDefault(); setContextTabId(tab.configId); setShowTabMenu(true) }}
                      >
                        <span className="max-w-[100px] truncate">{tab.dataId}</span>
                        <Badge variant="outline" className="text-[0.6rem] h-4 px-1 shrink-0">{tab.scopeLevel}</Badge>
                        <span className="ml-0.5 text-xs text-muted-foreground/60 hover:text-destructive cursor-pointer shrink-0"
                          onClick={(e) => { e.stopPropagation(); closeTab(tab.configId) }}>✕</span>
                      </button>
                    </DropdownMenuTrigger>
                    {contextTabId === tab.configId && showTabMenu && (
                      <DropdownMenuContent align="start" className="w-40" sideOffset={4}>
                        <DropdownMenuItem onClick={() => handleTabContextAction('close')}>关闭当前标签</DropdownMenuItem>
                        <DropdownMenuItem onClick={() => handleTabContextAction('closeOthers')}>关闭其他标签</DropdownMenuItem>
                        <DropdownMenuItem onClick={() => handleTabContextAction('closeAll')}>关闭所有标签</DropdownMenuItem>
                      </DropdownMenuContent>
                    )}
                  </DropdownMenu>
                )
              })
            )}
          </div>

          {/* 编辑器内容区 */}
          {activeTab ? (
            <div className="flex-1 flex flex-col min-h-0 rounded-lg border border-border overflow-hidden bg-background">
              {/* 视图切换 + 保存按钮 */}
              <div className="flex-shrink-0 flex items-center justify-between px-3 py-1 border-b border-border bg-muted/30">
                <div className="flex items-center gap-1">
                  <Button variant={activeView === 'edit' ? 'default' : 'ghost'} size="xs"
                    className={cn('h-7 px-3 text-xs')}
                    onClick={() => setViewModes((p) => ({ ...p, [activeTab.configId]: 'edit' }))}>
                    编辑
                  </Button>
                  <Button variant={activeView === 'diff' ? 'default' : 'ghost'} size="xs"
                    className={cn('h-7 px-3 text-xs')}
                    onClick={() => setViewModes((p) => ({ ...p, [activeTab.configId]: 'diff' }))}>
                    Diff
                  </Button>
                  <Button variant={activeView === 'effective' ? 'default' : 'ghost'} size="xs"
                    className={cn('h-7 px-3 text-xs')}
                    onClick={() => {
                      // 默认选中当前 tab 的 group 作为生效预览目标
                      setEffectiveTarget({ group: activeTab.group, serverId: activeTab.scopeTarget || undefined })
                      setViewModes((p) => ({ ...p, [activeTab.configId]: 'effective' }))
                    }}>
                    生效预览
                  </Button>
                </div>

                <div className="flex items-center gap-2">
                  {activeView === 'diff' && versionNumbers.length >= 2 && (
                    <div className="flex items-center gap-1">
                      <select className="h-7 rounded border border-input bg-background px-1.5 text-xs text-foreground"
                        value={diffVersions[activeTab.configId]?.from ?? ''}
                        onChange={(e) => setDiffVersions((p) => ({ ...p, [activeTab.configId]: { ...(p[activeTab.configId] ?? {}), from: e.target.value } }))}>
                        <option value="">旧版本</option>
                        {versionNumbers.map((v) => <option key={v} value={v}>v{v}</option>)}
                      </select>
                      <span className="text-xs text-gray-500">→</span>
                      <select className="h-7 rounded border border-input bg-background px-1.5 text-xs text-foreground"
                        value={diffVersions[activeTab.configId]?.to ?? ''}
                        onChange={(e) => setDiffVersions((p) => ({ ...p, [activeTab.configId]: { ...(p[activeTab.configId] ?? {}), to: e.target.value } }))}>
                        <option value="">新版本</option>
                        {versionNumbers.map((v) => <option key={v} value={v}>v{v}</option>)}
                      </select>
                    </div>
                  )}
                  <Button size="xs" className="h-7 px-3 text-xs bg-primary hover:bg-primary/80 text-primary-foreground"
                    onClick={saveCurrentTab} disabled={saveMut.isPending}>
                    {saveMut.isPending ? '保存中…' : '💾 保存'}
                  </Button>
                </div>
              </div>

              {/* Monaco 编辑器 */}
              <div className="flex-1 min-h-0">
                {activeView === 'edit' ? (
                  <CodeEditor
                    value={activeTab.content}
                    language={activeTab.format}
                    onChange={(content) => {
                      setOpenTabs((prev) => prev.map((t) =>
                        t.configId === activeTab.configId ? { ...t, content } : t
                      ))
                    }}
                  />
                ) : activeView === 'effective' ? (
                  <div className="flex-1 flex flex-col min-h-0">
                    {/* 生效预览目标选择 */}
                    <div className="flex-shrink-0 flex items-center gap-2 px-3 py-1.5 border-b border-border bg-muted/20">
                      <span className="text-xs text-muted-foreground">预览目标：</span>
                      <select className="h-7 rounded border border-input bg-background px-2 text-xs"
                        value={effectiveTarget.serverId ?? effectiveTarget.group ?? ''}
                        onChange={(e) => {
                          const val = e.target.value
                          if (val.startsWith('server-')) {
                            setEffectiveTarget({ serverId: val })
                          } else {
                            setEffectiveTarget({ group: val })
                          }
                        }}>
                        <option value="">选择服务器/分组</option>
                        {instancesQuery.data?.map((inst) => (
                          <option key={inst.serverId} value={inst.serverId}>
                            {inst.serverId} ({inst.group}/{inst.zone})
                          </option>
                        ))}
                      </select>
                      {effectiveQuery.data && (
                        <Badge variant="outline" className="text-xs">
                          md5: {effectiveQuery.data.md5.slice(0, 8)}
                        </Badge>
                      )}
                    </div>
                    {/* 生效预览内容 */}
                    {effectiveQuery.isLoading ? (
                      <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">加载中…</div>
                    ) : effectiveQuery.data ? (
                      <ScrollArea className="flex-1">
                        <div className="p-3 space-y-3">
                          {effectiveQuery.data.items.map((item) => (
                            <div key={item.dataId} className="rounded border border-border overflow-hidden">
                              <div className="px-2 py-1 bg-muted/30 text-xs font-medium flex items-center justify-between">
                                <span>{item.dataId} ({item.format})</span>
                                <span className="text-muted-foreground font-mono">md5: {item.md5.slice(0, 8)}</span>
                              </div>
                              <pre className="p-2 text-xs font-mono whitespace-pre-wrap bg-background border-t border-border max-h-[200px] overflow-y-auto">{item.content}</pre>
                              {item.sources.length > 0 && (
                                <div className="px-2 py-1 bg-muted/10 border-t border-border">
                                  <span className="text-[0.65rem] text-muted-foreground">来源：</span>
                                  {item.sources.map((src, idx) => (
                                    <span key={idx} className="ml-1 text-[0.65rem] text-blue-600">
                                      {src.path.join('.')} ({src.scope})
                                    </span>
                                  ))}
                                </div>
                              )}
                            </div>
                          ))}
                          {effectiveQuery.data.deletions.length > 0 && (
                            <div className="rounded border border-red-200 overflow-hidden">
                              <div className="px-2 py-1 bg-red-50 text-xs font-medium text-red-600">
                                被删除的键（{effectiveQuery.data.deletions.length} 条）
                              </div>
                              {effectiveQuery.data.deletions.map((del, idx) => (
                                <div key={idx} className="px-2 py-0.5 text-xs text-red-500 font-mono border-t border-red-100">
                                  {del.path.join('.')} ({del.scope})
                                </div>
                              ))}
                            </div>
                          )}
                        </div>
                      </ScrollArea>
                    ) : (
                      <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
                        选择服务器/分组查看生效配置
                      </div>
                    )}
                  </div>
                ) : (
                  <CodeEditor
                    original={diff.data?.fromContent ?? ''}
                    modified={diff.data?.toContent ?? activeTab.content}
                    language={activeTab.format}
                  />
                )}
              </div>

              {/* 底部历史修订面板 */}
              {revisions.data && revisions.data.length > 0 && (
                <div className="flex-shrink-0 border-t border-border">
                  <button
                    type="button"
                    className="flex w-full items-center justify-between px-3 py-1.5 text-xs font-medium text-muted-foreground hover:bg-muted/30 transition-colors"
                    onClick={() => setHistoryCollapsed((p) => ({ ...p, [activeTab.configId]: !p[activeTab.configId] }))}
                  >
                    <span className="flex items-center gap-1">
                      {historyCollapsed[activeTab.configId] ? <ChevronRight className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
                      历史修订（共 {revisions.data.length} 条，点击条目查看 Diff）
                    </span>
                  </button>
                  {!historyCollapsed[activeTab.configId] && (
                    <ScrollArea className="h-[140px]">
                      <div className="divide-y divide-border">
                        {revisions.data.map((rev) => (
                          <div
                            key={rev.version}
                            className={cn(
                              'flex items-center gap-3 px-3 py-1.5 cursor-pointer transition-colors text-xs',
                              highlightRev === rev.version ? 'bg-accent/50' : 'hover:bg-muted/30',
                            )}
                            onClick={() => {
                              setHighlightRev(rev.version)
                              const revList = revisions.data ?? []
                              const idx = revList.findIndex((r) => r.version === rev.version)
                              setDiffVersions((p) => ({ ...p, [activeTab.configId]: { from: String(rev.version), to: String(revList[idx + 1]?.version ?? rev.version) } }))
                              setViewModes((p) => ({ ...p, [activeTab.configId]: 'diff' }))
                            }}
                          >
                            <span className="w-10 shrink-0 font-medium text-foreground">v{rev.version}</span>
                            <span className="w-20 shrink-0 text-muted-foreground">{formatTime(rev.createdAt)}</span>
                            <span className="w-20 shrink-0 text-foreground">{rev.operator}</span>
                            <span className="flex-1 truncate text-muted-foreground">{rev.comment || '—'}</span>
                            <span className="font-mono text-muted-foreground">{rev.md5.slice(0, 8)}</span>
                            {rev.sourceRevision != null && (
                              <span className="text-xs text-blue-500">← v{rev.sourceRevision}</span>
                            )}
                          </div>
                        ))}
                      </div>
                    </ScrollArea>
                  )}
                </div>
              )}
            </div>
          ) : (
            <div className="flex-1 flex items-center justify-center rounded-lg border border-border bg-card">
              <div className="text-center text-muted-foreground">
                <p className="text-sm">从左侧选择配置文件</p>
                <p className="mt-1 text-xs">点击资源管理器中的文件打开编辑器</p>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

// ---- 资源管理器树节点 ----

interface TreeNode {
  key: string
  label: string
  type: 'folder' | 'file'
  children?: TreeNode[]
  data?: Record<string, unknown>
}

function ResourceTreeNode({ node, depth, selectedKey, onSelect }: {
  node: TreeNode
  depth: number
  selectedKey?: string | null
  onSelect: (node: TreeNode) => void
}) {
  const [expanded, setExpanded] = useState(depth < 2)
  const isFolder = node.type === 'folder'
  const isSelected = node.key === selectedKey
  const hasChildren = isFolder && node.children && node.children.length > 0

  const handleClick = () => {
    if (isFolder) setExpanded(!expanded)
    onSelect(node)
  }

  return (
    <div>
      <button
        type="button"
        className={cn(
          'flex w-full items-center gap-1 px-2 py-1 text-sm transition-colors',
          isSelected ? 'bg-accent text-accent-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground',
        )}
        style={{ paddingLeft: `${depth * 12 + 8}px` }}
        onClick={handleClick}
      >
        {hasChildren ? (
          expanded ? <span className="text-xs">▼</span> : <span className="text-xs">▶</span>
        ) : (
          <span className="w-3" />
        )}
        <span className="truncate">{node.label}</span>
      </button>
      {hasChildren && expanded && (
        <div>
          {node.children!.map((child) => (
            <ResourceTreeNode key={child.key} node={child} depth={depth + 1} selectedKey={selectedKey} onSelect={onSelect} />
          ))}
        </div>
      )}
    </div>
  )
}

// ---- 实例/分组树形选择器 ----

function TargetSelector({ instances, selectedTarget, onSelectTarget }: {
  instances: { serverId: string; group: string; zone: string | null; status: string }[]
  selectedTarget: { type: 'server' | 'group'; value: string } | null
  onSelectTarget: (t: { type: 'server' | 'group'; value: string } | null) => void
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set(['__root__']))

  const toggle = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  // 按 group > zone > servers 构建树
  const groupMap = new Map<string, { zones: Map<string, { serverId: string; status: string }[]> }>()
  for (const inst of instances) {
    if (!groupMap.has(inst.group)) groupMap.set(inst.group, { zones: new Map() })
    const g = groupMap.get(inst.group)!
    const zoneName = inst.zone ?? '(未分组)'
    if (!g.zones.has(zoneName)) g.zones.set(zoneName, [])
    g.zones.get(zoneName)!.push({ serverId: inst.serverId, status: inst.status })
  }

  const statusColor = (s: string) => s === 'online' ? 'bg-emerald-500' : s === 'lost' ? 'bg-amber-500' : 'bg-muted-foreground'

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <div className="flex-shrink-0 px-3 py-1.5 text-xs font-medium text-muted-foreground border-b border-border bg-muted/30">
        实例 / 分组
      </div>
      <ScrollArea className="flex-1 py-1">
        {/* 全部 */}
        <button
          type="button"
          className={cn(
            'flex w-full items-center gap-1 px-2 py-1 text-sm transition-colors',
            !selectedTarget ? 'bg-accent text-accent-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground',
          )}
          onClick={() => onSelectTarget(null)}
        >
          <span className="truncate">全部实例</span>
          <span className="text-[0.6rem] text-muted-foreground/60">({instances.length})</span>
        </button>

        {Array.from(groupMap.entries()).map(([group, { zones: zoneMap }]) => {
          const isGroupExpanded = expanded.has(`group-${group}`)
          const isGroupSelected = selectedTarget?.type === 'group' && selectedTarget.value === group
          return (
            <div key={group}>
              <button
                type="button"
                className={cn(
                  'flex w-full items-center gap-1 px-2 py-1 text-sm transition-colors',
                  isGroupSelected ? 'bg-accent text-accent-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground',
                )}
                style={{ paddingLeft: '16px' }}
                onClick={() => { toggle(`group-${group}`); onSelectTarget({ type: 'group', value: group }) }}
              >
                {isGroupExpanded ? <ChevronDown className="h-3 w-3 shrink-0" /> : <ChevronRight className="h-3 w-3 shrink-0" />}
                <span className="truncate">{group}</span>
                <span className="text-[0.6rem] text-muted-foreground/60">({Array.from(zoneMap.values()).flat().length})</span>
              </button>
              {isGroupExpanded && (
                <div>
                  {Array.from(zoneMap.entries()).map(([zone, servers]) => {
                    const isZoneExpanded = expanded.has(`zone-${group}-${zone}`)
                    return (
                      <div key={zone}>
                        <button
                          type="button"
                          className="flex w-full items-center gap-1 px-2 py-0.5 text-xs text-muted-foreground/70 hover:text-muted-foreground transition-colors"
                          style={{ paddingLeft: '28px' }}
                          onClick={() => toggle(`zone-${group}-${zone}`)}
                        >
                          {isZoneExpanded ? <ChevronDown className="h-2.5 w-2.5 shrink-0" /> : <ChevronRight className="h-2.5 w-2.5 shrink-0" />}
                          <span className="truncate">{zone}</span>
                          <span className="text-[0.6rem] text-muted-foreground/50">({servers.length})</span>
                        </button>
                        {isZoneExpanded && servers.map((s) => (
                          <button
                            key={s.serverId}
                            type="button"
                            className={cn(
                              'flex w-full items-center gap-1.5 px-2 py-0.5 text-xs transition-colors',
                              selectedTarget?.type === 'server' && selectedTarget.value === s.serverId
                                ? 'bg-accent text-accent-foreground'
                                : 'text-muted-foreground/60 hover:text-muted-foreground',
                            )}
                            style={{ paddingLeft: '40px' }}
                            onClick={() => onSelectTarget({ type: 'server', value: s.serverId })}
                          >
                            <span className={cn('h-2 w-2 shrink-0 rounded-full', statusColor(s.status))} />
                            <span className="truncate">{s.serverId}</span>
                          </button>
                        ))}
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })}
      </ScrollArea>
    </div>
  )
}
