// zone 汇总树形展示（FR-55）：把汇总区从扁平表格改为「大区 → 小区 → 子服」三级树形节点图。
// 纯展示组件，数据由 buildSummaryTree 派生（计数取自 summary，与原表口径一致）；不含拖拽交互。

import { useTranslation } from 'react-i18next'
import type { SummaryGroupNode, SummaryServer, SummaryTree, SummaryZoneNode } from './summaryTree'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

// 状态 → 状态点配色（与 ServerCard 一致：online 绿 / lost 琥珀 / offline 灰）
const DOT_COLOR: Record<string, string> = {
  online: 'bg-green-500',
  lost: 'bg-amber-500',
  offline: 'bg-muted-foreground',
}

// 服数 / 在线数计数徽标（小区与大区共用）
function CountBadge({ serverCount, onlineCount }: { serverCount: number; onlineCount: number }) {
  const { t } = useTranslation()
  return (
    <Badge variant="secondary" className="ml-2 font-normal">
      {t('zones.treeCount', { serverCount, onlineCount })}
    </Badge>
  )
}

// 子服叶节点：在线状态点 + serverId
function ServerLeaf({ server }: { server: SummaryServer }) {
  const { t } = useTranslation()
  return (
    <li className="flex items-center gap-2 py-0.5 text-sm">
      <span
        aria-label={t('common.statusAria', { status: server.status })}
        className={cn('size-2 shrink-0 rounded-full', DOT_COLOR[server.status] ?? 'bg-muted-foreground')}
      />
      <span className="font-mono">{server.serverId}</span>
    </li>
  )
}

// 小区节点：标题（小区名 + 计数）+ 子服列表（左缩进 + 竖向连接线）
function ZoneNode({ zone }: { zone: SummaryZoneNode }) {
  const { t } = useTranslation()
  return (
    <li>
      <div className="flex items-center py-0.5 text-sm">
        <span className="text-muted-foreground">{t('zones.treeZonePrefix')}</span>
        <span className="ml-1 font-medium">{zone.zone}</span>
        <CountBadge serverCount={zone.serverCount} onlineCount={zone.onlineCount} />
      </div>
      {zone.servers.length === 0 ? (
        <p className="ml-4 border-l py-0.5 pl-3 text-xs text-muted-foreground">{t('zones.treeNoServer')}</p>
      ) : (
        <ul className="ml-4 border-l pl-3">
          {zone.servers.map((s) => (
            <ServerLeaf key={s.serverId} server={s} />
          ))}
        </ul>
      )}
    </li>
  )
}

// 大区节点：标题（大区名 + 合计计数）+ 小区列表（左缩进 + 竖向连接线）
function GroupNode({ group }: { group: SummaryGroupNode }) {
  const { t } = useTranslation()
  return (
    <li>
      <div className="flex items-center py-0.5">
        <span className="text-sm font-semibold">{t('zones.treeGroupPrefix', { group: group.group })}</span>
        <CountBadge serverCount={group.serverCount} onlineCount={group.onlineCount} />
      </div>
      <ul className="ml-4 space-y-1 border-l pl-3">
        {group.zones.map((z) => (
          <ZoneNode key={z.zone} zone={z} />
        ))}
      </ul>
    </li>
  )
}

// 汇总树根：无大区时给空态文案
export default function ZoneSummaryTree({ tree }: { tree: SummaryTree }) {
  const { t } = useTranslation()
  if (tree.groups.length === 0) {
    return <p className="text-sm text-muted-foreground">{t('zones.treeEmpty')}</p>
  }
  return (
    <ul className="space-y-3">
      {tree.groups.map((g) => (
        <GroupNode key={g.group} group={g} />
      ))}
    </ul>
  )
}
