/**
 * 配置中心 — VS Code 风格编辑器（编排层）
 *
 * 布局：
 *   ┌────────┬──────────────────────────────────────┐
 *   │ 左侧    │ 标签栏（右键菜单关闭）                  │
 *   │ 面板    ├──────────────────────────────────────┤
 *   │ 上：    │ [编辑] [Diff] [生效预览]                │
 *   │ 文件树  │┌────────────────────────────────────┐│
 *   │ 下：    ││  Monaco Editor / DiffEditor         ││
 *   │ 实例/   │└────────────────────────────────────┘│
 *   │ 分组    │ ▼ 历史修订面板（可折叠）               │
 *   └────────┴──────────────────────────────────────┘
 *
 * 本组件只做数据获取与子组件编排；标签 UI 状态见 configs/useConfigTabs，
 * 各区块见 configs/ 下 ConfigFileTree / TargetSelector / ConfigTabBar / ConfigEditorPane / CreateConfigDialog。
 */

import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import {
  diffConfig,
  effectiveConfig,
  getConfig,
  listConfigs,
  listInstances,
  listNamespaces,
  listRevisions,
  publishConfig,
  zoneSummary,
} from '../api/client'
import type { CreateConfigParams } from '../api/client'
import { namespaceOptions } from '../api/format'
import { useMessage } from '../components/useMessage'
import { usePageHeader } from '@/components/PageHeader'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { LintError } from '@/lib/configLint'
import { useConfigTabs } from './configs/useConfigTabs'
import ConfigFileTree from './configs/ConfigFileTree'
import TargetSelector from './configs/TargetSelector'
import CreateConfigDialog from './configs/CreateConfigDialog'
import ImportFilesDialog from './configs/ImportFilesDialog'
import ReverseFetchDialog from './configs/ReverseFetchDialog'
import ConfigTabBar from './configs/ConfigTabBar'
import ConfigEditorPane from './configs/ConfigEditorPane'
import ConfigSaveConfirmDialog from './configs/ConfigSaveConfirmDialog'
import BatchOpsPanel from './configs/BatchOpsPanel'
import type { OpenTab, TreeNode } from './configs/types'

