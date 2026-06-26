// 全局「界面布局状态」：侧栏折叠态（改进 1 — 可折叠图标条）。
// 与 state/preferences.ts 同为纯前端展示选项、与登录态无关，落 localStorage 跨会话持久化。
// 镜像 preferences.ts 的订阅者模式（useSyncExternalStore + 集合广播），作为侧栏折叠态的单一真源；
// 与主题偏好分文件，避免两类无关状态耦合到同一序列化对象。

import { useSyncExternalStore } from 'react'

// localStorage 键名（界面布局状态序列化为一个 JSON）
const UI_STATE_KEY = 'beacon.ui'

// 界面布局状态快照
export interface UiState {
  // 侧栏是否折叠为窄图标条（true=折叠 w-14；false=展开 w-56）
  sidebarCollapsed: boolean
}

// 默认折叠：首次进入即窄图标条（按本批改进 1 约定）。
const DEFAULT_UI_STATE: UiState = {
  sidebarCollapsed: true,
}

// 订阅者集合：状态变化时通知所有使用方重渲染
const listeners = new Set<() => void>()

// 当前状态快照（避免 useSyncExternalStore 每次返回新对象引发无限重渲染）
let snapshot: UiState = readFromStorage()

// 从 localStorage 读取状态（不可用 / 解析失败时回退默认值）
function readFromStorage(): UiState {
  try {
    const raw = localStorage.getItem(UI_STATE_KEY)
    if (!raw) return DEFAULT_UI_STATE
    const parsed = JSON.parse(raw) as Partial<UiState>
    // 逐字段校验，非法值回落默认（应用层校验，不信任外部存储）
    return {
      sidebarCollapsed:
        typeof parsed.sidebarCollapsed === 'boolean'
          ? parsed.sidebarCollapsed
          : DEFAULT_UI_STATE.sidebarCollapsed,
    }
  } catch {
    return DEFAULT_UI_STATE
  }
}

// 写入 localStorage 并刷新快照、广播变化
function persist(next: UiState): void {
  try {
    localStorage.setItem(UI_STATE_KEY, JSON.stringify(next))
  } catch {
    // 隐私模式等场景写入失败，忽略（仅影响持久化，不影响本次会话使用）
  }
  snapshot = next
  for (const l of listeners) l()
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

function getSnapshot(): UiState {
  return snapshot
}

// 切换侧栏折叠态
export function toggleSidebar(): void {
  persist({ ...snapshot, sidebarCollapsed: !snapshot.sidebarCollapsed })
}

// 显式设置侧栏折叠态
export function setSidebarCollapsed(collapsed: boolean): void {
  persist({ ...snapshot, sidebarCollapsed: collapsed })
}

// useUiState 返回当前界面布局状态，状态变化时组件重渲染。
export function useUiState(): UiState {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
