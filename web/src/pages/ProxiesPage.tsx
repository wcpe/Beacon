// 代理服管理页（FR-52，独立页）：集中、透明展示 BC（bungee 代理）运行态——
// 状态 + 底层参数（连接数 / 线程 / 运行时长 / 后端可达性·延迟，FR-34）、后端子服清单（FR-36）、
// 所属小区默认入口（FR-48）、所属 zone。只读呈现既有事实，不做任何写操作 / 调度。
// 进页默认聚合全部环境的 BC，可按环境（namespace）下拉筛选（FR-51）；React Query 轮询刷新（与拓扑/实例页一致）。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { useQuery } from '@tanstack/react-query'
import { Cpu, DoorOpen, Network, Plug, Server, Timer } from 'lucide-react'
import { listDefaultEntries, listInstances, listNamespaces } from '../api/client'
import type { InstanceView } from '../api/types'
import { formatDuration, namespaceOptions } from '../api/format'
import StatCard from './dashboard/StatCard'
import StatusBadge from '../components/StatusBadge'
import AsyncSection from '@/components/AsyncSection'
import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Combobox } from '@/components/ui/combobox'

// 轮询周期（毫秒），与实例与健康页 / 拓扑页一致
const REFETCH_MS = 5000

// 后端可达性文案：有配置后端时显示 up/total，无后端显示「无后端」
function reachText(t: TFunction, up: number, total: number): string {
  return total > 0 ? `${up} / ${total}` : t('proxies.noBackend')
}

// 后端平均延迟文案：< 0（约定 -1）表示无可达后端样本，显示「不可用」而非负数
function latencyText(t: TFunction, ms: number): string {
  return ms >= 0 ? `${ms.toFixed(0)} ms` : t('proxies.unavailable')
}

export default function ProxiesPage() {
  const { t } = useTranslation()
  // 环境过滤（可编辑下拉，FR-51）：空表示聚合全部环境，进页默认即展示全部 BC。
  const [namespace, setNamespace] = useState('')

  // 环境下拉候选来自 listNamespaces；筛选框允许键入候选外的值（可编辑）。
  // 候选显示「编码 · 名称」，真实值仍是 code（FR-70）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const nsOptions = useMemo(
    () => namespaceOptions(namespacesQuery.data),
    [namespacesQuery.data],
  )

  // 只拉 bc（bungee）实例：含状态 / zone / 后端清单 / proxy 底层参数（FR-34/36）。
  // 空 namespace 聚合全部环境，与看板「留空聚合全部环境」口径一致。
  const proxies = useQuery({
    queryKey: ['proxies', namespace],
    queryFn: () => listInstances({ namespace: namespace || undefined, role: 'bungee' }),
    refetchInterval: REFETCH_MS,
  })

  // 各小区默认入口（FR-48）：按 BC 所属 (group, zone) 索引出其默认入口 serverId
  const entries = useQuery({
    queryKey: ['default-entries', namespace],
    queryFn: () => listDefaultEntries(namespace || undefined),
    refetchInterval: REFETCH_MS,
  })

  // (group, zone) → 默认入口 serverId 映射（仅本环境）：默认入口唯一键是 (namespace, group, zone)，
  // 同名 zone 码在不同大区下是不同小区，故须用 group+zone 复合键，否则后写覆盖先写致大区间串值
  const entryByZone = new Map<string, string>()
  for (const e of entries.data ?? []) {
    entryByZone.set(`${e.group}/${e.zone}`, e.defaultServerId)
  }

  const list = proxies.data ?? []

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{t('proxies.title')}</h1>
        {proxies.isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
      </div>

      <Card>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="p-namespace">{t('common.namespace')}</Label>
              {/* 环境筛选：可编辑下拉，候选来自 API 但允许键入列表外值（FR-51）；留空聚合全部环境 */}
              <Combobox
                id="p-namespace"
                aria-label={t('common.namespace')}
                className="w-40"
                placeholder={t('proxies.nsPlaceholder')}
                value={namespace}
                onChange={setNamespace}
                options={nsOptions}
                allowCustom
              />
            </div>
          </div>
          <p className="text-sm text-muted-foreground">
            {t('proxies.desc')}
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardContent>
          <AsyncSection isLoading={proxies.isLoading} isError={proxies.isError} error={proxies.error}>
            {list.length === 0 ? (
              <p className="py-12 text-center text-sm text-muted-foreground">{t('proxies.noProxies')}</p>
            ) : (
              <div className="space-y-4">
                {list.map((p) => (
                  <ProxyCard
                    key={`${p.namespace}/${p.serverId}`}
                    proxy={p}
                    defaultEntry={p.zone ? entryByZone.get(`${p.group}/${p.zone}`) : undefined}
                  />
                ))}
              </div>
            )}
          </AsyncSection>
        </CardContent>
      </Card>
    </div>
  )
}

