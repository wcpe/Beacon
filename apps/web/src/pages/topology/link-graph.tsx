// BC-子服链路图：用 zone-tree 结构 + server 计数以 DOM 网格自绘（不引新依赖）。
// 每个 BC 集群一列，列内代理节点 + 各大区（小区聚合子服计数）。
// 节点过多（超大量态）时按大区聚合折叠，并明示截断边界，禁止一次渲染 1200 节点。

import { useMemo } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Boxes, Network, TriangleAlert } from 'lucide-react'

import { AsyncSection, Badge } from '@beacon/ui'
import type { ZoneTreeResponse } from '@beacon/devmock'

import { fetchZoneTree } from '../../api/cluster'

// 小区节点总数上限：超过则按大区聚合折叠，不再逐个渲染小区（避免一次渲染上千节点）
const MAX_ZONE_NODES = 24

interface LinkGraphProps {
  namespaceId: number
}

// 统计一棵树里的小区节点总数（判定是否需要折叠）
function countZones(tree: ZoneTreeResponse): number {
  return tree.clusters.reduce(
    (sum, cluster) => sum + cluster.regions.reduce((rs, region) => rs + region.zones.length, 0),
    0,
  )
}

export default function LinkGraph({ namespaceId }: LinkGraphProps) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    // namespace 作用域切换时保留上一份结果，避免链路图短暂闪回加载态
    placeholderData: keepPreviousData,
  })
  const tree = query.data

  // 小区总数：决定是否折叠到大区粒度
  const zoneTotal = useMemo(() => (tree ? countZones(tree) : 0), [tree])
  const collapsed = zoneTotal > MAX_ZONE_NODES

  const isEmpty = tree?.clusters.length === 0

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <Network className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('cluster.topology.graph.title')}</h2>
      </div>
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        {isEmpty ? (
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
            <div className="flex gap-3 overflow-x-auto pb-2">
              {tree?.clusters.map((cluster) => {
                return (
                  <div key={cluster.id} className="min-w-56 shrink-0 overflow-hidden rounded-lg border border-border bg-card shadow-card">
                    {/* 集群节点（含代理数） */}
                    <div className="flex items-center gap-2 border-b border-border bg-brand-50 px-3 py-2 text-[12.5px] font-semibold text-brand-600">
                      <Boxes className="size-3.5" />
                      <span>{cluster.name}</span>
                      <Badge variant="brand" className="ml-auto tnum">
                        {t('cluster.topology.graph.proxy')} {cluster.proxyCount}
                      </Badge>
                    </div>
                    <div className="grid gap-1.5 p-2">
                      {cluster.regions.map((region) => {
                        const regionServerCount = region.zones.reduce((s, z) => s + z.serverCount, 0)
                        return (
                          <div key={region.id} className="rounded-md border border-border bg-surface-2 px-2 py-1.5">
                            <div className="flex items-center justify-between text-xs font-semibold text-ink-1">
                              <span>{region.name}</span>
                              <span className="text-ink-4 tnum">
                                {t('cluster.topology.graph.zone')} · {regionServerCount}
                              </span>
                            </div>
                            {/* 折叠态只展示大区聚合，展开态列出各小区（连线用左侧品牌色条示意） */}
                            {!collapsed && (
                              <ul className="mt-1.5 flex flex-wrap gap-1">
                                {region.zones.slice(0, MAX_ZONE_NODES).map((zone) => (
                                  <li
                                    key={zone.id}
                                    className="rounded border-l-2 border-l-brand bg-card px-1.5 py-0.5 text-[11px] font-mono text-ink-1"
                                  >
                                    {zone.name}
                                    <span className="ml-1 text-ink-4 tnum">{zone.serverCount}</span>
                                  </li>
                                ))}
                              </ul>
                            )}
                          </div>
                        )
                      })}
                    </div>
                  </div>
                )
              })}
            </div>
            {collapsed && (
              <p className="text-[11px] text-ink-4">
                {t('cluster.topology.graph.truncated', { shown: MAX_ZONE_NODES, total: zoneTotal })}
              </p>
            )}
          </>
        )}
      </AsyncSection>
    </section>
  )
}
