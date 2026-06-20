// 单张统计卡片：总览卡片组与 BC 代理面板共用，避免重复骨架（仅展示负载数字，不含名单）。

import type { ReactNode } from 'react'
import { Card, CardContent } from '@/components/ui/card'

interface StatCardProps {
  // 卡片标题（指标名）
  label: string
  // 主数值（已格式化文案）
  value: ReactNode
  // 辅助说明（如 used/max、样本数）
  hint?: ReactNode
  // 左侧图标
  icon: ReactNode
}

export default function StatCard({ label, value, hint, icon }: StatCardProps) {
  return (
    <Card>
      <CardContent className="flex items-start gap-3">
        <div className="mt-0.5 text-muted-foreground">{icon}</div>
        <div className="min-w-0">
          <div className="text-xs text-muted-foreground">{label}</div>
          <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
          {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
        </div>
      </CardContent>
    </Card>
  )
}
