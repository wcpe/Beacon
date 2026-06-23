// 按动作分布水平柱状排行（recharts，FR-73）：各 action 计数降序排行。
// action 已由父组件经审计 i18n 映射为中文标签（label），本组件只画计数柱、不再翻译。

import { useTranslation } from 'react-i18next'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

// 单条排行项：中文动作名 + 计数（已降序）
export interface ActionRankItem {
  action: string
  label: string
  count: number
}

interface ActionRankChartProps {
  // 排行条目（降序按 count）
  items: ReadonlyArray<ActionRankItem>
}

export default function ActionRankChart({ items }: ActionRankChartProps) {
  const { t } = useTranslation()
  if (items.length === 0) {
    return (
      <div className="flex h-48 items-center justify-center text-sm text-muted-foreground">
        {t('serviceAnalysis.empty')}
      </div>
    )
  }
  // 高度随条目数自适应：每条约 32px，保证多动作时不挤压（最低 192px）。
  const height = Math.max(192, items.length * 32 + 24)
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart
        data={items as ActionRankItem[]}
        layout="vertical"
        margin={{ top: 8, right: 24, bottom: 0, left: 8 }}
      >
        <CartesianGrid strokeDasharray="3 3" horizontal={false} className="stroke-border" />
        <XAxis type="number" allowDecimals={false} tick={{ fontSize: 11 }} />
        <YAxis
          type="category"
          dataKey="label"
          tick={{ fontSize: 11 }}
          width={96}
          interval={0}
        />
        <Tooltip formatter={(value) => [String(value), t('serviceAnalysis.actionCountHint', { count: value })]} />
        <Bar dataKey="count" fill="#2563eb" isAnimationActive={false} radius={[0, 4, 4, 0]}>
          {items.map((it) => (
            <Cell key={it.action} />
          ))}
        </Bar>
      </BarChart>
    </ResponsiveContainer>
  )
}
