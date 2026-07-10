// 编排预览装配（共享控件）：从变更单详情（selector / 生效方式）+ 影响面汇总推导
// OrchestrationPreview 所需数据——范围模式、已选对象显示名（大区 / 小区经结构树翻名，
// 单服直接用 serverId）、批次划分与传输量。/changes 详情「影响预览」Tab 与
// 历史详情「交付编排」共用；影响面汇总由调用方传入（各页自己的 impact 查询），不重复取数。
import { useQuery } from '@tanstack/react-query'

import type { ZoneTreeResponse } from '@beacon/devmock'

import { fetchZoneTree } from '../../api/cluster'
import type { ChangeImpactResponse, ChangeOrderDetail } from '../../api/delivery-changes'
import OrchestrationPreview, { type OrchestrationScopeInfo } from './orchestration-preview'

interface OrderOrchestrationProps {
  order: ChangeOrderDetail
  summary: ChangeImpactResponse['summary']
}

// selector → 范围模式（优先级：全量 > 大区 > 小区 > 单服，与向导保存的单模式 selector 对应）
function modeOf(selector: ChangeOrderDetail['selector']): OrchestrationScopeInfo['mode'] {
  if (selector.all) {
    return 'all'
  }
  if (selector.regions.length > 0) {
    return 'regions'
  }
  if (selector.zones.length > 0) {
    return 'zones'
  }
  return 'servers'
}

// 已选对象显示名：大区 / 小区从结构树翻名（小区带大区前缀），单服即 serverId，全量为空
function pickedNamesOf(
  selector: ChangeOrderDetail['selector'],
  mode: OrchestrationScopeInfo['mode'],
  tree: ZoneTreeResponse | undefined,
): string[] {
  if (mode === 'servers') {
    return selector.servers
  }
  if (mode === 'all' || tree === undefined) {
    return []
  }
  const regions = tree.clusters.flatMap((cluster) => cluster.regions)
  if (mode === 'regions') {
    return regions.filter((region) => selector.regions.includes(region.id)).map((region) => region.name)
  }
  return regions.flatMap((region) =>
    region.zones
      .filter((zone) => selector.zones.includes(zone.id))
      .map((zone) => `${region.name} / ${zone.name}`),
  )
}

export default function OrderOrchestration({ order, summary }: OrderOrchestrationProps) {
  const mode = modeOf(order.selector)

  // 结构树：把已选大区 / 小区 id 翻译成名称（与向导共用查询缓存 key）
  const treeQuery = useQuery({
    queryKey: ['change-orders', 'wizard-zone-tree', order.namespaceId],
    queryFn: () => fetchZoneTree(order.namespaceId),
    enabled: mode === 'regions' || mode === 'zones',
  })

  return (
    <OrchestrationPreview
      scope={{ mode, picked: pickedNamesOf(order.selector, mode, treeQuery.data) }}
      targetTotal={summary.targetTotal}
      batches={summary.batches}
      activationMethod={order.activationMethod}
      fileTotal={summary.fileTotal}
      configScopeCount={summary.configScopeCount}
      transferBytes={summary.transferBytes}
    />
  )
}
