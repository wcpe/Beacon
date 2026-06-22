// BC 代理面板（FR-34）：把 bc（bungee 代理）维度聚合排成卡片——代理数 / 连接数 / 平均线程 /
// 后端可达性 / 平均后端延迟。仅展示负载数字（健康事实），不含任何玩家名单 / 身份。
// 边界与角色分流：本面板只读 summary.bc（仅 role=bungee 聚合），bukkit 视图（总览 / 趋势 / 每服明细）不受影响。

import { useTranslation } from 'react-i18next'
import { Cpu, Network, Plug, Server, Timer } from 'lucide-react'
import StatCard from './StatCard'
import type { BCSummary } from '../../api/client'

interface BCPanelProps {
  // bc 维度聚合
  bc: BCSummary
}

export default function BCPanel({ bc }: BCPanelProps) {
  const { t } = useTranslation()
  // 平均后端延迟 < 0（约定 -1）表示无可用后端样本，展示「不可用」而非负数
  const latencyAvailable = bc.avgBackendLatencyMs >= 0
  const latencyText = latencyAvailable ? `${bc.avgBackendLatencyMs.toFixed(0)} ms` : t('dashboard.bcUnavailable')

  // 后端可达率文案：有配置后端时显示 up/total + 百分比，无后端显示「无后端」
  const reachText =
    bc.backendTotal > 0
      ? `${bc.backendUp} / ${bc.backendTotal}`
      : t('dashboard.bcNoBackend')
  const reachHint =
    bc.backendTotal > 0
      ? t('dashboard.bcReachPercent', { percent: Math.round((bc.backendUp / bc.backendTotal) * 100) })
      : t('dashboard.bcNoBackendConfigured')

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <StatCard
        label={t('dashboard.bcProxyCount')}
        value={bc.proxyCount}
        icon={<Server className="size-4" />}
      />
      <StatCard
        label={t('dashboard.bcTotalConnections')}
        value={bc.totalConnections}
        icon={<Plug className="size-4" />}
      />
      <StatCard
        label={t('dashboard.bcAvgThread')}
        value={bc.avgThreadCount.toFixed(0)}
        icon={<Cpu className="size-4" />}
      />
      <StatCard
        label={t('dashboard.bcBackendReach')}
        value={reachText}
        hint={reachHint}
        icon={<Network className="size-4" />}
      />
      <StatCard
        label={t('dashboard.bcAvgLatency')}
        value={latencyText}
        hint={latencyAvailable ? t('dashboard.bcPingHint') : t('dashboard.bcNoReachableSample')}
        icon={<Timer className="size-4" />}
      />
    </div>
  )
}
