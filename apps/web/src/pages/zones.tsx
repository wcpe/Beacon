// 区服分配页（/zones）：主从布局。页面主体是区服结构树（真树形 + 代理角色标注），
// 未分配 server 收敛为顶部「未分配 N」抽屉入口，点开才批量分配，不默认占版面。
// 高频任务「接入新服」的第二步：/servers 待确认 → 本页分配区服。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Boxes, Inbox } from 'lucide-react'

import { Badge, Button, SectionHeader } from '@beacon/ui'

import { fetchZoneTree } from '../api/cluster'
import NamespaceSelect from '../features/cluster/namespace-select'
import UnassignedBasket from './zones/unassigned-basket'
import ZoneTree from './zones/zone-tree'

export default function ZonesPage() {
  const { t } = useTranslation()
  // 当前作用域 namespace；null 时按 0 取全量（空态无 namespace 亦能渲染引导）
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0
  // 未分配抽屉开关
  const [basketOpen, setBasketOpen] = useState(false)

  // 未分配数：抽屉入口徽标用（结构树响应已含，无需另拉 server 列表）
  const treeQuery = useQuery({
    queryKey: ['zone-tree', effectiveNamespaceId],
    queryFn: () => fetchZoneTree(effectiveNamespaceId),
  })
  const unassignedCount = treeQuery.data?.unassignedCount ?? 0

  return (
    <section className="grid gap-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader size="lg" icon={<Boxes className="size-5" />} title={t('nav.zones')} className="border-b-0 pb-0" />
        <div className="flex items-center gap-3">
          <NamespaceSelect value={namespaceId} onChange={setNamespaceId} />
          {/* 未分配入口：收敛为抽屉，点开才批量分配 */}
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => {
              setBasketOpen(true)
            }}
          >
            <Inbox className="size-3.5" />
            {t('cluster.zones.basket.title')}
            {unassignedCount > 0 && (
              <Badge variant="warn" className="tnum">
                {unassignedCount}
              </Badge>
            )}
          </Button>
        </div>
      </div>
      <ZoneTree namespaceId={effectiveNamespaceId} />
      <UnassignedBasket namespaceId={effectiveNamespaceId} open={basketOpen} onOpenChange={setBasketOpen} />
    </section>
  )
}
