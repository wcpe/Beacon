// 可观测看板页（FR-32 / FR-34 / FR-43，见 docs/specs/dashboard-role-split.md）：
// 整体拆「子服(bukkit)」与「BC 代理」两大区块——
//   子服区：总览卡片（总人数 / 在线服务器 / 平均 TPS / 内存 / CPU，平均口径仅算 bukkit）+ 趋势图 + 子服明细；
//   BC 区：BCPanel（bc 维度聚合）+ bc 明细。
// 每服明细按 role 分组（bukkit / bungee 各一表）。
// 边界：只展示负载数字（健康事实），绝不展示任何玩家名单 / 身份（后端也不返回）。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { listNamespaces, metricsSummary, metricsTrend } from '../api/client'
import type { ServerPlayers, TrendWindow } from '../api/client'
import { formatBytes } from '../api/format'
import SummaryCards from './dashboard/SummaryCards'
import BCPanel from './dashboard/BCPanel'
import TrendChart from './dashboard/TrendChart'
import AsyncSection from '@/components/AsyncSection'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Combobox } from '@/components/ui/combobox'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

// 总览快照刷新周期（毫秒）：与实例健康页一致，短周期反映当前负载
const SUMMARY_REFETCH_MS = 5000

// 可选时间窗（预设窗口）：label 经 i18n key 在渲染时解析
const WINDOWS: Array<{ value: TrendWindow; labelKey: string }> = [
  { value: '1h', labelKey: 'dashboard.win1h' },
  { value: '6h', labelKey: 'dashboard.win6h' },
  { value: '24h', labelKey: 'dashboard.win24h' },
]

// 角色编码（与后端 metric_aggregate role 约定一致）：
// bungee 进 BC 区，其余（含 bukkit 与未知角色）兜底进子服区，避免新角色漏展示。
const ROLE_BUNGEE = 'bungee'

// 每服人数明细行（含角色，供按角色分组）
type ServerRow = ServerPlayers

export default function DashboardPage() {
  const { t } = useTranslation()
  // 环境过滤（可编辑下拉，FR-51）：空表示聚合全部环境，进页默认即聚合全部。
  const [namespace, setNamespace] = useState('')
  const [window, setWindow] = useState<TrendWindow>('1h')

  // 环境下拉候选来自 listNamespaces；筛选框允许键入候选外的值（可编辑）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const namespaceOptions = useMemo(
    () => (namespacesQuery.data ?? []).map((n) => n.code),
    [namespacesQuery.data],
  )

  const summaryQuery = useQuery({
    queryKey: ['metrics-summary', namespace],
    queryFn: () => metricsSummary(namespace || undefined),
    refetchInterval: SUMMARY_REFETCH_MS,
  })

  const trendQuery = useQuery({
    queryKey: ['metrics-trend', namespace, window],
    queryFn: () => metricsTrend({ namespace: namespace || undefined, window }),
  })

  const isFetching = summaryQuery.isFetching || trendQuery.isFetching
  const points = trendQuery.data?.points ?? []
  // CPU 折线专用点：把无样本哨兵（avgCpuLoad < 0，约定 -1）置为 null，
  // recharts 据此断线，避免 -1 被当数据点画成 -100% 污染 Y 轴尺度。
  const cpuPoints = points.map((p) => ({
    ...p,
    avgCpuLoad: p.avgCpuLoad < 0 ? null : p.avgCpuLoad,
  }))

  // 每服明细按角色分组：bukkit 一组、bungee 一组（其它角色归到子服明细兜底）。
  const allServers = summaryQuery.data?.servers ?? []
  const bukkitServers = allServers.filter((s) => s.role !== ROLE_BUNGEE)
  const bcServers = allServers.filter((s) => s.role === ROLE_BUNGEE)

  // 子服明细列：serverId 与在线人数（不含名单）
  const serverColumns: DataTableColumn<ServerRow>[] = [
    { header: t('dashboard.colServerId'), className: 'font-mono', cell: (r) => r.serverId },
    { header: t('dashboard.colPlayerCount'), cell: (r) => r.playerCount },
  ]
  // bc 明细列：serverId 与在线连接数（bc 的 playerCount 即代理在线连接）
  const bcServerColumns: DataTableColumn<ServerRow>[] = [
    { header: t('dashboard.colServerId'), className: 'font-mono', cell: (r) => r.serverId },
    { header: t('dashboard.colConnections'), cell: (r) => r.playerCount },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{t('dashboard.title')}</h1>
        {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
      </div>

      <Card>
        <CardContent>
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="d-namespace">{t('common.namespace')}</Label>
              {/* 环境筛选：可编辑下拉，候选来自 API 但允许键入列表外值（FR-51）；留空聚合全部环境 */}
              <Combobox
                id="d-namespace"
                aria-label={t('common.namespace')}
                className="w-40"
                placeholder={t('dashboard.nsPlaceholder')}
                value={namespace}
                onChange={setNamespace}
                options={namespaceOptions}
                allowCustom
              />
            </div>
          </div>
        </CardContent>
      </Card>

      {/* ===== 区块一：子服（bukkit）===== */}
      {/* 总览卡片 / 趋势 / 子服明细的平均口径均仅算 bukkit，与 BC 区分离 */}
      <section className="space-y-4">
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
          <CardContent className="space-y-4">
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
              <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
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

        {/* 子服明细：serverId → 在线人数（仅 bukkit 角色） */}
        <Card>
          <CardContent className="space-y-3">
            <div className="text-base font-medium">{t('dashboard.serverDetail')}</div>
            <AsyncSection
              isLoading={summaryQuery.isLoading}
              isError={summaryQuery.isError}
              error={summaryQuery.error}
            >
              <DataTable
                columns={serverColumns}
                rows={bukkitServers}
                rowKey={(r) => r.serverId}
                emptyText={t('dashboard.bukkitEmpty')}
              />
            </AsyncSection>
          </CardContent>
        </Card>
      </section>

      {/* ===== 区块二：BC 代理（bungee）===== */}
      {/* bc 维度聚合 + bc 明细，与子服区完全分离 */}
      <section className="space-y-4">
        <h2 className="text-lg font-semibold">{t('dashboard.sectionBc')}</h2>

        {/* BC 维度聚合卡片（FR-34）：仅 role=bungee 聚合 */}
        <AsyncSection
          isLoading={summaryQuery.isLoading}
          isError={summaryQuery.isError}
          error={summaryQuery.error}
        >
          {summaryQuery.data && <BCPanel bc={summaryQuery.data.bc} />}
        </AsyncSection>

        {/* BC 明细：serverId → 在线连接（仅 bungee 角色） */}
        <Card>
          <CardContent className="space-y-3">
            <div className="text-base font-medium">{t('dashboard.bcDetail')}</div>
            <AsyncSection
              isLoading={summaryQuery.isLoading}
              isError={summaryQuery.isError}
              error={summaryQuery.error}
            >
              <DataTable
                columns={bcServerColumns}
                rows={bcServers}
                rowKey={(r) => r.serverId}
                emptyText={t('dashboard.bcEmpty')}
              />
            </AsyncSection>
          </CardContent>
        </Card>
      </section>
    </div>
  )
}
