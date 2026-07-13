// 冷查询 keyset 游标翻页栈（FR-152）：冷查询无总数，用 keyset 游标双向翻页。
// 栈记录已访问各页的起始游标，栈顶为当前页（首页为空串）。sched-decisions / audits 冷查询共用。
import { useCallback, useMemo, useState } from 'react'

export interface CursorStack {
  // 当前页起始游标（首页为空串），作为取数入参
  cursor: string
  // 当前页序（1 起，仅用于展示「第 N 页」）
  pageIndex: number
  // 是否可回上一页
  canPrev: boolean
  // 前进一页：压入后端返回的 nextCursor
  goNext: (nextCursor: string) => void
  // 回退一页：弹出栈顶
  goPrev: () => void
  // 重置回首页（切换冷查询开关 / 过滤 / 时间窗时调用）
  reset: () => void
}

export function useCursorStack(): CursorStack {
  const [stack, setStack] = useState<string[]>([''])
  const goNext = useCallback((nextCursor: string) => {
    setStack((s) => [...s, nextCursor])
  }, [])
  const goPrev = useCallback(() => {
    setStack((s) => (s.length > 1 ? s.slice(0, -1) : s))
  }, [])
  const reset = useCallback(() => {
    setStack([''])
  }, [])
  return useMemo(
    () => ({
      cursor: stack[stack.length - 1],
      pageIndex: stack.length,
      canPrev: stack.length > 1,
      goNext,
      goPrev,
      reset,
    }),
    [stack, goNext, goPrev, reset],
  )
}
