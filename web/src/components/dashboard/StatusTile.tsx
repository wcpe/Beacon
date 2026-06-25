// 单台服务器瓷砖（StatusTile）：角色图标（bukkit=Server / bungee=Router）+ serverId + 健康色左边条与状态点
// + 1~2 个关键指标（子服：TPS · 人数 / BC：连接数 · 后端可达）。用于状态墙网格，一眼扫全集群健康。
// 数据全部取自 InstanceView 现有字段（role/status/playerCount/tps/proxy.*），只展示负载数字，不含玩家名单 / 身份。

import { useTranslation } from 'react-i18next'
import { Router, Server } from 'lucide-react'
import { cn } from '@/lib/utils'
import { levelSolid, levelText, statusLevel } from './health'
import type { InstanceView } from '@/api/types'

// 角色编码（与后端 metric_aggregate role 约定一致）：bungee 进 BC，其余按子服。
const ROLE_BUNGEE = 'bungee'

interface StatusTileProps {
  // 实例视图（来自 listInstances）
  instance: InstanceView
  // 放大版（大屏用）：瓷砖更大、字号更高、状态点更明显。
  large?: boolean
}

// 单个紧凑指标：标签 + 值（等宽数字）。
function TileMetric({ label, value, large }: { label: string; value: string; large?: boolean }) {
  return (
    <div className="min-w-0">
      <div className={cn('truncate text-muted-foreground', large ? 'text-xs' : 'text-[11px]')}>{label}</div>
      <div className={cn('tabular-nums', large ? 'text-lg font-semibold' : 'text-sm font-medium')}>{value}</div>
    </div>
  )
}

export default function StatusTile({ instance, large }: StatusTileProps) {
  const { t } = useTranslation()
  const isBungee = instance.role === ROLE_BUNGEE
  const level = statusLevel(instance.status)
  const RoleIcon = isBungee ? Router : Server

  // 关键指标：子服取 TPS·人数；BC 取连接数·后端可达（up/total）。均来自现有字段。
  // 数值字段缺省时按 0 兜底（后端恒返回，但容错优于崩溃；与页面 CPU/延迟哨兵兜底同口径）。
  const proxy = instance.proxy
  const metrics = isBungee
    ? [
        { label: t('statusTile.connections'), value: String(proxy?.onlineConnections ?? 0) },
        {
          label: t('statusTile.backend'),
          value: `${proxy?.backendUp ?? 0} / ${proxy?.backendTotal ?? 0}`,
        },
      ]
    : [
        { label: t('statusTile.tps'), value: (instance.tps ?? 0).toFixed(1) },
        { label: t('statusTile.players'), value: String(instance.playerCount ?? 0) },
      ]

  return (
    <div
      className={cn(
        'relative flex flex-col gap-2 overflow-hidden rounded-lg bg-card ring-1 ring-foreground/10',
        large ? 'p-4' : 'p-3',
      )}
    >
      {/* 左侧健康色条：按状态等级上色，远观即知该台健康。 */}
      <span aria-hidden className={cn('absolute inset-y-0 left-0 w-1', levelSolid(level))} />
      <div className={cn('flex items-center gap-2', large ? 'pl-2' : 'pl-1.5')}>
        <RoleIcon aria-hidden className={cn('shrink-0 text-muted-foreground', large ? 'size-5' : 'size-4')} />
        <span
          className={cn('truncate font-medium', large ? 'text-base' : 'text-sm')}
          title={instance.serverId}
        >
          {instance.serverId}
        </span>
        {/* 状态点 + 状态文案：语义健康色 */}
        <span className="ml-auto flex items-center gap-1.5">
          <span
            aria-hidden
            className={cn('inline-block rounded-full', large ? 'size-2.5' : 'size-2', levelSolid(level))}
          />
          <span className={cn('font-medium', large ? 'text-sm' : 'text-xs', levelText(level))}>
            {t(`status.${instance.status}`, { defaultValue: instance.status })}
          </span>
        </span>
      </div>
      <div className={cn('grid grid-cols-2 gap-2', large ? 'pl-2' : 'pl-1.5')}>
        {metrics.map((m) => (
          <TileMetric key={m.label} label={m.label} value={m.value} large={large} />
        ))}
      </div>
    </div>
  )
}
