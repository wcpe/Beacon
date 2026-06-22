// 总览卡片组：把当前快照聚合（总人数 / 在线服务器 / 平均 TPS / 平均内存 / 平均 CPU）排成卡片。
// 只展示负载数字，不含任何玩家名单 / 身份。

import { Activity, Cpu, MemoryStick, Server, Users } from 'lucide-react'
import { formatBytes } from '../../api/format'
import type { MetricsSummary } from '../../api/client'
import StatCard from './StatCard'

interface SummaryCardsProps {
  // 当前快照聚合
  summary: MetricsSummary
}

export default function SummaryCards({ summary }: SummaryCardsProps) {
  // avgCpuLoad < 0（约定 -1）表示无可用 CPU 样本，展示「不可用」而非负数
  const cpuAvailable = summary.avgCpuLoad >= 0
  const cpuText = cpuAvailable ? `${(summary.avgCpuLoad * 100).toFixed(0)}%` : '不可用'

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <StatCard
        label="总在线玩家数"
        value={summary.totalPlayers}
        icon={<Users className="size-4" />}
      />
      <StatCard
        label="在线服务器数"
        value={summary.onlineServers}
        icon={<Server className="size-4" />}
      />
      <StatCard
        label="平均 TPS"
        value={summary.avgTps.toFixed(1)}
        icon={<Activity className="size-4" />}
      />
      <StatCard
        label="平均内存"
        value={formatBytes(summary.avgMemUsed)}
        hint={`最大 ${formatBytes(summary.avgMemMax)}`}
        icon={<MemoryStick className="size-4" />}
      />
      <StatCard
        label="平均 CPU 负载"
        value={cpuText}
        hint={cpuAvailable ? `${summary.cpuSampleCount} 个有效样本` : '无可用 CPU 样本'}
        icon={<Cpu className="size-4" />}
      />
    </div>
  )
}
