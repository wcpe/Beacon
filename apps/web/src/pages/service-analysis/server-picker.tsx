// 服务器选择（左侧吸顶列）：可搜索多选在线子服，紧凑列表，固定不随右侧滚动。
// 数据源复用集群域 fetchServers（只读，不改 cluster.ts）。选服即时驱动右侧指标与对比。

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Check, Search, Server, ServerOff } from 'lucide-react'

import {
  AsyncSection,
  Button,
  CardGridSkeleton,
  Input,
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
  const [keyword, setKeyword] = useState('')

  // 拉全部子服，客户端筛在线（指标时序仅对在线子服有意义）
  const query = useQuery({
    queryKey: ['service-analysis', 'servers'],
    queryFn: () => fetchServers({ kind: 'backend', pageSize: 200 }),
  })

  const online = useMemo<ServerItem[]>(
    () => (query.data?.items ?? []).filter((s) => s.online),
    [query.data],
  )

  // 关键词过滤（按 serverId）
  const servers = useMemo<ServerItem[]>(() => {
    const kw = keyword.trim().toLowerCase()
    if (kw === '') {
      return online
    }
    return online.filter((s) => s.serverId.toLowerCase().includes(kw))
  }, [online, keyword])

  return (
    <div className="lg:sticky lg:top-0 grid max-h-[calc(100vh-9rem)] grid-rows-[auto_auto_minmax(0,1fr)] overflow-hidden rounded-xl border border-border bg-card shadow-card">
      {/* 标题 + 已选计数 / 清空（吸顶不滚） */}
      <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <Server className="size-[15px]" />
        </span>
        <span className="min-w-0 flex-1 truncate text-[13px] font-semibold text-ink-1">
          {t('observability.serviceAnalysis.pickServers')}
        </span>
        {selected.size > 0 && (
          <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={onClear}>
            {t('observability.serviceAnalysis.clear')}
          </Button>
        )}
      </div>

      {/* 搜索框 + 计数（吸顶不滚） */}
      <div className="grid gap-1.5 border-b border-border px-3 py-2.5">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-4" />
          <Input
            aria-label={t('observability.serviceAnalysis.searchServers')}
            placeholder={t('observability.serviceAnalysis.searchServers')}
            value={keyword}
            onChange={(e) => {
              setKeyword(e.target.value)
            }}
            className="h-8 w-full pl-8 text-xs"
          />
        </div>
        <div className="flex items-center justify-between text-[11px] text-ink-4">
          <span>{t('observability.serviceAnalysis.onlineCount', { count: online.length })}</span>
          {selected.size > 0 && (
            <span className="text-brand-600">
              {t('observability.serviceAnalysis.selectedCount', { count: selected.size })}
            </span>
          )}
        </div>
      </div>

      {/* 可选服务器列表（自身滚动） */}
      <div className="overflow-y-auto p-2">
        <AsyncSection
          isLoading={query.isLoading}
          isError={query.isError}
          error={query.error}
          skeleton={<CardGridSkeleton count={4} />}
        >
          {online.length === 0 ? (
            <div className="flex items-center gap-2 px-2 py-6 text-xs text-ink-3">
              <ServerOff className="size-4 shrink-0 text-ink-4" />
              {t('observability.serviceAnalysis.empty')}
            </div>
          ) : servers.length === 0 ? (
            <p className="px-2 py-6 text-xs text-ink-3">{t('observability.serviceAnalysis.searchEmpty')}</p>
          ) : (
            <div className="grid gap-1">
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
                      'flex items-center gap-2 rounded-lg border px-2.5 py-2 text-left transition-colors',
                      checked
                        ? 'border-brand-300 bg-brand-50/60 ring-1 ring-brand-200'
                        : 'border-transparent hover:border-brand-200 hover:bg-brand-50/30',
                    )}
                  >
                    <span
                      className={cn(
                        'grid size-4 shrink-0 place-items-center rounded border transition-colors',
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
    </div>
  )
}
