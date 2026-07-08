// 拓扑可视化图（自绘 SVG，不引新依赖）：分层布局 BC 集群 → 大区 → 小区（聚合子服计数），
// 层间连线呈现结构；请求链路（消息边）直接画在小区↔小区之间——正常边中性/品牌色，
// 异常边 crit 红并带失败率标签与异常标记；连线上叠加轻量数据流动画（CSS stroke-dashoffset）。
// 选中某条异常链路 → 在图区右侧固定侧面板展示明细，图本身不 reflow、不跳动。
// 1000+ 节点时按大区聚合折叠（图只画到小区聚合粒度，不逐台画上千点），并明示截断边界。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ArrowRight, Boxes, Network, TriangleAlert, Waves } from 'lucide-react'

import { AsyncSection, Badge, Button, cn } from '@beacon/ui'
import type { MessageEdgeStat, ZoneTreeResponse } from '@beacon/devmock'

import { fetchMessageEdges, fetchServers, fetchZoneTree } from '../../api/cluster'

// 小区节点总数上限：超过则按大区聚合折叠，不再逐个渲染小区（避免一次渲染上千节点）
const MAX_ZONE_NODES = 24
// 边失败率阈值：超过即视为异常链路高亮
const ABNORMAL_RATE = 5

interface TopologyGraphProps {
  namespaceId: number
}

// 布局用的节点盒
interface Box {
  x: number
  y: number
  w: number
  h: number
}

// 画在图上的请求链路：两端已解析到某个已渲染节点盒（小区或折叠态的大区）
interface RenderEdge {
  key: string
  edge: MessageEdgeStat
  from: Box
  to: Box
  abnormal: boolean
}

// 统计一棵树里的小区节点总数（判定是否需要折叠）
function countZones(tree: ZoneTreeResponse): number {
  return tree.clusters.reduce(
    (sum, cluster) => sum + cluster.regions.reduce((rs, region) => rs + region.zones.length, 0),
    0,
  )
}

// 三次贝塞尔路径（自底部弯向另一节点顶部），让链路曲线不与结构线重叠、易辨识
function edgePath(from: Box, to: Box): string {
  const x1 = from.x + from.w / 2
  const y1 = from.y + from.h / 2
  const x2 = to.x + to.w / 2
  const y2 = to.y + to.h / 2
  const dx = (x2 - x1) / 2
  return `M ${String(x1)} ${String(y1)} C ${String(x1 + dx)} ${String(y1)}, ${String(x2 - dx)} ${String(y2)}, ${String(x2)} ${String(y2)}`
}

