// 服务器选择：列出在线子服（backend），多选用于指标时序与多服对比。
// 数据源复用集群域 fetchServers（只读，不改 cluster.ts）。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Check, Server, ServerOff } from 'lucide-react'

import {
  AsyncSection,
  Button,
  CardGridSkeleton,
  SectionHeader,
  cn,
} from '@beacon/ui'
import type { ServerItem } from '@beacon/devmock'

import { fetchServers } from '../../api/cluster'

interface ServerPickerProps {
  // 已选 serverId 集合
  selected: Set<string>
  onToggle: (serverId: string) => void
  onClear: () => void
}

export default function ServerPicker({ selected, onToggle, onClear }: ServerPickerProps) {
  const { t } = useTranslation()
  // 拉全部子服，客户端筛在线（指标时序仅对在线子服有意义）
  const query = useQuery({
    queryKey: ['service-analysis', 'servers'],
    queryFn: () => fetchServers({ kind: 'backend', pageSize: 200 }),
  })

  const servers = useMemo<ServerItem[]>(
    () => (query.data?.items ?? []).filter((s) => s.online),
    [query.data],
  )

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<Server className="size-4" />}
        title={t('observability.serviceAnalysis.pickServers')}
        count={servers.length > 0 ? t('observability.serviceAnalysis.onlineCount', { count: servers.length }) : undefined}
        actions={
          selected.size > 0 ? (
            <>
              <span className="text-xs text-ink-3">
                {t('observability.serviceAnalysis.selectedCount', { count: selected.size })}
              </span>
              <Button size="sm" variant="outline" onClick={onClear}>
                {t('observability.serviceAnalysis.clear')}
              </Button>
            </>
          ) : undefined
        }
      />

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={4} />}
      >
        {servers.length === 0 ? (
          <div className="flex items-center gap-2.5 rounded-xl border border-dashed border-border bg-card/60 px-4 py-8 text-sm text-ink-3">
            <ServerOff className="size-4 shrink-0 text-ink-4" />
            {t('observability.serviceAnalysis.empty')}
          </div>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(12rem,1fr))] gap-2.5">
            {servers.map((s) => {
              const checked = selected.has(s.serverId)
              return (
                <button
                  key={s.serverId}
                  type="button"
                  role="checkbox"
                  aria-checked={checked}
                  aria-label={s.serverId}
                  onClick={() => {
                    onToggle(s.serverId)
                  }}
                  className={cn(
                    'flex items-center gap-2.5 rounded-xl border bg-card px-3 py-2.5 text-left shadow-card transition-colors',
                    checked
                      ? 'border-brand-300 bg-brand-50/60 ring-1 ring-brand-200'
                      : 'border-border hover:border-brand-200 hover:bg-brand-50/30',
                  )}
                >
                  <span
                    className={cn(
                      'grid size-5 shrink-0 place-items-center rounded-md border transition-colors',
                      checked ? 'border-brand bg-brand text-white' : 'border-border bg-card text-transparent',
                    )}
                  >
                    <Check className="size-3.5" strokeWidth={3} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-mono text-xs font-medium text-ink-1">
                      {s.serverId}
                    </span>
                    <span className="block truncate text-[11px] text-ink-4">{s.zoneName ?? '—'}</span>
                  </span>
                </button>
              )
            })}
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
