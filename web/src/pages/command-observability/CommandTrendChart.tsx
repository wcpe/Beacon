// 命令量每日趋势折线图（recharts，FR-104）：把每日命令量序列渲染成三条折线——下发 / 完成 / 失败。
// 只画聚合计数（控制命令生命周期），不涉及任何文件内容 / 玩家名单。日期按 UTC 日聚合（后端约定）。

import { useTranslation } from 'react-i18next'
import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import type { CommandDayCount } from '../../api/types'

interface CommandTrendChartProps {
  // 每日命令量序列（升序按 date）
  points: ReadonlyArray<CommandDayCount>
}

export default function CommandTrendChart({ points }: CommandTrendChartProps) {
  const { t } = useTranslation()
  if (points.length === 0) {
    return (
      <div className="flex h-48 items-center justify-center text-sm text-muted-foreground">
        {t('commandObs.trendEmpty')}
      </div>
    )
  }
  // 三条折线的图例 / tooltip 中文文案（与 dataKey 对应）
  const labels: Record<string, string> = {
    issued: t('commandObs.legendIssued'),
    done: t('commandObs.legendDone'),
    failed: t('commandObs.legendFailed'),
  }
  return (
    <ResponsiveContainer width="100%" height={240}>
      <LineChart data={points as CommandDayCount[]} margin={{ top: 8, right: 16, bottom: 0, left: 8 }}>
        <CartesianGrid strokeDasharray="3 3" className="stroke-border" />
        <XAxis dataKey="date" tick={{ fontSize: 11 }} minTickGap={24} />
        <YAxis allowDecimals={false} tick={{ fontSize: 11 }} width={40} />
        <Tooltip formatter={(value, name) => [String(value), labels[String(name)] ?? String(name)]} />
        <Legend formatter={(value) => labels[String(value)] ?? String(value)} />
        {/* 下发=蓝 / 完成=绿 / 失败=红，与健康色语义一致 */}
        <Line type="monotone" dataKey="issued" stroke="#2563eb" strokeWidth={2} dot={false} isAnimationActive={false} />
        <Line type="monotone" dataKey="done" stroke="#16a34a" strokeWidth={2} dot={false} isAnimationActive={false} />
        <Line type="monotone" dataKey="failed" stroke="#dc2626" strokeWidth={2} dot={false} isAnimationActive={false} />
      </LineChart>
    </ResponsiveContainer>
  )
}
