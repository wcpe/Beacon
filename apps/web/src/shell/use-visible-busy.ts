// 取数忙碌态的最短展示时长：把瞬时完成的请求「撑」到肉眼可见的时长。
//
// 为什么需要：内网/本机请求常在个位数毫秒内返回。若把 isFetching 直接绑到视觉反馈，
// 淡出只存在 5ms（实测 71936→71941），用户完全看不到，等于没做。
// 这里在取数结束后再保持 minimumMs，让「数值刚更新」这件事可被感知。
//
// 与「后台轮询不闪」的区别：轮询每 8 秒才一次，每次保持 400ms 只占 5%，
// 不会造成持续闪烁；而首屏连续请求会合并成一段连续的忙碌窗口。
import { useEffect, useRef, useState } from 'react'

/** 取数忙碌态最短展示时长（ms）：与页眉刷新按钮的 400ms 对齐，视觉节奏一致。 */
export const MIN_FETCH_BUSY_MS = 400

/**
 * 把「取数中」转成「可感知的忙碌窗口」。
 * @param isFetching React Query 的取数中标志（含轮询）
 * @param forceBusy  额外强制忙碌（如用户主动刷新的最短窗口）
 */
export function useVisibleBusy(isFetching: boolean, forceBusy = false): boolean {
  const [visible, setVisible] = useState(isFetching || forceBusy)
  // 本轮忙碌窗口的结束时刻；0 表示当前无窗口
  const holdUntil = useRef(0)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const active = isFetching || forceBusy
    if (active) {
      // 取数中：立刻进入忙碌，并不断把保持期推到「现在 + 最短时长」
      holdUntil.current = Date.now() + MIN_FETCH_BUSY_MS
      if (timer.current !== null) {
        clearTimeout(timer.current)
        timer.current = null
      }
      setVisible(true)
      return
    }
    // 取数结束：若还没满最短时长，补齐剩余时间再收起
    const remaining = holdUntil.current - Date.now()
    if (remaining <= 0) {
      setVisible(false)
      return
    }
    timer.current = setTimeout(() => {
      timer.current = null
      setVisible(false)
    }, remaining)
  }, [isFetching, forceBusy])

  // 卸载时清理定时器，避免测试 teardown 后 setState
  useEffect(() => {
    return () => {
      if (timer.current !== null) {
        clearTimeout(timer.current)
      }
    }
  }, [])

  return visible
}
