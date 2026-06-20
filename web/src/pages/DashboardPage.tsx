// 可观测看板页（FR-32，见 docs/specs/control-plane-observability-dashboard.md）：
// 总览卡片（总人数 / 在线服务器 / 平均 TPS / 内存 / CPU）+ 趋势图（人数 / TPS / 内存 / CPU）+ 每服明细。
// 边界：只展示负载数字（健康事实），绝不展示任何玩家名单 / 身份（后端也不返回）。

import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { metricsSummary, metricsTrend } from '../api/client'
import type { TrendWindow } from '../api/client'
import { formatBytes } from '../api/format'
import SummaryCards from './dashboard/SummaryCards'
import BCPanel from './dashboard/BCPanel'
import TrendChart from './dashboard/TrendChart'
import AsyncSection from '@/components/AsyncSection'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

// 总览快照刷新周期（毫秒）：与实例健康页一致，短周期反映当前负载
const SUMMARY_REFETCH_MS = 5000

// 可选时间窗（预设窗口）
const WINDOWS: Array<{ value: TrendWindow; label: string }> = [
  { value: '1h', label: '近 1 小时' },
  { value: '6h', label: '近 6 小时' },
  { value: '24h', label: '近 24 小时' },
]

// 每服人数明细行
interface ServerRow {
  serverId: string
  playerCount: number
}

export default function DashboardPage() {
  // 环境过滤：输入框为待提交值，namespace 为已生效查询值（空表示聚合全部环境）
  const [nsInput, setNsInput] = useState('')
  const [namespace, setNamespace] = useState('')
  const [window, setWindow] = useState<TrendWindow>('1h')

  const summaryQuery = useQuery({
    queryKey: ['metrics-summary', namespace],
    queryFn: () => metricsSummary(namespace || undefined),
    refetchInterval: SUMMARY_REFETCH_MS,
  })

  const trendQuery = useQuery({
    queryKey: ['metrics-trend', namespace, window],
    queryFn: () => metricsTrend({ namespace: namespace || undefined, window }),
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setNamespace(nsInput.trim())
  }

  const isFetching = summaryQuery.isFetching || trendQuery.isFetching
  const points = trendQuery.data?.points ?? []
  // CPU 折线专用点：把无样本哨兵（avgCpuLoad < 0，约定 -1）置为 null，
  // recharts 据此断线，避免 -1 被当数据点画成 -100% 污染 Y 轴尺度。
  const cpuPoints = points.map((p) => ({
    ...p,
    avgCpuLoad: p.avgCpuLoad < 0 ? null : p.avgCpuLoad,
  }))

  // 每服明细列：仅 serverId 与在线人数（不含名单）
  const serverColumns: DataTableColumn<ServerRow>[] = [
    { header: 'serverId', className: 'font-mono', cell: (r) => r.serverId },
    { header: '在线人数', cell: (r) => r.playerCount },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">可观测看板</h1>
        {isFetching && <span className="text-sm text-muted-foreground">（刷新中…）</span>}
      </div>

      <Card>
        <CardContent>
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="d-namespace">环境</Label>
              <Input
                id="d-namespace"
                placeholder="留空聚合全部环境"
                value={nsInput}
                onChange={(e) => setNsInput(e.target.value)}
              />
            </div>
            <Button type="submit">查询</Button>
          </form>
        </CardContent>
      </Card>

      {/* 总览卡片：当前快照聚合 */}
      <AsyncSection
        isLoading={summaryQuery.isLoading}
        isError={summaryQuery.isError}
        error={summaryQuery.error}
      >
        {summaryQuery.data && <SummaryCards summary={summaryQuery.data} />}
      </AsyncSection>

      {/* BC 代理面板（FR-34）：按角色分流展示 bc 专属指标，bukkit 视图不受影响 */}
      <Card>
        <CardContent className="space-y-3">
          <div className="text-base font-medium">BC 代理</div>
          <AsyncSection
            isLoading={summaryQuery.isLoading}
            isError={summaryQuery.isError}
            error={summaryQuery.error}
          >
            {summaryQuery.data && <BCPanel bc={summaryQuery.data.bc} />}
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 趋势图：时间窗切换 + 四指标折线 */}
      <Card>
        <CardContent className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="text-base font-medium">历史趋势</div>
            <Tabs value={window} onValueChange={(v) => setWindow(v as TrendWindow)}>
              <TabsList>
                {WINDOWS.map((w) => (
                  <TabsTrigger key={w.value} value={w.value}>
                    {w.label}
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
                title="在线玩家数"
                points={points}
                metric="totalPlayers"
                color="#2563eb"
                formatValue={(v) => String(Math.round(v))}
              />
              <TrendChart
                title="平均 TPS"
                points={points}
                metric="avgTps"
                color="#16a34a"
                formatValue={(v) => v.toFixed(1)}
              />
              <TrendChart
                title="平均内存"
                points={points}
                metric="avgMemUsed"
                color="#d97706"
                formatValue={(v) => formatBytes(v)}
              />
              <TrendChart
                title="平均 CPU 负载"
                points={cpuPoints}
                metric="avgCpuLoad"
                color="#dc2626"
                formatValue={(v) => (v < 0 ? '不可用' : `${(v * 100).toFixed(0)}%`)}
              />
            </div>
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 每服明细：serverId → 在线人数 */}
      <Card>
        <CardContent className="space-y-3">
          <div className="text-base font-medium">每服明细</div>
          <AsyncSection
            isLoading={summaryQuery.isLoading}
            isError={summaryQuery.isError}
            error={summaryQuery.error}
          >
            <DataTable
              columns={serverColumns}
              rows={summaryQuery.data?.servers}
              rowKey={(r) => r.serverId}
              emptyText="无在线实例"
            />
          </AsyncSection>
        </CardContent>
      </Card>
    </div>
  )
}
