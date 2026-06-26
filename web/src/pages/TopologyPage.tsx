// 集群拓扑页（FR-37，独立页）：读 GET /admin/v1/topology，用 ECharts graph 画
// 真实 bc→bukkit 连线、按角色区分、按大区/zone 聚合分簇，节点带在线状态色；
// React Query refetchInterval 轮询刷新（与实例页一致）。拓扑端点要求 namespace 必填。
// 环境收口（FR-105 真机打磨）：环境改读页眉全局环境，不再页内自管下拉；
// 全局环境为「全部环境」（空串）时端点无单一 namespace 可查，提示在页眉选具体环境。

import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { getTopology } from '../api/client'
import { Network } from 'lucide-react'
import TopologyGraph from './topology/TopologyGraph'
import AsyncSection from '@/components/AsyncSection'
import { usePageHeader } from '@/components/PageHeader'
import { useEnvironment } from '@/state/environment'
import SectionHeader from '@/components/SectionHeader'

// 拓扑轮询周期（毫秒），与实例与健康页一致
const REFETCH_MS = 5000

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

  // 第二层页眉：标题 + 刷新中副标题；本页为环境范围页
  usePageHeader({
    title: t('topology.title'),
    subtitle: isFetching ? t('common.refreshing') : undefined,
    envScoped: true,
  })

  return (
    <div className="space-y-6">
      {/* 图例段（FR-107 卡片降级 + FR-105 真机打磨）：环境已收口至页眉全局环境槽，本段仅保留图例 + 计数。 */}
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm text-muted-foreground">
        <span className="flex items-center gap-1.5">
          <span className="inline-block h-3 w-3 rounded-sm bg-[#7c3aed]" />
          {t('topology.legendBc', { count: bcCount })}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="inline-block h-3 w-3 rounded-full bg-[#2563eb]" />
          {t('topology.legendSub', { count: subCount })}
        </span>
        <span>{t('topology.legendEdge')}</span>
      </div>

      {/* 画布段（FR-107 卡片降级）：区段标题 + 细线轻分隔，TopologyGraph（ECharts，FR-37）数据 / 交互 / 轮询不动 */}
      <section className="space-y-3">
        <SectionHeader icon={<Network className="size-4" />} title={t('topology.title')} />
        {namespace === '' ? (
          // 全局环境为「全部环境」时端点无单一 namespace 可查：提示在页眉选具体环境出图
          <p className="py-12 text-center text-sm text-muted-foreground">{t('topology.noNamespace')}</p>
        ) : (
          <AsyncSection isLoading={isLoading} isError={isError} error={error}>
            {data &&
              (data.nodes.length === 0 ? (
                <p className="py-12 text-center text-sm text-muted-foreground">{t('topology.noNodes')}</p>
              ) : (
                <TopologyGraph data={data} />
              ))}
          </AsyncSection>
        )}
      </section>
    </div>
  )
}
