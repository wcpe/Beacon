// BC 代理面板（FR-34）：把 bc（bungee 代理）维度聚合排成卡片——代理数 / 连接数 / 平均线程 /
// 后端可达性 / 平均后端延迟。仅展示负载数字（健康事实），不含任何玩家名单 / 身份。
// 边界与角色分流：本面板只读 summary.bc（仅 role=bungee 聚合），bukkit 视图（总览 / 趋势 / 每服明细）不受影响。

import { Cpu, Network, Plug, Server, Timer } from 'lucide-react'
import StatCard from './StatCard'
import type { BCSummary } from '../../api/client'

interface BCPanelProps {
  // bc 维度聚合
  bc: BCSummary
}

export default function BCPanel({ bc }: BCPanelProps) {
  // 平均后端延迟 < 0（约定 -1）表示无可用后端样本，展示「不可用」而非负数
  const latencyAvailable = bc.avgBackendLatencyMs >= 0
  const latencyText = latencyAvailable ? `${bc.avgBackendLatencyMs.toFixed(0)} ms` : '不可用'

  // 后端可达率文案：有配置后端时显示 up/total + 百分比，无后端显示「无后端」
  const reachText =
    bc.backendTotal > 0
      ? `${bc.backendUp} / ${bc.backendTotal}`
      : '无后端'
  const reachHint =
    bc.backendTotal > 0
      ? `${Math.round((bc.backendUp / bc.backendTotal) * 100)}% 可达`
      : '该代理未配置后端'

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <StatCard
        label="在线 BC 代理数"
        value={bc.proxyCount}
        icon={<Server className="size-4" />}
      />
      <StatCard
        label="代理总连接数"
        value={bc.totalConnections}
        icon={<Plug className="size-4" />}
      />
      <StatCard
        label="平均线程数"
        value={bc.avgThreadCount.toFixed(0)}
        icon={<Cpu className="size-4" />}
      />
      <StatCard
        label="后端可达性"
        value={reachText}
        hint={reachHint}
        icon={<Network className="size-4" />}
      />
      <StatCard
        label="平均后端延迟"
        value={latencyText}
        hint={latencyAvailable ? '到可达后端的 ping RTT' : '无可达后端样本'}
        icon={<Timer className="size-4" />}
      />
    </div>
  )
}
