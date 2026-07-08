// 区服结构树：BC 集群 → 大区 → 小区 → 子服，真正的层级树形（缩进 + 连线 + 展开折叠）。
// 各层可新建。代理服（BC/bungee，kind=proxy）用清晰角色标签与图标区分于子服（bukkit，kind=backend）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  Boxes,
  ChevronDown,
  ChevronRight,
  Layers,
  MapPin,
  Network,
  Plus,
  Server,
} from 'lucide-react'

import { AsyncSection, Badge, Button, cn } from '@beacon/ui'
import type { ServerItem, ZoneTreeResponse } from '@beacon/devmock'

import {
  ApiClientError,
  createBcCluster,
  createRegion,
  createZone,
  fetchServers,
  fetchZoneTree,
} from '../../api/cluster'
import { readAssignDrag, type AssignDragPayload } from '../../features/cluster/assign-drag'
import { messageOf as assignMessageOf, useAssignServers } from '../../features/cluster/use-assign-servers'
import CreateNodeDialog from './create-node-dialog'

// 放置目标类型：backend 落小区、proxy 落集群；其余节点非目标
type DropTargetKind = 'zone' | 'cluster'

// 新建意图：集群（顶层）/ 大区（挂集群）/ 小区（挂大区）
type CreateIntent =
  | { level: 'cluster' }
  | { level: 'region'; bcClusterId: number }
  | { level: 'zone'; regionId: number }

