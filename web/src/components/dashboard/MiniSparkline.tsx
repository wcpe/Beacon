// 迷你趋势线（MiniSparkline）：把一组数值序列画成无坐标轴的小折线，嵌在分角色面板里看趋势走向。
// 复用项目现有图表库 recharts（不引新库），去掉轴 / 网格 / tooltip，仅留一条线。
// 只画聚合数字（人数 / TPS 等），不涉及任何玩家名单 / 身份。

import { Line, LineChart, ResponsiveContainer, YAxis } from 'recharts'

interface MiniSparklineProps {
  // 数值序列（升序时间）；null 表示该点无样本（断线，不参与尺度）。
  values: ReadonlyArray<number | null>
  // 线色（CSS 颜色值，建议传语义色 token 如 var(--primary)）
  color: string
  // 高度（px），默认 32
  height?: number
}

export default function MiniSparkline({ values, color, height = 32 }: MiniSparklineProps) {
  // recharts 需要对象数组；包成 { i, v } 点。
  const data = values.map((v, i) => ({ i, v }))
  // 全空（无任何有效点）时不画，交由父组件呈现空态。
  const hasData = values.some((v) => v !== null && Number.isFinite(v))
  if (!hasData) return null

  return (
    <ResponsiveContainer width="100%" height={height}>
      <LineChart data={data} margin={{ top: 2, right: 2, bottom: 2, left: 2 }}>
        {/* 自适应数据域、隐藏轴：仅借 YAxis 做尺度，不渲染刻度。 */}
        <YAxis hide domain={['dataMin', 'dataMax']} />
        <Line
          type="monotone"
          dataKey="v"
          stroke={color}
          strokeWidth={1.5}
          dot={false}
          isAnimationActive={false}
          connectNulls={false}
        />
      </LineChart>
    </ResponsiveContainer>
  )
}
