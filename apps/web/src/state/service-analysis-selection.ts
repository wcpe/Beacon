// 服务分析页选中服务器持久化：刷新 / 重进页面恢复上次多选。
// 与 state/env-filter.ts 同构：模块级快照 + localStorage + 订阅集合。

import { useSyncExternalStore } from 'react'

const STORAGE_KEY = 'beacon.serviceAnalysis.selectedServers'

const listeners = new Set<() => void>()

// 快照为 serverId 数组（稳定序列化顺序）
let snapshot: string[] = readFromStorage()

function readFromStorage(): string[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === null || raw === '') {
      return []
    }
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) {
      return []
    }
    return parsed.filter((item): item is string => typeof item === 'string' && item !== '')
  } catch {
    return []
  }
}

function persist(ids: string[]): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(ids))
  } catch {
    // 隐私模式等写入失败忽略
  }
  snapshot = ids
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

function getSnapshot(): string[] {
  return snapshot
}

/** 覆盖整份选中列表（toggle / clear 后调用） */
export function setServiceAnalysisSelected(ids: Iterable<string>): void {
  persist([...ids])
}

/** 测试用：清空选中与持久化，避免跨用例串状态 */
export function resetServiceAnalysisSelectedForTests(): void {
  persist([])
}

/** 读取当前选中 serverId 列表 */
export function getServiceAnalysisSelected(): string[] {
  return snapshot
}

/** React hook：选中 serverId 列表（数组引用仅在变更时更新） */
export function useServiceAnalysisSelected(): string[] {
  return useSyncExternalStore(subscribe, getSnapshot, () => [])
}
