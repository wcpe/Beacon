// 全局「用户主动刷新」信号：页眉刷新按钮置位，页面内容区据此展示加载态。
//
// 为什么需要单独的信号：页面骨架原先只判 query.isPending，而刷新走 invalidateQueries——
// 有缓存时 isPending 保持 false，于是点刷新页面上什么都不会变，用户以为没生效。
// 也不能直接用 useIsFetching：后台 8 秒轮询同样会让它长期为 true，
// 那样页面会每 8 秒闪一次骨架。
import { useEffect, useState } from 'react'

/** 用户主动刷新的忙碌窗口（ms）。与页眉 MIN_REFRESH_BUSY_MS 一致，保证整页反馈同步。 */
export const PAGE_REFRESH_BUSY_MS = 400

type Listener = (refreshing: boolean) => void

let refreshing = false
let deadline = 0
let endTimer: ReturnType<typeof setTimeout> | null = null
const listeners = new Set<Listener>()

function emit(): void {
  for (const fn of listeners) {
    fn(refreshing)
  }
}

/**
 * 标记用户主动刷新开始。
 * 调用方在刷新请求完成后应调用 endPageRefreshing，或等待最短窗口自动结束。
 */
export function beginPageRefreshing(): void {
  if (endTimer !== null) {
    clearTimeout(endTimer)
    endTimer = null
  }
  deadline = Date.now() + PAGE_REFRESH_BUSY_MS
  if (!refreshing) {
    refreshing = true
    emit()
  }
}

/** 请求完成后调用：若已满最短窗口立即结束，否则等窗口走完再结束。 */
export function endPageRefreshing(): void {
  const remaining = deadline - Date.now()
  if (endTimer !== null) {
    clearTimeout(endTimer)
    endTimer = null
  }
  if (remaining <= 0) {
    if (refreshing) {
      refreshing = false
      emit()
    }
    return
  }
  endTimer = setTimeout(() => {
    endTimer = null
    // 期间若有新的刷新开始，deadline 会被推后，此处需重新判断。
    if (Date.now() >= deadline && refreshing) {
      refreshing = false
      emit()
    } else if (refreshing) {
      endPageRefreshing()
    }
  }, remaining)
}

/** 订阅主动刷新状态；用于页面内容区决定是否展示加载态。 */
export function usePageRefreshing(): boolean {
  const [value, setValue] = useState(refreshing)
  useEffect(() => {
    const fn: Listener = (next) => {
      setValue(next)
    }
    listeners.add(fn)
    // 订阅瞬间同步一次，避免挂载期间错过状态变化。
    setValue(refreshing)
    return () => {
      listeners.delete(fn)
    }
  }, [])
  return value
}

/** 测试清理模块级刷新状态，避免跨用例残留定时器或订阅者。 */
export function resetPageRefreshingForTest(): void {
  if (endTimer !== null) {
    clearTimeout(endTimer)
    endTimer = null
  }
  refreshing = false
  deadline = 0
  listeners.clear()
}
