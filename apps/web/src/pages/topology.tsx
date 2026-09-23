// 拓扑页（/topology）：两种模式切换（Tabs）——
// ① 可视化模式：分层 SVG 拓扑图（全局 BC↔子服链路 + 异常链路高亮 + 明细）
// ② 数据剖析模式：异常链路排名表 / 按边失败率 / 时延分布等指标聚合视角
// 排障任务：/dashboard 下钻 → /servers 详情 → 本页看链路与异常边，可与 /commands /audits 互跳。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Network, Table2 } from 'lucide-react'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@beacon/ui'

import NamespaceSelect, { ALL_NAMESPACES } from '../features/cluster/namespace-select'
import EdgesPanel from './topology/edges-panel'
import TopologyGraph from './topology/topology-graph'

export default function TopologyPage() {
  const { t } = useTranslation()
  // 当前作用域 namespace；null / ALL_NAMESPACES(0) 均表示「全部命名空间」
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  // 「全部」只用参数省略表达：观测范围契约（FR-213）拒绝显式 namespaceId=0
  // （返回 400 invalid_observation_scope），故此处不把 0 透传给查询。
  const apiNamespaceId =
    namespaceId === null || namespaceId === ALL_NAMESPACES ? undefined : namespaceId
  // 视图模式：graph 可视化 / data 数据剖析
  const [mode, setMode] = useState('graph')

  return (
    <section className="grid gap-4">
      <Tabs value={mode} onValueChange={setMode}>
        {/* 模式切换与命名空间选择器同一行：不另起独立操作行 */}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <TabsList>
            <TabsTrigger value="graph" className="gap-1.5">
              <Network className="size-3.5" />
              {t('cluster.topology.mode.graph')}
            </TabsTrigger>
            <TabsTrigger value="data" className="gap-1.5">
              <Table2 className="size-3.5" />
              {t('cluster.topology.mode.data')}
            </TabsTrigger>
          </TabsList>
          <NamespaceSelect value={namespaceId} onChange={setNamespaceId} />
        </div>
        <TabsContent value="graph">
          <TopologyGraph namespaceId={apiNamespaceId} />
        </TabsContent>
        <TabsContent value="data">
          <EdgesPanel />
        </TabsContent>
      </Tabs>
    </section>
  )
}
