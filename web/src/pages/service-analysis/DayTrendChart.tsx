// 每日趋势折线图（recharts，FR-73）：把每日操作数序列渲染成单条折线。
// 只画聚合计数（运维操作审计），不涉及任何玩家名单 / 身份。日期按 UTC 日聚合（后端约定）。

import { useTranslation } from 'react-i18next'
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import type { AuditDayCount } from '../../api/types'

interface DayTrendChartProps {
  // 每日操作数序列（升序按 date）
  points: ReadonlyArray<AuditDayCount>
}

export default function DayTrendChart({ points }: DayTrendChartProps) {
  const { t } = useTranslation()
  if (points.length === 0) {
    return (
      <div className="flex h-48 items-center justify-center text-sm text-muted-foreground">
        {t('serviceAnalysis.empty')}
      </div>
    )
  }
  return (
    <ResponsiveContainer width="100%" height={240}>
      <LineChart data={points as AuditDayCount[]} margin={{ top: 8, right: 16, bottom: 0, left: 8 }}>
        <CartesianGrid strokeDasharray="3 3" className="stroke-border" />
        <XAxis dataKey="date" tick={{ fontSize: 11 }} minTickGap={24} />
        <YAxis allowDecimals={false} tick={{ fontSize: 11 }} width={40} />
        <Tooltip formatter={(value) => [String(value), t('serviceAnalysis.byDayTitle')]} />
        <Line
          type="monotone"
          dataKey="count"
          stroke="#2563eb"
          strokeWidth={2}
          dot={false}
          isAnimationActive={false}
        />
      </LineChart>
    </ResponsiveContainer>
  )
}
