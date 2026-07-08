// 分配目标选择器（可搜索树）：选大区 / 小区 / 代理不拍平成下拉，而是按
// BC 集群 → 大区 → 小区 / 代理 的层级树呈现，顶部搜索框按名称过滤节点。
// backend 选到小区叶（zone id），proxy 选到集群叶（bc_cluster id）。
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Boxes, Check, ChevronDown, ChevronRight, Layers, MapPin, Search } from 'lucide-react'

import { Input, cn } from '@beacon/ui'
import type { ZoneTreeResponse } from '@beacon/devmock'

interface AssignTargetTreeProps {
  tree: ZoneTreeResponse | undefined
  // backend 落小区、proxy 落集群
  kind: 'backend' | 'proxy'
  // 选中目标 id（字符串，空串未选）
  value: string
  onChange: (value: string) => void
}

// 小写去空白，做包含式过滤
function matches(name: string, keyword: string): boolean {
  return name.toLowerCase().includes(keyword.trim().toLowerCase())
}

export default function AssignTargetTree({ tree, kind, value, onChange }: AssignTargetTreeProps) {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  // 展开的集群 / 大区键；搜索时全展开以露出命中叶
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const searching = keyword.trim() !== ''

  const toggle = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(key)) {
        next.delete(key)
      } else {
        next.add(key)
      }
      return next
    })
  }

  // 过滤后的集群列表：proxy 模式按集群名过滤；backend 模式保留含命中小区的分支
  const clusters = useMemo(() => {
    const all = tree?.clusters ?? []
    if (!searching) {
      return all
    }
    if (kind === 'proxy') {
      return all.filter((c) => matches(c.name, keyword))
    }
    return all
      .map((c) => ({
        ...c,
        regions: c.regions
          .map((r) => ({
            ...r,
            zones: r.zones.filter(
              (z) => matches(z.name, keyword) || matches(r.name, keyword) || matches(c.name, keyword),
            ),
          }))
          .filter((r) => r.zones.length > 0),
      }))
      .filter((c) => c.regions.length > 0)
  }, [tree, kind, keyword, searching])

  const isOpen = (key: string) => searching || expanded.has(key)

  return (
    <div className="grid gap-2">
      {/* 搜索框：按名称过滤树节点 */}
      <div className="relative">
        <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-4" />
        <Input
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
          }}
          placeholder={t('cluster.zones.assign.searchTarget')}
          aria-label={t('cluster.zones.assign.searchTarget')}
          className="h-8 pl-8 text-sm"
        />
      </div>

      <div
        role="tree"
        aria-label={t(kind === 'backend' ? 'cluster.zones.assign.targetZone' : 'cluster.zones.assign.targetCluster')}
        className="max-h-64 overflow-y-auto rounded-md border border-border bg-surface-2 p-1.5"
      >
        {clusters.length === 0 ? (
          <p className="px-2 py-4 text-center text-xs text-ink-4">{t('cluster.zones.assign.noTargetMatch')}</p>
        ) : (
          <ul className="grid gap-0.5">
            {clusters.map((cluster) => {
              const clusterKey = `c:${String(cluster.id)}`
              // proxy 模式：集群本身即目标叶
              if (kind === 'proxy') {
                const selected = value === String(cluster.id)
                return (
                  <li key={cluster.id}>
                    <TargetLeaf
                      depth={0}
                      icon={<Boxes className="size-3.5 text-brand" />}
                      label={cluster.name}
                      selected={selected}
                      onSelect={() => {
                        onChange(String(cluster.id))
                      }}
                    />
                  </li>
                )
              }
              // backend 模式：集群 / 大区为分组，小区为目标叶
              const clusterOpen = isOpen(clusterKey)
              return (
                <li key={cluster.id}>
                  <GroupRow
                    depth={0}
                    open={clusterOpen}
                    icon={<Boxes className="size-3.5 text-brand" />}
                    label={cluster.name}
                    onToggle={() => {
                      toggle(clusterKey)
                    }}
                  />
                  {clusterOpen && (
                    <ul>
                      {cluster.regions.map((region) => {
                        const regionKey = `r:${String(region.id)}`
                        const regionOpen = isOpen(regionKey)
                        return (
                          <li key={region.id}>
                            <GroupRow
                              depth={1}
                              open={regionOpen}
                              icon={<Layers className="size-3.5 text-ink-4" />}
                              label={region.name}
                              onToggle={() => {
                                toggle(regionKey)
                              }}
                            />
                            {regionOpen && (
                              <ul>
                                {region.zones.map((zone) => (
                                  <li key={zone.id}>
                                    <TargetLeaf
                                      depth={2}
                                      icon={<MapPin className="size-3.5 text-brand" />}
                                      label={zone.name}
                                      selected={value === String(zone.id)}
                                      onSelect={() => {
                                        onChange(String(zone.id))
                                      }}
                                    />
                                  </li>
                                ))}
                              </ul>
                            )}
                          </li>
                        )
                      })}
                    </ul>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}

// 分组行（集群 / 大区）：仅用于展开折叠，不可选为目标
function GroupRow({
  depth,
  open,
  icon,
  label,
  onToggle,
}: {
  depth: number
  open: boolean
  icon: React.ReactNode
  label: string
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[12.5px] font-medium text-ink-1 transition-colors hover:bg-brand-50"
      style={{ paddingLeft: `${String(depth * 16 + 6)}px` }}
    >
      <span className="grid size-4 shrink-0 place-items-center text-ink-4">
        {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
      </span>
      {icon}
      <span className="truncate">{label}</span>
    </button>
  )
}

// 目标叶（小区 / 代理集群）：可点选中
function TargetLeaf({
  depth,
  icon,
  label,
  selected,
  onSelect,
}: {
  depth: number
  icon: React.ReactNode
  label: string
  selected: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      role="treeitem"
      aria-selected={selected}
      onClick={onSelect}
      className={cn(
        'flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[12.5px] transition-colors',
        selected ? 'bg-brand-50 font-semibold text-brand-600' : 'text-ink-1 hover:bg-surface-2',
      )}
      style={{ paddingLeft: `${String(depth * 16 + 6)}px` }}
    >
      <span className="size-4 shrink-0" />
      {icon}
      <span className="truncate font-mono">{label}</span>
      {selected && <Check className="ml-auto size-3.5 text-brand" aria-hidden />}
    </button>
  )
}
