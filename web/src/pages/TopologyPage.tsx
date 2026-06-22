// 集群拓扑页（FR-37，独立页）：读 GET /admin/v1/topology，用 ECharts graph 画
// 真实 bc→bukkit 连线、按角色区分、按大区/zone 聚合分簇，节点带在线状态色；
// React Query refetchInterval 轮询刷新（与实例页一致）。拓扑端点要求 namespace 必填，
// 环境改为下拉（候选来自 listNamespaces）并默认选第一个环境直接出图（增强 FR-51）。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { getTopology, listNamespaces } from '../api/client'
import TopologyGraph from './topology/TopologyGraph'
import AsyncSection from '@/components/AsyncSection'
import { Combobox } from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'

// 拓扑轮询周期（毫秒），与实例与健康页一致
const REFETCH_MS = 5000

export default function TopologyPage() {
  const { t } = useTranslation()
  // 已生效的环境查询值（端点必填，空则不查询）
  const [namespace, setNamespace] = useState('')

  // 环境候选：来自 listNamespaces，作为下拉候选并供默认选首项
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const namespaceOptions = (namespacesQuery.data ?? []).map((n) => n.code)

  // 候选就绪且未选环境时默认选第一个，直接出图（无需手动选，FR-51）
  useEffect(() => {
    if (namespace === '' && namespaceOptions.length > 0) {
      setNamespace(namespaceOptions[0])
    }
  }, [namespace, namespaceOptions])

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['topology', namespace],
    queryFn: () => getTopology(namespace),
    enabled: namespace !== '', // 未选环境不发请求（端点 namespace 必填）
    refetchInterval: REFETCH_MS,
  })

  const bcCount = data?.nodes.filter((n) => n.role === 'bungee').length ?? 0
  const subCount = data?.nodes.filter((n) => n.role === 'bukkit').length ?? 0

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{t('topology.title')}</h1>
        {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
      </div>

      <Card>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="t-namespace">{t('common.namespace')}</Label>
              {/* 环境改为严格下拉（候选来自 listNamespaces），选中即出图（FR-51） */}
              <Combobox
                id="t-namespace"
                aria-label={t('common.namespace')}
                className="w-48"
                value={namespace}
                onChange={setNamespace}
                options={namespaceOptions}
                allowCustom={false}
                placeholder={t('topology.nsPlaceholder')}
              />
            </div>
          </div>
          {/* 图例 + 计数：与图内 legend 呼应，运维一眼看清 BC / 子服区分与边含义 */}
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
        </CardContent>
      </Card>

      <Card>
        <CardContent>
          {namespace === '' ? (
            <p className="py-12 text-center text-sm text-muted-foreground">
              {namespacesQuery.isLoading ? t('topology.loadingNs') : t('topology.noNamespace')}
            </p>
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
        </CardContent>
      </Card>
    </div>
  )
}
