// 拓扑可视化图（A 版经典网络拓扑放射图，自绘 SVG，不引新依赖）：
// 左列 BC 集群代理节点（靛蓝圆形、稍大）放射连到右侧按大区分区摆放的小区聚合圆形节点；
// 小区节点外环按内部子服健康占比着色（健康绿 / 降级琥珀 / 危急红 / 离线灰）；
// 链路 = 「源集群 → 目标小区」的聚合消息链路：粗细 ∝ 消息量、异常边红色加粗并直显失败率标签，
// 沿线虚线流动动画（CSS stroke-dashoffset，节制、respect prefers-reduced-motion、可开关）。
// 点选链路 / 小区节点 → 右侧固定侧面板展示明细 / 概要，图本身不 reflow、不跳动。
// 超大量（如 48 小区 / 1200 台）时按大区聚合为节点并明示聚合；动画只跑有限条边（异常优先）。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ArrowRight, Boxes, Network, TriangleAlert, Waves } from 'lucide-react'

import { AsyncSection, Badge, Button, cn } from '@beacon/ui'
import type { HealthItem, MessageEdgeStat, ZoneTreeResponse } from '@beacon/devmock'

import { fetchMessageEdges, fetchServers, fetchZoneTree } from '../../api/cluster'
import { fetchHealthList } from '../../api/metrics'

// 小区节点总数上限：超过则按大区聚合折叠（节点 = 大区），不再逐个渲染小区
const MAX_ZONE_NODES = 24
// 链路失败率阈值：聚合失败率超过即视为异常链路高亮
const ABNORMAL_RATE = 5
// 图上渲染的聚合链路上限：正常情况下「集群 × 节点」组合远小于该值，仅作防御性硬闸
const MAX_RENDER_EDGES = 60
// 参与数据流动画的链路上限：仅有限条跑流动画（异常优先），超出静态呈现，避免过多元素持续重绘
const MAX_FLOW_EDGES = 24
// 直显失败率标签的异常链路上限：只标注最差的前几条（选中的链路始终标注），避免标签堆叠不可读
const MAX_EDGE_LABELS = 8

// ---- 放射布局几何常量（确定性静态计算，不做力导向物理模拟）----
// 代理节点半径（靛蓝、稍大）
const PROXY_R = 42
// 小区 / 大区聚合节点半径
const NODE_R = 33
// 分区内节点网格：单元宽高（含名称与健康说明文字）
const CELL_W = 150
const CELL_H = 128
// 分区盒内边距与标题高度
const GROUP_PAD_X = 16
const GROUP_HEAD = 44
const GROUP_PAD_B = 10
// 分区盒之间的纵向间距
const GROUP_GAP = 20
// 分区列起始 x / 代理列中心 x
const GROUP_X = 236
const PROXY_CX = 104
// 分区内每行最多节点数
const MAX_COLS = 4
// 画布内边距
const PAD = 16

interface TopologyGraphProps {
  namespaceId: number
}

// 节点健康占比计数（小区聚合内部子服 / 折叠态为大区聚合）
interface NodeHealth {
  ok: number
  warn: number
  crit: number
  off: number
}

// 放射图节点：展开态 = 小区，折叠态 = 大区
interface GraphNode {
  id: number
  name: string
  serverCount: number
  clusterId: number
  cx: number
  cy: number
}

// 分区背景盒：展开态 = 大区，折叠态 = 集群
interface GraphGroup {
  id: number
  name: string
  nodeCount: number
  serverCount: number
  x: number
  y: number
  w: number
  h: number
}

// 左列代理节点：每个 BC 集群一个
interface ProxyNode {
  clusterId: number
  name: string
  proxyCount: number
  cx: number
  cy: number
}

interface GraphLayout {
  nodes: GraphNode[]
  groups: GraphGroup[]
  proxies: ProxyNode[]
  width: number
  height: number
}

// 聚合链路：源集群 → 目标节点；total=0 表示仅结构连线（无消息统计）
interface GraphLink {
  key: string
  clusterId: number
  nodeId: number
  total: number
  failRatePercent: number
  abnormal: boolean
  worstEdge: MessageEdgeStat | null
  rawCount: number
  flow: boolean
}

// 侧面板选中态：原始消息边（server→server）或节点概要
type Selection = { kind: 'edge'; key: string } | { kind: 'node'; id: number } | null

// 统计一棵树里的小区节点总数（判定是否需要折叠）
function countZones(tree: ZoneTreeResponse): number {
  return tree.clusters.reduce(
    (sum, cluster) => sum + cluster.regions.reduce((rs, region) => rs + region.zones.length, 0),
    0,
  )
}

// 三次贝塞尔链路路径：从代理圆右缘弯向节点圆左缘（控制点水平外推，走势同 mockup）
function linkPath(proxy: ProxyNode, node: GraphNode): string {
  const x1 = proxy.cx + PROXY_R
  const y1 = proxy.cy
  const x2 = node.cx - NODE_R
  const y2 = node.cy
  const dx = Math.max(40, (x2 - x1) / 2)
  return `M ${String(x1)} ${String(y1)} C ${String(x1 + dx)} ${String(y1)}, ${String(x2 - dx)} ${String(y2)}, ${String(x2)} ${String(y2)}`
}

