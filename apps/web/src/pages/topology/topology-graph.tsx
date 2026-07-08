// 拓扑可视化图（自绘 SVG，不引新依赖）：分层布局 BC 集群 → 大区 → 小区（聚合子服计数），
// 层间连线呈现全局 BC↔子服链路结构。异常链路（失败率高的边）在图上高亮，点击看该链路明细。
// 1000+ 节点时按大区/小区聚合折叠（图只画到小区聚合粒度，不逐台画上千点），并明示截断边界。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ArrowRight, Boxes, Network, TriangleAlert } from 'lucide-react'

import { AsyncSection, Badge } from '@beacon/ui'
import type { MessageEdgeStat, ZoneTreeResponse } from '@beacon/devmock'

import { fetchMessageEdges, fetchZoneTree } from '../../api/cluster'

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

// 统计一棵树里的小区节点总数（判定是否需要折叠）
function countZones(tree: ZoneTreeResponse): number {
  return tree.clusters.reduce(
    (sum, cluster) => sum + cluster.regions.reduce((rs, region) => rs + region.zones.length, 0),
    0,
  )
}

export default function TopologyGraph({ namespaceId }: TopologyGraphProps) {
  const { t } = useTranslation()
  const treeQuery = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    placeholderData: keepPreviousData,
  })
  const edgesQuery = useQuery({ queryKey: ['message-edges'], queryFn: fetchMessageEdges })
  const tree = treeQuery.data

  // 被选中的异常链路（点击高亮边打开明细）
  const [selectedKey, setSelectedKey] = useState<string | null>(null)

  const zoneTotal = useMemo(() => (tree ? countZones(tree) : 0), [tree])
  const collapsed = zoneTotal > MAX_ZONE_NODES
  const isEmpty = tree?.clusters.length === 0

  // 异常边（失败率超阈值），按失败率降序，画在图顶部作为「异常链路层」
  const abnormalEdges = useMemo(
    () =>
      [...(edgesQuery.data?.edges ?? [])]
        .filter((e) => e.failRatePercent >= ABNORMAL_RATE)
        .sort((a, b) => b.failRatePercent - a.failRatePercent),
    [edgesQuery.data],
  )
  const edgeKey = (edge: MessageEdgeStat) => `${edge.sourceServerId}→${edge.resolvedServerId}`
  const selectedEdge = useMemo(
    () => abnormalEdges.find((e) => edgeKey(e) === selectedKey) ?? null,
    [abnormalEdges, selectedKey],
  )

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

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
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

            {/* 分层拓扑图：SVG 自绘，横向滚动容纳多集群列 */}
            <div className="overflow-x-auto pb-2">
              <svg
                width={layout.width}
                height={layout.height}
                viewBox={`0 0 ${String(layout.width)} ${String(layout.height)}`}
                role="img"
                aria-label={t('cluster.topology.graph.title')}
                className="min-w-full"
              >
                {/* 连线：集群 → 大区 → 小区（层次化，非力导向） */}
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

            {collapsed && (
              <p className="text-[11px] text-ink-4">
                {t('cluster.topology.graph.truncated', { shown: MAX_ZONE_NODES, total: zoneTotal })}
              </p>
            )}

            {/* 异常链路层：可视化高亮失败率高的边，点击看该链路明细 */}
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

                {/* 选中链路明细：样本消息 + 主要失败原因 */}
                {selectedEdge && (
                  <div className="mt-1 grid gap-2 rounded-md border border-border bg-card px-3 py-2.5 text-sm">
                    <p className="flex items-center gap-1 font-mono text-xs text-ink-2">
                      <Boxes className="size-3.5 text-brand" />
                      {selectedEdge.sourceServerId}
                      <ArrowRight className="size-3 text-ink-4" />
                      {selectedEdge.resolvedServerId}
                    </p>
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
                          <li key={id} className="font-mono text-xs text-ink-3">
                            {id}
                          </li>
                        ))}
                      </ul>
                    </div>
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </AsyncSection>
    </section>
  )
}
