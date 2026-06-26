// 全局「环境」上下文（FR-105）：把 namespace(环境) 升为前端全局状态。
// 与具体页面解耦、跨页记住、刷新仍在，由两层页眉的环境选择器读写。
// 镜像 state/preferences.ts 的订阅者模式（useSyncExternalStore + 集合广播 + localStorage），作为环境的单一真源。
// 注意：本 store 仅负责「页眉选择 + 持久化 + 跨页保持」；各页内部筛选改读全局环境留各自后续 FR，本 FR 不联动数据刷新。

import { useSyncExternalStore } from 'react'

// localStorage 键名（存一个字符串：当前环境 namespace）
const ENVIRONMENT_KEY = 'beacon.environment'

// 默认环境：空字符串表示「全部环境 / 未选」。
const DEFAULT_ENVIRONMENT = ''

// 订阅者集合：环境变化时通知所有使用方重渲染
const listeners = new Set<() => void>()

// 当前环境快照（字符串本身即不可变，无需额外缓存对象）
let snapshot: string = readFromStorage()

// 从 localStorage 读取环境（不可用 / 非字符串时回退默认值）
function readFromStorage(): string {
  try {
    const raw = localStorage.getItem(ENVIRONMENT_KEY)
    // 不存在时回退默认；存在则直接采用（任意字符串都是合法 namespace，空串表示全部）
    return raw ?? DEFAULT_ENVIRONMENT
  } catch {
    return DEFAULT_ENVIRONMENT
  }
}

// 写入 localStorage 并刷新快照、广播变化
function persist(next: string): void {
  try {
    localStorage.setItem(ENVIRONMENT_KEY, next)
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

function getSnapshot(): string {
  return snapshot
}

// 设置当前环境（空串表示全部 / 未选）
export function setEnvironment(ns: string): void {
  persist(ns)
}

// 取当前环境（非 React 上下文可直接调用）
export function currentEnvironment(): string {
  return snapshot
}

// useEnvironment 返回当前环境，环境变化时组件重渲染。
export function useEnvironment(): string {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