// 放射布局：分区盒纵向堆叠在右侧，分区内节点按网格排布；代理列在左侧纵向均匀分布
function buildLayout(tree: ZoneTreeResponse, collapsed: boolean): GraphLayout {
  const nodes: GraphNode[] = []
  const groups: GraphGroup[] = []
  let cursorY = PAD
  let maxGroupW = 0

  // 展开态：分区 = 大区、节点 = 小区；折叠态：分区 = 集群、节点 = 大区
  interface GroupSpec {
    id: number
    name: string
    items: { id: number; name: string; serverCount: number; clusterId: number }[]
  }
  const specs: GroupSpec[] = []
  for (const cluster of tree.clusters) {
    if (collapsed) {
      specs.push({
        id: cluster.id,
        name: cluster.name,
        items: cluster.regions.map((region) => ({
          id: region.id,
          name: region.name,
          serverCount: region.zones.reduce((s, z) => s + z.serverCount, 0),
          clusterId: cluster.id,
        })),
      })
    } else {
      for (const region of cluster.regions) {
        specs.push({
          id: region.id,
          name: region.name,
          items: region.zones.map((zone) => ({
            id: zone.id,
            name: zone.name,
            serverCount: zone.serverCount,
            clusterId: cluster.id,
          })),
        })
      }
    }
  }

  for (const spec of specs) {
    const n = spec.items.length
    const cols = Math.min(Math.max(n, 1), MAX_COLS)
    const rows = Math.max(Math.ceil(n / cols), 1)
    const w = GROUP_PAD_X * 2 + cols * CELL_W
    const h = GROUP_HEAD + rows * CELL_H + GROUP_PAD_B
    groups.push({
      id: spec.id,
      name: spec.name,
      nodeCount: n,
      serverCount: spec.items.reduce((s, it) => s + it.serverCount, 0),
      x: GROUP_X,
      y: cursorY,
      w,
      h,
    })
    spec.items.forEach((item, i) => {
      const col = i % cols
      const row = Math.floor(i / cols)
      nodes.push({
        ...item,
        cx: GROUP_X + GROUP_PAD_X + col * CELL_W + CELL_W / 2,
        cy: cursorY + GROUP_HEAD + row * CELL_H + NODE_R + 8,
      })
    })
    maxGroupW = Math.max(maxGroupW, w)
    cursorY += h + GROUP_GAP
  }

  // 画布尺寸：右侧分区决定宽度；高度保证代理列有起码的呼吸空间
  const clusterCount = tree.clusters.length
  const minProxyHeight = PAD + 40 + clusterCount * (PROXY_R * 2 + 28) + PAD
  const height = Math.max(cursorY - GROUP_GAP + PAD, minProxyHeight)
  const width = GROUP_X + maxGroupW + PAD

  // 代理列：顶部让出层标题空间后纵向均匀分布
  const top = PAD + 44
  const span = height - top - PAD
  const proxies: ProxyNode[] = tree.clusters.map((cluster, i) => ({
    clusterId: cluster.id,
    name: cluster.name,
    proxyCount: cluster.proxyCount,
    cx: PROXY_CX,
    cy: top + (span * (i + 0.5)) / clusterCount,
  }))

  return { nodes, groups, proxies, width, height }
}

// 健康占比环分段：pathLength=100 便于按百分比切段，起点在圆顶部（dashoffset 25）
function ringSegments(health: NodeHealth): { color: string; share: number; offset: number }[] {
  const total = health.ok + health.warn + health.crit + health.off
  if (total === 0) {
    return []
  }
  const parts = [
    { color: 'var(--color-ok)', count: health.ok },
    { color: 'var(--color-warn)', count: health.warn },
    { color: 'var(--color-crit)', count: health.crit },
    { color: 'var(--color-off)', count: health.off },
  ]
  const segments: { color: string; share: number; offset: number }[] = []
  let cum = 0
  for (const part of parts) {
    if (part.count === 0) {
      continue
    }
    const share = (part.count / total) * 100
    segments.push({ color: part.color, share, offset: 25 - cum })
    cum += share
  }
  return segments
}

