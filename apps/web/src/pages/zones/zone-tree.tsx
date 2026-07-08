// 区服结构树：BC 集群 → 大区 → 小区，各层可新建。小区显示子服计数 / 默认入口计数。
import { useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Boxes, Layers, MapPin, Plus } from 'lucide-react'

import { AsyncSection, Badge, Button } from '@beacon/ui'
import type { ZoneTreeResponse } from '@beacon/devmock'

import {
  ApiClientError,
  createBcCluster,
  createRegion,
  createZone,
  fetchZoneTree,
} from '../../api/cluster'
import CreateNodeDialog from './create-node-dialog'

// 新建意图：集群（顶层）/ 大区（挂集群）/ 小区（挂大区）
type CreateIntent =
  | { level: 'cluster' }
  | { level: 'region'; bcClusterId: number }
  | { level: 'zone'; regionId: number }

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function ZoneTree({ namespaceId }: { namespaceId: number }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [intent, setIntent] = useState<CreateIntent | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    // namespace 作用域切换时保留上一份结果，避免结构树短暂闪回加载态
    placeholderData: keepPreviousData,
  })

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['zone-tree'] })
  }

  const createMutation = useMutation({
    mutationFn: ({ name, description }: { name: string; description: string }) => {
      if (intent === null) {
        return Promise.reject(new Error('无新建意图'))
      }
      if (intent.level === 'cluster') {
        return createBcCluster({ namespaceId, name, description })
      }
      if (intent.level === 'region') {
        return createRegion({ bcClusterId: intent.bcClusterId, name, description })
      }
      return createZone({ regionId: intent.regionId, name, description })
    },
    onSuccess: async () => {
      await invalidate()
      setIntent(null)
    },
    onError: (error) => {
      setErrorText(messageOf(error))
    },
  })

  const tree: ZoneTreeResponse | undefined = query.data

  const dialogTitle =
    intent?.level === 'cluster'
      ? t('cluster.zones.create.clusterTitle')
      : intent?.level === 'region'
        ? t('cluster.zones.create.regionTitle')
        : t('cluster.zones.create.zoneTitle')

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <Boxes className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('cluster.zones.tree.title')}</h2>
        <Button
          size="sm"
          className="ml-auto gap-1"
          onClick={() => {
            setErrorText(null)
            setIntent({ level: 'cluster' })
          }}
        >
          <Plus className="size-3.5" />
          {t('cluster.zones.tree.newCluster')}
        </Button>
      </div>

      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        {tree?.clusters.length === 0 ? (
          <p className="rounded-lg border border-dashed border-border-strong px-4 py-8 text-center text-sm text-ink-3">
            {t('cluster.zones.tree.empty')}
          </p>
        ) : (
          <ul className="grid gap-2.5">
            {tree?.clusters.map((cluster) => (
              <li key={cluster.id} className="overflow-hidden rounded-lg border border-border">
                {/* BC 集群头（品牌浅底） */}
                <div className="flex items-center justify-between gap-2 border-b border-border bg-brand-50 px-3 py-2">
                  <span className="flex items-center gap-2 text-[12.5px] font-semibold text-brand-600">
                    <Boxes className="size-3.5" />
                    {cluster.name}
                    <Badge variant="brand" className="tnum">
                      {t('cluster.zones.tree.proxyCount', { count: cluster.proxyCount })}
                    </Badge>
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="gap-1"
                    onClick={() => {
                      setErrorText(null)
                      setIntent({ level: 'region', bcClusterId: cluster.id })
                    }}
                  >
                    <Plus className="size-3.5" />
                    {t('cluster.zones.tree.newRegion')}
                  </Button>
                </div>
                <ul className="grid gap-2 p-3">
                  {cluster.regions.map((region) => (
                    <li key={region.id} className="rounded-md border border-border bg-surface-2 p-2.5">
                      <div className="flex items-center justify-between gap-2">
                        <span className="flex items-center gap-1.5 text-[12.5px] font-semibold text-ink-1">
                          <Layers className="size-3.5 text-ink-4" />
                          {region.name}
                        </span>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="gap-1"
                          onClick={() => {
                            setErrorText(null)
                            setIntent({ level: 'zone', regionId: region.id })
                          }}
                        >
                          <Plus className="size-3.5" />
                          {t('cluster.zones.tree.newZone')}
                        </Button>
                      </div>
                      <ul className="mt-2 flex flex-wrap gap-1.5">
                        {region.zones.map((zone) => (
                          <li
                            key={zone.id}
                            className="flex items-center gap-1.5 rounded-md border border-border bg-card px-2 py-1 text-xs shadow-card"
                          >
                            <MapPin className="size-3 text-brand" />
                            <span className="font-mono font-medium text-ink-1">{zone.name}</span>
                            <span className="text-ink-4 tnum">
                              {t('cluster.zones.tree.serverCount', { count: zone.serverCount })}
                            </span>
                            {zone.defaultEntryCount > 0 && (
                              <Badge variant="brand">{t('cluster.zones.tree.defaultEntry')}</Badge>
                            )}
                          </li>
                        ))}
                      </ul>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        )}
      </AsyncSection>

      <CreateNodeDialog
        open={intent !== null}
        onOpenChange={(open) => {
          if (!open) {
            setIntent(null)
          }
        }}
        title={dialogTitle}
        pending={createMutation.isPending}
        errorText={errorText}
        onSubmit={(name, description) => {
          createMutation.mutate({ name, description })
        }}
      />
    </section>
  )
}
