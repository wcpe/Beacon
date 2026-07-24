// 拓扑页（/topology）：两种模式切换（Tabs）——
// ① 可视化模式：分层 SVG 拓扑图（全局 BC↔子服链路 + 异常链路高亮 + 明细）
// ② 数据剖析模式：异常链路排名表 / 按边失败率 / 时延分布等指标聚合视角
// 排障任务：/dashboard 下钻 → /servers 详情 → 本页看链路与异常边，可与 /commands /audits 互跳。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Network, Table2 } from 'lucide-react'

import { PageHeader, Tabs, TabsContent, TabsList, TabsTrigger } from '@beacon/ui'

import NamespaceSelect from '../features/cluster/namespace-select'
import EdgesPanel from './topology/edges-panel'
import TopologyGraph from './topology/topology-graph'

export default function TopologyPage() {
  const { t } = useTranslation()
  // 当前作用域 namespace；null 时按 0 取全量（空态无 namespace 亦能渲染引导）
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0
  // 视图模式：graph 可视化 / data 数据剖析
  const [mode, setMode] = useState('graph')

  return (
    <section className="grid gap-3.5">
      <PageHeader
        icon={<Network className="size-5" />}
        title={t('nav.topology')}
        actions={<NamespaceSelect value={namespaceId} onChange={setNamespaceId} />}
      />

      <Tabs value={mode} onValueChange={setMode}>
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
        <TabsContent value="graph">
          <TopologyGraph namespaceId={effectiveNamespaceId} />
        </TabsContent>
        <TabsContent value="data">
          <EdgesPanel />
        </TabsContent>
      </Tabs>
    </section>
  )
}
