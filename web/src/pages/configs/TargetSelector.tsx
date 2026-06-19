// 实例 / 分组树形选择器：按 group > zone > servers 组织，选中后用于过滤左侧配置树。

import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { cn } from '@/lib/utils'

export default function TargetSelector({
  instances,
  selectedTarget,
  onSelectTarget,
}: {
  instances: { serverId: string; group: string; zone: string | null; status: string }[]
  selectedTarget: { type: 'server' | 'group'; value: string } | null
  onSelectTarget: (t: { type: 'server' | 'group'; value: string } | null) => void
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set(['__root__']))

  const toggle = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  // 按 group > zone > servers 构建树
  const groupMap = new Map<string, { zones: Map<string, { serverId: string; status: string }[]> }>()
  for (const inst of instances) {
    if (!groupMap.has(inst.group)) groupMap.set(inst.group, { zones: new Map() })
    const g = groupMap.get(inst.group)!
    const zoneName = inst.zone ?? '(未分组)'
    if (!g.zones.has(zoneName)) g.zones.set(zoneName, [])
    g.zones.get(zoneName)!.push({ serverId: inst.serverId, status: inst.status })
  }

  const statusColor = (s: string) =>
    s === 'online' ? 'bg-emerald-500' : s === 'lost' ? 'bg-amber-500' : 'bg-muted-foreground'

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <div className="flex-shrink-0 px-3 py-1.5 text-xs font-medium text-muted-foreground border-b border-border bg-muted/30">
        实例 / 分组
      </div>
      <ScrollArea className="flex-1 py-1">
        {/* 全部 */}
        <button
          type="button"
          className={cn(
            'flex w-full items-center gap-1 px-2 py-1 text-sm transition-colors',
            !selectedTarget
              ? 'bg-accent text-accent-foreground'
              : 'text-muted-foreground hover:bg-muted hover:text-foreground',
          )}
          onClick={() => onSelectTarget(null)}
        >
          <span className="truncate">全部实例</span>
          <span className="text-[0.6rem] text-muted-foreground/60">({instances.length})</span>
        </button>

        {Array.from(groupMap.entries()).map(([group, { zones: zoneMap }]) => {
          const isGroupExpanded = expanded.has(`group-${group}`)
          const isGroupSelected = selectedTarget?.type === 'group' && selectedTarget.value === group
          return (
            <div key={group}>
              <button
                type="button"
                className={cn(
                  'flex w-full items-center gap-1 px-2 py-1 text-sm transition-colors',
                  isGroupSelected
                    ? 'bg-accent text-accent-foreground'
                    : 'text-muted-foreground hover:bg-muted hover:text-foreground',
                )}
                style={{ paddingLeft: '16px' }}
                onClick={() => {
                  toggle(`group-${group}`)
                  onSelectTarget({ type: 'group', value: group })
                }}
              >
                {isGroupExpanded ? (
                  <ChevronDown className="h-3 w-3 shrink-0" />
                ) : (
                  <ChevronRight className="h-3 w-3 shrink-0" />
                )}
                <span className="truncate">{group}</span>
                <span className="text-[0.6rem] text-muted-foreground/60">
                  ({Array.from(zoneMap.values()).flat().length})
                </span>
              </button>
              {isGroupExpanded && (
                <div>
                  {Array.from(zoneMap.entries()).map(([zone, servers]) => {
                    const isZoneExpanded = expanded.has(`zone-${group}-${zone}`)
                    return (
                      <div key={zone}>
                        <button
                          type="button"
                          className="flex w-full items-center gap-1 px-2 py-0.5 text-xs text-muted-foreground/70 hover:text-muted-foreground transition-colors"
                          style={{ paddingLeft: '28px' }}
                          onClick={() => toggle(`zone-${group}-${zone}`)}
                        >
                          {isZoneExpanded ? (
                            <ChevronDown className="h-2.5 w-2.5 shrink-0" />
                          ) : (
                            <ChevronRight className="h-2.5 w-2.5 shrink-0" />
                          )}
                          <span className="truncate">{zone}</span>
                          <span className="text-[0.6rem] text-muted-foreground/50">({servers.length})</span>
                        </button>
                        {isZoneExpanded &&
                          servers.map((s) => (
                            <button
                              key={s.serverId}
                              type="button"
                              className={cn(
                                'flex w-full items-center gap-1.5 px-2 py-0.5 text-xs transition-colors',
                                selectedTarget?.type === 'server' && selectedTarget.value === s.serverId
                                  ? 'bg-accent text-accent-foreground'
                                  : 'text-muted-foreground/60 hover:text-muted-foreground',
                              )}
                              style={{ paddingLeft: '40px' }}
                              onClick={() => onSelectTarget({ type: 'server', value: s.serverId })}
                            >
                              <span className={cn('h-2 w-2 shrink-0 rounded-full', statusColor(s.status))} />
                              <span className="truncate">{s.serverId}</span>
                            </button>
                          ))}
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })}
      </ScrollArea>
    </div>
  )
}
