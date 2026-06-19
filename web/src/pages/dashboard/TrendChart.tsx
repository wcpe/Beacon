// 趋势折线图（recharts）：把一组时间序列点按某个指标渲染成单条折线。
// 只画聚合数字（人数 / TPS / 内存 / CPU），不涉及任何玩家名单 / 身份。

import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import type { TrendPoint } from '../../api/client'

// 单图所选指标：从趋势点取数值的字段
type MetricKey = 'totalPlayers' | 'avgTps' | 'avgMemUsed' | 'avgCpuLoad'

interface TrendChartProps {
  // 标题（图上方文案）
  title: string
  // 时间序列点（升序）
  points: TrendPoint[]
  // 取哪个指标画线
  metric: MetricKey
  // 折线颜色（CSS 颜色值）
  color: string
  // Y 轴 / tooltip 数值格式化（如字节人类可读、TPS 保留一位）
  formatValue: (v: number) => string
}

// 把 RFC3339 时间格式化为简短的本地「时:分」标签（X 轴用）
function shortTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

export default function TrendChart({ title, points, metric, color, formatValue }: TrendChartProps) {
  return (
    <div className="space-y-2">
      <div className="text-sm font-medium">{title}</div>
      {points.length === 0 ? (
        <div className="flex h-48 items-center justify-center text-sm text-muted-foreground">
          所选时间窗内暂无样本
        </div>
      ) : (
        <ResponsiveContainer width="100%" height={192}>
          <LineChart data={points} margin={{ top: 8, right: 16, bottom: 0, left: 8 }}>
            <CartesianGrid strokeDasharray="3 3" className="stroke-border" />
            <XAxis
              dataKey="sampledAt"
              tickFormatter={shortTime}
              tick={{ fontSize: 11 }}
              minTickGap={24}
            />
            <YAxis tickFormatter={formatValue} tick={{ fontSize: 11 }} width={56} />
            <Tooltip
              labelFormatter={(v) => shortTime(String(v))}
              formatter={(value) => [formatValue(Number(value)), title]}
            />
            <Line
              type="monotone"
              dataKey={metric}
              stroke={color}
              strokeWidth={2}
              dot={false}
              isAnimationActive={false}
            />
          </LineChart>
        </ResponsiveContainer>
      )}
    </div>
  )
}
