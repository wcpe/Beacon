// 区服结构树：BC 集群 → 大区 → 小区，各层可新建。小区显示子服计数 / 默认入口计数。
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, Badge, Button, SectionHeader } from '@beacon/ui'
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
    <section className="grid gap-3">
      <div className="flex items-center justify-between">
        <SectionHeader title={t('cluster.zones.tree.title')} />
        <Button
          size="sm"
          onClick={() => {
            setErrorText(null)
            setIntent({ level: 'cluster' })
          }}
        >
          {t('cluster.zones.tree.newCluster')}
        </Button>
      </div>

      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        {tree?.clusters.length === 0 ? (
          <p className="rounded-md border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
            {t('cluster.zones.tree.empty')}
          </p>
        ) : (
          <ul className="grid gap-2">
            {tree?.clusters.map((cluster) => (
              <li key={cluster.id} className="rounded-lg border">
                <div className="flex items-center justify-between gap-2 border-b bg-muted/40 px-3 py-2">
                  <span className="flex items-center gap-2 font-medium">
                    <span>{cluster.name}</span>
                    <Badge variant="secondary">{t('cluster.zones.tree.proxyCount', { count: cluster.proxyCount })}</Badge>
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setErrorText(null)
                      setIntent({ level: 'region', bcClusterId: cluster.id })
                    }}
                  >
                    {t('cluster.zones.tree.newRegion')}
                  </Button>
                </div>
                <ul className="grid gap-1.5 p-3">
                  {cluster.regions.map((region) => (
                    <li key={region.id} className="rounded-md bg-secondary/40 p-2">
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-sm font-medium">{region.name}</span>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => {
                            setErrorText(null)
                            setIntent({ level: 'zone', regionId: region.id })
                          }}
                        >
                          {t('cluster.zones.tree.newZone')}
                        </Button>
                      </div>
                      <ul className="mt-1.5 flex flex-wrap gap-1.5">
                        {region.zones.map((zone) => (
                          <li
                            key={zone.id}
                            className="flex items-center gap-1.5 rounded-md bg-background px-2 py-1 text-xs ring-1 ring-border"
                          >
                            <span className="font-mono">{zone.name}</span>
                            <span className="text-muted-foreground">
                              {t('cluster.zones.tree.serverCount', { count: zone.serverCount })}
                            </span>
                            {zone.defaultEntryCount > 0 && (
                              <Badge variant="outline">{t('cluster.zones.tree.defaultEntry')}</Badge>
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
