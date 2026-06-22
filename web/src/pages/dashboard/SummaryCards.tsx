// 总览卡片组：把当前快照聚合（总人数 / 在线服务器 / 平均 TPS / 平均内存 / 平均 CPU）排成卡片。
// 只展示负载数字，不含任何玩家名单 / 身份。

import { useTranslation } from 'react-i18next'
import { Activity, Cpu, MemoryStick, Server, Users } from 'lucide-react'
import { formatBytes } from '../../api/format'
import type { MetricsSummary } from '../../api/client'
import StatCard from './StatCard'

interface SummaryCardsProps {
  // 当前快照聚合
  summary: MetricsSummary
}

export default function SummaryCards({ summary }: SummaryCardsProps) {
  const { t } = useTranslation()
  // avgCpuLoad < 0（约定 -1）表示无可用 CPU 样本，展示「不可用」而非负数
  const cpuAvailable = summary.avgCpuLoad >= 0
  const cpuText = cpuAvailable ? `${(summary.avgCpuLoad * 100).toFixed(0)}%` : t('dashboard.cpuUnavailable')

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <StatCard
        label={t('dashboard.cardTotalPlayers')}
        value={summary.totalPlayers}
        icon={<Users className="size-4" />}
      />
      <StatCard
        label={t('dashboard.cardOnlineServers')}
        value={summary.onlineServers}
        icon={<Server className="size-4" />}
      />
      <StatCard
        label={t('dashboard.cardAvgTps')}
        value={summary.avgTps.toFixed(1)}
        icon={<Activity className="size-4" />}
      />
      <StatCard
        label={t('dashboard.cardAvgMem')}
        value={formatBytes(summary.avgMemUsed)}
        hint={t('dashboard.cardMemMax', { max: formatBytes(summary.avgMemMax) })}
        icon={<MemoryStick className="size-4" />}
      />
      <StatCard
        label={t('dashboard.cardAvgCpu')}
        value={cpuText}
        hint={cpuAvailable ? t('dashboard.cardCpuSamples', { count: summary.cpuSampleCount }) : t('dashboard.cardCpuNoSample')}
        icon={<Cpu className="size-4" />}
      />
    </div>
  )
}
