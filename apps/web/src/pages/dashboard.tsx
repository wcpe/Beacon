// 运维总览页（/dashboard，FR-154）：一屏看全局——健康 / 玩家流 / 告警 / 调度概览，只看不改。
// 各卡异常可下钻到对应页（/servers /service-analysis /alert-events）。1920x1080 一屏高密度双列。
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'

import AlertOverview from './dashboard/alert-overview'
import FlowOverview from './dashboard/flow-overview'
import HealthOverview from './dashboard/health-overview'
import SchedOverview from './dashboard/sched-overview'

export default function DashboardPage() {
  const { t } = useTranslation()
  return (
    <section className="grid gap-4">
      <SectionHeader size="lg" title={t('nav.dashboard')} />
      {/* 健康总览占满整行，其余三区两列平铺，宽屏一屏看全 */}
      <HealthOverview />
      <div className="grid gap-4 xl:grid-cols-2">
        <FlowOverview />
        <AlertOverview />
        <SchedOverview />
      </div>
    </section>
  )
}
