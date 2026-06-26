// 列表页顶部汇总条（FR-106）：一排紧凑 metric 小卡，给该页关键计数。
// 纯展示：只接收已派生好的 { 标签, 数值, 语义色 }，不含任何取数/派生逻辑（派生留各页）。
// 视觉：bg-secondary 无边框小卡，标签 11px 弱色、数值 ~20px。语义色用现有 token，不硬编码颜色。

import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

// 数值语义色调：默认前景色；warning 琥珀、danger 危险、success 绿、muted 弱色。
export type SummaryTone = 'default' | 'warning' | 'danger' | 'success' | 'muted'

// 单个 metric 项
export interface SummaryItem {
  // 指标标签（小号弱色）
  label: ReactNode
  // 指标数值（大号）
  value: ReactNode
  // 数值语义色（缺省 default）
  tone?: SummaryTone
}

// 语义色 → 数值文字类名（均走 CSS 变量 / 既有 Tailwind 调色，不硬编码十六进制）
const TONE_CLASS: Record<SummaryTone, string> = {
  default: 'text-foreground',
  warning: 'text-amber-600',
  danger: 'text-destructive',
  success: 'text-green-600',
  muted: 'text-muted-foreground',
}

// SummaryStrip：渲染一排紧凑 metric 小卡；items 为空则不渲染（避免空条占位）。
export default function SummaryStrip({ items }: { items: SummaryItem[] }) {
  if (items.length === 0) return null
  return (
    <div className="flex flex-wrap gap-2">
      {items.map((it, i) => (
        <div key={i} className="min-w-24 rounded-md bg-secondary px-3 py-2">
          <div className="text-[11px] leading-none text-muted-foreground">{it.label}</div>
          <div className={cn('mt-1 text-xl font-semibold leading-none', TONE_CLASS[it.tone ?? 'default'])}>
            {it.value}
          </div>
        </div>
      ))}
    </div>
  )
}
