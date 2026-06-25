// 可观测看板页（FR-32 / FR-34 / FR-43 + 瘦身 FR-64）：状态墙 + 分角色趋势面板 + 时序监控图，密度提高、全程图标。
// 结构自上而下：① 集群状态总览条（分段健康条 + 计数 + 全局 KPI chips）；② 服务器状态墙（每台一块瓷砖，健康色编码，
// 一眼扫全集群）；③ 分角色面板（子服 / BC 各一面板：图标头 + 紧凑 IconStat 组 + 内嵌迷你趋势）；④ 时序监控图（历史趋势，
// 时间窗切换 + 四指标折线，图标标题 + hover tooltip 看某时间点数值）。环境筛选 + 一键清空（FR-63）保留。
// 边界：只展示负载数字（健康事实），绝不展示任何玩家名单 / 身份（后端也不返回）。

import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  Boxes,
  CircleCheck,
  CircleX,
  Cpu,
  Database,
  Gauge,
  LayoutGrid,
  MemoryStick,
  Network,
  Plug,
  Router,
  Server,
  Timer,
  TriangleAlert,
  Users,
  Zap,
} from 'lucide-react'
import { listInstances, listNamespaces, metricsSummary, metricsTrend } from '../api/client'
import type { TrendWindow } from '../api/client'
import { formatBytes, namespaceOptions } from '../api/format'
import TrendChart from './dashboard/TrendChart'
import IconStat from '@/components/dashboard/IconStat'
import StatusTile from '@/components/dashboard/StatusTile'
import HealthBar, { type HealthSegment } from '@/components/dashboard/HealthBar'
import MiniSparkline from '@/components/dashboard/MiniSparkline'
import { ratioLevel } from '@/components/dashboard/health'
import AsyncSection from '@/components/AsyncSection'
import { TileGridSkeleton, CardGridSkeleton } from '@/components/skeletons'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Combobox } from '@/components/ui/combobox'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

// 总览快照刷新周期（毫秒）：与服务器页一致，短周期反映当前负载
const SUMMARY_REFETCH_MS = 5000

// 可选时间窗（预设窗口）：label 经 i18n key 在渲染时解析
const WINDOWS: Array<{ value: TrendWindow; labelKey: string }> = [
  { value: '1h', labelKey: 'dashboard.win1h' },
  { value: '6h', labelKey: 'dashboard.win6h' },
  { value: '24h', labelKey: 'dashboard.win24h' },
]

// 全局 KPI chip：图标 + 大数值 + 标签，紧凑横排（替代低密度大数字卡）。
function KpiChip({ icon, value, label }: { icon: React.ReactNode; value: React.ReactNode; label: string }) {
  return (
    <div className="flex items-center gap-2 rounded-lg bg-muted/50 px-3 py-2">
      <span aria-hidden className="text-muted-foreground">
        {icon}
      </span>
      <div className="leading-tight">
        <div className="text-base font-semibold tabular-nums">{value}</div>
        <div className="text-[11px] text-muted-foreground">{label}</div>
      </div>
    </div>
  )
}