export default function TopologyGraph({ namespaceId }: TopologyGraphProps) {
  const { t } = useTranslation()
  const treeQuery = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    placeholderData: keepPreviousData,
  })
  const edgesQuery = useQuery({ queryKey: ['message-edges'], queryFn: fetchMessageEdges })
  // 用于把消息边两端 serverId 解析到所属小区 / 集群，并统计代理在线数
  const serversQuery = useQuery({
    queryKey: ['servers', 'topology', namespaceId],
    queryFn: () => fetchServers({ namespaceId, pageSize: 2000 }),
    placeholderData: keepPreviousData,
  })
  // 健康视图列表：serverId → 健康等级，供小区节点按健康占比着色（一次全量，避免 N+1）
  const healthQuery = useQuery({
    queryKey: ['health', 'topology', namespaceId],
    queryFn: () => fetchHealthList({ namespaceId, pageSize: 2000 }),
  })
  const tree = treeQuery.data

  // 侧面板选中态（点链路 / 节点 / 下方异常链路 chip 打开，不 reflow 图）
  const [selection, setSelection] = useState<Selection>(null)
  // 数据流动画开关（默认开，节制低速）
  const [flowOn, setFlowOn] = useState(true)

  const zoneTotal = useMemo(() => (tree ? countZones(tree) : 0), [tree])
  const collapsed = zoneTotal > MAX_ZONE_NODES
  const isEmpty = tree?.clusters.length === 0

  const allEdges = edgesQuery.data?.edges ?? []
  // 异常原始边（失败率超阈值），按失败率降序，供 chip 列表与计数
  const abnormalEdges = useMemo(
    () =>
      [...allEdges]
        .filter((e) => e.failRatePercent >= ABNORMAL_RATE)
        .sort((a, b) => b.failRatePercent - a.failRatePercent),
    [allEdges],
  )
  const edgeKey = (edge: MessageEdgeStat) => `${edge.sourceServerId}→${edge.resolvedServerId}`
  const selectedEdge = useMemo(
    () => (selection?.kind === 'edge' ? (allEdges.find((e) => edgeKey(e) === selection.key) ?? null) : null),
    [allEdges, selection],
  )

  // serverId → zoneId（后端子服）：解析消息边两端归属
  const zoneOfServer = useMemo(() => {
    const map = new Map<string, number>()
    for (const s of serversQuery.data?.items ?? []) {
      if (s.kind === 'backend' && s.zoneId !== null) {
        map.set(s.serverId, s.zoneId)
      }
    }
    return map
  }, [serversQuery.data])

  // 结构索引：小区 → 大区 / 大区 → 集群（消息边归属解析与折叠映射共用）
  const structure = useMemo(() => {
    const regionOfZone = new Map<number, number>()
    const clusterOfRegion = new Map<number, number>()
    for (const cluster of tree?.clusters ?? []) {
      for (const region of cluster.regions) {
        clusterOfRegion.set(region.id, cluster.id)
        for (const zone of region.zones) {
          regionOfZone.set(zone.id, region.id)
        }
      }
    }
    return { regionOfZone, clusterOfRegion }
  }, [tree])

  // 各集群代理在线数（左列节点状态点 + 代理层副标题）
  const proxyStat = useMemo(() => {
    const byCluster = new Map<number, { online: number; total: number }>()
    let online = 0
    let total = 0
    for (const s of serversQuery.data?.items ?? []) {
      if (s.kind !== 'proxy' || s.bcClusterId === null) {
        continue
      }
      const stat = byCluster.get(s.bcClusterId) ?? { online: 0, total: 0 }
      stat.total += 1
      total += 1
      if (s.online) {
        stat.online += 1
        online += 1
      }
      byCluster.set(s.bcClusterId, stat)
    }
    return { byCluster, online, total }
  }, [serversQuery.data])

  // serverId → 健康等级
  const healthByServer = useMemo(() => {
    const map = new Map<string, HealthItem>()
    for (const item of healthQuery.data?.items ?? []) {
      map.set(item.serverId, item)
    }
    return map
  }, [healthQuery.data])

  // 节点健康占比：展开态按小区聚合、折叠态按大区聚合（离线优先于健康等级）
  const healthOfNode = useMemo(() => {
    const map = new Map<number, NodeHealth>()
    for (const s of serversQuery.data?.items ?? []) {
      if (s.kind !== 'backend' || s.zoneId === null) {
        continue
      }
      const nodeId = collapsed ? structure.regionOfZone.get(s.zoneId) : s.zoneId
      if (nodeId === undefined) {
        continue
      }
      const bucket = map.get(nodeId) ?? { ok: 0, warn: 0, crit: 0, off: 0 }
      if (!s.online) {
        bucket.off += 1
      } else {
        const level = healthByServer.get(s.serverId)?.level ?? 'healthy'
        if (level === 'degraded') {
          bucket.warn += 1
        } else if (level === 'unhealthy') {
          bucket.crit += 1
        } else {
          bucket.ok += 1
        }
      }
      map.set(nodeId, bucket)
    }
    return map
  }, [serversQuery.data, healthByServer, collapsed, structure])

  // 放射布局（确定性静态计算，useMemo 缓存）
  const layout = useMemo(() => {
    if (!tree || tree.clusters.length === 0) {
      return null
    }
    return buildLayout(tree, collapsed)
  }, [tree, collapsed])

  // 聚合链路：结构主链路（集群 → 其下每个节点）+ 消息统计按「源集群 → 目标节点」聚合；
  // 上千条服务器间原始边天然收敛为少量集群 → 节点链路（跨集群消息也会生成对应链路），
  // 仍保留异常优先排序 + 硬性截断作防御闸，避免极端形态下渲染过多动画边。
  const linkInfo = useMemo(() => {
    if (!layout || !tree) {
      return { links: [] as GraphLink[], aggregatedRaw: 0, statsLinks: 0, truncated: false, resolvedTotal: 0 }
    }
    interface MutableLink {
      clusterId: number
      nodeId: number
      total: number
      failNum: number
      worstEdge: MessageEdgeStat | null
      rawCount: number
    }
    const map = new Map<string, MutableLink>()
    const keyOf = (clusterId: number, nodeId: number) => `${String(clusterId)}→${String(nodeId)}`
    // 结构主链路：集群放射到其下每个节点
    for (const node of layout.nodes) {
      map.set(keyOf(node.clusterId, node.id), {
        clusterId: node.clusterId,
        nodeId: node.id,
        total: 0,
        failNum: 0,
        worstEdge: null,
        rawCount: 0,
      })
    }
    // 消息统计聚合：源端解析到集群、目标端解析到节点（小区 / 折叠态大区）
    const resolveNodeId = (serverId: string): number | undefined => {
      const zoneId = zoneOfServer.get(serverId)
      if (zoneId === undefined) {
        return undefined
      }
      return collapsed ? structure.regionOfZone.get(zoneId) : zoneId
    }
    const resolveClusterId = (serverId: string): number | undefined => {
      const zoneId = zoneOfServer.get(serverId)
      if (zoneId === undefined) {
        return undefined
      }
      const regionId = structure.regionOfZone.get(zoneId)
      return regionId === undefined ? undefined : structure.clusterOfRegion.get(regionId)
    }
    let aggregatedRaw = 0
    for (const edge of allEdges) {
      const clusterId = resolveClusterId(edge.sourceServerId)
      const nodeId = resolveNodeId(edge.resolvedServerId)
      if (clusterId === undefined || nodeId === undefined) {
        continue
      }
      const key = keyOf(clusterId, nodeId)
      let link = map.get(key)
      if (!link) {
        // 跨集群消息链路：结构上不属于该集群的节点也可能有真实流量
        link = { clusterId, nodeId, total: 0, failNum: 0, worstEdge: null, rawCount: 0 }
        map.set(key, link)
      }
      link.total += edge.total
      link.failNum += edge.failed + edge.expired
      link.rawCount += 1
      if (!link.worstEdge || edge.failRatePercent > link.worstEdge.failRatePercent) {
        link.worstEdge = edge
      }
      aggregatedRaw += 1
    }
    const all = [...map.values()].map((l) => {
      const failRatePercent = l.total === 0 ? 0 : Math.round((l.failNum / l.total) * 1000) / 10
      return {
        key: keyOf(l.clusterId, l.nodeId),
        clusterId: l.clusterId,
        nodeId: l.nodeId,
        total: l.total,
        failRatePercent,
        abnormal: l.total > 0 && failRatePercent >= ABNORMAL_RATE,
        worstEdge: l.worstEdge,
        rawCount: l.rawCount,
        flow: false,
      }
    })
    // 异常优先、其次失败率与流量降序，截断后仍保留最值得关注的链路
    const sorted = all.sort(
      (a, b) =>
        Number(b.abnormal) - Number(a.abnormal) || b.failRatePercent - a.failRatePercent || b.total - a.total,
    )
    const truncated = sorted.length > MAX_RENDER_EDGES
    const capped = sorted.slice(0, MAX_RENDER_EDGES)
    // 仅前 MAX_FLOW_EDGES 条有流量的链路参与动画（异常已排最前）
    const links = capped.map((l, i) => ({ ...l, flow: i < MAX_FLOW_EDGES && l.total > 0 }))
    return {
      links,
      aggregatedRaw,
      statsLinks: links.filter((l) => l.total > 0).length,
      truncated,
      resolvedTotal: sorted.length,
    }
  }, [layout, tree, allEdges, zoneOfServer, collapsed, structure])
  const links = linkInfo.links

  // 渲染索引与派生量
  const nodeById = useMemo(() => new Map((layout?.nodes ?? []).map((n) => [n.id, n])), [layout])
  const proxyByCluster = useMemo(
    () => new Map((layout?.proxies ?? []).map((p) => [p.clusterId, p])),
    [layout],
  )
  const maxLinkTotal = useMemo(() => Math.max(1, ...links.map((l) => l.total)), [links])
  const serverTotal = useMemo(
    () =>
      (tree?.clusters ?? []).reduce(
        (sum, c) => sum + c.regions.reduce((rs, r) => rs + r.zones.reduce((zs, z) => zs + z.serverCount, 0), 0),
        0,
      ),
    [tree],
  )
  // 选中原始边所属的聚合链路（侧面板展示聚合上下文 + 图上高亮）
  const selectedLink = useMemo(() => {
    if (selection?.kind !== 'edge') {
      return null
    }
    return links.find((l) => l.worstEdge !== null && edgeKey(l.worstEdge) === selection.key) ?? null
  }, [links, selection])
  const selectedNode = selection?.kind === 'node' ? (nodeById.get(selection.id) ?? null) : null
  const selectedNodeHealth = selectedNode ? (healthOfNode.get(selectedNode.id) ?? null) : null

  // 健康占比说明文字的四段配置（节点下方 caption 与侧面板健康分布共用）
  const healthParts = (health: NodeHealth) =>
    [
      { key: 'ok', count: health.ok, color: 'var(--color-ok)', label: t('cluster.topology.graph.statusOk') },
      { key: 'warn', count: health.warn, color: 'var(--color-warn)', label: t('cluster.topology.graph.statusWarn') },
      { key: 'crit', count: health.crit, color: 'var(--color-crit)', label: t('cluster.topology.graph.statusCrit') },
      { key: 'off', count: health.off, color: 'var(--color-off)', label: t('cluster.topology.graph.statusOff') },
    ].filter((p) => p.count > 0)

  const linkWidth = (link: GraphLink): number => {
    if (link.total === 0) {
      return 1.1
    }
    const w = 1.6 + (link.total / maxLinkTotal) * 2.8
    return link.abnormal ? Math.max(w, 3) : w
  }

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      {/* 数据流动画的作用域样式：沿路径流动的虚线偏移动画（不引库） */}
      <style>{`
        @keyframes beacon-edge-flow { to { stroke-dashoffset: -24; } }
        .beacon-flow { animation: beacon-edge-flow 1.6s linear infinite; }
        .beacon-flow-fast { animation-duration: 0.8s; }
        @media (prefers-reduced-motion: reduce) { .beacon-flow { animation: none; } }
      `}</style>

      <div className="flex flex-wrap items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <Network className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('cluster.topology.graph.title')}</h2>
        {/* 概览 chips：代理在线 / 小区 / 子服 / 异常链路 */}
        {tree && !isEmpty && (
          <>
            <Badge variant="secondary" className="tnum">
              {t('cluster.topology.graph.chipProxy')} {proxyStat.online}/{proxyStat.total}
            </Badge>
            <Badge variant="secondary" className="tnum">
              {t('cluster.topology.graph.chipZone')} {zoneTotal}
            </Badge>
            <Badge variant="secondary" className="tnum">
              {t('cluster.topology.graph.chipServer')} {serverTotal}
            </Badge>
          </>
        )}
        {abnormalEdges.length > 0 && (
          <Badge variant="crit" className="tnum">
            {t('cluster.topology.edges.abnormal')} {abnormalEdges.length}
          </Badge>
        )}
        {/* 数据流动画开关：默认开，低速节制 */}
        <Button
          variant={flowOn ? 'default' : 'outline'}
          size="sm"
          className="ml-auto gap-1.5"
          onClick={() => {
            setFlowOn((v) => !v)
          }}
          aria-pressed={flowOn}
        >
          <Waves className="size-3.5" />
          {t('cluster.topology.graph.flowToggle')}
        </Button>
      </div>

      <AsyncSection isLoading={treeQuery.isLoading} isError={treeQuery.isError} error={treeQuery.error}>
        {isEmpty || !layout ? (
          <p className="rounded-lg border border-dashed border-border-strong px-4 py-8 text-center text-sm text-ink-3">
            {t('cluster.topology.graph.empty')}
          </p>
        ) : (
          <>
            {collapsed && (
              <p className="flex items-center gap-1.5 rounded-md border border-warn-bd bg-warn-bg px-3 py-2 text-sm text-warn">
                <TriangleAlert className="size-3.5" />
                {t('cluster.topology.graph.collapseHint')}
              </p>
            )}

            {/* 图区 + 右侧固定明细侧面板：非模态、图不 reflow */}
            <div className="flex items-start gap-3">
              {/* 放射拓扑画布：SVG 自绘 + 左下角图例悬浮卡 */}
              <div className="relative min-w-0 flex-1">
                <div className="overflow-x-auto pb-2">
                  <svg
                    width={layout.width}
                    height={layout.height}
                    viewBox={`0 0 ${String(layout.width)} ${String(layout.height)}`}
                    preserveAspectRatio="xMinYMin meet"
                    role="img"
                    aria-label={t('cluster.topology.graph.title')}
                    className="min-w-full"
                  >
                    <defs>
                      {/* 代理节点靛蓝渐变 */}
                      <linearGradient id="beacon-proxy-grad" x1="0" y1="0" x2="1" y2="1">
                        <stop offset="0%" stopColor="var(--color-brand)" />
                        <stop offset="100%" stopColor="var(--color-brand-600)" />
                      </linearGradient>
                    </defs>

                    {/* ===== 分区背景：极淡底色 + 虚线框 + 角落标签 ===== */}
                    {layout.groups.map((g) => (
                      <g key={`grp-${String(g.id)}`}>
                        <rect
                          x={g.x}
                          y={g.y}
                          width={g.w}
                          height={g.h}
                          rx={16}
                          fill="var(--color-brand)"
                          fillOpacity={0.03}
                          stroke="var(--color-brand-100)"
                          strokeDasharray="6 6"
                        />
                        <text x={g.x + 18} y={g.y + 26} fontSize={12} fontWeight={700} fill="var(--color-ink-3)">
                          {g.name}
                        </text>
                        <text x={g.x + 18} y={g.y + 26} dx={g.name.length * 13 + 10} fontSize={10} fill="var(--color-ink-4)">
                          {collapsed
                            ? t('cluster.topology.graph.clusterMeta', { regions: g.nodeCount, servers: g.serverCount })
                            : t('cluster.topology.graph.regionMeta', { zones: g.nodeCount, servers: g.serverCount })}
                        </text>
                      </g>
                    ))}

                    {/* ===== 代理层标题 ===== */}
                    <text x={PROXY_CX} y={PAD + 12} textAnchor="middle" fontSize={12} fontWeight={700} fill="var(--color-ink-3)">
                      {t('cluster.topology.graph.proxyLayerTitle')}
                    </text>
                    <text x={PROXY_CX} y={PAD + 28} textAnchor="middle" fontSize={10} fill="var(--color-ink-4)">
                      {t('cluster.topology.graph.proxyLayerSub', { online: proxyStat.online, total: proxyStat.total })}
                    </text>

                    {/* ===== 聚合链路：底线（粗细 ∝ 消息量）+ 流动虚线 + 异常失败率标签 ===== */}
                    {links.map((link, linkIndex) => {
                      const proxy = proxyByCluster.get(link.clusterId)
                      const node = nodeById.get(link.nodeId)
                      if (!proxy || !node) {
                        return null
                      }
                      const active = selectedLink !== null && selectedLink.key === link.key
                      const color = link.abnormal ? 'var(--color-crit)' : 'var(--color-brand)'
                      const d = linkPath(proxy, node)
                      const midX = (proxy.cx + PROXY_R + node.cx - NODE_R) / 2
                      const midY = (proxy.cy + node.cy) / 2
                      const width = linkWidth(link)
                      const worst = link.worstEdge
                      const label = `${String(link.failRatePercent)}% ${t('cluster.topology.edges.failRate')}`
                      return (
                        <g
                          key={`link-${link.key}`}
                          className={worst !== null ? 'cursor-pointer' : undefined}
                          role={worst !== null ? 'button' : undefined}
                          aria-label={worst !== null ? `${proxy.name} → ${node.name}` : undefined}
                          onClick={
                            worst !== null
                              ? () => {
                                  setSelection(active ? null : { kind: 'edge', key: edgeKey(worst) })
                                }
                              : undefined
                          }
                        >
                          <title>
                            {`${proxy.name} → ${node.name}`}
                            {link.total > 0 && ` · ${String(link.total)} · ${String(link.failRatePercent)}%`}
                          </title>
                          {/* 选中光晕：垫底 */}
                          {active && (
                            <path d={d} fill="none" stroke={color} strokeOpacity={0.12} strokeWidth={width + 9} strokeLinecap="round" />
                          )}
                          {/* 底层链路：正常品牌淡色、异常红色加粗（选中最实，未选异常适度收敛避免糊成一片） */}
                          <path
                            d={d}
                            fill="none"
                            stroke={color}
                            strokeWidth={width}
                            strokeOpacity={active ? 0.9 : link.abnormal ? 0.55 : 0.18}
                            strokeLinecap="round"
                          />
                          {/* 数据流动效：沿路径流动的虚线（仅有限条参与动画） */}
                          {flowOn && link.flow && (
                            <path
                              d={d}
                              fill="none"
                              stroke={color}
                              strokeWidth={Math.min(width, 2)}
                              strokeOpacity={link.abnormal ? 0.9 : 0.4}
                              strokeDasharray="4 9"
                              strokeLinecap="round"
                              className={cn('beacon-flow', link.abnormal && 'beacon-flow-fast')}
                            />
                          )}
                          {/* 异常链路失败率标签：只直显最差的前几条（选中的始终显示），防标签堆叠 */}
                          {link.abnormal && (linkIndex < MAX_EDGE_LABELS || active) && (
                            <g transform={`translate(${String(midX)}, ${String(midY)})`}>
                              <rect x={-46} y={-11} width={92} height={22} rx={11} fill="var(--color-card)" stroke="var(--color-crit-bd)" strokeWidth={active ? 1.5 : 1} />
                              <circle cx={-34} cy={0} r={3} fill="var(--color-crit)" />
                              <text x={5} y={3.5} textAnchor="middle" fontSize={10.5} fontWeight={700} fill="var(--color-crit)">
                                {label}
                              </text>
                            </g>
                          )}
                        </g>
                      )
                    })}

                    {/* ===== 代理节点（靛蓝、稍大、左列，每集群一个） ===== */}
                    {layout.proxies.map((p) => {
                      const stat = proxyStat.byCluster.get(p.clusterId)
                      const dotColor =
                        stat === undefined || stat.total === 0 || stat.online === 0
                          ? 'var(--color-off)'
                          : stat.online < stat.total
                            ? 'var(--color-warn)'
                            : 'var(--color-ok)'
                      return (
                        <g key={`p-${String(p.clusterId)}`}>
                          <title>{`${p.name} · ${t('cluster.topology.graph.proxyBadge', { count: p.proxyCount })}`}</title>
                          <circle cx={p.cx} cy={p.cy} r={PROXY_R} fill="url(#beacon-proxy-grad)" />
                          <circle cx={p.cx} cy={p.cy} r={PROXY_R} fill="none" stroke="var(--color-brand-600)" strokeOpacity={0.3} />
                          <text x={p.cx} y={p.cy - 2} textAnchor="middle" fontSize={12.5} fontWeight={700} fill="white">
                            {p.name}
                          </text>
                          <text x={p.cx} y={p.cy + 14} textAnchor="middle" fontSize={9.5} fill="rgba(255,255,255,.78)" className="tnum">
                            {t('cluster.topology.graph.proxyBadge', { count: p.proxyCount })}
                          </text>
                          <circle cx={p.cx + PROXY_R * 0.68} cy={p.cy - PROXY_R * 0.68} r={5.5} fill={dotColor} stroke="var(--color-card)" strokeWidth={2} />
                        </g>
                      )
                    })}

                    {/* ===== 小区 / 大区聚合节点：外环 = 健康占比，点击看该区概要 ===== */}
                    {layout.nodes.map((node) => {
                      const health = healthOfNode.get(node.id) ?? { ok: 0, warn: 0, crit: 0, off: 0 }
                      const segments = ringSegments(health)
                      const parts = healthParts(health)
                      const active = selection?.kind === 'node' && selection.id === node.id
                      return (
                        <g
                          key={`n-${String(node.id)}`}
                          className="cursor-pointer"
                          role="button"
                          aria-label={node.name}
                          onClick={() => {
                            setSelection(active ? null : { kind: 'node', id: node.id })
                          }}
                        >
                          <title>{`${node.name} · ${t('cluster.topology.graph.serverCountShort', { count: node.serverCount })}`}</title>
                          {/* 选中态：外圈虚线光环 */}
                          {active && (
                            <circle cx={node.cx} cy={node.cy} r={NODE_R + 9} fill="none" stroke="var(--color-brand)" strokeOpacity={0.5} strokeWidth={1.5} strokeDasharray="4 5" />
                          )}
                          <circle cx={node.cx} cy={node.cy} r={NODE_R} fill="var(--color-card)" stroke="var(--color-border)" />
                          {/* 健康占比环：无健康数据时保持中性描边 */}
                          {segments.map((seg) => (
                            <circle
                              key={`${String(node.id)}-${seg.color}`}
                              cx={node.cx}
                              cy={node.cy}
                              r={NODE_R}
                              fill="none"
                              stroke={seg.color}
                              strokeWidth={4.5}
                              pathLength={100}
                              strokeDasharray={`${seg.share.toFixed(2)} ${(100 - seg.share).toFixed(2)}`}
                              strokeDashoffset={seg.offset.toFixed(2)}
                            />
                          ))}
                          <text x={node.cx} y={node.cy - 2} textAnchor="middle" fontSize={node.name.length > 9 ? 9 : 11} fontWeight={700} fill="var(--color-ink-1)">
                            {node.name}
                          </text>
                          <text x={node.cx} y={node.cy + 13} textAnchor="middle" fontSize={9.5} fill="var(--color-ink-3)" className="tnum">
                            {t('cluster.topology.graph.serverCountShort', { count: node.serverCount })}
                          </text>
                          {/* 节点下方健康说明：仅列非零状态 */}
                          {parts.length > 0 && (
                            <text x={node.cx} y={node.cy + NODE_R + 18} textAnchor="middle" fontSize={9.5}>
                              {parts.map((part, i) => (
                                <tspan key={part.key} fill={part.color}>
                                  {i > 0 ? ' · ' : ''}
                                  {String(part.count)} {part.label}
                                </tspan>
                              ))}
                            </text>
                          )}
                        </g>
                      )
                    })}
                  </svg>
                </div>

                {/* ===== 图例（右下角悬浮卡，纯说明不拦截交互；避开左列代理节点） ===== */}
                <div className="pointer-events-none absolute right-3 bottom-3 w-[200px] rounded-lg border border-border bg-card/90 p-2.5 text-[10.5px] text-ink-3 shadow-card backdrop-blur-sm">
                  <p className="text-[9.5px] font-semibold tracking-[0.5px] text-ink-4 uppercase">
                    {t('cluster.topology.graph.legendTitle')}
                  </p>
                  <p className="mt-1.5 flex items-center gap-2">
                    <svg width="24" height="6" aria-hidden>
                      <line x1="1" y1="3" x2="23" y2="3" stroke="var(--color-brand)" strokeOpacity="0.3" strokeWidth="2.5" strokeLinecap="round" />
                    </svg>
                    {t('cluster.topology.graph.legendNormal', { rate: ABNORMAL_RATE })}
                  </p>
                  <p className="mt-1 flex items-center gap-2">
                    <svg width="24" height="6" aria-hidden>
                      <line x1="1" y1="3" x2="23" y2="3" stroke="var(--color-crit)" strokeWidth="3.5" strokeLinecap="round" />
                    </svg>
                    {t('cluster.topology.graph.legendAbnormal')}
                  </p>
                  <p className="mt-1 flex items-center gap-2">
                    <svg width="24" height="10" aria-hidden>
                      <line x1="1" y1="3" x2="23" y2="3" stroke="var(--color-ink-4)" strokeWidth="1" strokeLinecap="round" />
                      <line x1="1" y1="8" x2="23" y2="8" stroke="var(--color-ink-4)" strokeWidth="3.5" strokeLinecap="round" />
                    </svg>
                    {t('cluster.topology.graph.legendWidth')}
                  </p>
                  <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5">
                    <span className="inline-flex items-center gap-1">
                      <i className="size-2 rounded-full" style={{ background: 'var(--color-ok)' }} />
                      {t('cluster.topology.graph.statusOk')}
                    </span>
                    <span className="inline-flex items-center gap-1">
                      <i className="size-2 rounded-full" style={{ background: 'var(--color-warn)' }} />
                      {t('cluster.topology.graph.statusWarn')}
                    </span>
                    <span className="inline-flex items-center gap-1">
                      <i className="size-2 rounded-full" style={{ background: 'var(--color-crit)' }} />
                      {t('cluster.topology.graph.statusCrit')}
                    </span>
                    <span className="inline-flex items-center gap-1">
                      <i className="size-2 rounded-full" style={{ background: 'var(--color-off)' }} />
                      {t('cluster.topology.graph.statusOff')}
                    </span>
                  </p>
                  <p className="mt-1 flex items-center gap-2 text-ink-4">
                    <svg width="24" height="6" aria-hidden>
                      <line x1="1" y1="3" x2="23" y2="3" stroke="var(--color-brand)" strokeOpacity="0.5" strokeWidth="1.6" strokeDasharray="3 4" strokeLinecap="round" />
                    </svg>
                    {t('cluster.topology.graph.legendFlow')}
                  </p>
                </div>
              </div>

              {/* 固定右侧明细侧面板：常驻布局列，选中链路 / 节点在此展示，图不被推动 */}
              <aside className="w-[248px] shrink-0 self-start rounded-lg border border-border bg-surface-2 p-3">
                {selectedEdge ? (
                  <div className="grid gap-2.5 text-sm">
                    {/* 聚合链路上下文：集群 → 节点 + 聚合口径 */}
                    {selectedLink && (
                      <div className="grid gap-1 rounded-md border border-border bg-card px-2.5 py-2">
                        <p className="flex items-center gap-1 text-xs font-semibold text-ink-1">
                          <span className="truncate text-brand">{proxyByCluster.get(selectedLink.clusterId)?.name}</span>
                          <ArrowRight className="size-3 shrink-0 text-ink-4" />
                          <span className="truncate">{nodeById.get(selectedLink.nodeId)?.name}</span>
                        </p>
                        <p className="text-[10.5px] text-ink-4">
                          {t('cluster.topology.graph.linkAgg', { count: selectedLink.rawCount })}
                        </p>
                      </div>
                    )}
                    <p
                      className={cn(
                        'flex items-center gap-1 font-mono text-xs',
                        selectedEdge.failRatePercent >= ABNORMAL_RATE ? 'text-crit' : 'text-ink-2',
                      )}
                    >
                      <Boxes className="size-3.5 shrink-0 text-brand" />
                      <span className="truncate">{selectedEdge.sourceServerId}</span>
                      <ArrowRight className="size-3 shrink-0 text-ink-4" />
                      <span className="truncate">{selectedEdge.resolvedServerId}</span>
                    </p>
                    <div className="flex flex-wrap gap-1.5">
                      <Badge variant={selectedEdge.failRatePercent >= ABNORMAL_RATE ? 'crit' : 'secondary'} className="tnum">
                        {t('cluster.topology.edges.failRate')} {selectedEdge.failRatePercent}%
                      </Badge>
                      <Badge variant="secondary" className="tnum">
                        {t('cluster.topology.edges.total')} {selectedEdge.total}
                      </Badge>
                      <Badge variant="secondary" className="tnum">
                        P95 {selectedEdge.p95DurationMs}ms
                      </Badge>
                    </div>
                    {selectedEdge.topFailReasons.length > 0 && (
                      <div>
                        <p className="text-[11px] font-semibold tracking-[0.3px] text-ink-4 uppercase">
                          {t('cluster.topology.edges.topReasons')}
                        </p>
                        <ul className="mt-1 flex flex-wrap gap-1.5">
                          {selectedEdge.topFailReasons.map((reason) => (
                            <li key={reason.reason}>
                              <Badge variant="crit" className="tnum">
                                {reason.reason} · {reason.count}
                              </Badge>
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                    <div>
                      <p className="text-[11px] font-semibold tracking-[0.3px] text-ink-4 uppercase">
                        {t('cluster.topology.edges.sampleMessages')}
                      </p>
                      <ul className="mt-1 grid gap-0.5">
                        {selectedEdge.sampleMessageIds.map((id) => (
                          <li key={id} className="truncate font-mono text-xs text-ink-3">
                            {id}
                          </li>
                        ))}
                      </ul>
                    </div>
                  </div>
                ) : selectedNode ? (
                  <div className="grid gap-2.5 text-sm">
                    <p className="flex items-center gap-1.5 text-xs font-semibold text-ink-1">
                      <Boxes className="size-3.5 shrink-0 text-brand" />
                      <span className="truncate">{selectedNode.name}</span>
                    </p>
                    <p className="text-[11px] font-semibold tracking-[0.3px] text-ink-4 uppercase">
                      {collapsed ? t('cluster.topology.graph.nodeRegionTitle') : t('cluster.topology.graph.nodeZoneTitle')}
                    </p>
                    <div className="flex flex-wrap gap-1.5">
                      <Badge variant="secondary" className="tnum">
                        {t('cluster.topology.graph.nodeServers')} {selectedNode.serverCount}
                      </Badge>
                    </div>
                    <div>
                      <p className="text-[11px] font-semibold tracking-[0.3px] text-ink-4 uppercase">
                        {t('cluster.topology.graph.healthBreakdown')}
                      </p>
                      <ul className="mt-1 grid gap-1">
                        {(selectedNodeHealth ? healthParts(selectedNodeHealth) : []).map((part) => (
                          <li key={part.key} className="flex items-center gap-2 text-xs text-ink-2">
                            <i className="size-2 shrink-0 rounded-full" style={{ background: part.color }} />
                            <span>{part.label}</span>
                            <span className="tnum ml-auto font-semibold text-ink-1">{part.count}</span>
                          </li>
                        ))}
                      </ul>
                    </div>
                  </div>
                ) : (
                  <p className="py-6 text-center text-xs text-ink-4">{t('cluster.topology.graph.detailEmpty')}</p>
                )}
              </aside>
            </div>

            {collapsed && (
              <p className="text-[11px] text-ink-4">
                {t('cluster.topology.graph.truncated', { shown: MAX_ZONE_NODES, total: zoneTotal })}
              </p>
            )}
            {/* 聚合明示：上千条服务器间原始边已收敛为少量集群 → 小区链路 */}
            {linkInfo.aggregatedRaw > linkInfo.statsLinks && (
              <p className="text-[11px] text-ink-4">
                {t('cluster.topology.graph.edgesAggregated', {
                  raw: linkInfo.aggregatedRaw,
                  shown: linkInfo.statsLinks,
                })}
              </p>
            )}
            {/* 防御性截断明示：极端形态下仅画最值得关注的前 N 条 */}
            {linkInfo.truncated && (
              <p className="text-[11px] text-ink-4">
                {t('cluster.topology.graph.edgesTruncated', {
                  shown: links.length,
                  total: linkInfo.resolvedTotal,
                })}
              </p>
            )}

            {/* 异常链路快捷选择：点击 chip 在右侧侧面板看明细（与图上点边等效） */}
            {abnormalEdges.length > 0 && (
              <div className="grid gap-1.5 rounded-lg border border-crit-bd bg-crit-bg/40 p-3">
                <p className="flex items-center gap-1.5 text-[12px] font-semibold text-crit">
                  <TriangleAlert className="size-3.5" />
                  {t('cluster.topology.graph.abnormalLinks')}
                </p>
                <ul className="flex flex-wrap gap-1.5">
                  {abnormalEdges.slice(0, MAX_RENDER_EDGES).map((edge) => {
                    const key = edgeKey(edge)
                    const active = selection?.kind === 'edge' && selection.key === key
                    return (
                      <li key={key}>
                        <button
                          type="button"
                          onClick={() => {
                            setSelection(active ? null : { kind: 'edge', key })
                          }}
                          className={
                            'flex items-center gap-1 rounded-md border px-2 py-1 font-mono text-[11px] transition-colors ' +
                            (active
                              ? 'border-crit bg-card text-crit'
                              : 'border-crit-bd bg-card/70 text-ink-2 hover:border-crit')
                          }
                        >
                          {edge.sourceServerId}
                          <ArrowRight className="size-3 text-ink-4" />
                          {edge.resolvedServerId}
                          <Badge variant="crit" className="tnum">{edge.failRatePercent}%</Badge>
                        </button>
                      </li>
                    )
                  })}
                </ul>
              </div>
            )}
          </>
        )}
      </AsyncSection>
    </section>
  )
}
