// NOC 大屏只读看板（FR-92）：状态墙 + 一条大趋势，暗底高对比、远观可读。
// 结构：① 顶部集群总览（巨号 在线/亚健康/失联 + 巨号 玩家/均TPS）；② 服务器状态墙（放大版 StatusTile，
// 健康色明显，远看哪台红/黄一目了然）；③ 底部一条大趋势（在线玩家，大图）。
// 纯只读——不含任何操作入口（无下线 / drain / 改派 / 编辑 / 筛选），守 FR-92 大屏只读边界；
// 固定深色 NOC（本页强制 dark 视觉，不跟随主题）。复用现有只读查询，零新增后端端点；聚合全部环境。

import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { CircleCheck, CircleX, TriangleAlert, Users, Zap } from 'lucide-react'
import { listInstances, metricsSummary, metricsTrend } from '../api/client'
import TrendChart from './dashboard/TrendChart'
import StatusTile from '@/components/dashboard/StatusTile'
import AsyncSection from '@/components/AsyncSection'

// 大屏快照刷新周期（毫秒）：与看板一致，短周期实时反映当前负载
const WALLBOARD_REFETCH_MS = 5000

// 大屏顶部一格巨号指标：图标 + 巨号数值 + 标签（远观可读）。
function BigStat({
  icon,
  value,
  label,
  className,
}: {
  icon: React.ReactNode
  value: React.ReactNode
  label: string
  className?: string
}) {
  return (
    <div className="flex flex-col items-center gap-1 rounded-xl bg-white/5 px-6 py-5 ring-1 ring-white/10">
      <span aria-hidden className={className ?? 'text-slate-300'}>
        {icon}
      </span>
      <div className={`text-5xl font-bold tabular-nums ${className ?? 'text-slate-100'}`}>{value}</div>
      <div className="text-sm text-slate-400">{label}</div>
    </div>
  )
}

export default function WallboardPage() {
  const { t } = useTranslation()

  // 聚合全部环境（大屏不做筛选），与看板复用同一只读查询。
  const summaryQuery = useQuery({
    queryKey: ['metrics-summary', ''],
    queryFn: () => metricsSummary(undefined),
    refetchInterval: WALLBOARD_REFETCH_MS,
  })

  // 健康分布 + 状态墙：在册实例既按 status 前端计数，也逐台渲染放大瓷砖。
  const instancesQuery = useQuery({
    queryKey: ['instances', 'wallboard-health'],
    queryFn: () => listInstances({}),
    refetchInterval: WALLBOARD_REFETCH_MS,
  })

  // 底部大趋势：在线玩家近 1 小时走向（大屏固定 1h，复用只读趋势查询，零新端点）。
  const trendQuery = useQuery({
    queryKey: ['metrics-trend', '', '1h'],
    queryFn: () => metricsTrend({ window: '1h' }),
    refetchInterval: WALLBOARD_REFETCH_MS,
  })

  const summary = summaryQuery.data

  // 健康分布计数：在册实例按 status 计数。
  const instances = instancesQuery.data ?? []
  const healthOnline = instances.filter((i) => i.status === 'online').length
  const healthDegraded = instances.filter((i) => i.status === 'degraded').length
  const healthLost = instances.filter((i) => i.status === 'lost').length

  const points = trendQuery.data?.points ?? []

  return (
    // 固定深色 NOC 容器：撑满大屏、暗底高对比；本页视觉强制 dark，不跟随全局主题。
    <div className="min-h-full space-y-6 rounded-xl bg-slate-950 p-6 text-slate-100">
      {/* ① 顶部集群总览：健康三态 + 玩家 / 均TPS 巨号 */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
        <BigStat
          icon={<CircleCheck className="size-7" />}
          value={healthOnline}
          label={t('dashboard.healthOnlineLabel')}
          className="text-green-400"
        />
        <BigStat
          icon={<TriangleAlert className="size-7" />}
          value={healthDegraded}
          label={t('dashboard.healthDegradedLabel')}
          className="text-amber-400"
        />
        <BigStat
          icon={<CircleX className="size-7" />}
          value={healthLost}
          label={t('dashboard.healthLostLabel')}
          className="text-red-400"
        />
        <BigStat
          icon={<Users className="size-7" />}
          value={summary?.totalPlayers ?? '-'}
          label={t('dashboard.cardTotalPlayers')}
        />
        <BigStat
          icon={<Zap className="size-7" />}
          value={summary ? summary.avgTps.toFixed(1) : '-'}
          label={t('dashboard.cardAvgTps')}
        />
      </div>

      {/* ② 服务器状态墙：放大版瓷砖，健康色明显，远看哪台红/黄一目了然 */}
      <section className="space-y-3">
        <h2 className="text-2xl font-semibold text-slate-200">{t('dashboard.statusWallTitle')}</h2>
        <AsyncSection
          isLoading={instancesQuery.isLoading}
          isError={instancesQuery.isError}
          error={instancesQuery.error}
        >
          {instances.length === 0 ? (
            <div className="rounded-lg bg-white/5 p-6 text-slate-400">{t('dashboard.statusWallEmpty')}</div>
          ) : (
            // 暗底下瓷砖用深色变体：覆盖 StatusTile 的卡片底/描边以贴合 NOC 暗底（[&_…] 任意值选择器，不改组件）。
            <div className="grid grid-cols-[repeat(auto-fill,minmax(15rem,1fr))] gap-3 [&_[class*='bg-card']]:bg-white/5 [&_[class*='ring-foreground']]:ring-white/10">
              {instances.map((inst) => (
                <StatusTile key={`${inst.namespace}/${inst.serverId}`} instance={inst} large />
              ))}
            </div>
          )}
        </AsyncSection>
      </section>

      {/* ③ 底部一条大趋势：在线玩家近 1 小时（大图，远观走向） */}
      <section className="space-y-3">
        <h2 className="text-2xl font-semibold text-slate-200">{t('dashboard.chartPlayers')}</h2>
        <div className="rounded-lg bg-white/5 p-4 ring-1 ring-white/10">
          <AsyncSection isLoading={trendQuery.isLoading} isError={trendQuery.isError} error={trendQuery.error}>
            <div className="h-64">
              <TrendChart
                title={t('dashboard.chartPlayers')}
                icon={<Users className="size-5" />}
                points={points}
                metric="totalPlayers"
                color="var(--chart-1)"
                formatValue={(v) => String(Math.round(v))}
              />
            </div>
          </AsyncSection>
        </div>
      </section>
    </div>
  )
}
