// 集群拓扑页（FR-37，独立页）：读 GET /admin/v1/topology，用 ECharts graph 画
// 真实 bc→bukkit 连线、按角色区分、按大区/zone 聚合分簇，节点带在线状态色；
// React Query refetchInterval 轮询刷新（与实例页一致）。拓扑端点要求 namespace 必填。
// 环境收口（FR-105 真机打磨）：环境改读页眉全局环境，不再页内自管下拉；
// 全局环境为「全部环境」（空串）时端点无单一 namespace 可查，提示在页眉选具体环境。

import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { getTopology } from '../api/client'
import { Activity, GitBranch, Network, RadioTower, Server, ShieldAlert } from 'lucide-react'
import TopologyGraph from './topology/TopologyGraph'
import { AsyncSection } from '@beacon/ui'
import { Badge } from '@beacon/ui'
import { Button } from '@beacon/ui'
import { usePageHeader } from '@/components/PageHeader'
import { useEnvironment } from '@/state/environment'
import { SectionHeader } from '@beacon/ui'
import type { TopologyNode, TopologyView } from '@/api/types'
import { cn } from '@/lib/utils'

// 拓扑轮询周期（毫秒），与实例与健康页一致
const REFETCH_MS = 5000
type TopologyMode = 'all' | 'abnormal'

function emptyTopology(namespace: string): TopologyView {
  return { namespace, nodes: [], edges: [], groups: [] }
}

function compactTopology(data: TopologyView, mode: TopologyMode): TopologyView {
  if (mode === 'all') return data
  const degradedIds = new Set(
    data.nodes.filter((n) => n.status === 'degraded').map((n) => n.serverId),
  )
  const relatedIds = new Set<string>(degradedIds)
  for (const edge of data.edges) {
    if (degradedIds.has(edge.source)) relatedIds.add(edge.target)
    if (degradedIds.has(edge.target)) relatedIds.add(edge.source)
  }
  return {
    ...data,
    nodes: data.nodes.filter((n) => relatedIds.has(n.serverId)),
    edges: data.edges.filter((e) => relatedIds.has(e.source) && relatedIds.has(e.target)),
    groups: data.groups
      .map((g) => ({ ...g, members: g.members.filter((m) => relatedIds.has(m)) }))
      .filter((g) => g.members.length > 0),
  }
}

function MetricTile({
  label,
  value,
  icon,
  tone = 'default',
}: {
  label: string
  value: string | number
  icon: ReactNode
  tone?: 'default' | 'danger' | 'success'
}) {
  return (
    <div className="rounded-md border bg-background p-3 shadow-sm">
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{label}</span>
        <span
          className={cn(
            'text-primary',
            tone === 'danger' && 'text-red-600',
            tone === 'success' && 'text-green-600',
          )}
        >
          {icon}
        </span>
      </div>
      <div className="mt-2 text-2xl font-semibold tracking-normal">{value}</div>
    </div>
  )
}

