// 拓扑图（ECharts graph）：bc 与 bukkit 用不同颜色/符号区分，画真实 bc→bukkit 连线，
// 按大区/zone 聚合分簇，节点按在线状态着色。纯展示组件，数据由 TopologyPage 喂入。
// 抽成独立组件便于页面测试以轻量桩替身规避 ECharts 在 jsdom 下的 canvas 依赖（同 DashboardPage/TrendChart 套路）。

import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import * as echarts from 'echarts/core'
import { GraphChart } from 'echarts/charts'
import { TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsOption, ECharts } from 'echarts'
import type { TopologyView } from '@/api/types'

// 按需注册 ECharts 模块（graph 图 + tooltip/legend + canvas 渲染器），避免全量 barrel 进主 chunk
echarts.use([GraphChart, TooltipComponent, LegendComponent, CanvasRenderer])

// 角色配色与符号：bc（bungee）方块、bukkit 圆形，色相区分。显示名经 i18n（topology.roleBungee/Bukkit）。
const ROLE_STYLE: Record<string, { symbol: string; color: string; labelKey: string }> = {
  bungee: { symbol: 'rect', color: '#7c3aed', labelKey: 'topology.roleBungee' },
  bukkit: { symbol: 'circle', color: '#2563eb', labelKey: 'topology.roleBukkit' },
}

// 在线状态描边色：online 绿、degraded 琥珀（可用集合仅这两态）。
const STATUS_BORDER: Record<string, string> = {
  online: '#16a34a',
  degraded: '#f59e0b',
}

// 把 (group,zone) 拼成分簇标签（zone 为空显示「未分配」）。
function clusterLabel(t: TFunction, group: string, zone: string | null): string {
  return `${group || t('topology.clusterNoGroup')} / ${zone ?? t('topology.clusterNoZone')}`
}

// 角色显示名（未知角色回退为原值）。
function roleLabel(t: TFunction, role: string): string {
  const key = ROLE_STYLE[role]?.labelKey
  return key ? t(key) : role
}

// 把拓扑数据转为 ECharts graph option（纯函数，便于推理）。t 注入用于角色 / 分簇文案 i18n。
function toOption(t: TFunction, data: TopologyView): EChartsOption {
  // 按角色分类（图例可按角色筛选）
  const roles = Array.from(new Set(data.nodes.map((n) => n.role)))
  const categories = roles.map((r) => ({ name: roleLabel(t, r) }))
  const roleIndex = new Map(roles.map((r, i) => [r, i]))

  const nodes = data.nodes.map((n) => {
    const style = ROLE_STYLE[n.role] ?? { symbol: 'circle', color: '#64748b' }
    return {
      id: n.serverId,
      name: n.serverId,
      symbol: style.symbol,
      symbolSize: n.role === 'bungee' ? 46 : 34,
      category: roleIndex.get(n.role) ?? 0,
      itemStyle: {
        color: style.color,
        borderColor: STATUS_BORDER[n.status] ?? '#94a3b8',
        borderWidth: 3,
      },
      // tooltip 展示完整事实（角色/大区/zone/状态/地址）
      value: `${roleLabel(t, n.role)} · ${clusterLabel(t, n.group, n.zone)} · ${n.status} · ${n.address}`,
    }
  })

  const links = data.edges.map((e) => ({ source: e.source, target: e.target }))

  return {
    tooltip: {
      // 节点 tooltip 展示完整事实；边 tooltip 仅展示其连接名（ECharts 默认拼为 source > target）
      formatter: (params) => {
        const p = Array.isArray(params) ? params[0] : params
        if (p.dataType === 'edge') return p.name ?? ''
        return `${p.name}<br/>${typeof p.value === 'string' ? p.value : ''}`
      },
    },
    legend: [{ data: categories.map((c) => c.name), top: 8 }],
    series: [
      {
        type: 'graph',
        // 力导布局：同簇节点经斥力/连线自然聚拢，bc→bukkit 连线把子服拉到其代理附近
        layout: 'force',
        roam: true,
        draggable: true,
        categories,
        force: { repulsion: 220, edgeLength: 120, gravity: 0.08 },
        label: { show: true, position: 'right', fontSize: 11 },
        lineStyle: { color: '#94a3b8', width: 1.5, curveness: 0.05 },
        emphasis: { focus: 'adjacency', lineStyle: { width: 3 } },
        data: nodes,
        links,
      },
    ],
  }
}

export default function TopologyGraph({ data }: { data: TopologyView }) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement | null>(null)
  const chartRef = useRef<ECharts | null>(null)

  // 初始化图实例并随容器尺寸自适应（仅一次）。
  useEffect(() => {
    if (!containerRef.current) return
    const chart = echarts.init(containerRef.current)
    chartRef.current = chart
    const onResize = () => chart.resize()
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('resize', onResize)
      chart.dispose()
      chartRef.current = null
    }
  }, [])

  // 数据变化时增量更新 option（notMerge=false 保留用户拖拽/缩放视角）。
  useEffect(() => {
    chartRef.current?.setOption(toOption(t, data))
  }, [t, data])

  return <div ref={containerRef} className="h-[600px] w-full" />
}
