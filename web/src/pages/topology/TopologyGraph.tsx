// 拓扑图（ECharts graph）：bc 与 bukkit 用不同颜色/符号区分，画真实 bc→bukkit 连线，
// 按大区/zone 聚合分簇，节点按在线状态着色。纯展示组件，数据由 TopologyPage 喂入。
// 抽成独立组件便于页面测试以轻量桩替身规避 ECharts 在 jsdom 下的 canvas 依赖（同 DashboardPage/TrendChart 套路）。

import { useEffect, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import * as echarts from 'echarts/core'
import { GraphChart } from 'echarts/charts'
import { TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsOption, ECharts } from 'echarts'
import type { TopologyNode, TopologyView } from '@/api/types'

// 按需注册 ECharts 模块（graph 图 + tooltip/legend + canvas 渲染器），避免全量 barrel 进主 chunk
echarts.use([GraphChart, TooltipComponent, LegendComponent, CanvasRenderer])

// 角色配色与符号：bc 方块、bukkit 圆形，虚拟大区/小区节点用圆角矩形。
const ROLE_STYLE: Record<string, { symbol: string; color: string; labelKey: string }> = {
  bungee: { symbol: 'roundRect', color: '#2563eb', labelKey: 'topology.roleBungee' },
  bukkit: { symbol: 'circle', color: '#16a34a', labelKey: 'topology.roleBukkit' },
  group: { symbol: 'roundRect', color: '#e0edff', labelKey: 'topology.roleGroup' },
  zone: { symbol: 'roundRect', color: '#e9f7ef', labelKey: 'topology.roleZone' },
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

function sortedNodes(nodes: TopologyNode[]): TopologyNode[] {
  return [...nodes].sort((a, b) => a.serverId.localeCompare(b.serverId))
}

function groupKey(group: string): string {
  return `group:${group || '__none__'}`
}

function zoneKey(group: string, zone: string | null): string {
  return `zone:${group || '__none__'}:${zone ?? '__none__'}`
}

function nodeTooltip(t: TFunction, n: TopologyNode): string {
  return [
    `${n.serverId}`,
    `${roleLabel(t, n.role)} · ${clusterLabel(t, n.group, n.zone)}`,
    `${t('topology.tooltipStatus')} ${n.status}`,
    `${t('topology.tooltipAddress')} ${n.address}`,
  ].join('<br/>')
}

interface GraphSpec {
  option: EChartsOption
}

// 把拓扑数据转为 ECharts graph option（纯函数，便于推理）。t 注入用于角色 / 分簇文案 i18n。
function toGraphSpec(t: TFunction, data: TopologyView, selectedId?: string): GraphSpec {
  const largeGraph = data.nodes.length > 300
  const bungeeNodes = sortedNodes(data.nodes.filter((n) => n.role === 'bungee'))
  const serverNodes = sortedNodes(data.nodes.filter((n) => n.role !== 'bungee'))
  const groupNames = Array.from(
    new Set(data.groups.map((g) => g.group || t('topology.clusterNoGroup'))),
  ).sort()
  const groups = data.groups
    .map((g) => ({ ...g, orderKey: `${g.group || t('topology.clusterNoGroup')}/${g.zone ?? ''}` }))
    .sort((a, b) => a.orderKey.localeCompare(b.orderKey))
  const rowGap = largeGraph ? 70 : 92
  const baseY = 70
  const groupY = new Map<string, number>()
  const zoneY = new Map<string, number>()
  groups.forEach((g, index) => {
    const y = baseY + index * rowGap
    zoneY.set(zoneKey(g.group, g.zone), y)
  })
  groupNames.forEach((name) => {
    const ys = groups
      .filter((g) => (g.group || t('topology.clusterNoGroup')) === name)
      .map((g) => zoneY.get(zoneKey(g.group, g.zone)) ?? baseY)
    groupY.set(name, ys.reduce((sum, y) => sum + y, 0) / Math.max(ys.length, 1))
  })

  const categories = ['bungee', 'group', 'zone', 'bukkit'].map((r) => ({ name: roleLabel(t, r) }))
  const roleIndex = new Map(['bungee', 'group', 'zone', 'bukkit'].map((r, i) => [r, i]))
  const serverPosition = new Map<string, { x: number; y: number }>()
  const sampleLinkIds = new Set<string>()
  groups.forEach((g) => {
    g.members.forEach((serverId, index) => {
      const zoneBaseY = zoneY.get(zoneKey(g.group, g.zone)) ?? baseY
      const columns = largeGraph ? 18 : 8
      const rows = largeGraph ? 9 : 6
      const column = index % columns
      const row = Math.floor(index / columns) % rows
      const x = (largeGraph ? 570 : 620) + column * (largeGraph ? 13 : 28)
      const y = zoneBaseY + (row - (rows - 1) / 2) * (largeGraph ? 8 : 18)
      serverPosition.set(serverId, { x, y })
      if (index < (largeGraph ? 2 : 8)) sampleLinkIds.add(serverId)
    })
  })

  const graphNodes = [
    ...bungeeNodes.map((n, index) => {
      const style = ROLE_STYLE.bungee
      return {
        id: n.serverId,
        name: n.serverId,
        kind: 'instance',
        serverId: n.serverId,
        x: 70,
        y: baseY + index * (largeGraph ? 52 : 78),
        symbol: style.symbol,
        symbolSize: largeGraph ? [92, 30] : [116, 42],
        category: roleIndex.get('bungee') ?? 0,
        itemStyle: {
          color: style.color,
          borderColor:
            selectedId === n.serverId ? '#111827' : (STATUS_BORDER[n.status] ?? '#94a3b8'),
          borderWidth: selectedId === n.serverId ? 4 : 2,
        },
        label: { show: true, color: '#ffffff', fontWeight: 700 },
        value: nodeTooltip(t, n),
      }
    }),
    ...groupNames.map((name) => {
      const style = ROLE_STYLE.group
      return {
        id: groupKey(name),
        name,
        kind: 'group',
        x: 245,
        y: groupY.get(name) ?? baseY,
        symbol: style.symbol,
        symbolSize: largeGraph ? [108, 28] : [128, 40],
        category: roleIndex.get('group') ?? 1,
        itemStyle: { color: style.color, borderColor: '#93c5fd', borderWidth: 1.5 },
        label: { show: true, color: '#1e3a8a', fontWeight: 700 },
        value: `${t('topology.roleGroup')} · ${name}`,
      }
    }),
    ...groups.map((g) => {
      const style = ROLE_STYLE.zone
      const name = g.zone ?? t('topology.clusterNoZone')
      return {
        id: zoneKey(g.group, g.zone),
        name,
        kind: 'zone',
        x: 430,
        y: zoneY.get(zoneKey(g.group, g.zone)) ?? baseY,
        symbol: style.symbol,
        symbolSize: largeGraph ? [96, 26] : [112, 38],
        category: roleIndex.get('zone') ?? 2,
        itemStyle: { color: style.color, borderColor: '#86efac', borderWidth: 1.5 },
        label: { show: true, color: '#166534', fontWeight: 700 },
        value: `${t('topology.roleZone')} · ${clusterLabel(t, g.group, g.zone)} · ${g.members.length}`,
      }
    }),
    ...serverNodes.map((n) => {
      const style = ROLE_STYLE[n.role] ?? ROLE_STYLE.bukkit
      const position = serverPosition.get(n.serverId) ?? { x: 650, y: baseY }
      const emphasized = selectedId === n.serverId || n.status === 'degraded'
      return {
        id: n.serverId,
        name: n.serverId,
        kind: 'instance',
        serverId: n.serverId,
        x: position.x,
        y: position.y,
        symbol: style.symbol,
        symbolSize: largeGraph ? (emphasized ? 12 : 6) : 28,
        category: roleIndex.get('bukkit') ?? 3,
        itemStyle: {
          color: n.status === 'degraded' ? '#f97316' : style.color,
          borderColor:
            selectedId === n.serverId ? '#111827' : (STATUS_BORDER[n.status] ?? '#94a3b8'),
          borderWidth: selectedId === n.serverId ? 3 : 1,
          opacity: largeGraph && !emphasized ? 0.48 : 1,
        },
        label: { show: emphasized, color: '#0f172a', position: 'right' as const, fontSize: 10 },
        value: nodeTooltip(t, n),
      }
    }),
  ]

  const reachableGroups = new Map<string, Set<string>>()
  for (const edge of data.edges) {
    const target = data.nodes.find((n) => n.serverId === edge.target)
    if (!target) continue
    const key = target.group || t('topology.clusterNoGroup')
    const set = reachableGroups.get(edge.source) ?? new Set<string>()
    set.add(key)
    reachableGroups.set(edge.source, set)
  }

  const serverSet = new Set(serverNodes.map((n) => n.serverId))
  const criticalLinkIds = new Set<string>(sampleLinkIds)
  if (selectedId) criticalLinkIds.add(selectedId)
  serverNodes.filter((n) => n.status === 'degraded').forEach((n) => criticalLinkIds.add(n.serverId))

  const links = [
    ...bungeeNodes.flatMap((bc) => {
      const names = reachableGroups.get(bc.serverId)
      return (names && names.size > 0 ? Array.from(names) : groupNames).map((group) => ({
        source: bc.serverId,
        target: groupKey(group),
      }))
    }),
    ...groups.map((g) => ({
      source: groupKey(g.group || t('topology.clusterNoGroup')),
      target: zoneKey(g.group, g.zone),
    })),
    ...groups.flatMap((g) =>
      g.members
        .filter((serverId) => serverSet.has(serverId))
        .filter((serverId) => !largeGraph || criticalLinkIds.has(serverId))
        .map((serverId) => ({ source: zoneKey(g.group, g.zone), target: serverId })),
    ),
  ]

  return {
    option: {
      backgroundColor: '#ffffff',
      tooltip: {
        borderColor: '#dbe3ef',
        textStyle: { color: '#0f172a', fontSize: 12 },
        formatter: (params) => {
          const p = Array.isArray(params) ? params[0] : params
          if (p.dataType === 'edge') return p.name ?? ''
          return typeof p.value === 'string' ? p.value : p.name
        },
      },
      legend: [
        { data: categories.map((c) => c.name), top: 6, right: 8, itemWidth: 12, itemHeight: 8 },
      ],
      series: [
        {
          type: 'graph',
          layout: 'none',
          roam: true,
          draggable: false,
          categories,
          label: { show: !largeGraph, position: 'inside', fontSize: 11, overflow: 'truncate' },
          lineStyle: { color: '#8fb5e7', width: 1.2, curveness: 0.04 },
          emphasis: { focus: 'adjacency', lineStyle: { width: 2.5 } },
          data: graphNodes,
          links,
        },
      ],
      animationDurationUpdate: 250,
    },
  }
}

export default function TopologyGraph({
  data,
  selectedId,
  onSelect,
}: {
  data: TopologyView
  selectedId?: string
  onSelect?: (serverId: string) => void
}) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement | null>(null)
  const chartRef = useRef<ECharts | null>(null)
  const spec = useMemo(() => toGraphSpec(t, data, selectedId), [t, data, selectedId])

  // 初始化图实例并随容器尺寸自适应（仅一次）。
  useEffect(() => {
    if (!containerRef.current) return
    const chart = echarts.init(containerRef.current)
    chartRef.current = chart
    const onResize = () => {
      if (!chart.isDisposed()) chart.resize()
    }
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('resize', onResize)
      if (!chart.isDisposed()) chart.dispose()
      chartRef.current = null
    }
  }, [])

  // 数据变化时增量更新 option（notMerge=false 保留用户拖拽/缩放视角）。
  useEffect(() => {
    const chart = chartRef.current
    if (!chart || chart.isDisposed()) return
    chart.setOption(spec.option)
  }, [spec.option])

  useEffect(() => {
    const chart = chartRef.current
    if (!chart || !onSelect) return
    const handler = (params: unknown) => {
      const event = params as { dataType?: string; data?: unknown }
      const node = event.data as { kind?: string; serverId?: string } | null
      if (event.dataType !== 'node' || node?.kind !== 'instance' || !node.serverId) {
        return
      }
      onSelect(node.serverId)
    }
    chart.on('click', handler)
    return () => {
      if (chart.isDisposed()) return
      chart.off('click', handler)
    }
  }, [onSelect])

  return <div ref={containerRef} className="h-full min-h-0 w-full" />
}
