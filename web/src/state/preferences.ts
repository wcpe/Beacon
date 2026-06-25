// 全局「界面偏好」：暗色主题 + 表格紧凑密度（FR-92）。
// 偏好是纯前端展示选项，与登录态无关，故落 localStorage（跨会话持久化）。
// 镜像 state/auth.ts 的订阅者模式（useSyncExternalStore + 集合广播），作为主题/密度的单一真源。
// 不引入 next-themes，避免与本 store 形成双真源。

import { useSyncExternalStore } from 'react'

// localStorage 键名（整组偏好序列化为一个 JSON）
const PREFERENCES_KEY = 'beacon.preferences'

// 主题：浅色 / 暗色
export type Theme = 'light' | 'dark'
// 表格密度：舒适（默认）/ 紧凑（更小行高与内边距）
export type Density = 'comfortable' | 'compact'

// 界面偏好快照
export interface Preferences {
  theme: Theme
  density: Density
}

// 默认偏好：浅色 + 舒适。
const DEFAULT_PREFERENCES: Preferences = {
  theme: 'light',
  density: 'comfortable',
}

// 订阅者集合：偏好变化时通知所有使用方重渲染
const listeners = new Set<() => void>()

// 当前偏好快照（避免 useSyncExternalStore 每次返回新对象引发无限重渲染）
let snapshot: Preferences = readFromStorage()

// 从 localStorage 读取偏好（不可用 / 解析失败时回退默认值）
function readFromStorage(): Preferences {
  try {
    const raw = localStorage.getItem(PREFERENCES_KEY)
    if (!raw) return DEFAULT_PREFERENCES
    const parsed = JSON.parse(raw) as Partial<Preferences>
    // 逐字段校验，非法值回落默认（应用层枚举校验，不信任外部存储）
    return {
      theme: parsed.theme === 'dark' ? 'dark' : 'light',
      density: parsed.density === 'compact' ? 'compact' : 'comfortable',
    }
  } catch {
    return DEFAULT_PREFERENCES
  }
}

// 写入 localStorage 并刷新快照、广播变化
function persist(next: Preferences): void {
  try {
    localStorage.setItem(PREFERENCES_KEY, JSON.stringify(next))
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

function getSnapshot(): Preferences {
  return snapshot
}

// 设置主题（浅 / 暗）
export function setTheme(theme: Theme): void {
  persist({ ...snapshot, theme })
}

// 设置表格密度（舒适 / 紧凑）
export function setDensity(density: Density): void {
  persist({ ...snapshot, density })
}

// 取当前偏好（非 React 上下文可直接调用，如首屏同步应用主题）
export function currentPreferences(): Preferences {
  return snapshot
}

// 把主题落到文档根节点的 .dark 类（暗色样式由 index.css 的 .dark 变量驱动）。
// 首屏在渲染前同步调用一次避免闪烁，运行期由订阅者跟随偏好变化再调。
export function applyThemeToDocument(theme: Theme): void {
  document.documentElement.classList.toggle('dark', theme === 'dark')
}

// usePreferences 返回当前偏好，偏好变化时组件重渲染。
export function usePreferences(): Preferences {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