interface ProxyCardProps {
  // 单台 BC 实例（role=bungee）
  proxy: InstanceView
  // 该 BC 所属小区的默认入口 serverId（FR-48；未设/未分配 zone 时 undefined）
  defaultEntry?: string
}

// 单台 BC 卡片：头部状态/地址/zone + 底层参数卡片组 + 后端子服清单 + 默认入口。
function ProxyCard({ proxy, defaultEntry }: ProxyCardProps) {
  const { t } = useTranslation()
  const m = proxy.proxy
  return (
    <Card data-testid={`proxy-card-${proxy.serverId}`}>
      <CardContent className="space-y-4">
        {/* 头部：serverId + 状态 + 大区/小区 + 地址 + 版本 */}
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <span className="font-mono text-base font-semibold">{proxy.serverId}</span>
          <StatusBadge status={proxy.status} />
          <span className="text-sm text-muted-foreground">{t('proxies.groupLabel', { group: proxy.group })}</span>
          {proxy.zone === null ? (
            <Badge variant="outline" className="border-amber-500 text-amber-600">
              {t('proxies.zoneUnassigned')}
            </Badge>
          ) : (
            <span className="text-sm text-muted-foreground">{t('proxies.zoneLabel', { zone: proxy.zone })}</span>
          )}
          <span className="font-mono text-sm text-muted-foreground">{proxy.address}</span>
          <span className="text-sm text-muted-foreground">{t('proxies.versionLabel', { version: proxy.version })}</span>
        </div>

        {/* 底层参数（FR-34）：连接数 / 线程 / 运行时长 / 后端可达性 / 后端平均延迟 */}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
          <StatCard label={t('proxies.cardConnections')} value={m.onlineConnections} icon={<Plug className="size-5" />} />
          <StatCard label={t('proxies.cardThreads')} value={m.threadCount} icon={<Cpu className="size-5" />} />
          <StatCard
            label={t('proxies.cardUptime')}
            value={formatDuration(m.uptimeMs / 1000)}
            icon={<Timer className="size-5" />}
          />
          <StatCard
            label={t('proxies.cardBackendReach')}
            value={reachText(t, m.backendUp, m.backendTotal)}
            hint={
              m.backendTotal > 0
                ? t('proxies.reachPercent', { percent: Math.round((m.backendUp / m.backendTotal) * 100) })
                : t('proxies.noBackendConfigured')
            }
            icon={<Network className="size-5" />}
          />
          <StatCard
            label={t('proxies.cardBackendLatency')}
            value={latencyText(t, m.backendAvgLatencyMs)}
            hint={m.backendAvgLatencyMs >= 0 ? t('proxies.pingHint') : t('proxies.noReachableSample')}
            icon={<Server className="size-5" />}
          />
        </div>

        {/* 后端子服清单（FR-36）：该 BC 当前代理的后端 serverId 集合 */}
        <div>
          <div className="mb-1.5 text-sm font-medium">{t('proxies.backendsTitle', { count: proxy.backends.length })}</div>
          {proxy.backends.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {proxy.backends.map((b) => (
                <Badge key={b} variant="secondary" className="font-mono">
                  {b}
                </Badge>
              ))}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">{t('proxies.noBackend')}</p>
          )}
        </div>

        {/* 所属小区默认入口（FR-48）：该 BC home-zone 被指派的默认/fallback 服 */}
        <div className="flex items-center gap-2 text-sm">
          <DoorOpen className="size-4 text-muted-foreground" />
          <span className="text-muted-foreground">{t('proxies.defaultEntryLabel')}</span>
          {defaultEntry ? (
            <Badge variant="outline" className="font-mono" data-testid="proxy-default-entry">
              {defaultEntry}
            </Badge>
          ) : (
            <span className="text-muted-foreground">{t('proxies.defaultEntryUnset')}</span>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