export default function TopologyGraph({ namespaceId }: TopologyGraphProps) {
  const { t } = useTranslation()
  const treeQuery = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    placeholderData: keepPreviousData,
  })
  const edgesQuery = useQuery({ queryKey: ['message-edges'], queryFn: fetchMessageEdges })
  // 用于把消息边两端 serverId 解析到所属小区（进而定位节点盒）
  const serversQuery = useQuery({
    queryKey: ['servers', 'topology', namespaceId],
    queryFn: () => fetchServers({ namespaceId, pageSize: 2000 }),
    placeholderData: keepPreviousData,
  })
  const tree = treeQuery.data

  // 被选中的请求链路（点击边或 chip 打开右侧明细）
  const [selectedKey, setSelectedKey] = useState<string | null>(null)
  // 数据流动画开关（默认开，节制低速）
  const [flowOn, setFlowOn] = useState(true)

  const zoneTotal = useMemo(() => (tree ? countZones(tree) : 0), [tree])
  const collapsed = zoneTotal > MAX_ZONE_NODES
  const isEmpty = tree?.clusters.length === 0

  const allEdges = edgesQuery.data?.edges ?? []
  // 异常边（失败率超阈值），按失败率降序，供 chip 列表与计数
  const abnormalEdges = useMemo(
    () =>
      [...allEdges]
        .filter((e) => e.failRatePercent >= ABNORMAL_RATE)
        .sort((a, b) => b.failRatePercent - a.failRatePercent),
    [allEdges],
  )
  const edgeKey = (edge: MessageEdgeStat) => `${edge.sourceServerId}→${edge.resolvedServerId}`
  const selectedEdge = useMemo(
    () => allEdges.find((e) => edgeKey(e) === selectedKey) ?? null,
    [allEdges, selectedKey],
  )

  // serverId → zoneId 映射（后端子服）：解析请求边两端所属小区
  const zoneOfServer = useMemo(() => {
    const map = new Map<string, number>()
    for (const s of serversQuery.data?.items ?? []) {
      if (s.kind === 'backend' && s.zoneId !== null) {
        map.set(s.serverId, s.zoneId)
      }
    }
    return map
  }, [serversQuery.data])

  // 分层布局：为 SVG 计算各层节点坐标。
  // 第 0 层：BC 集群；第 1 层：大区；第 2 层：小区聚合（折叠态只到大区）。
  const layout = useMemo(() => {
    if (!tree || tree.clusters.length === 0) {
      return null
    }
    const COL_W = 168
    const GAP_X = 28
    const NODE_H = 46
    const ROW_GAP = 18
    const LAYER_GAP_Y = 90
    const PAD = 16

    // 每个集群一列；列内纵向堆叠大区（及其小区）。
    const clusterBoxes: { id: number; name: string; proxyCount: number; box: Box }[] = []
    const regionBoxes: { id: number; clusterId: number; name: string; serverCount: number; box: Box }[] = []
    const zoneBoxes: { id: number; regionId: number; name: string; serverCount: number; box: Box }[] = []

    let maxColHeight = 0
    tree.clusters.forEach((cluster, ci) => {
      const colX = PAD + ci * (COL_W + GAP_X)
      // 集群节点在列顶
      clusterBoxes.push({
        id: cluster.id,
        name: cluster.name,
        proxyCount: cluster.proxyCount,
        box: { x: colX, y: PAD, w: COL_W, h: NODE_H },
      })
      // 大区层从集群下方开始
      let cursorY = PAD + NODE_H + LAYER_GAP_Y
      for (const region of cluster.regions) {
        const regionServerCount = region.zones.reduce((s, z) => s + z.serverCount, 0)
        regionBoxes.push({
          id: region.id,
          clusterId: cluster.id,
          name: region.name,
          serverCount: regionServerCount,
          box: { x: colX, y: cursorY, w: COL_W, h: NODE_H },
        })
        cursorY += NODE_H + ROW_GAP
        // 展开态：小区聚合节点挂在大区下（缩进半列宽），折叠态省略
        if (!collapsed) {
          for (const zone of region.zones) {
            zoneBoxes.push({
              id: zone.id,
              regionId: region.id,
              name: zone.name,
              serverCount: zone.serverCount,
              box: { x: colX + 14, y: cursorY, w: COL_W - 14, h: 30 },
            })
            cursorY += 30 + 8
          }
          cursorY += 6
        }
      }
      maxColHeight = Math.max(maxColHeight, cursorY)
    })

    const width = PAD * 2 + tree.clusters.length * COL_W + (tree.clusters.length - 1) * GAP_X
    const height = maxColHeight + PAD
    return { clusterBoxes, regionBoxes, zoneBoxes, width, height }
  }, [tree, collapsed])

  // 请求链路边：把消息边两端解析到节点盒（展开态到小区盒；折叠态到所属大区盒），画在图上。
  const renderEdges = useMemo<RenderEdge[]>(() => {
    if (!layout) {
      return []
    }
    // 小区 id → 盒；小区 id → 大区盒（折叠态用）
    const zoneBoxById = new Map(layout.zoneBoxes.map((z) => [z.id, z.box]))
    const regionOfZone = new Map<number, number>()
    for (const cluster of tree?.clusters ?? []) {
      for (const region of cluster.regions) {
        for (const zone of region.zones) {
          regionOfZone.set(zone.id, region.id)
        }
      }
    }
    const regionBoxById = new Map(layout.regionBoxes.map((r) => [r.id, r.box]))

    // 解析某 serverId 到当前粒度下的节点盒
    const resolveBox = (serverId: string): Box | null => {
      const zoneId = zoneOfServer.get(serverId)
      if (zoneId === undefined) {
        return null
      }
      if (collapsed) {
        const regionId = regionOfZone.get(zoneId)
        return regionId === undefined ? null : (regionBoxById.get(regionId) ?? null)
      }
      return zoneBoxById.get(zoneId) ?? null
    }

    const result: RenderEdge[] = []
    for (const edge of allEdges) {
      if (edge.resolvedServerId === '') {
        continue
      }
      const from = resolveBox(edge.sourceServerId)
      const to = resolveBox(edge.resolvedServerId)
      // 两端可定位且不是同一节点（同节点自环不画）
      if (from === null || to === null || from === to) {
        continue
      }
      result.push({
        key: edgeKey(edge),
        edge,
        from,
        to,
        abnormal: edge.failRatePercent >= ABNORMAL_RATE,
      })
    }
    return result
  }, [layout, allEdges, zoneOfServer, collapsed, tree])

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      {/* 数据流动画的作用域样式：沿路径流动的虚线偏移动画（不引库） */}
      <style>{`
        @keyframes beacon-edge-flow { to { stroke-dashoffset: -24; } }
        .beacon-flow { animation: beacon-edge-flow 1.4s linear infinite; }
        .beacon-flow-fast { animation-duration: 0.7s; }
        @media (prefers-reduced-motion: reduce) { .beacon-flow { animation: none; } }
      `}</style>

      <div className="flex items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <Network className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('cluster.topology.graph.title')}</h2>
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
              {/* 分层拓扑图：SVG 自绘，横向滚动容纳多集群列 */}
              <div className="min-w-0 flex-1 overflow-x-auto pb-2">
                <svg
                  width={layout.width}
                  height={layout.height}
                  viewBox={`0 0 ${String(layout.width)} ${String(layout.height)}`}
                  role="img"
                  aria-label={t('cluster.topology.graph.title')}
                  className="min-w-full"
                >
                  {/* 结构连线：集群 → 大区 → 小区（层次化，非力导向） */}
                  {layout.regionBoxes.map((region) => {
                    const cluster = layout.clusterBoxes.find((c) => c.id === region.clusterId)
                    if (!cluster) return null
                    return (
                      <line
                        key={`cl-${String(region.id)}`}
                        x1={cluster.box.x + cluster.box.w / 2}
                        y1={cluster.box.y + cluster.box.h}
                        x2={region.box.x + region.box.w / 2}
                        y2={region.box.y}
                        stroke="var(--color-border-strong)"
                        strokeWidth={1.5}
                      />
                    )
                  })}
                  {layout.zoneBoxes.map((zone) => {
                    const region = layout.regionBoxes.find((r) => r.id === zone.regionId)
                    if (!region) return null
                    return (
                      <line
                        key={`rz-${String(zone.id)}`}
                        x1={region.box.x + 16}
                        y1={region.box.y + region.box.h}
                        x2={zone.box.x + 8}
                        y2={zone.box.y + zone.box.h / 2}
                        stroke="var(--color-border)"
                        strokeWidth={1}
                      />
                    )
                  })}

                  {/* 请求链路边：直接画在节点间，按状态着色 + 数据流动画 */}
                  {renderEdges.map((re) => {
                    const active = re.key === selectedKey
                    const color = re.abnormal ? 'var(--color-crit)' : 'var(--color-brand)'
                    const d = edgePath(re.from, re.to)
                    const midX = (re.from.x + re.from.w / 2 + re.to.x + re.to.w / 2) / 2
                    const midY = (re.from.y + re.from.h / 2 + re.to.y + re.to.h / 2) / 2
                    return (
                      <g
                        key={`edge-${re.key}`}
                        className="cursor-pointer"
                        onClick={() => {
                          setSelectedKey(active ? null : re.key)
                        }}
                      >
                        {/* 底层静态链路：正常细淡、异常粗红；选中加粗 */}
                        <path
                          d={d}
                          fill="none"
                          stroke={color}
                          strokeWidth={re.abnormal ? 2 : 1.25}
                          strokeOpacity={active ? 0.9 : re.abnormal ? 0.7 : 0.35}
                        />
                        {/* 数据流动效：沿路径流动的虚线（正常品牌 / 异常红） */}
                        {flowOn && (
                          <path
                            d={d}
                            fill="none"
                            stroke={color}
                            strokeWidth={re.abnormal ? 2.5 : 1.5}
                            strokeDasharray="4 8"
                            strokeLinecap="round"
                            className={cn('beacon-flow', re.abnormal && 'beacon-flow-fast')}
                            opacity={0.85}
                          />
                        )}
                        {/* 异常链路标记：失败率标签 + 三角警示 */}
                        {re.abnormal && (
                          <g transform={`translate(${String(midX)}, ${String(midY)})`}>
                            <rect x={-20} y={-9} width={40} height={16} rx={8} fill="var(--color-crit)" />
                            <text x={0} y={3} textAnchor="middle" fill="white" fontSize={10} fontWeight={700}>
                              {re.edge.failRatePercent}%
                            </text>
                          </g>
                        )}
                      </g>
                    )
                  })}

                  {/* 集群节点（含代理数） */}
                  {layout.clusterBoxes.map((c) => (
                    <g key={`c-${String(c.id)}`}>
                      <rect
                        x={c.box.x}
                        y={c.box.y}
                        width={c.box.w}
                        height={c.box.h}
                        rx={8}
                        fill="var(--color-brand-50)"
                        stroke="var(--color-brand-100)"
                      />
                      <text x={c.box.x + 12} y={c.box.y + 20} fill="var(--color-brand-600)" fontSize={12} fontWeight={600}>
                        {c.name}
                      </text>
                      <text x={c.box.x + 12} y={c.box.y + 36} fill="var(--color-brand)" fontSize={10}>
                        {t('cluster.topology.graph.proxy')} · {c.proxyCount}
                      </text>
                    </g>
                  ))}

                  {/* 大区节点（含聚合子服计数） */}
                  {layout.regionBoxes.map((r) => (
                    <g key={`r-${String(r.id)}`}>
                      <rect
                        x={r.box.x}
                        y={r.box.y}
                        width={r.box.w}
                        height={r.box.h}
                        rx={6}
                        fill="var(--color-surface-2)"
                        stroke="var(--color-border)"
                      />
                      <text x={r.box.x + 10} y={r.box.y + 19} fill="var(--color-ink-1)" fontSize={11} fontWeight={600}>
                        {r.name}
                      </text>
                      <text x={r.box.x + 10} y={r.box.y + 34} fill="var(--color-ink-4)" fontSize={10}>
                        {t('cluster.topology.graph.zone')} · {r.serverCount}
                      </text>
                    </g>
                  ))}

                  {/* 小区聚合节点（展开态） */}
                  {layout.zoneBoxes.map((z) => (
                    <g key={`z-${String(z.id)}`}>
                      <rect
                        x={z.box.x}
                        y={z.box.y}
                        width={z.box.w}
                        height={z.box.h}
                        rx={5}
                        fill="var(--color-card)"
                        stroke="var(--color-border)"
                      />
                      <rect x={z.box.x} y={z.box.y} width={3} height={z.box.h} fill="var(--color-brand)" />
                      <text x={z.box.x + 10} y={z.box.y + 19} fill="var(--color-ink-1)" fontSize={10}>
                        {z.name}
                      </text>
                      <text x={z.box.x + z.box.w - 8} y={z.box.y + 19} textAnchor="end" fill="var(--color-ink-4)" fontSize={10}>
                        {z.serverCount}
                      </text>
                    </g>
                  ))}
                </svg>
              </div>

              {/* 固定右侧明细侧面板：常驻布局列，选中链路在此展示，图不被推动 */}
              <aside className="w-[248px] shrink-0 self-start rounded-lg border border-border bg-surface-2 p-3">
                {selectedEdge ? (
                  <div className="grid gap-2.5 text-sm">
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

            {/* 异常链路快捷选择：点击 chip 在右侧侧面板看明细（与图上点边等效） */}
            {abnormalEdges.length > 0 && (
              <div className="grid gap-1.5 rounded-lg border border-crit-bd bg-crit-bg/40 p-3">
                <p className="flex items-center gap-1.5 text-[12px] font-semibold text-crit">
                  <TriangleAlert className="size-3.5" />
                  {t('cluster.topology.graph.abnormalLinks')}
                </p>
                <ul className="flex flex-wrap gap-1.5">
                  {abnormalEdges.map((edge) => {
                    const key = edgeKey(edge)
                    const active = key === selectedKey
                    return (
                      <li key={key}>
                        <button
                          type="button"
                          onClick={() => { setSelectedKey(active ? null : key) }}
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
