// 配置中心标签状态 hook：集中管理打开的标签、活跃标签、各标签的视图/Diff/历史折叠等 UI 状态。
// 标签级状态按 configId 存于 map，切换标签时各自保留（与拆分前行为一致）。

import { useCallback, useState } from 'react'
import type { ConfigView, RevisionView } from '../../api/types'
import type { OpenTab, ViewMode } from './types'
import type { TabContextAction } from './ConfigTabBar'

export function useConfigTabs() {
  // 左侧资源树选中的节点 key
  const [selectedFile, setSelectedFile] = useState<string | null>(null)
  // 打开的标签
  const [openTabs, setOpenTabs] = useState<OpenTab[]>([])
  const [activeTabKey, setActiveTabKey] = useState<string | null>(null)
  // 各标签视图模式
  const [viewModes, setViewModes] = useState<Record<number, ViewMode>>({})
  // 各标签 Diff 版本选择
  const [diffVersions, setDiffVersions] = useState<Record<number, { from: string; to: string }>>({})
  // 各标签历史面板折叠状态
  const [historyCollapsed, setHistoryCollapsed] = useState<Record<number, boolean>>({})
  // 历史面板高亮的修订版本
  const [highlightRev, setHighlightRev] = useState<number | undefined>()
  // 右键菜单状态
  const [contextTabId, setContextTabId] = useState<number | null>(null)
  const [showTabMenu, setShowTabMenu] = useState(false)
  // 生效预览目标（serverId 或 group，全局共享一个）
  const [effectiveTarget, setEffectiveTarget] = useState<{ serverId?: string; group?: string }>({})

  const activeId = Number(activeTabKey)
  const activeTab = openTabs.find((t) => t.configId === activeId)
  const activeView = viewModes[activeId] ?? 'edit'
  const activeDiff = diffVersions[activeId] ?? { from: '', to: '' }

  // 打开一组匹配的配置为标签，并激活首个；selectedKey 同步到资源树选中态。
  const openConfigs = useCallback((matched: ConfigView[], selectedKey: string) => {
    if (matched.length === 0) return
    setSelectedFile(selectedKey)
    setOpenTabs((prev) => {
      const existingIds = new Set(prev.map((t) => t.configId))
      const fresh = matched
        .filter((c) => !existingIds.has(c.id))
        .map((c) => ({
          configId: c.id,
          dataId: c.dataId,
          namespace: c.namespace,
          group: c.group,
          scopeLevel: c.scopeLevel,
          scopeTarget: c.scopeTarget,
          format: c.format,
          content: c.content ?? '',
        }))
      return [...prev, ...fresh]
    })
    setActiveTabKey(String(matched[0].id))
    setViewModes((prev) => ({ ...prev, [matched[0].id]: 'edit' }))
  }, [])

  const closeTab = useCallback(
    (configId: number) => {
      setOpenTabs((prev) => {
        const next = prev.filter((t) => t.configId !== configId)
        if (activeTabKey === String(configId)) {
          setActiveTabKey(next.length > 0 ? String(next[next.length - 1].configId) : null)
        }
        return next
      })
    },
    [activeTabKey],
  )

  const contextAction = useCallback(
    (action: TabContextAction) => {
      const tabId = contextTabId
      if (tabId === null) return
      if (action === 'close') {
        closeTab(tabId)
      } else if (action === 'closeOthers') {
        setOpenTabs((prev) => {
          const keep = prev.find((t) => t.configId === tabId)
          if (keep) {
            setActiveTabKey(String(keep.configId))
            return [keep]
          }
          return prev
        })
      } else if (action === 'closeAll') {
        setOpenTabs([])
        setActiveTabKey(null)
      }
      setShowTabMenu(false)
      setContextTabId(null)
    },
    [contextTabId, closeTab],
  )

  const openContextMenu = useCallback((configId: number) => {
    setContextTabId(configId)
    setShowTabMenu(true)
  }, [])

  const setViewMode = useCallback((configId: number, mode: ViewMode) => {
    setViewModes((p) => ({ ...p, [configId]: mode }))
  }, [])

  const setDiffVersion = useCallback((configId: number, next: { from: string; to: string }) => {
    setDiffVersions((p) => ({ ...p, [configId]: next }))
  }, [])

  const toggleHistory = useCallback((configId: number) => {
    setHistoryCollapsed((p) => ({ ...p, [configId]: !p[configId] }))
  }, [])

  // 更新某标签的编辑内容（编辑器输入与详情加载同步均用此）
  const setTabContent = useCallback((configId: number, content: string) => {
    setOpenTabs((prev) => prev.map((t) => (t.configId === configId ? { ...t, content } : t)))
  }, [])

  // 切到生效预览：以当前标签的 group / 覆盖目标作为默认预览目标
  const activateEffective = useCallback((tab: OpenTab) => {
    setEffectiveTarget({ group: tab.group, serverId: tab.scopeTarget || undefined })
    setViewModes((p) => ({ ...p, [tab.configId]: 'effective' }))
  }, [])

  // 点击历史条目：高亮该版本，并以「该版本 → 上一更旧版本」进入 Diff
  const selectRevision = useCallback(
    (configId: number, rev: RevisionView, revisions: RevisionView[]) => {
      setHighlightRev(rev.version)
      const idx = revisions.findIndex((r) => r.version === rev.version)
      setDiffVersions((p) => ({
        ...p,
        [configId]: { from: String(rev.version), to: String(revisions[idx + 1]?.version ?? rev.version) },
      }))
      setViewModes((p) => ({ ...p, [configId]: 'diff' }))
    },
    [],
  )

  return {
    // 状态
    selectedFile,
    openTabs,
    activeTabKey,
    historyCollapsed,
    highlightRev,
    contextTabId,
    showTabMenu,
    effectiveTarget,
    // 派生
    activeTab,
    activeView,
    activeDiff,
    // 动作
    setActiveTabKey,
    openConfigs,
    closeTab,
    contextAction,
    openContextMenu,
    setViewMode,
    setDiffVersion,
    toggleHistory,
    setTabContent,
    setEffectiveTarget,
    activateEffective,
    selectRevision,
  }
}
