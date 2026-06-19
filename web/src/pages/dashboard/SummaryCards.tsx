// 总览卡片组：把当前快照聚合（总人数 / 在线服务器 / 平均 TPS / 平均内存 / 平均 CPU）排成卡片。
// 只展示负载数字，不含任何玩家名单 / 身份。

import type { ReactNode } from 'react'
import { Activity, Cpu, MemoryStick, Server, Users } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { formatBytes } from '../../api/format'
import type { MetricsSummary } from '../../api/client'

// 单张统计卡片
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

function StatCard({ label, value, hint, icon }: StatCardProps) {
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

interface SummaryCardsProps {
  // 当前快照聚合
  summary: MetricsSummary
}

export default function SummaryCards({ summary }: SummaryCardsProps) {
  // avgCpuLoad < 0（约定 -1）表示无可用 CPU 样本，展示「不可用」而非负数
  const cpuAvailable = summary.avgCpuLoad >= 0
  const cpuText = cpuAvailable ? `${(summary.avgCpuLoad * 100).toFixed(0)}%` : '不可用'

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-5">
      <StatCard
        label="总在线玩家数"
        value={summary.totalPlayers}
        icon={<Users className="size-5" />}
      />
      <StatCard
        label="在线服务器数"
        value={summary.onlineServers}
        icon={<Server className="size-5" />}
      />
      <StatCard
        label="平均 TPS"
        value={summary.avgTps.toFixed(1)}
        icon={<Activity className="size-5" />}
      />
      <StatCard
        label="平均内存"
        value={formatBytes(summary.avgMemUsed)}
        hint={`最大 ${formatBytes(summary.avgMemMax)}`}
        icon={<MemoryStick className="size-5" />}
      />
      <StatCard
        label="平均 CPU 负载"
        value={cpuText}
        hint={cpuAvailable ? `${summary.cpuSampleCount} 个有效样本` : '无可用 CPU 样本'}
        icon={<Cpu className="size-5" />}
      />
    </div>
  )
}