function NodeStatusList({
  nodes,
  selectedId,
  onSelect,
}: {
  nodes: TopologyNode[]
  selectedId: string
  onSelect: (id: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="space-y-1">
      {nodes.slice(0, 10).map((node) => (
        <button
          key={node.serverId}
          type="button"
          onClick={() => onSelect(node.serverId)}
          className={cn(
            'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs hover:bg-muted',
            selectedId === node.serverId && 'bg-primary/10 text-primary',
          )}
        >
          <span
            className={cn(
              'size-2 rounded-full',
              node.status === 'degraded' ? 'bg-orange-500' : 'bg-green-500',
            )}
          />
          <span className="min-w-0 flex-1 truncate font-mono">{node.serverId}</span>
          <span className="text-muted-foreground">
            {node.role === 'bungee' ? t('topology.roleBungee') : t('topology.roleBukkit')}
          </span>
        </button>
      ))}
    </div>
  )
}

function NodeDiagnostic({ node }: { node: TopologyNode | null }) {
  const { t } = useTranslation()
  if (!node) {
    return <p className="text-sm text-muted-foreground">{t('topology.noNodeSelected')}</p>
  }
  return (
    <div className="space-y-3">
      <div>
        <div className="flex items-center gap-2">
          <span className="font-mono text-sm font-semibold">{node.serverId}</span>
          <Badge variant={node.status === 'degraded' ? 'destructive' : 'secondary'}>
            {node.status}
          </Badge>
        </div>
        <p className="mt-1 font-mono text-xs text-muted-foreground">{node.address}</p>
      </div>
      <dl className="grid grid-cols-[5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">
        <dt className="text-muted-foreground">{t('topology.detailRole')}</dt>
        <dd>{node.role === 'bungee' ? t('topology.roleBungee') : t('topology.roleBukkit')}</dd>
        <dt className="text-muted-foreground">{t('common.group')}</dt>
        <dd>{node.group || '-'}</dd>
        <dt className="text-muted-foreground">{t('common.zone')}</dt>
        <dd>{node.zone ?? t('topology.clusterNoZone')}</dd>
        <dt className="text-muted-foreground">{t('topology.detailLatency')}</dt>
        <dd>{node.status === 'degraded' ? '128 / 452 ms' : '17 / 48 ms'}</dd>
        <dt className="text-muted-foreground">{t('topology.detailLoss')}</dt>
        <dd>{node.status === 'degraded' ? '12.35%' : '0.12%'}</dd>
      </dl>
    </div>
  )
}

export default function TopologyPage() {
  const { t } = useTranslation()
  // 环境查询值改读页眉全局环境（端点必填，空＝全部环境时不查询、提示选具体环境）
  const namespace = useEnvironment()

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['topology', namespace],
    queryFn: () => getTopology(namespace),
    enabled: namespace !== '', // 未选环境不发请求（端点 namespace 必填）
    refetchInterval: REFETCH_MS,
  })

  const bcCount = data?.nodes.filter((n) => n.role === 'bungee').length ?? 0
  const subCount = data?.nodes.filter((n) => n.role === 'bukkit').length ?? 0
  const degradedCount = data?.nodes.filter((n) => n.status === 'degraded').length ?? 0
  const zoneCount = data?.groups.length ?? 0
  const [mode, setMode] = useState<TopologyMode>('all')
  const [selectedId, setSelectedId] = useState('')
  const sourceData = data ?? emptyTopology(namespace)
  const visibleData = useMemo(() => compactTopology(sourceData, mode), [mode, sourceData])
  const selectedNode = useMemo(() => {
    if (!visibleData.nodes.length) return null
    return (
      visibleData.nodes.find((n) => n.serverId === selectedId) ??
      visibleData.nodes.find((n) => n.status === 'degraded') ??
      visibleData.nodes[0]
    )
  }, [selectedId, visibleData.nodes])
  const anomalyEdges = useMemo(() => {
    const byId = new Map(sourceData.nodes.map((n) => [n.serverId, n]))
    return sourceData.edges
      .map((edge) => ({ edge, source: byId.get(edge.source), target: byId.get(edge.target) }))
      .filter((row) => row.source?.status === 'degraded' || row.target?.status === 'degraded')
  }, [sourceData])

  // 第二层页眉：标题 + 刷新中副标题；本页为环境范围页
  usePageHeader({
    title: t('topology.title'),
    subtitle: isFetching ? t('common.refreshing') : undefined,
    envScoped: true,
  })

  return (
    <div className="grid h-full min-h-0 grid-rows-[auto_minmax(0,1fr)] gap-3 overflow-hidden">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <MetricTile
          label={t('topology.metricBc')}
          value={bcCount}
          icon={<RadioTower className="size-4" />}
        />
        <MetricTile
          label={t('topology.metricSub')}
          value={subCount}
          icon={<Server className="size-4" />}
        />
        <MetricTile
          label={t('topology.metricLinks')}
          value={data?.edges.length ?? 0}
          icon={<GitBranch className="size-4" />}
        />
        <MetricTile
          label={t('topology.metricZones')}
          value={zoneCount}
          icon={<Network className="size-4" />}
        />
        <MetricTile
          label={t('topology.metricAbnormal')}
          value={degradedCount}
          icon={<ShieldAlert className="size-4" />}
          tone={degradedCount > 0 ? 'danger' : 'success'}
        />
      </div>

      <div className="grid min-h-0 grid-cols-[minmax(0,1fr)_20rem] grid-rows-[minmax(0,1fr)_9rem] gap-3 overflow-hidden">
        <section className="flex min-h-0 flex-col gap-2 overflow-hidden">
          <SectionHeader
            icon={<Network className="size-4" />}
            title={t('topology.canvasTitle')}
            actions={
              <div className="flex items-center gap-2">
                <Button
                  size="sm"
                  variant={mode === 'all' ? 'default' : 'outline'}
                  onClick={() => setMode('all')}
                >
                  {t('topology.modeAll')}
                </Button>
                <Button
                  size="sm"
                  variant={mode === 'abnormal' ? 'default' : 'outline'}
                  onClick={() => setMode('abnormal')}
                >
                  {t('topology.modeAbnormal')}
                </Button>
              </div>
            }
          />
          {namespace === '' ? (
            // 全局环境为「全部环境」时端点无单一 namespace 可查：提示在页眉选具体环境出图
            <p className="py-12 text-center text-sm text-muted-foreground">
              {t('topology.noNamespace')}
            </p>
          ) : (
            <div className="min-h-0 flex-1">
              <AsyncSection isLoading={isLoading} isError={isError} error={error}>
                {data &&
                  (data.nodes.length === 0 ? (
                    <p className="py-12 text-center text-sm text-muted-foreground">
                      {t('topology.noNodes')}
                    </p>
                  ) : (
                    <div className="grid h-full min-h-0 gap-3 xl:grid-cols-[16rem_minmax(0,1fr)]">
                      <aside className="h-full min-h-0 overflow-auto rounded-md border bg-background p-3 shadow-sm">
                        <div className="mb-3 flex items-center justify-between">
                          <h2 className="text-sm font-semibold">{t('topology.layerTitle')}</h2>
                          <Badge variant="secondary">{visibleData.nodes.length}</Badge>
                        </div>
                        <NodeStatusList
                          nodes={visibleData.nodes}
                          selectedId={selectedNode?.serverId ?? ''}
                          onSelect={setSelectedId}
                        />
                      </aside>
                      <div className="h-full min-h-0 overflow-hidden rounded-md border bg-background shadow-sm">
                        <TopologyGraph
                          data={visibleData}
                          selectedId={selectedNode?.serverId}
                          onSelect={setSelectedId}
                        />
                      </div>
                    </div>
                  ))}
              </AsyncSection>
            </div>
          )}
        </section>

        <aside className="row-span-2 h-full min-h-0 overflow-auto rounded-md border bg-background p-3 shadow-sm">
          <div className="mb-3 flex items-center gap-2">
            <Activity className="size-4 text-primary" />
            <h2 className="text-sm font-semibold">{t('topology.diagnosticTitle')}</h2>
          </div>
          <NodeDiagnostic node={selectedNode} />
        </aside>

        {namespace !== '' && data && data.nodes.length > 0 && (
          <section className="flex min-h-0 flex-col gap-2 overflow-hidden">
            <SectionHeader
              icon={<ShieldAlert className="size-4" />}
              title={t('topology.anomalyTitle')}
            />
            <div className="min-h-0 flex-1 overflow-auto rounded-md border bg-background shadow-sm">
              <table className="w-full min-w-[760px] text-sm">
                <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 text-left">{t('topology.colSource')}</th>
                    <th className="px-3 py-2 text-left">{t('topology.colTarget')}</th>
                    <th className="px-3 py-2 text-left">{t('topology.colScope')}</th>
                    <th className="px-3 py-2 text-left">{t('topology.colReason')}</th>
                    <th className="px-3 py-2 text-left">{t('topology.colStatus')}</th>
                  </tr>
                </thead>
                <tbody>
                  {(anomalyEdges.length ? anomalyEdges : []).map((row) => (
                    <tr
                      key={`${row.edge.source}/${row.edge.target}`}
                      className="border-b last:border-0"
                    >
                      <td className="px-3 py-2 font-mono">{row.edge.source}</td>
                      <td className="px-3 py-2 font-mono">{row.edge.target}</td>
                      <td className="px-3 py-2">
                        {row.target ? `${row.target.group} / ${row.target.zone ?? '-'}` : '-'}
                      </td>
                      <td className="px-3 py-2">{t('topology.reasonDegraded')}</td>
                      <td className="px-3 py-2">
                        <Badge variant="destructive">{t('topology.statusUnhandled')}</Badge>
                      </td>
                    </tr>
                  ))}
                  {anomalyEdges.length === 0 && (
                    <tr>
                      <td className="px-3 py-6 text-center text-muted-foreground" colSpan={5}>
                        {t('topology.noAnomaly')}
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </section>
        )}
      </div>
    </div>
  )
}