export default function ConfigsPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()
  const tabs = useConfigTabs()

  // 左侧：选中的目标（实例 serverId 或分组 group），用于过滤配置树
  const [selectedTarget, setSelectedTarget] = useState<{ type: 'server' | 'group'; value: string } | null>(
    null,
  )

  // 配置列表
  const list = useQuery({ queryKey: ['configs'], queryFn: () => listConfigs({}) })
  // 实例列表（左侧目标选择器 + 生效预览目标下拉 + 新建对话框 server 层目标）
  const instancesQuery = useQuery({ queryKey: ['instances-all'], queryFn: () => listInstances({}) })
  // 环境列表（新建对话框环境下拉，去硬编码）
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  // zone 汇总（新建对话框大区 / 小区下拉来源）
  const zonesQuery = useQuery({ queryKey: ['zones-summary'], queryFn: () => zoneSummary() })

  // 新建对话框：开合与预填初值（「复制到实例」复用同一对话框，预填后唤起）
  const [createOpen, setCreateOpen] = useState(false)
  const [createInitial, setCreateInitial] = useState<CreateConfigParams | undefined>()

  // 保存确认对话框（FR-67）：保存前先看 diff + 填备注，确认才发布。
  const [saveConfirmOpen, setSaveConfirmOpen] = useState(false)
  const [saveComment, setSaveComment] = useState('')

  // 当前编辑态的客户端格式校验错误（FR-75）：非法则禁用保存 / 拦发布。
  const [lintError, setLintError] = useState<LintError | null>(null)

  // 新建对话框动态选项：环境 / 大区 / 小区 / 实例（均由既有 list 端点派生，无硬编码）
  // 环境候选显示「编码 · 名称」，真实值仍是 code（FR-70）
  const nsOptions = useMemo(
    () => namespaceOptions(namespacesQuery.data),
    [namespacesQuery.data],
  )
  // 大区候选：zone 汇总与实例列表去重并集（兼容无 zone 指派但已注册的大区）
  const groupOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of zonesQuery.data ?? []) if (z.group) set.add(z.group)
    for (const i of instancesQuery.data ?? []) if (i.group) set.add(i.group)
    return Array.from(set).sort()
  }, [zonesQuery.data, instancesQuery.data])
  // 小区候选：zone 汇总与实例列表去重并集
  const zoneOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of zonesQuery.data ?? []) if (z.zone) set.add(z.zone)
    for (const i of instancesQuery.data ?? []) if (i.zone) set.add(i.zone)
    return Array.from(set).sort()
  }, [zonesQuery.data, instancesQuery.data])

  // 「复制到实例」：以源配置为底，预填为 server 层覆盖（目标待选），保留源内容供改 diff。
  const copyToInstance = useCallback(
    (tab: OpenTab) => {
      setCreateInitial({
        namespace: tab.namespace,
        group: tab.group,
        dataId: tab.dataId,
        scopeLevel: 'server',
        scopeTarget: '',
        format: tab.format,
        content: tab.content,
        comment: '',
      })
      setCreateOpen(true)
    },
    [],
  )

  const activeTab = tabs.activeTab

  // 切换活跃标签时清空格式校验态：新标签的编辑器挂载后会重新上抛真实结果
  useEffect(() => {
    setLintError(null)
  }, [activeTab?.configId])

  // 当前活跃标签的配置详情
  const detail = useQuery({
    queryKey: ['config', activeTab?.configId],
    queryFn: () => (activeTab ? getConfig(activeTab.configId) : Promise.reject(new Error('no tab'))),
    enabled: !!activeTab,
  })

  // 详情加载完成后，把内容同步进对应标签
  useEffect(() => {
    if (detail.data?.content && activeTab) {
      tabs.setTabContent(activeTab.configId, detail.data.content)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail.data?.content])

  // 当前标签的历史修订
  const revisions = useQuery({
    queryKey: ['config-revisions', activeTab?.configId],
    queryFn: () => (activeTab ? listRevisions(activeTab.configId) : Promise.resolve([])),
    enabled: !!activeTab,
  })

  // Diff 数据（按当前标签的版本选择）
  const activeDiff = tabs.activeDiff
  const diff = useQuery({
    queryKey: ['config-diff', activeTab?.configId, activeDiff.from, activeDiff.to],
    queryFn: () =>
      activeTab && activeDiff.from && activeDiff.to
        ? diffConfig(activeTab.configId, Number(activeDiff.from), Number(activeDiff.to))
        : Promise.resolve(null),
    enabled: !!activeTab && activeDiff.from !== '' && activeDiff.to !== '',
  })

  // 生效预览数据
  const effectiveQuery = useQuery({
    queryKey: ['config-effective', tabs.effectiveTarget],
    queryFn: () =>
      effectiveConfig({
        namespace: 'prod',
        serverId: tabs.effectiveTarget.serverId,
        group: tabs.effectiveTarget.group,
      }),
    enabled: !!(tabs.effectiveTarget.serverId || tabs.effectiveTarget.group),
  })

  // 保存 mutation
  const saveMut = useMutation({
    mutationFn: (params: { id: number; content: string; comment: string }) =>
      publishConfig(params.id, params.content, params.comment),
    onSuccess: (r) => {
      msg.showSuccess(t('configs.msgSaved', { version: r.version }))
      setSaveConfirmOpen(false)
      qc.invalidateQueries({ queryKey: ['configs'] })
      // 同时失效当前配置的历史修订，保存后历史面板即时刷新出新版本
      qc.invalidateQueries({ queryKey: ['config-revisions'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 点保存：不直接发布，先打开保存确认对话框（看 diff + 填备注）（FR-67）。
  // 客户端格式校验失败（FR-75）时拦在发布前：不弹对话框、提示先修正。
  const requestSave = useCallback(() => {
    if (!activeTab) return
    if (lintError) {
      msg.showError(t('editor.saveDisabledByLint'))
      return
    }
    setSaveComment(t('configs.saveComment'))
    setSaveConfirmOpen(true)
  }, [activeTab, lintError, msg, t])

  // 确认保存：对话框内确认才真正发布当前编辑态内容
  const confirmSave = useCallback(() => {
    if (!activeTab) return
    saveMut.mutate({ id: activeTab.configId, content: activeTab.content, comment: saveComment })
  }, [activeTab, saveMut, saveComment])

  // Ctrl+S 全局快捷键：唤起保存确认对话框（确认才发布）
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault()
        requestSave()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [requestSave])

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

  // 批量操作面板数据源（FR-74）：与文件树同口径按 selectedTarget 过滤的扁平配置列表
  const batchConfigsList = useMemo(() => {
    let items = list.data ?? []
    if (selectedTarget) {
      if (selectedTarget.type === 'server') {
        items = items.filter((c) => c.scopeTarget === selectedTarget.value)
      } else if (selectedTarget.type === 'group') {
        items = items.filter((c) => c.group === selectedTarget.value)
      }
    }
    return items
  }, [list.data, selectedTarget])

  // 点击资源管理器节点 → 打开标签
  const handleFileSelect = useCallback(
    (node: TreeNode) => {
      if (node.type !== 'file' || !node.data) return
      const { namespace, group, dataId } = node.data as {
        namespace: string
        group: string
        dataId: string
      }
      const items = list.data ?? []
      const matched = items.filter(
        (c) => c.namespace === namespace && c.group === group && c.dataId === dataId,
      )
      tabs.openConfigs(matched, node.key)
    },
    [list.data, tabs],
  )

  const versionNumbers = (revisions.data ?? []).map((r) => r.version)

  // 页眉（FR-105）：标题 + 计数徽章移入计数槽，导入 / 反向抓取 / 新建配置移入主操作槽（受控逻辑仍在本组件）
  usePageHeader({
    title: t('configs.title'),
    envScoped: true,
    count: (
      <Badge variant="outline" className="text-xs">
        {t('configs.countBadge', { count: list.data?.length ?? 0 })}
      </Badge>
    ),
    actions: (
      <div className="flex items-center gap-2">
        {/* 导入到组（FR-38）：把一份目录批量上传到某组的文件树（通道B 整文件覆盖） */}
        <ImportFilesDialog namespaces={nsOptions} groups={groupOptions} />
        {/* 反向抓取（FR-39）：从某在线实例读其真实 plugins 反向 ingest 为组 / 实例级覆盖 */}
        <ReverseFetchDialog instances={instancesQuery.data ?? []} groups={groupOptions} />
        <CreateConfigDialog
          namespaces={nsOptions}
          groups={groupOptions}
          zones={zoneOptions}
          instances={instancesQuery.data ?? []}
          open={createOpen}
          onOpenChange={(o) => {
            setCreateOpen(o)
            // 关闭即清预填，下次点「新建配置」从空白起（避免残留上次复制的内容）
            if (!o) setCreateInitial(undefined)
          }}
          initial={createInitial}
        />
      </div>
    ),
  })

  return (
    <div className="flex flex-col h-full overflow-hidden gap-2">
      {/* ===== 主体 ===== */}
      <div className="flex flex-1 min-h-0 gap-2">
        {/* ===== 左侧面板（两段式） ===== */}
        <aside className="w-56 shrink-0 flex flex-col rounded-lg border border-border overflow-hidden bg-card">
          {/* 上半：配置文件树 */}
          <div className="flex-1 flex flex-col min-h-0 border-b border-border">
            <div className="flex-shrink-0 px-3 py-2 text-xs font-medium text-muted-foreground border-b border-border bg-muted/30 flex items-center justify-between">
              <span>{t('configs.treeHeader')}</span>
              {selectedTarget && (
                <Button
                  variant="link"
                  size="xs"
                  className="h-auto p-0 text-[0.65rem]"
                  onClick={() => setSelectedTarget(null)}
                >
                  {t('configs.clearTarget')}
                </Button>
              )}
            </div>
            <div className="flex-1 overflow-y-auto py-1">
              {list.isLoading ? (
                // 文件树首屏骨架：若干层级条占位，替掉加载期的空白
                <div className="space-y-2 px-3 py-1">
                  {Array.from({ length: 6 }).map((_, i) => (
                    <Skeleton key={i} className={`h-4 ${i % 3 === 0 ? 'w-32' : 'w-24 ml-3'}`} />
                  ))}
                </div>
              ) : (
                <ConfigFileTree
                  nodes={treeData}
                  selectedKey={tabs.selectedFile}
                  onSelect={handleFileSelect}
                />
              )}
            </div>
          </div>

          {/* 下半：实例/分组选择器（树形） */}
          <TargetSelector
            instances={instancesQuery.data ?? []}
            selectedTarget={selectedTarget}
            onSelectTarget={setSelectedTarget}
          />
        </aside>

        {/* ===== 右侧编辑器区域 ===== */}
        <div className="flex-1 flex flex-col min-w-0 gap-2">
          {/* 标签栏 */}
          <ConfigTabBar
            openTabs={tabs.openTabs}
            activeTabKey={tabs.activeTabKey}
            contextTabId={tabs.contextTabId}
            showTabMenu={tabs.showTabMenu}
            onSelect={(id) => tabs.setActiveTabKey(String(id))}
            onClose={tabs.closeTab}
            onContextMenu={tabs.openContextMenu}
            onContextAction={tabs.contextAction}
          />

          {/* 编辑器内容区 */}
          {activeTab ? (
            <ConfigEditorPane
              tab={activeTab}
              view={tabs.activeView}
              onSetView={(m) => tabs.setViewMode(activeTab.configId, m)}
              onActivateEffective={() => tabs.activateEffective(activeTab)}
              editor={{
                onChange: (content) => tabs.setTabContent(activeTab.configId, content),
                onValidate: setLintError,
              }}
              save={{ onSave: requestSave, saving: saveMut.isPending, lintInvalid: !!lintError }}
              onCopyToInstance={() => copyToInstance(activeTab)}
              diff={{
                versionNumbers,
                selected: activeDiff,
                onChange: (next) => tabs.setDiffVersion(activeTab.configId, next),
                data: diff.data,
              }}
              effective={{
                instances: instancesQuery.data ?? [],
                target: tabs.effectiveTarget,
                onTargetChange: tabs.setEffectiveTarget,
                isLoading: effectiveQuery.isLoading,
                data: effectiveQuery.data,
              }}
              history={{
                revisions: revisions.data ?? [],
                collapsed: tabs.historyCollapsed[activeTab.configId] ?? false,
                highlightRev: tabs.highlightRev,
                onToggleCollapse: () => tabs.toggleHistory(activeTab.configId),
                onSelectRevision: (rev) =>
                  tabs.selectRevision(activeTab.configId, rev, revisions.data ?? []),
              }}
            />
          ) : (
            <div className="flex-1 flex items-center justify-center rounded-lg border border-border bg-card">
              <div className="text-center text-muted-foreground">
                <p className="text-sm">{t('configs.emptyHintLine1')}</p>
                <p className="mt-1 text-xs">{t('configs.emptyHintLine2')}</p>
              </div>
            </div>
          )}

          {/* 保存确认对话框（FR-67）：diff（上一保存版本 ⟷ 当前编辑态）+ 备注，确认才发布 */}
          {activeTab && (
            <ConfigSaveConfirmDialog
              open={saveConfirmOpen}
              namespace={activeTab.namespace}
              group={activeTab.group}
              dataId={activeTab.dataId}
              scopeLevel={activeTab.scopeLevel}
              scopeTarget={activeTab.scopeTarget}
              format={activeTab.format}
              originalContent={detail.data?.content ?? ''}
              currentContent={activeTab.content}
              comment={saveComment}
              pending={saveMut.isPending}
              onCommentChange={setSaveComment}
              onConfirm={confirmSave}
              onCancel={() => setSaveConfirmOpen(false)}
            />
          )}
        </div>
      </div>

      {/* ===== 批量操作区（FR-74）：多选 + 删除/禁用/启用/导出，独立于上方编辑器 ===== */}
      <BatchOpsPanel configs={batchConfigsList} />
    </div>
  )
}
