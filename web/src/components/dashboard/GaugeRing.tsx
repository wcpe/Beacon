// 进度环（GaugeRing）：用 CSS conic-gradient 画一个占比环（值/上限→占比），按健康等级变色（绿→琥珀→红）。
// 环中心放 lucide 图标，环下方放标签与「值 / 上限」文案。用于控制面健康页的子系统吃紧总览。
// 纯展示组件，不连数据；占比与等级由父组件按真实字段算好传入。

import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { levelRing, levelText, type HealthLevel } from './health'

interface GaugeRingProps {
  // 环中心图标（lucide）
  icon: ReactNode
  // 占比 0~1（已用/上限）；无上限或不可算时传 null，环退化为「满灰底 + 仅显数值」。
  ratio: number | null
  // 健康等级：决定环颜色与数值文本色。
  level: HealthLevel
  // 环下方标签（指标名）
  label: string
  // 环中心下方主数值文案（如「12 / 20」或「66」）
  valueText: string
  // 副说明（可选，如占比百分比）
  hint?: string
}

// 环直径与线宽（px）：远观清晰又不过大。
const SIZE = 88
const STROKE = 9

export default function GaugeRing({ icon, ratio, level, label, valueText, hint }: GaugeRingProps) {
  // 占比夹到 [0,1]；null（不可算）按 0 画（全灰底），仅靠中心数值表达。
  const pct = ratio === null || !Number.isFinite(ratio) ? 0 : Math.min(Math.max(ratio, 0), 1)
  const deg = Math.round(pct * 360)
  const ringColor = levelRing(level)
  // conic-gradient：已占比段用等级色，余段用中性轨道色；内圈用 background 挖空成环。
  const ringStyle = {
    background: `conic-gradient(${ringColor} ${deg}deg, var(--muted) ${deg}deg 360deg)`,
  }

  return (
    <div className="flex flex-col items-center gap-2 text-center">
      <div
        className="relative grid place-items-center rounded-full"
        style={{ width: SIZE, height: SIZE, ...ringStyle }}
        role="img"
        aria-label={`${label} ${valueText}`}
      >
        {/* 内圈挖空：用卡片背景色盖出环宽 */}
        <div
          className="grid place-items-center rounded-full bg-card"
          style={{ width: SIZE - STROKE * 2, height: SIZE - STROKE * 2 }}
        >
          <div className={cn('flex flex-col items-center leading-none', levelText(level))}>
            <span aria-hidden>{icon}</span>
          </div>
        </div>
      </div>
      <div className="space-y-0.5">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className={cn('text-base font-semibold tabular-nums', levelText(level))}>{valueText}</div>
        {hint && <div className="text-[11px] text-muted-foreground">{hint}</div>}
      </div>
    </div>
  )
}