// 树节点展开集合的键（按层级 + id 唯一）
type NodeKey = string

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function ZoneTree({
  namespaceId,
  draggingKind,
}: {
  namespaceId: number
  // 当前拖拽中的服务器 kind（由父页面共享）：dragover 阶段据此高亮兼容目标
  draggingKind?: 'backend' | 'proxy' | null
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [intent, setIntent] = useState<CreateIntent | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)
  // 展开的节点键集合；默认全部集群与大区展开、小区收起（子服按需展开避免一次渲染上千行）
  const [expanded, setExpanded] = useState<Set<NodeKey>>(new Set())
  // 首次数据到达后自动展开集群 + 大区一层（只做一次，之后尊重用户手动折叠）
  const [autoExpanded, setAutoExpanded] = useState(false)

  const query = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    // namespace 作用域切换时保留上一份结果，避免结构树短暂闪回加载态
    placeholderData: keepPreviousData,
  })

  // 该 namespace 下全量 server（用于小区展开时列出子服 + 代理标注），按需一次拉取。
  const serversQuery = useQuery({
    queryKey: ['servers', 'tree', namespaceId],
    queryFn: () => fetchServers({ namespaceId, pageSize: 2000 }),
    placeholderData: keepPreviousData,
  })
  const serversByZone = useMemo(() => {
    const map = new Map<number, ServerItem[]>()
    for (const s of serversQuery.data?.items ?? []) {
      if (s.kind === 'backend' && s.zoneId !== null) {
        const list = map.get(s.zoneId) ?? []
        list.push(s)
        map.set(s.zoneId, list)
      }
    }
    return map
  }, [serversQuery.data])
  const proxiesByCluster = useMemo(() => {
    const map = new Map<number, ServerItem[]>()
    for (const s of serversQuery.data?.items ?? []) {
      if (s.kind === 'proxy' && s.bcClusterId !== null) {
        const list = map.get(s.bcClusterId) ?? []
        list.push(s)
        map.set(s.bcClusterId, list)
      }
    }
    return map
  }, [serversQuery.data])

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

  // 拖拽落区：当前 drag-over 的目标键（高亮）与最近一次落区错误/结果反馈
  const [dragOverKey, setDragOverKey] = useState<string | null>(null)
  const [dropFeedback, setDropFeedback] = useState<{ tone: 'ok' | 'error'; text: string } | null>(null)
  const dropAssign = useAssignServers()

  // kind 与目标是否兼容：backend→小区、proxy→集群
  const kindFits = (kind: 'backend' | 'proxy', targetKind: DropTargetKind): boolean =>
    targetKind === 'zone' ? kind === 'backend' : kind === 'proxy'

  // dragover 阶段用共享 draggingKind 判定（此时读不到 dataTransfer 数据）
  const targetAcceptsDragging = (targetKind: DropTargetKind): boolean =>
    draggingKind != null && kindFits(draggingKind, targetKind)

  // 落下阶段用真实载荷判定
  const isCompatible = (payload: AssignDragPayload | null, targetKind: DropTargetKind): boolean =>
    payload !== null && kindFits(payload.kind, targetKind)

  // 落区：读取拖拽载荷，兼容则调用分配 mutation
  const handleDrop = (targetKind: DropTargetKind, targetId: number, dt: DataTransfer) => {
    setDragOverKey(null)
    const payload = readAssignDrag(dt)
    if (!isCompatible(payload, targetKind) || payload === null) {
      return
    }
    setDropFeedback(null)
    dropAssign.mutate(
      {
        serverIds: [payload.id],
        target: { kind: targetKind === 'zone' ? 'zone' : 'bc_cluster', id: targetId },
      },
      {
        onSuccess: (response) => {
          const failed = response.results.find((r) => !r.ok)
          if (failed) {
            setDropFeedback({
              tone: 'error',
              text: t('cluster.zones.drag.dropFail', { serverId: payload.serverId }),
            })
          } else {
            setDropFeedback({ tone: 'ok', text: t('cluster.zones.drag.dropOk', { serverId: payload.serverId }) })
          }
        },
        onError: (error) => {
          setDropFeedback({ tone: 'error', text: assignMessageOf(error) })
        },
      },
    )
  }

  const tree: ZoneTreeResponse | undefined = query.data

  // 首帧自动展开集群 + 大区
  if (tree && !autoExpanded) {
    const next = new Set<NodeKey>()
    for (const cluster of tree.clusters) {
      next.add(`cluster:${String(cluster.id)}`)
      for (const region of cluster.regions) {
        next.add(`region:${String(region.id)}`)
      }
    }
    setExpanded(next)
    setAutoExpanded(true)
  }

  const toggle = (key: NodeKey) => {
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

  const dialogTitle =
    intent?.level === 'cluster'
      ? t('cluster.zones.create.clusterTitle')
      : intent?.level === 'region'
        ? t('cluster.zones.create.regionTitle')
        : t('cluster.zones.create.zoneTitle')

  return (
    <section className="grid gap-0 rounded-xl border border-border bg-card shadow-card">
      {/* 吸顶标题条 + 新建集群 */}
      <div className="sticky top-0 z-10 flex items-center gap-2.5 rounded-t-xl border-b border-border bg-card/95 px-4 py-3 backdrop-blur supports-backdrop-filter:bg-card/80">
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

      {/* 拖拽提示：拖拽中提示可放置目标；落区后给成功/失败反馈（非模态、行内） */}
      {draggingKind != null && (
        <p className="mx-3 mt-2 rounded-md border border-brand-100 bg-brand-50 px-3 py-1.5 text-[11.5px] text-brand-600">
          {draggingKind === 'proxy' ? t('cluster.zones.drag.hintProxy') : t('cluster.zones.drag.hintBackend')}
        </p>
      )}
      {dropFeedback && draggingKind == null && (
        <p
          className={cn(
            'mx-3 mt-2 rounded-md px-3 py-1.5 text-[11.5px]',
            dropFeedback.tone === 'ok'
              ? 'border border-ok-bd bg-ok-bg text-ok'
              : 'border border-crit-bd bg-crit-bg text-crit',
          )}
        >
          {dropFeedback.text}
        </p>
      )}

      <div className="max-h-[calc(100vh-16rem)] overflow-y-auto p-3">
        <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
          {tree?.clusters.length === 0 ? (
            <p className="rounded-lg border border-dashed border-border-strong px-4 py-8 text-center text-sm text-ink-3">
              {t('cluster.zones.tree.empty')}
            </p>
          ) : (
            <ul className="grid gap-1" role="tree">
              {tree?.clusters.map((cluster) => {
                const clusterKey = `cluster:${String(cluster.id)}`
                const clusterOpen = expanded.has(clusterKey)
                const proxies = proxiesByCluster.get(cluster.id) ?? []
                return (
                  <li key={cluster.id} role="treeitem" aria-expanded={clusterOpen}>
                    {/* BC 集群节点：代理服（BC/bungee）明确标注，同时作为 proxy 落区目标 */}
                    <TreeRow
                      depth={0}
                      open={clusterOpen}
                      onToggle={() => { toggle(clusterKey) }}
                      icon={<Boxes className="size-3.5 text-brand" />}
                      label={cluster.name}
                      tone="cluster"
                      dropTarget={{
                        active: dragOverKey === clusterKey,
                        accepts: () => targetAcceptsDragging('cluster'),
                        onDragEnter: () => { setDragOverKey(clusterKey) },
                        onDragLeave: () => { setDragOverKey((k) => (k === clusterKey ? null : k)) },
                        onDrop: (dt) => { handleDrop('cluster', cluster.id, dt) },
                      }}
                      trailing={
                        <>
                          <Badge variant="brand" className="gap-1 tnum">
                            <Network className="size-3" />
                            {t('cluster.zones.tree.proxyRole')} · {cluster.proxyCount}
                          </Badge>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="gap-1"
                            onClick={(e) => {
                              e.stopPropagation()
                              setErrorText(null)
                              setIntent({ level: 'region', bcClusterId: cluster.id })
                            }}
                          >
                            <Plus className="size-3.5" />
                            {t('cluster.zones.tree.newRegion')}
                          </Button>
                        </>
                      }
                    />
                    {clusterOpen && (
                      <ul role="group">
                        {/* 代理服子项（BC 层直属，角色徽标区分子服） */}
                        {proxies.map((proxy) => (
                          <li key={`proxy-${String(proxy.id)}`}>
                            <TreeLeaf
                              depth={1}
                              icon={<Network className="size-3 text-brand-600" />}
                              label={proxy.serverId}
                              role={t('cluster.servers.kind.proxy')}
                              roleTone="brand"
                              online={proxy.online}
                            />
                          </li>
                        ))}
                        {cluster.regions.map((region) => {
                          const regionKey = `region:${String(region.id)}`
                          const regionOpen = expanded.has(regionKey)
                          return (
                            <li key={region.id} role="treeitem" aria-expanded={regionOpen}>
                              <TreeRow
                                depth={1}
                                open={regionOpen}
                                onToggle={() => { toggle(regionKey) }}
                                icon={<Layers className="size-3.5 text-ink-4" />}
                                label={region.name}
                                tone="region"
                                trailing={
                                  <Button
                                    size="sm"
                                    variant="ghost"
                                    className="gap-1"
                                    onClick={(e) => {
                                      e.stopPropagation()
                                      setErrorText(null)
                                      setIntent({ level: 'zone', regionId: region.id })
                                    }}
                                  >
                                    <Plus className="size-3.5" />
                                    {t('cluster.zones.tree.newZone')}
                                  </Button>
                                }
                              />
                              {regionOpen && (
                                <ul role="group">
                                  {region.zones.map((zone) => {
                                    const zoneKey = `zone:${String(zone.id)}`
                                    const zoneOpen = expanded.has(zoneKey)
                                    const zoneServers = serversByZone.get(zone.id) ?? []
                                    return (
                                      <li key={zone.id} role="treeitem" aria-expanded={zoneOpen}>
                                        <TreeRow
                                          depth={2}
                                          open={zoneOpen}
                                          onToggle={() => { toggle(zoneKey) }}
                                          disabled={zone.serverCount === 0}
                                          icon={<MapPin className="size-3.5 text-brand" />}
                                          label={zone.name}
                                          mono
                                          tone="zone"
                                          dropTarget={{
                                            active: dragOverKey === zoneKey,
                                            accepts: () => targetAcceptsDragging('zone'),
                                            onDragEnter: () => { setDragOverKey(zoneKey) },
                                            onDragLeave: () => { setDragOverKey((k) => (k === zoneKey ? null : k)) },
                                            onDrop: (dt) => { handleDrop('zone', zone.id, dt) },
                                          }}
                                          trailing={
                                            <>
                                              <span className="text-ink-4 tnum">
                                                {t('cluster.zones.tree.serverCount', { count: zone.serverCount })}
                                              </span>
                                              {zone.defaultEntryCount > 0 && (
                                                <Badge variant="brand">{t('cluster.zones.tree.defaultEntry')}</Badge>
                                              )}
                                            </>
                                          }
                                        />
                                        {zoneOpen && (
                                          <ul role="group">
                                            {zoneServers.map((s) => (
                                              <li key={s.id}>
                                                <TreeLeaf
                                                  depth={3}
                                                  icon={<Server className="size-3 text-ink-3" />}
                                                  label={s.serverId}
                                                  role={t('cluster.servers.kind.backend')}
                                                  roleTone="secondary"
                                                  online={s.online}
                                                  extra={
                                                    <>
                                                      {s.isDefaultEntry && (
                                                        <Badge variant="brand">{t('cluster.zones.tree.defaultEntry')}</Badge>
                                                      )}
                                                      {s.draining && (
                                                        <Badge variant="warn">{t('cluster.zones.tree.draining')}</Badge>
                                                      )}
                                                    </>
                                                  }
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
                  </li>
                )
              })}
            </ul>
          )}
        </AsyncSection>
      </div>

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

// 可展开的树行（集群 / 大区 / 小区）：左侧缩进 + 展开箭头 + 图标 + 名称 + 右侧尾随内容。
// 作为放置目标时（dropTarget 提供）：drag-over 兼容则高亮，松手触发落区。
function TreeRow({
  depth,
  open,
  onToggle,
  disabled,
  icon,
  label,
  mono,
  tone,
  trailing,
  dropTarget,
}: {
  depth: number
  open: boolean
  onToggle: () => void
  disabled?: boolean
  icon: React.ReactNode
  label: string
  mono?: boolean
  tone: 'cluster' | 'region' | 'zone'
  trailing?: React.ReactNode
  // 放置目标能力：判定兼容、drag-over 高亮态、进入/离开/落下回调
  dropTarget?: {
    active: boolean
    accepts: () => boolean
    onDragEnter: () => void
    onDragLeave: () => void
    onDrop: (dt: DataTransfer) => void
  }
}) {
  const toneClass =
    tone === 'cluster'
      ? 'bg-brand-50/60 font-semibold text-brand-600'
      : tone === 'region'
        ? 'bg-surface-2 font-semibold text-ink-1'
        : 'font-medium text-ink-1'
  return (
    <div
      className={cn(
        'group flex items-center gap-1.5 rounded-md px-2 py-1.5 transition-colors',
        toneClass,
        !disabled && 'cursor-pointer hover:bg-brand-50',
        // 拖拽兼容目标 drag-over 高亮：品牌色描边 + 底色
        dropTarget?.active && 'bg-brand-100 ring-2 ring-brand ring-inset',
      )}
      style={{ paddingLeft: `${String(depth * 18 + 8)}px` }}
      onClick={disabled ? undefined : onToggle}
      role="button"
      tabIndex={disabled ? -1 : 0}
      onKeyDown={(e) => {
        if (!disabled && (e.key === 'Enter' || e.key === ' ')) {
          e.preventDefault()
          onToggle()
        }
      }}
      onDragOver={
        dropTarget
          ? (e) => {
              if (dropTarget.accepts()) {
                // 允许放置（否则浏览器默认禁止 drop）
                e.preventDefault()
                e.dataTransfer.dropEffect = 'move'
                if (!dropTarget.active) {
                  dropTarget.onDragEnter()
                }
              }
            }
          : undefined
      }
      onDragLeave={
        dropTarget
          ? () => {
              dropTarget.onDragLeave()
            }
          : undefined
      }
      onDrop={
        dropTarget
          ? (e) => {
              e.preventDefault()
              dropTarget.onDrop(e.dataTransfer)
            }
          : undefined
      }
    >
      {/* 展开箭头（无子项时占位保持对齐） */}
      <span className="grid size-4 shrink-0 place-items-center text-ink-4">
        {disabled ? null : open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
      </span>
      {icon}
      <span className={cn('text-[12.5px]', mono && 'font-mono')}>{label}</span>
      {trailing && <span className="ml-auto flex items-center gap-1.5 text-xs">{trailing}</span>}
    </div>
  )
}

// 叶子行（代理服 / 子服）：无展开箭头，带角色徽标 + 在线状态点。
function TreeLeaf({
  depth,
  icon,
  label,
  role,
  roleTone,
  online,
  extra,
}: {
  depth: number
  icon: React.ReactNode
  label: string
  role: string
  roleTone: 'brand' | 'secondary'
  online: boolean
  extra?: React.ReactNode
}) {
  return (
    <div
      className="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs hover:bg-surface-2"
      style={{ paddingLeft: `${String(depth * 18 + 8)}px` }}
    >
      {/* 无子项，占位对齐箭头列 */}
      <span className="size-4 shrink-0" />
      {icon}
      <span className="font-mono font-medium text-ink-1">{label}</span>
      <Badge variant={roleTone} className="gap-1">
        {role}
      </Badge>
      {extra}
      <span
        className={cn('ml-auto size-1.5 rounded-full', online ? 'bg-ok' : 'bg-crit')}
        aria-label={online ? '在线' : '失联'}
      />
    </div>
  )
}
