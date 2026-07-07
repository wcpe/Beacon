// 拓扑页（/topology）：BC-子服链路图 + 消息异常链路概览。
// 排障任务：/dashboard 下钻 → /servers 详情 → 本页看链路与异常边，可与 /commands /audits 互跳。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'

import NamespaceSelect from '../features/cluster/namespace-select'
import EdgesPanel from './topology/edges-panel'
import LinkGraph from './topology/link-graph'

export default function TopologyPage() {
  const { t } = useTranslation()
  // 当前作用域 namespace；null 时按 0 取全量（空态无 namespace 亦能渲染引导）
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader size="lg" title={t('nav.topology')} />
        <NamespaceSelect value={namespaceId} onChange={setNamespaceId} />
      </div>
      <LinkGraph namespaceId={effectiveNamespaceId} />
      <EdgesPanel />
    </section>
  )
}
