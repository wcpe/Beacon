// 区服分配页（/zones）：BC / 大区 / 小区结构树（各层可新建）+ 未分配篮批量首次分配。
// 高频任务「接入新服」的第二步：/servers 待确认 → 本页分配区服。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Boxes } from 'lucide-react'

import { SectionHeader } from '@beacon/ui'

import NamespaceSelect from '../features/cluster/namespace-select'
import UnassignedBasket from './zones/unassigned-basket'
import ZoneTree from './zones/zone-tree'

export default function ZonesPage() {
  const { t } = useTranslation()
  // 当前作用域 namespace；null 时按 0 取全量（空态无 namespace 亦能渲染引导）
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader size="lg" icon={<Boxes className="size-5" />} title={t('nav.zones')} className="border-b-0 pb-0" />
        <NamespaceSelect value={namespaceId} onChange={setNamespaceId} />
      </div>
      <div className="grid gap-3.5 lg:grid-cols-2">
        <ZoneTree namespaceId={effectiveNamespaceId} />
        <UnassignedBasket namespaceId={effectiveNamespaceId} />
      </div>
    </section>
  )
}