export default function DashboardPage() {
  const { t } = useTranslation()
  // 环境过滤（可编辑下拉，FR-51）：空表示聚合全部环境，进页默认即聚合全部。
  const [namespace, setNamespace] = useState('')
  const [window, setWindow] = useState<TrendWindow>('1h')

  // 环境下拉候选来自 listNamespaces；筛选框允许键入候选外的值（可编辑）。
  // 候选显示「编码 · 名称」，真实值仍是 code（FR-70）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const nsOptions = useMemo(() => namespaceOptions(namespacesQuery.data), [namespacesQuery.data])

  const summaryQuery = useQuery({
    queryKey: ['metrics-summary', namespace],
    queryFn: () => metricsSummary(namespace || undefined),
    refetchInterval: SUMMARY_REFETCH_MS,
  })

  const trendQuery = useQuery({
    queryKey: ['metrics-trend', namespace, window],
    queryFn: () => metricsTrend({ namespace: namespace || undefined, window }),
  })

  // 健康分布 + 状态墙（FR-64）：在册实例既用于按 status 前端计数，也用于逐台渲染状态墙瓷砖。
  const instancesQuery = useQuery({
    queryKey: ['instances', 'dashboard-health', namespace],
    queryFn: () => listInstances({ namespace: namespace || undefined }),
    refetchInterval: SUMMARY_REFETCH_MS,
  })

  const isFetching = summaryQuery.isFetching || trendQuery.isFetching
  const points = trendQuery.data?.points ?? []
  // CPU 折线专用点：把无样本哨兵（avgCpuLoad < 0，约定 -1）置为 null，
  // recharts 据此断线，避免 -1 被当数据点画成 -100% 污染 Y 轴尺度。
  const cpuPoints = points.map((p) => ({
    ...p,
    avgCpuLoad: p.avgCpuLoad < 0 ? null : p.avgCpuLoad,
  }))

  const summary = summaryQuery.data

  // 健康分布计数（FR-64）：在册实例按 status 计数。
  const instances = instancesQuery.data ?? []
  const healthOnline = instances.filter((i) => i.status === 'online').length
  const healthDegraded = instances.filter((i) => i.status === 'degraded').length
  const healthLost = instances.filter((i) => i.status === 'lost').length
  const healthOffline = instances.filter((i) => i.status === 'offline').length

  // 分段健康条：在线 / 亚健康 / 失联 / 离线（占比 = 计数 / 总数）。
  const healthSegments: HealthSegment[] = [
    { label: t('dashboard.healthOnlineLabel'), count: healthOnline, level: 'ok' },
    { label: t('dashboard.healthDegradedLabel'), count: healthDegraded, level: 'warn' },
    { label: t('dashboard.healthLostLabel'), count: healthLost, level: 'danger' },
    { label: t('dashboard.healthOfflineLabel'), count: healthOffline, level: 'muted' },
  ]

  // CPU / 内存可用性与等级（KPI chips 与面板共用）。
  const cpuAvailable = (summary?.avgCpuLoad ?? -1) >= 0
  const cpuText = cpuAvailable ? `${((summary?.avgCpuLoad ?? 0) * 100).toFixed(0)}%` : t('dashboard.cpuUnavailable')
  const memRatio = summary && summary.avgMemMax > 0 ? summary.avgMemUsed / summary.avgMemMax : null

  // 迷你趋势序列：从趋势点抽各指标数值序列喂 sparkline（CPU 哨兵已在 cpuPoints 置 null）。
  const playersSeries = points.map((p) => p.totalPlayers)
  const tpsSeries = points.map((p) => p.avgTps)
  const memSeries = points.map((p) => p.avgMemUsed)
  const cpuSeries = cpuPoints.map((p) => p.avgCpuLoad)

  // BC 面板数值（FR-34）：仅 role=bungee 聚合。
  const bc = summary?.bc
  const bcLatencyAvailable = (bc?.avgBackendLatencyMs ?? -1) >= 0
  const bcReachText = bc && bc.backendTotal > 0 ? `${bc.backendUp} / ${bc.backendTotal}` : t('dashboard.bcNoBackend')

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <h1 className="text-xl font-semibold">{t('dashboard.title')}</h1>
          {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
        </div>
        {/* 环境筛选：移到页眉右侧，紧凑不占整卡。可编辑下拉（FR-51）；留空聚合全部、可一键清空（FR-63）。 */}
        <div className="flex items-center gap-2">
          <Label htmlFor="d-namespace" className="text-sm text-muted-foreground">
            {t('common.namespace')}
          </Label>
          <Combobox
            id="d-namespace"
            aria-label={t('common.namespace')}
            className="w-44"
            placeholder={t('dashboard.nsPlaceholder')}
            value={namespace}
            onChange={setNamespace}
            options={nsOptions}
            allowCustom
            clearable
            clearLabel={t('dashboard.clearFilter')}
          />
        </div>
      </div>

      {/* ① 集群状态总览条：分段健康条 + 在线/亚健康/失联/离线计数 + 全局 KPI chips */}
      <Card>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
            <HealthBar segments={healthSegments} className="min-w-[10rem] flex-1" />
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
              <span className="flex items-center gap-1.5 text-green-700 dark:text-green-400">
                <CircleCheck aria-hidden className="size-4" />
                <span className="tabular-nums">{t('dashboard.healthOnline', { count: healthOnline })}</span>
              </span>
              <span className="flex items-center gap-1.5 text-amber-600 dark:text-amber-400">
                <TriangleAlert aria-hidden className="size-4" />
                <span className="tabular-nums">{t('dashboard.healthDegraded', { count: healthDegraded })}</span>
              </span>
              <span className="flex items-center gap-1.5 text-red-700 dark:text-red-400">
                <CircleX aria-hidden className="size-4" />
                <span className="tabular-nums">{t('dashboard.healthLost', { count: healthLost })}</span>
              </span>
              <span className="flex items-center gap-1.5 text-muted-foreground">
                <span className="tabular-nums">{t('dashboard.healthOffline', { count: healthOffline })}</span>
              </span>
            </div>
          </div>
          {/* 全局 KPI chips：玩家 / 均TPS / 均CPU / 均内存（带图标，紧凑横排） */}
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <KpiChip
              icon={<Users className="size-4" />}
              value={summary?.totalPlayers ?? '-'}
              label={t('dashboard.cardTotalPlayers')}
            />
            <KpiChip
              icon={<Zap className="size-4" />}
              value={summary ? summary.avgTps.toFixed(1) : '-'}
              label={t('dashboard.cardAvgTps')}
            />
            <KpiChip icon={<Cpu className="size-4" />} value={summary ? cpuText : '-'} label={t('dashboard.cardAvgCpu')} />
            <KpiChip
              icon={<Database className="size-4" />}
              value={summary ? formatBytes(summary.avgMemUsed) : '-'}
              label={t('dashboard.cardAvgMem')}
            />
          </div>
        </CardContent>
      </Card>

      {/* ② 服务器状态墙：每台在线服务器一块瓷砖，健康色编码 + 角色图标 + 关键指标，一眼扫全集群健康 */}
      <section className="space-y-2">
        <h2 className="flex items-center gap-2 text-lg font-semibold">
          <LayoutGrid aria-hidden className="size-5 text-muted-foreground" />
          {t('dashboard.statusWallTitle')}
        </h2>
        <AsyncSection
          isLoading={instancesQuery.isLoading}
          isError={instancesQuery.isError}
          error={instancesQuery.error}
          skeleton={<TileGridSkeleton />}
        >
          {instances.length === 0 ? (
            <Card>
              <CardContent className="text-sm text-muted-foreground">{t('dashboard.statusWallEmpty')}</CardContent>
            </Card>
          ) : (
            <div className="grid grid-cols-[repeat(auto-fill,minmax(11rem,1fr))] gap-2.5">
              {instances.map((inst) => (
                <StatusTile key={`${inst.namespace}/${inst.serverId}`} instance={inst} />
              ))}
            </div>
          )}
        </AsyncSection>
      </section>

      {/* ③ 分角色面板：子服 / BC 各一面板（图标头 + 紧凑 IconStat 组 + 内嵌迷你趋势） */}
      <AsyncSection
        isLoading={summaryQuery.isLoading}
        isError={summaryQuery.isError}
        error={summaryQuery.error}
        skeleton={<CardGridSkeleton count={2} heightClass="h-56" gridClass="grid grid-cols-1 gap-3 xl:grid-cols-2" />}
      >
        {summary && (
          <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
            {/* 子服（bukkit）面板：玩家 / 服务器数 / 均TPS / 均内存 / 均CPU + 玩家迷你趋势 */}
            <Card>
              <CardContent className="space-y-3">
                <h2 className="flex items-center gap-2 text-base font-semibold">
                  <Server aria-hidden className="size-4 text-muted-foreground" />
                  {t('dashboard.sectionBukkit')}
                </h2>
                <div className="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-3">
                  <IconStat
                    icon={<Users className="size-4" />}
                    label={t('dashboard.cardTotalPlayers')}
                    value={summary.totalPlayers}
                  />
                  <IconStat
                    icon={<Server className="size-4" />}
                    label={t('dashboard.cardOnlineServers')}
                    value={summary.onlineServers}
                  />
                  <IconStat
                    icon={<Activity className="size-4" />}
                    label={t('dashboard.cardAvgTps')}
                    value={summary.avgTps.toFixed(1)}
                  />
                  <IconStat
                    icon={<MemoryStick className="size-4" />}
                    label={t('dashboard.cardAvgMem')}
                    value={formatBytes(summary.avgMemUsed)}
                    hint={t('dashboard.cardMemMax', { max: formatBytes(summary.avgMemMax) })}
                    level={memRatio === null ? undefined : ratioLevel(memRatio)}
                  />
                  <IconStat
                    icon={<Cpu className="size-4" />}
                    label={t('dashboard.cardAvgCpu')}
                    value={cpuText}
                    hint={
                      cpuAvailable
                        ? t('dashboard.cardCpuSamples', { count: summary.cpuSampleCount })
                        : t('dashboard.cardCpuNoSample')
                    }
                    level={cpuAvailable ? ratioLevel(summary.avgCpuLoad) : undefined}
                  />
                </div>
                {/* 内嵌迷你趋势：在线玩家近况走向（复用趋势数据，靛蓝色 token） */}
                <div className="space-y-1 border-t pt-2">
                  <div className="text-[11px] text-muted-foreground">{t('dashboard.sparklinePlayers')}</div>
                  <MiniSparkline values={playersSeries} color="var(--chart-1)" />
                </div>
              </CardContent>
            </Card>

            {/* BC 代理（bungee）面板：代理数 / 连接 / 线程 / 后端可达 / 延迟 + 连接迷你趋势 */}
            <Card>
              <CardContent className="space-y-3">
                <h2 className="flex items-center gap-2 text-base font-semibold">
                  <Router aria-hidden className="size-4 text-muted-foreground" />
                  {t('dashboard.sectionBc')}
                </h2>
                {bc && (
                  <div className="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-3">
                    <IconStat
                      icon={<Server className="size-4" />}
                      label={t('dashboard.bcProxyCount')}
                      value={bc.proxyCount}
                    />
                    <IconStat
                      icon={<Plug className="size-4" />}
                      label={t('dashboard.bcTotalConnections')}
                      value={bc.totalConnections}
                    />
                    <IconStat
                      icon={<Cpu className="size-4" />}
                      label={t('dashboard.bcAvgThread')}
                      value={bc.avgThreadCount.toFixed(0)}
                    />
                    <IconStat
                      icon={<Network className="size-4" />}
                      label={t('dashboard.bcBackendReach')}
                      value={bcReachText}
                      hint={
                        bc.backendTotal > 0
                          ? t('dashboard.bcReachPercent', {
                              percent: Math.round((bc.backendUp / bc.backendTotal) * 100),
                            })
                          : t('dashboard.bcNoBackendConfigured')
                      }
                      level={
                        bc.backendTotal > 0 ? ratioLevel(1 - bc.backendUp / bc.backendTotal, 0.01, 0.5) : undefined
                      }
                    />
                    <IconStat
                      icon={<Timer className="size-4" />}
                      label={t('dashboard.bcAvgLatency')}
                      value={bcLatencyAvailable ? `${bc.avgBackendLatencyMs.toFixed(0)} ms` : t('dashboard.bcUnavailable')}
                      hint={bcLatencyAvailable ? t('dashboard.bcPingHint') : t('dashboard.bcNoReachableSample')}
                    />
                  </div>
                )}
                {/* 内嵌迷你趋势：均 TPS 近况走向（BC 无独立趋势序列，借集群 TPS 反映整体活跃） */}
                <div className="space-y-1 border-t pt-2">
                  <div className="text-[11px] text-muted-foreground">{t('dashboard.sparklineTps')}</div>
                  <MiniSparkline values={tpsSeries} color="var(--chart-4)" />
                </div>
              </CardContent>
            </Card>
          </div>
        )}
      </AsyncSection>

      {/* ④ 时序监控图：历史趋势（时间窗切换 + 四指标折线，图标标题 + hover tooltip 看某时间点数值） */}
      <Card>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2 text-base font-medium">
              <Gauge aria-hidden className="size-4 text-muted-foreground" />
              {t('dashboard.trendTitle')}
            </div>
            <Tabs value={window} onValueChange={(v) => setWindow(v as TrendWindow)}>
              <TabsList>
                {WINDOWS.map((w) => (
                  <TabsTrigger key={w.value} value={w.value}>
                    {t(w.labelKey)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          </div>
          <AsyncSection isLoading={trendQuery.isLoading} isError={trendQuery.isError} error={trendQuery.error}>
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
              <TrendChart
                title={t('dashboard.chartPlayers')}
                icon={<Users className="size-4" />}
                points={points}
                metric="totalPlayers"
                color="var(--chart-1)"
                formatValue={(v) => String(Math.round(v))}
              />
              <TrendChart
                title={t('dashboard.chartAvgTps')}
                icon={<Zap className="size-4" />}
                points={points}
                metric="avgTps"
                color="var(--chart-2)"
                formatValue={(v) => v.toFixed(1)}
              />
              <TrendChart
                title={t('dashboard.chartAvgMem')}
                icon={<MemoryStick className="size-4" />}
                points={points}
                metric="avgMemUsed"
                color="var(--chart-3)"
                formatValue={(v) => formatBytes(v)}
              />
              <TrendChart
                title={t('dashboard.chartAvgCpu')}
                icon={<Cpu className="size-4" />}
                points={cpuPoints}
                metric="avgCpuLoad"
                color="var(--chart-5)"
                formatValue={(v) => (v < 0 ? t('dashboard.cpuUnavailable') : `${(v * 100).toFixed(0)}%`)}
              />
            </div>
            {/* CPU 内存迷你趋势补位（与上方面板呼应，复用 cpuSeries/memSeries 避免变量未用） */}
            <div className="grid grid-cols-1 gap-3 border-t pt-2 sm:grid-cols-2">
              <div className="space-y-1">
                <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                  <Database aria-hidden className="size-3.5" />
                  {t('dashboard.sparklineMem')}
                </div>
                <MiniSparkline values={memSeries} color="var(--chart-3)" />
              </div>
              <div className="space-y-1">
                <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                  <Cpu aria-hidden className="size-3.5" />
                  {t('dashboard.sparklineCpu')}
                </div>
                <MiniSparkline values={cpuSeries} color="var(--chart-5)" />
              </div>
            </div>
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 底部导航链接（FR-64）：逐服深数据 / 拓扑入口 */}
      <div className="flex flex-wrap gap-4 text-sm">
        <Link to="/servers" className="flex items-center gap-1 text-primary hover:underline">
          <Boxes aria-hidden className="size-4" />
          {t('dashboard.linkServers')}
        </Link>
        <Link to="/topology" className="flex items-center gap-1 text-primary hover:underline">
          <Network aria-hidden className="size-4" />
          {t('dashboard.linkTopology')}
        </Link>
      </div>
    </div>
  )
}
