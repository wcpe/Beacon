// 作用域实体选择器（配置中心共用）：给定非 namespace 层级选择具体实体。
// bc_cluster / region / zone 候选来自结构树（Combobox 客户端过滤，量级可控）；
// server 走服务端搜索（fetchServers keyword 分页，1000+ 子服不卡，UX §4 全局契约）。
// 空层首次贡献、（假想）有效预览目标、diff effective 描述符三处复用。
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Check, Search } from 'lucide-react'

import { AsyncSection, Combobox, Input, cn } from '@beacon/ui'
import type { ConfigScopeLevel } from '@beacon/contracts'

import { fetchServers, fetchZoneTree } from '../../api/cluster'

/** 已选实体（id 为 scope_ref_id / 目标 id，name 供展示） */
export interface ScopeRef {
  id: number
  name: string
}

interface ScopeRefPickerProps {
  namespaceId: number
  // namespace 层不需要实体选择，调用方直接用 namespaceId
  level: Exclude<ConfigScopeLevel, 'namespace'>
  value: ScopeRef | null
  onChange: (picked: ScopeRef | null) => void
  // 排除的实体 id（如已有贡献链的实体，避免误走「首次贡献」入口）
  excludeIds?: number[]
  id?: string
}

export default function ScopeRefPicker({ namespaceId, level, value, onChange, excludeIds, id }: ScopeRefPickerProps) {
  if (level === 'server') {
    return (
      <ServerSearchPicker
        namespaceId={namespaceId}
        value={value}
        onChange={onChange}
        excludeIds={excludeIds}
        id={id}
      />
    )
  }
  return (
    <TreeLevelPicker
      namespaceId={namespaceId}
      level={level}
      value={value}
      onChange={onChange}
      excludeIds={excludeIds}
      id={id}
    />
  )
}

interface TreeLevelPickerProps {
  namespaceId: number
  level: 'bc_cluster' | 'region' | 'zone'
  value: ScopeRef | null
  onChange: (picked: ScopeRef | null) => void
  excludeIds?: number[]
  id?: string
}

/** bc_cluster / region / zone：结构树候选 + Combobox 严格选 */
function TreeLevelPicker({ namespaceId, level, value, onChange, excludeIds, id }: TreeLevelPickerProps) {
  const { t } = useTranslation()
  const treeQuery = useQuery({
    queryKey: ['configs', 'zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
  })

  const options = useMemo(() => {
    const clusters = treeQuery.data?.clusters ?? []
    const exclude = new Set(excludeIds ?? [])
    const all =
      level === 'bc_cluster'
        ? clusters.map((c) => ({ value: String(c.id), label: c.name }))
        : level === 'region'
          ? clusters.flatMap((c) => c.regions.map((r) => ({ value: String(r.id), label: `${c.name} / ${r.name}` })))
          : clusters.flatMap((c) =>
              c.regions.flatMap((r) => r.zones.map((z) => ({ value: String(z.id), label: `${r.name} / ${z.name}` }))),
            )
    return all.filter((o) => !exclude.has(Number(o.value)))
  }, [treeQuery.data, level, excludeIds])

  return (
    <AsyncSection isLoading={treeQuery.isLoading} isError={treeQuery.isError} error={treeQuery.error}>
      <Combobox
        id={id}
        aria-label={t('delivery.configs.scopePicker.pickLabel')}
        value={value === null ? '' : String(value.id)}
        onChange={(next) => {
          const hit = options.find((o) => o.value === next)
          onChange(hit ? { id: Number(hit.value), name: hit.label } : null)
        }}
        options={options}
        allowCustom={false}
        placeholder={t('delivery.configs.scopePicker.pickLabel')}
        emptyText={t('delivery.configs.scopePicker.empty')}
      />
    </AsyncSection>
  )
}

interface ServerSearchPickerProps {
  namespaceId: number
  value: ScopeRef | null
  onChange: (picked: ScopeRef | null) => void
  excludeIds?: number[]
  id?: string
}

/** server：搜索框 + 服务端过滤结果列表（单选），不整页拉取 */
function ServerSearchPicker({ namespaceId, value, onChange, excludeIds, id }: ServerSearchPickerProps) {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')

  const serversQuery = useQuery({
    queryKey: ['configs', 'server-search', namespaceId, keyword],
    queryFn: () =>
      fetchServers({
        namespaceId,
        keyword: keyword.trim() === '' ? undefined : keyword.trim(),
        pageSize: 20,
      }),
  })

  const exclude = new Set(excludeIds ?? [])
  const items = (serversQuery.data?.items ?? []).filter((s) => !exclude.has(s.id))

  return (
    <div className="grid gap-2">
      <div className="relative">
        <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-4" />
        <Input
          id={id}
          aria-label={t('delivery.configs.scopePicker.searchServer')}
          placeholder={t('delivery.configs.scopePicker.searchServer')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
          }}
          className="h-8 w-full pl-8 text-xs"
        />
      </div>
      <AsyncSection isLoading={serversQuery.isLoading} isError={serversQuery.isError} error={serversQuery.error}>
        {items.length === 0 ? (
          <p className="px-1 py-2 text-xs text-ink-3">{t('delivery.configs.scopePicker.serverEmpty')}</p>
        ) : (
          <div className="grid max-h-48 gap-1 overflow-y-auto" role="listbox">
            {items.map((s) => {
              const checked = value?.id === s.id
              return (
                <button
                  key={s.id}
                  type="button"
                  role="option"
                  aria-selected={checked}
                  aria-label={s.serverId}
                  onClick={() => {
                    onChange(checked ? null : { id: s.id, name: s.serverId })
                  }}
                  className={cn(
                    'flex items-center gap-2 rounded-lg border px-2.5 py-1.5 text-left transition-colors',
                    checked
                      ? 'border-brand-300 bg-brand-50/60'
                      : 'border-transparent hover:border-brand-200 hover:bg-brand-50/30',
                  )}
                >
                  <span
                    className={cn(
                      'grid size-4 shrink-0 place-items-center rounded-full border',
                      checked ? 'border-brand bg-brand text-white' : 'border-border bg-card text-transparent',
                    )}
                  >
                    <Check className="size-3" strokeWidth={3} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-mono text-xs font-medium text-ink-1">{s.serverId}</span>
                    <span className="block truncate text-[11px] text-ink-4">{s.zoneName ?? '—'}</span>
                  </span>
                </button>
              )
            })}
          </div>
        )}
      </AsyncSection>
    </div>
  )
}
