// 带图标的 KPI（IconStat）：图标 + 标签 + 大数值（可选副说明），替掉裸数字卡。
// 比 StatCard 更轻（无 Card 外壳），用于分角色面板内的紧凑 KPI 组，多个并排成行。
// 纯展示组件；数值由父组件格式化好传入。可选健康等级给数值上色。

import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { levelText, type HealthLevel } from './health'

interface IconStatProps {
  // 左侧图标（lucide）
  icon: ReactNode
  // 指标标签
  label: string
  // 主数值（已格式化文案）
  value: ReactNode
  // 副说明（可选，如 used/max、样本数）
  hint?: ReactNode
  // 可选健康等级：传入时给主数值上健康色（默认前景色）。
  level?: HealthLevel
}

export default function IconStat({ icon, label, value, hint, level }: IconStatProps) {
  return (
    <div className="flex items-start gap-2.5">
      <div className="mt-0.5 shrink-0 text-muted-foreground">{icon}</div>
      <div className="min-w-0">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className={cn('mt-0.5 text-lg font-semibold tabular-nums', level && levelText(level))}>{value}</div>
        {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
      </div>
    </div>
  )
}
