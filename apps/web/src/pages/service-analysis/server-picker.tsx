// 服务器选择：列出在线子服（backend），多选用于指标时序与多服对比。
// 数据源复用集群域 fetchServers（只读，不改 cluster.ts）。

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Button,
  Checkbox,
  SectionHeader,
  TableSkeleton,
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
      <div className="flex flex-wrap items-center justify-between gap-2">
        <SectionHeader title={t('observability.serviceAnalysis.pickServers')} />
        {selected.size > 0 && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <span>{t('observability.serviceAnalysis.selectedCount', { count: selected.size })}</span>
            <Button size="sm" variant="outline" onClick={onClear}>
              {t('observability.serviceAnalysis.clear')}
            </Button>
          </div>
        )}
      </div>

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={2} rows={4} />}
      >
        {servers.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('observability.serviceAnalysis.empty')}</p>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(11rem,1fr))] gap-2">
            {servers.map((s) => (
              <label
                key={s.serverId}
                className="flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm"
              >
                <Checkbox
                  checked={selected.has(s.serverId)}
                  onCheckedChange={() => {
                    onToggle(s.serverId)
                  }}
                  aria-label={s.serverId}
                />
                <span className="font-mono text-xs">{s.serverId}</span>
                <span className="ml-auto text-xs text-muted-foreground">{s.zoneName ?? '-'}</span>
              </label>
            ))}
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
