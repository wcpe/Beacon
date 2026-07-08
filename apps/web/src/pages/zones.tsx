// 区服分配页（/zones）：主从布局。页面主体是区服结构树（真树形 + 代理角色标注），
// 未分配 server 收敛为顶部「未分配 N」入口，点开在右侧展开非模态窄栏（不遮罩主区、可从窄栏往树里拖）。
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
  // 未分配窄栏开关
  const [basketOpen, setBasketOpen] = useState(false)
  // 当前正在拖拽的服务器 kind（null 未拖拽）：供树高亮兼容目标。
  // HTML5 拖拽在 dragover 阶段读不到 dataTransfer 数据（仅 types），故用共享状态判定兼容性。
  const [draggingKind, setDraggingKind] = useState<'backend' | 'proxy' | null>(null)

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
          {/* 未分配入口：开合右侧非模态窄栏 */}
          <Button
            variant={basketOpen ? 'default' : 'outline'}
            size="sm"
            className="gap-1.5"
            onClick={() => {
              setBasketOpen((v) => !v)
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
      {/* 主从布局：结构树占主区，未分配窄栏（非模态）在右侧共存，不遮罩、不 reflow 主区 */}
      <div className="flex items-start gap-3.5">
        <div className="min-w-0 flex-1">
          <ZoneTree namespaceId={effectiveNamespaceId} draggingKind={draggingKind} />
        </div>
        <UnassignedBasket
          namespaceId={effectiveNamespaceId}
          open={basketOpen}
          onClose={() => {
            setBasketOpen(false)
          }}
          onDraggingKindChange={setDraggingKind}
        />
      </div>
    </section>
  )
}
