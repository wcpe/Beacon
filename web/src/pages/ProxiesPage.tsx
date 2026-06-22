// 代理服管理页（FR-52，独立页）：集中、透明展示某环境下所有 BC（bungee 代理）运行态——
// 状态 + 底层参数（连接数 / 线程 / 运行时长 / 后端可达性·延迟，FR-34）、后端子服清单（FR-36）、
// 所属小区默认入口（FR-48）、所属 zone。只读呈现既有事实，不做任何写操作 / 调度。
// 实例端点按 role=bungee 过滤要求 namespace，故先选环境再出页；React Query 轮询刷新（与拓扑/实例页一致）。

import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Cpu, DoorOpen, Network, Plug, Server, Timer } from 'lucide-react'
import { listDefaultEntries, listInstances } from '../api/client'
import type { InstanceView } from '../api/types'
import { formatDuration } from '../api/format'
import StatCard from './dashboard/StatCard'
import StatusBadge from '../components/StatusBadge'
import AsyncSection from '@/components/AsyncSection'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'

// 轮询周期（毫秒），与实例与健康页 / 拓扑页一致
const REFETCH_MS = 5000

// 后端可达性文案：有配置后端时显示 up/total，无后端显示「无后端」
function reachText(up: number, total: number): string {
  return total > 0 ? `${up} / ${total}` : '无后端'
}

// 后端平均延迟文案：< 0（约定 -1）表示无可达后端样本，显示「不可用」而非负数
function latencyText(ms: number): string {
  return ms >= 0 ? `${ms.toFixed(0)} ms` : '不可用'
}

export default function ProxiesPage() {
  // 输入框为待提交值，namespace 为已生效查询值（实例 role 过滤需 namespace，空则不查询）
  const [nsInput, setNsInput] = useState('')
  const [namespace, setNamespace] = useState('')

  // 只拉 bc（bungee）实例：含状态 / zone / 后端清单 / proxy 底层参数（FR-34/36）
  const proxies = useQuery({
    queryKey: ['proxies', namespace],
    queryFn: () => listInstances({ namespace, role: 'bungee' }),
    enabled: namespace !== '',
    refetchInterval: REFETCH_MS,
  })

  // 该环境各小区默认入口（FR-48）：按 BC 所属 zone 索引出其默认入口 serverId
  const entries = useQuery({
    queryKey: ['default-entries', namespace],
    queryFn: () => listDefaultEntries(namespace),
    enabled: namespace !== '',
    refetchInterval: REFETCH_MS,
  })

  // (group, zone) → 默认入口 serverId 映射（仅本环境）：默认入口唯一键是 (namespace, group, zone)，
  // 同名 zone 码在不同大区下是不同小区，故须用 group+zone 复合键，否则后写覆盖先写致大区间串值
  const entryByZone = new Map<string, string>()
  for (const e of entries.data ?? []) {
    entryByZone.set(`${e.group}/${e.zone}`, e.defaultServerId)
  }

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setNamespace(nsInput.trim())
  }

  const list = proxies.data ?? []

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">代理服管理</h1>
        {proxies.isFetching && <span className="text-sm text-muted-foreground">（刷新中…）</span>}
      </div>

      <Card>
        <CardContent className="space-y-3">
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="p-namespace">环境</Label>
              <Input
                id="p-namespace"
                placeholder="如 prod / test"
                value={nsInput}
                onChange={(e) => setNsInput(e.target.value)}
              />
            </div>
            <Button type="submit">查询</Button>
          </form>
          <p className="text-sm text-muted-foreground">
            只读呈现该环境全部 BC（bungee 代理）运行态：状态 + 连接数 / 线程 / 运行时长 / 后端可达性·延迟、后端子服清单、所属小区默认入口。
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardContent>
          {namespace === '' ? (
            <p className="py-12 text-center text-sm text-muted-foreground">
              请先在上方输入环境并查询，以查看该环境的 BC 代理运行态。
            </p>
          ) : (
            <AsyncSection isLoading={proxies.isLoading} isError={proxies.isError} error={proxies.error}>
              {list.length === 0 ? (
                <p className="py-12 text-center text-sm text-muted-foreground">该环境暂无在线 BC 代理。</p>
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
          )}
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
  const m = proxy.proxy
  return (
    <Card data-testid={`proxy-card-${proxy.serverId}`}>
      <CardContent className="space-y-4">
        {/* 头部：serverId + 状态 + 大区/小区 + 地址 + 版本 */}
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <span className="font-mono text-base font-semibold">{proxy.serverId}</span>
          <StatusBadge status={proxy.status} />
          <span className="text-sm text-muted-foreground">大区 {proxy.group}</span>
          {proxy.zone === null ? (
            <Badge variant="outline" className="border-amber-500 text-amber-600">
              小区未分配
            </Badge>
          ) : (
            <span className="text-sm text-muted-foreground">小区 {proxy.zone}</span>
          )}
          <span className="font-mono text-sm text-muted-foreground">{proxy.address}</span>
          <span className="text-sm text-muted-foreground">版本 {proxy.version}</span>
        </div>

        {/* 底层参数（FR-34）：连接数 / 线程 / 运行时长 / 后端可达性 / 后端平均延迟 */}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
          <StatCard label="在线连接数" value={m.onlineConnections} icon={<Plug className="size-5" />} />
          <StatCard label="JVM 线程数" value={m.threadCount} icon={<Cpu className="size-5" />} />
          <StatCard
            label="运行时长"
            value={formatDuration(m.uptimeMs / 1000)}
            icon={<Timer className="size-5" />}
          />
          <StatCard
            label="后端可达性"
            value={reachText(m.backendUp, m.backendTotal)}
            hint={
              m.backendTotal > 0
                ? `${Math.round((m.backendUp / m.backendTotal) * 100)}% 可达`
                : '该代理未配置后端'
            }
            icon={<Network className="size-5" />}
          />
          <StatCard
            label="后端平均延迟"
            value={latencyText(m.backendAvgLatencyMs)}
            hint={m.backendAvgLatencyMs >= 0 ? '到可达后端的 ping RTT' : '无可达后端样本'}
            icon={<Server className="size-5" />}
          />
        </div>

        {/* 后端子服清单（FR-36）：该 BC 当前代理的后端 serverId 集合 */}
        <div>
          <div className="mb-1.5 text-sm font-medium">后端子服（{proxy.backends.length}）</div>
          {proxy.backends.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {proxy.backends.map((b) => (
                <Badge key={b} variant="secondary" className="font-mono">
                  {b}
                </Badge>
              ))}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">无后端</p>
          )}
        </div>

        {/* 所属小区默认入口（FR-48）：该 BC home-zone 被指派的默认/fallback 服 */}
        <div className="flex items-center gap-2 text-sm">
          <DoorOpen className="size-4 text-muted-foreground" />
          <span className="text-muted-foreground">小区默认入口：</span>
          {defaultEntry ? (
            <Badge variant="outline" className="font-mono" data-testid="proxy-default-entry">
              {defaultEntry}
            </Badge>
          ) : (
            <span className="text-muted-foreground">未设</span>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
