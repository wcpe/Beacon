// 运维总览页（/dashboard，FR-154，对齐 B 版明亮 SaaS）：一屏看全局——KPI 指标带 + 服务器状态墙 +
// 玩家流/连接流趋势 + 告警概览 + 调度概览，只看不改。各卡异常可下钻到对应页。
// 数据仍取自现有 mock 端点（metrics / connections / observability），只重塑呈现。
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'

import AlertOverview from './dashboard/alert-overview'
import FlowOverview from './dashboard/flow-overview'
import HealthOverview from './dashboard/health-overview'
import SchedOverview from './dashboard/sched-overview'
import ServerWall from './dashboard/server-wall'

export default function DashboardPage() {
  const { t } = useTranslation()
  return (
    <div className="grid gap-3.5">
      {/* 页标题（纯标题，非面包屑，与其余页面一致） */}
      <SectionHeader size="lg" title={t('nav.dashboard')} />
      {/* 顶部 KPI 指标带（含区段标题与健康分布环） */}
      <HealthOverview />
      {/* 中区：服务器状态墙（宽）+ 玩家流 / 连接流趋势 */}
      <div className="grid gap-3.5 xl:grid-cols-[1.7fr_1fr]">
        <ServerWall />
        <FlowOverview />
      </div>
      {/* 底区：告警概览 + 调度概览 */}
      <div className="grid gap-3.5 xl:grid-cols-2">
        <AlertOverview />
        <SchedOverview />
      </div>
    </div>
  )
}
