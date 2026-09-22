// 运维总览页（/dashboard，FR-154，E1 布局优化后形态）：一屏看全局——KPI 指标带 + 服务器状态墙 +
// 玩家流/连接流趋势 + 告警概览 + 调度概览，只看不改。各卡异常可下钻到对应页。
// 健康与调度概览已接真（/admin/v2 metrics·health·sched-decisions）；连接流端点随后续阶段
// 提供（卡内降级占位），告警卡消费既有 /admin/v1/alert-events。
import AlertOverview from './dashboard/alert-overview'
import FlowOverview from './dashboard/flow-overview'
import HealthOverview from './dashboard/health-overview'
import SchedOverview from './dashboard/sched-overview'
import ServerWall from './dashboard/server-wall'

export default function DashboardPage() {
  return (
    <div className="grid gap-3">
      {/* 顶部 KPI 指标带（E5：无区段标题，右上保留下钻链接；健康分布环在 KpiStrip 内） */}
      <HealthOverview />
      {/* 中区：服务器状态墙（宽）+ 玩家流 / 连接流趋势 */}
      <div className="grid gap-3 xl:grid-cols-[1.7fr_1fr]">
        <ServerWall />
        <FlowOverview />
      </div>
      {/* 底区：告警概览 + 调度概览 */}
      <div className="grid gap-3 xl:grid-cols-2">
        <AlertOverview />
        <SchedOverview />
      </div>
    </div>
  )
}
