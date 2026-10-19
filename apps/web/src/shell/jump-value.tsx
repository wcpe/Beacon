// 数值跳动：值发生变化时给数字一次放大+高亮动画，让「刚更新过」可被看见。
//
// 为什么不用「变淡」：页眉 CPU 常在 0%~1% 之间来回，数值本身变化极小，
// 单纯的透明度变化几乎无法察觉（实测 400ms 淡出用户仍反馈「看不到 loading」）。
// 跳动把注意力引到本次更新上，是「数字在刷新」最直接的表达。
import { useEffect, useRef, useState } from 'react'

interface JumpValueProps {
  // 用于判断是否变化的值（通常是格式化后的字符串）
  value: string
  className?: string
}

export default function JumpValue({ value, className }: JumpValueProps) {
  const previous = useRef(value)
  // 每次变化递增，作为 key 让动画重新播放
  const [tick, setTick] = useState(0)
  // 首次渲染不跳动：那是「初次出现」，不是「更新」
  const [animate, setAnimate] = useState(false)

  useEffect(() => {
    if (previous.current === value) return
    previous.current = value
    setTick((n) => n + 1)
    setAnimate(true)
  }, [value])

  return (
    <span
      // key 变化时元素重建，CSS 动画因此重新触发（同一元素重复加同类名不会重播）
      key={tick}
      className={[animate ? 'animate-value-jump' : '', className ?? ''].filter(Boolean).join(' ')}
      // 动画结束移除类名，避免后续无关重渲染重复播放
      onAnimationEnd={() => {
        setAnimate(false)
      }}
    >
      {value}
    </span>
  )
}
