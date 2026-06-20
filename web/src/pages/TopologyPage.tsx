// 集群拓扑页（FR-37，独立页）：读 GET /admin/v1/topology，用 ECharts graph 画
// 真实 bc→bukkit 连线、按角色区分、按大区/zone 聚合分簇，节点带在线状态色；
// React Query refetchInterval 轮询刷新（与实例页一致）。拓扑端点要求 namespace 必填，故先选环境再出图。

import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getTopology } from '../api/client'
import TopologyGraph from './topology/TopologyGraph'
import AsyncSection from '@/components/AsyncSection'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'

// 拓扑轮询周期（毫秒），与实例与健康页一致
const REFETCH_MS = 5000

export default function TopologyPage() {
  // 输入框为待提交值，namespace 为已生效查询值（端点必填，空则不查询）
  const [nsInput, setNsInput] = useState('')
  const [namespace, setNamespace] = useState('')

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['topology', namespace],
    queryFn: () => getTopology(namespace),
    enabled: namespace !== '', // 未选环境不发请求（端点 namespace 必填）
    refetchInterval: REFETCH_MS,
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setNamespace(nsInput.trim())
  }

  const bcCount = data?.nodes.filter((n) => n.role === 'bungee').length ?? 0
  const subCount = data?.nodes.filter((n) => n.role === 'bukkit').length ?? 0

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">集群拓扑</h1>
        {isFetching && <span className="text-sm text-muted-foreground">（刷新中…）</span>}
      </div>

      <Card>
        <CardContent className="space-y-3">
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="t-namespace">环境</Label>
              <Input
                id="t-namespace"
                placeholder="如 prod / test"
                value={nsInput}
                onChange={(e) => setNsInput(e.target.value)}
              />
            </div>
            <Button type="submit">查询</Button>
          </form>
          {/* 图例 + 计数：与图内 legend 呼应，运维一眼看清 BC / 子服区分与边含义 */}
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm text-muted-foreground">
            <span className="flex items-center gap-1.5">
              <span className="inline-block h-3 w-3 rounded-sm bg-[#7c3aed]" />
              BC 代理（{bcCount}）
            </span>
            <span className="flex items-center gap-1.5">
              <span className="inline-block h-3 w-3 rounded-full bg-[#2563eb]" />
              子服（{subCount}）
            </span>
            <span>连线 = bc→其后端子服（FR-36 事实）；描边色 = 在线状态（绿 online / 橙 degraded）</span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardContent>
          {namespace === '' ? (
            <p className="py-12 text-center text-sm text-muted-foreground">请先在上方输入环境并查询，以查看该环境的集群拓扑。</p>
          ) : (
            <AsyncSection isLoading={isLoading} isError={isError} error={error}>
              {data &&
                (data.nodes.length === 0 ? (
                  <p className="py-12 text-center text-sm text-muted-foreground">该环境暂无在线实例。</p>
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
