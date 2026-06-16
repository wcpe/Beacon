// 全局「操作人」状态：写操作（发布/回滚/改派/下线等）需带 operator 字段。
// 持久化到 localStorage，刷新后保留；为空时由页面提示必填。

import { useCallback, useSyncExternalStore } from 'react'

// localStorage 键名
const KEY = 'beacon.operator'

// 订阅者集合：operator 变化时通知所有使用方重渲染
const listeners = new Set<() => void>()

// 读取当前 operator（localStorage 不可用时回退空串）
function read(): string {
  try {
    return localStorage.getItem(KEY) ?? ''
  } catch {
    return ''
  }
}

// 写入并广播变化
function write(value: string): void {
  try {
    localStorage.setItem(KEY, value)
  } catch {
    // 隐私模式等场景写入失败，忽略（仅影响持久化，不影响本次会话使用）
  }
  for (const l of listeners) l()
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

// useOperator 返回当前操作人与设置函数，跨页面共享同一份状态。
export function useOperator(): [string, (value: string) => void] {
  const operator = useSyncExternalStore(subscribe, read, read)
  const setOperator = useCallback((value: string) => write(value), [])
  return [operator, setOperator]
}
