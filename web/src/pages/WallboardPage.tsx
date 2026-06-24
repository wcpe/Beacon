// NOC 大屏只读看板（FR-92）：复用可观测看板的只读数据与卡片组件，以大字号 / 大间距全屏呈现。
// 纯只读——不含任何操作入口（无下线 / drain / 改派 / 编辑 / 筛选），只消费现有只读查询，零新增后端端点。
// 聚合全部环境（不带 namespace 参数），短周期轮询以适配值班墙实时刷新。

import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Activity, HeartPulse, Server } from 'lucide-react'
import { listInstances, metricsSummary } from '../api/client'
import SummaryCards from './dashboard/SummaryCards'
import BCPanel from './dashboard/BCPanel'
import StatCard from './dashboard/StatCard'
import AsyncSection from '@/components/AsyncSection'

// 大屏快照刷新周期（毫秒）：与看板一致，短周期实时反映当前负载
const WALLBOARD_REFETCH_MS = 5000

// 角色编码（与后端 metric_aggregate role 约定一致）：bungee 进 BC 计数，其余进子服计数。
const ROLE_BUNGEE = 'bungee'

export default function WallboardPage() {
  const { t } = useTranslation()

  // 聚合全部环境（大屏不做筛选），与看板复用同一只读查询。
  const summaryQuery = useQuery({
    queryKey: ['metrics-summary', ''],
    queryFn: () => metricsSummary(undefined),
    refetchInterval: WALLBOARD_REFETCH_MS,
  })

  // 健康分布：在册实例按 status 前端计数（online/lost/offline）。
  const instancesQuery = useQuery({
    queryKey: ['instances', 'wallboard-health'],
    queryFn: () => listInstances({}),
    refetchInterval: WALLBOARD_REFETCH_MS,
  })

  // 在线分角色计数：按 metricsSummary.servers 的 role 分桶（bungee 进 BC，其余进子服）。
  const allServers = summaryQuery.data?.servers ?? []
  const bukkitCount = allServers.filter((s) => s.role !== ROLE_BUNGEE).length
  const bungeeCount = allServers.filter((s) => s.role === ROLE_BUNGEE).length
  const onlineServers = summaryQuery.data?.onlineServers ?? 0

  // 健康分布计数：在册实例按 status 计数。
  const instances = instancesQuery.data ?? []
  const healthOnline = instances.filter((i) => i.status === 'online').length
  const healthLost = instances.filter((i) => i.status === 'lost').length
  const healthOffline = instances.filter((i) => i.status === 'offline').length

  return (
    <div className="space-y-8">
      {/* 顶部三联粗况：在线角色 / 在线服务器 / 健康分布 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <StatCard
          label={t('dashboard.roleSummaryTitle')}
          value={t('dashboard.roleSummaryBukkit', { count: bukkitCount })}
          hint={t('dashboard.roleSummaryBungee', { count: bungeeCount })}
          icon={<Server className="size-5" />}
        />
        <StatCard
          label={t('dashboard.onlineServersTitle')}
          value={onlineServers}
          hint={t('dashboard.roleSummaryOnline', { count: bukkitCount + bungeeCount })}
          icon={<Activity className="size-5" />}
        />
        <StatCard
          label={t('dashboard.healthTitle')}
          value={t('dashboard.healthOnline', { count: healthOnline })}
          hint={`${t('dashboard.healthLost', { count: healthLost })} · ${t('dashboard.healthOffline', { count: healthOffline })}`}
          icon={<HeartPulse className="size-5" />}
        />
      </div>

      {/* 子服（bukkit）总览卡片 */}
      <section className="space-y-4">
        <h2 className="text-2xl font-semibold">{t('dashboard.sectionBukkit')}</h2>
        <AsyncSection
          isLoading={summaryQuery.isLoading}
          isError={summaryQuery.isError}
          error={summaryQuery.error}
        >
          {summaryQuery.data && <SummaryCards summary={summaryQuery.data} />}
        </AsyncSection>
      </section>

      {/* BC 代理（bungee）总览卡片 */}
      <section className="space-y-4">
        <h2 className="text-2xl font-semibold">{t('dashboard.sectionBc')}</h2>
        <AsyncSection
          isLoading={summaryQuery.isLoading}
          isError={summaryQuery.isError}
          error={summaryQuery.error}
        >
          {summaryQuery.data && <BCPanel bc={summaryQuery.data.bc} />}
        </AsyncSection>
      </section>
    </div>
  )
}
