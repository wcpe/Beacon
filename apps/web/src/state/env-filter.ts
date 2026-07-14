// 全局「env 过滤器」客户端状态（FR-178）：顶栏按 env 过滤各页视图的选中态。
// env 是纯展示 / 过滤维度——本状态只影响前端视图取数范围，绝不改任何权威数据。
// 选中的 env id 存 localStorage 持久（关页 / 刷新保持）；0 表示「全部环境」（不过滤）。
// 与 state/auth.ts 同构：模块级快照 + 订阅集合 + useSyncExternalStore，避免每次返回新对象引发无限重渲染。

import { useSyncExternalStore } from 'react'

// localStorage 键名
const ENV_FILTER_KEY = 'beacon.envFilter'

// 「全部环境」哨兵：不按 env 过滤
export const ALL_ENVS = 0

// 订阅者集合：选中 env 变化时通知所有使用方重渲染
const listeners = new Set<() => void>()

// 当前选中 env id 快照
let snapshot: number = readFromStorage()

// 从 localStorage 读取选中 env id（非法 / 不可用时回退「全部」）
function readFromStorage(): number {
  try {
    const raw = localStorage.getItem(ENV_FILTER_KEY)
    if (raw === null) {
      return ALL_ENVS
    }
    const parsed = Number.parseInt(raw, 10)
    return Number.isNaN(parsed) || parsed < 0 ? ALL_ENVS : parsed
  } catch {
    return ALL_ENVS
  }
}

// 写入 localStorage 并刷新快照、广播变化
function persist(envId: number): void {
  try {
    localStorage.setItem(ENV_FILTER_KEY, String(envId))
  } catch {
    // 隐私模式等写入失败忽略（仅影响持久化，不影响本次会话）
  }
  snapshot = envId
  for (const listener of listeners) {
    listener()
  }
}

function subscribe(callback: () => void): () => void {
  listeners.add(callback)
  return () => {
    listeners.delete(callback)
  }
}

function getSnapshot(): number {
  return snapshot
}

// 设置选中 env（0 = 全部环境）
export function setEnvFilter(envId: number): void {
  persist(envId)
}

// 取当前选中 env id（非 React 上下文可直接调用；0 = 全部）
export function currentEnvId(): number {
  return snapshot
}

// useEnvFilter 返回当前选中 env id，变化时组件重渲染（0 = 全部环境）。
export function useEnvFilter(): number {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
