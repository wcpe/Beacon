// 可观测看板页（FR-32 / FR-34 / FR-43 + 瘦身 FR-64）：集群粗览，一屏不深滚。
// 保留 KPI 总览卡片（5 卡，平均口径仅算 bukkit）+ BC 维度 5 卡 + 关键趋势（4 折线）；
// 加「在线分角色摘要」行（按 metricsSummary.servers 的 role 计数 + onlineServers）+「健康分布」（listInstances 按 status 前端计数）。
// 逐服明细表已移交服务器页（/servers）。环境筛选 + 一键清空（FR-63）保留。
// 边界：只展示负载数字（健康事实），绝不展示任何玩家名单 / 身份（后端也不返回）。

import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Activity, HeartPulse, Server } from 'lucide-react'
import { listInstances, listNamespaces, metricsSummary, metricsTrend } from '../api/client'
import type { TrendWindow } from '../api/client'
import { formatBytes, namespaceOptions } from '../api/format'
import SummaryCards from './dashboard/SummaryCards'
import BCPanel from './dashboard/BCPanel'
import TrendChart from './dashboard/TrendChart'
import StatCard from './dashboard/StatCard'
import AsyncSection from '@/components/AsyncSection'
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

// 角色编码（与后端 metric_aggregate role 约定一致）：bungee 进 BC 计数，其余进子服计数。
const ROLE_BUNGEE = 'bungee'

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

  // 健康分布（FR-64）：在册实例按 status 前端计数（online/lost/offline）。
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

  // 在线分角色计数（FR-64）：按 metricsSummary.servers 的 role 分桶（bungee 进 BC，其余进子服）。
  const allServers = summaryQuery.data?.servers ?? []
  const bukkitCount = allServers.filter((s) => s.role !== ROLE_BUNGEE).length
  const bungeeCount = allServers.filter((s) => s.role === ROLE_BUNGEE).length
  const onlineServers = summaryQuery.data?.onlineServers ?? 0

  // 健康分布计数（FR-64）：在册实例按 status 计数。
  const instances = instancesQuery.data ?? []
  const healthOnline = instances.filter((i) => i.status === 'online').length
  const healthLost = instances.filter((i) => i.status === 'lost').length
  const healthOffline = instances.filter((i) => i.status === 'offline').length

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{t('dashboard.title')}</h1>
        {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
      </div>

      <Card>
        <CardContent>
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="d-namespace">{t('common.namespace')}</Label>
              {/* 环境筛选：可编辑下拉，候选来自 API 但允许键入列表外值（FR-51）；留空聚合全部环境。
                  clearable：选某环境后可一键清回空值（聚合全部，FR-63） */}
              <Combobox
                id="d-namespace"
                aria-label={t('common.namespace')}
                className="w-40"
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
        </CardContent>
      </Card>

      {/* 在线分角色摘要 + 健康分布（FR-64）：粗况一行卡片，替代逐服明细表 */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <StatCard
          label={t('dashboard.roleSummaryTitle')}
          value={t('dashboard.roleSummaryBukkit', { count: bukkitCount })}
          hint={t('dashboard.roleSummaryBungee', { count: bungeeCount })}
          icon={<Server className="size-4" />}
        />
        <StatCard
          label={t('dashboard.onlineServersTitle')}
          value={onlineServers}
          hint={t('dashboard.roleSummaryOnline', { count: bukkitCount + bungeeCount })}
          icon={<Activity className="size-4" />}
        />
        <StatCard
          label={t('dashboard.healthTitle')}
          value={t('dashboard.healthOnline', { count: healthOnline })}
          hint={`${t('dashboard.healthLost', { count: healthLost })} · ${t('dashboard.healthOffline', { count: healthOffline })}`}
          icon={<HeartPulse className="size-4" />}
        />
      </div>

      {/* ===== 子服（bukkit）粗览 ===== */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">{t('dashboard.sectionBukkit')}</h2>

        {/* 总览卡片：当前快照聚合（平均 TPS·内存·CPU 仅算 bukkit） */}
        <AsyncSection
          isLoading={summaryQuery.isLoading}
          isError={summaryQuery.isError}
          error={summaryQuery.error}
        >
          {summaryQuery.data && <SummaryCards summary={summaryQuery.data} />}
        </AsyncSection>

        {/* 趋势图：时间窗切换 + 四指标折线（仅 bukkit 口径） */}
        <Card>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-base font-medium">{t('dashboard.trendTitle')}</div>
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
            <AsyncSection
              isLoading={trendQuery.isLoading}
              isError={trendQuery.isError}
              error={trendQuery.error}
            >
              <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
                <TrendChart
                  title={t('dashboard.chartPlayers')}
                  points={points}
                  metric="totalPlayers"
                  color="#2563eb"
                  formatValue={(v) => String(Math.round(v))}
                />
                <TrendChart
                  title={t('dashboard.chartAvgTps')}
                  points={points}
                  metric="avgTps"
                  color="#16a34a"
                  formatValue={(v) => v.toFixed(1)}
                />
                <TrendChart
                  title={t('dashboard.chartAvgMem')}
                  points={points}
                  metric="avgMemUsed"
                  color="#d97706"
                  formatValue={(v) => formatBytes(v)}
                />
                <TrendChart
                  title={t('dashboard.chartAvgCpu')}
                  points={cpuPoints}
                  metric="avgCpuLoad"
                  color="#dc2626"
                  formatValue={(v) => (v < 0 ? t('dashboard.cpuUnavailable') : `${(v * 100).toFixed(0)}%`)}
                />
              </div>
            </AsyncSection>
          </CardContent>
        </Card>
      </section>

      {/* ===== BC 代理（bungee）粗览 ===== */}
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">{t('dashboard.sectionBc')}</h2>
        {/* BC 维度聚合卡片（FR-34）：仅 role=bungee 聚合 */}
        <AsyncSection
          isLoading={summaryQuery.isLoading}
          isError={summaryQuery.isError}
          error={summaryQuery.error}
        >
          {summaryQuery.data && <BCPanel bc={summaryQuery.data.bc} />}
        </AsyncSection>
      </section>

      {/* 底部导航链接（FR-64）：逐服深数据 / 拓扑入口 */}
      <div className="flex flex-wrap gap-4 text-sm">
        <Link to="/servers" className="text-primary hover:underline">
          {t('dashboard.linkServers')}
        </Link>
        <Link to="/topology" className="text-primary hover:underline">
          {t('dashboard.linkTopology')}
        </Link>
      </div>
    </div>
  )
}
