// 区服结构树：BC 集群 → 大区 → 小区 → 子服，真正的层级树形（缩进 + 连线 + 展开折叠）。
// 各层可新建。代理服（BC/bungee，kind=proxy）用清晰角色标签与图标区分于子服（bukkit，kind=backend）。
import { useEffect, useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import {
  ArrowRightLeft,
  Boxes,
  ChevronDown,
  ChevronRight,
  DoorOpen,
  Layers,
  Link2Off,
  MapPin,
  Network,
  Plus,
  Server,
  ShieldAlert,
  Trash2,
} from 'lucide-react'

import { AsyncSection, Badge, Button, cn, DestructiveConfirmDialog } from '@beacon/ui'
import type { ServerItem, ZoneTreeResponse } from '@beacon/contracts'

import {
  ApiClientError,
  createBcCluster,
  createRegion,
  createZone,
  deleteBcCluster,
  deleteRegion,
  deleteZone,
  fetchIdentities,
  fetchServers,
  fetchZoneTree,
  rezoneServers,
  setDefaultEntry,
  setDraining,
  unbindIdentity,
} from '../../api/cluster'
import { readAssignDrag, writeAssignDrag, type AssignDragPayload } from '../../features/cluster/assign-drag'
import { messageOf as assignMessageOf, useAssignServers } from '../../features/cluster/use-assign-servers'
import { notifyError, notifySuccess } from '../../lib/notify'
import ReasonDialog from '../servers/reason-dialog'
import CreateNodeDialog from './create-node-dialog'
import DragConfirmDialog, { type PendingDrop } from './drag-confirm-dialog'
import RezoneDialog from './rezone-dialog'
import ServerContextMenu, { type ContextMenuItem } from './server-context-menu'

// 放置目标类型：backend 落小区、proxy 落集群；其余节点非目标
type DropTargetKind = 'zone' | 'cluster'

// 新建意图：集群（顶层）/ 大区（挂集群）/ 小区（挂大区）
type CreateIntent =
  | { level: 'cluster' }
  | { level: 'region'; bcClusterId: number }
  | { level: 'zone'; regionId: number }

// 删除意图：空节点才允许（后端守卫；前端按计数禁点并二次确认）
type DeleteIntent =
  | { level: 'cluster'; id: number; name: string; canDelete: boolean }
  | { level: 'region'; id: number; name: string; canDelete: boolean }
  | { level: 'zone'; id: number; name: string; canDelete: boolean }

// 树节点展开集合的键（按层级 + id 唯一）
type NodeKey = string

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function ZoneTree({
  namespaceId,
  draggingKind,
  onDraggingKindChange,
}: {
  namespaceId: number
  // 当前拖拽中的服务器 kind（由父页面共享）：dragover 阶段据此高亮兼容目标
  draggingKind?: 'backend' | 'proxy' | null
  // 树内已分配服务器拖起/结束时上报 kind（改派拖拽用），供树高亮兼容目标
  onDraggingKindChange?: (kind: 'backend' | 'proxy' | null) => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [intent, setIntent] = useState<CreateIntent | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)
  // 展开的节点键集合；默认只展开集群 + 大区，小区默认收起（huge 1200+ 子服时禁止首屏全量挂叶子）
  const [expanded, setExpanded] = useState<Set<NodeKey>>(new Set())
  // 首次数据到达后自动展开集群 + 大区一层（只做一次，之后尊重用户手动折叠）
  const [autoExpanded, setAutoExpanded] = useState(false)
  // 小区叶子「再显示」上限：单区超过此数需点「展开全部」才挂全量 DOM
  const [zoneLeafLimit, setZoneLeafLimit] = useState<Record<string, number>>({})
  const DEFAULT_ZONE_LEAF_LIMIT = 40

  const query = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    // namespace 作用域切换时保留上一份结果，避免结构树短暂闪回加载态
    placeholderData: keepPreviousData,
  })

  // 切换 namespace 时清空展开态与叶子上限，避免旧树键串到新 ns、也避免 huge 下沿用「全展开」
  useEffect(() => {
    setExpanded(new Set())
    setAutoExpanded(false)
    setZoneLeafLimit({})
  }, [namespaceId])

  // 仅当存在已展开的小区时再拉 server 列表（代理层始终需要，故有任意 cluster 展开即拉）
  const needsServerList = useMemo(() => {
    for (const key of expanded) {
      if (key.startsWith('cluster:') || key.startsWith('zone:')) {
        return true
      }
    }
    // 首帧 auto-expand 前也允许预取，避免展开后空白一帧；huge 下数据重但只拉一次
    return autoExpanded
  }, [expanded, autoExpanded])

  // 该 namespace 下 server 列表：小区展开时列子服 + 集群下列代理。pageSize 顶到 2000 覆盖 huge。
  const serversQuery = useQuery({
    queryKey: ['servers', 'tree', namespaceId],
    queryFn: () => fetchServers({ namespaceId, pageSize: 2000 }),
    placeholderData: keepPreviousData,
    enabled: needsServerList || query.isSuccess,
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
    await queryClient.invalidateQueries({ queryKey: ['servers'] })
  }

  const createMutation = useMutation({
    mutationFn: ({ name, description }: { name: string; description: string }) => {
      if (intent === null) {
        return Promise.reject(new Error('无新建意图'))
      }
      if (intent.level === 'cluster') {
        // namespaceId=0 表示「全部命名空间」，创建必须落到具体 namespace，否则后端返回参数错误
        if (namespaceId <= 0) {
          return Promise.reject(new Error(t('cluster.zones.create.needNamespace')))
        }
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

  // 待删除节点（空节点才可删；后端仍做子节点 / 已分配服守卫）
  const [deleting, setDeleting] = useState<DeleteIntent | null>(null)
  // 删除失败仅弹窗内 errorText + toast，不塞 impacts、不插页内横幅
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const deleteMutation = useMutation({
    mutationFn: (target: DeleteIntent) => {
      if (target.level === 'cluster') {
        return deleteBcCluster(target.id)
      }
      if (target.level === 'region') {
        return deleteRegion(target.id)
      }
      return deleteZone(target.id)
    },
    onSuccess: async (_data, target) => {
      await invalidate()
      setDeleting(null)
      setDeleteError(null)
      notifySuccess(t('cluster.zones.tree.deleteOk', { name: target.name }))
    },
    onError: (error) => {
      const text = messageOf(error)
      setDeleteError(text)
      notifyError(text)
    },
  })

  // 树内默认入口 / 排空（维护）：与 /servers 同一 API；失败 toast，排空确认框可再看 errorText
  const [treeOpError, setTreeOpError] = useState<string | null>(null)
  const defaultEntryMutation = useMutation({
    mutationFn: ({ row, next }: { row: ServerItem; next: boolean }) => setDefaultEntry(row.id, next),
    onSuccess: async (_data, vars) => {
      await invalidate()
      setTreeOpError(null)
      notifySuccess(
        vars.next
          ? t('cluster.servers.actions.setDefaultEntry')
          : t('cluster.servers.actions.clearDefaultEntry'),
      )
    },
    onError: (error) => {
      const text = messageOf(error)
      setTreeOpError(text)
      notifyError(text)
    },
  })
  const drainingMutation = useMutation({
    mutationFn: ({ row, next, reason }: { row: ServerItem; next: boolean; reason: string }) =>
      setDraining(row.serverId, next, reason),
    onSuccess: async (_data, vars) => {
      await invalidate()
      setTreeOpError(null)
      setDrainingServer(null)
      notifySuccess(
        vars.next
          ? t('cluster.servers.actions.startDraining')
          : t('cluster.servers.actions.stopDraining'),
      )
    },
    onError: (error) => {
      const text = messageOf(error)
      setTreeOpError(text)
      notifyError(text)
    },
  })
  // 排空需原因确认
  const [drainingServer, setDrainingServer] = useState<{ row: ServerItem; next: boolean } | null>(null)

  // 拖拽落区：当前 drag-over 的目标键（高亮）与最近一次落区错误/结果反馈
  const [dragOverKey, setDragOverKey] = useState<string | null>(null)
  const [dropFeedback, setDropFeedback] = useState<{ tone: 'ok' | 'error'; text: string } | null>(null)
  // 待确认的拖拽落区意图（松手后先弹确认，确认才写）
  const [pendingDrop, setPendingDrop] = useState<PendingDrop | null>(null)
  const [dropError, setDropError] = useState<string | null>(null)
  const dropAssign = useAssignServers()

  const tree: ZoneTreeResponse | undefined = query.data

  // 目标 id → 可读名：小区含集群/大区路径，集群直接用名（确认弹窗展示）
  const nameOfTarget = (targetKind: DropTargetKind, targetId: number): string => {
    if (!tree) {
      return String(targetId)
    }
    if (targetKind === 'cluster') {
      return tree.clusters.find((c) => c.id === targetId)?.name ?? String(targetId)
    }
    for (const cluster of tree.clusters) {
      for (const region of cluster.regions) {
        for (const zone of region.zones) {
          if (zone.id === targetId) {
            return `${cluster.name} / ${region.name} / ${zone.name}`
          }
        }
      }
    }
    return String(targetId)
  }

  // 换区改派 mutation：已分配服务器改归属走 server-rezones 工单
  const rezoneMutation = useMutation({
    mutationFn: ({ serverRowId, target, reason }: { serverRowId: number; target: PendingDrop['target']; reason: string }) =>
      rezoneServers({ serverIds: [serverRowId], target, reason }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['servers'] })
      await queryClient.invalidateQueries({ queryKey: ['zone-tree'] })
    },
  })

  // kind 与目标是否兼容：backend→小区、proxy→集群
  const kindFits = (kind: 'backend' | 'proxy', targetKind: DropTargetKind): boolean =>
    targetKind === 'zone' ? kind === 'backend' : kind === 'proxy'

  // dragover 阶段用共享 draggingKind 判定（此时读不到 dataTransfer 数据）
  const targetAcceptsDragging = (targetKind: DropTargetKind): boolean =>
    draggingKind != null && kindFits(draggingKind, targetKind)

  // 落下阶段用真实载荷判定
  const isCompatible = (payload: AssignDragPayload | null, targetKind: DropTargetKind): boolean =>
    payload !== null && kindFits(payload.kind, targetKind)

  // 落区：读取拖拽载荷，兼容则暂存意图并弹二次确认（不立即写）。
  // 未分配（fromId 为空）→ 首次分配；已分配拖到不同节点 → 换区改派；拖到原属节点无操作。
  const handleDrop = (targetKind: DropTargetKind, targetId: number, dt: DataTransfer) => {
    setDragOverKey(null)
    const payload = readAssignDrag(dt)
    if (!isCompatible(payload, targetKind) || payload === null) {
      return
    }
    const target = { kind: targetKind === 'zone' ? ('zone' as const) : ('bc_cluster' as const), id: targetId }
    const targetName = nameOfTarget(targetKind, targetId)
    setDropError(null)
    // 已分配（携带原归属）
    if (payload.fromId != null) {
      // 拖回原属节点：无操作
      if (payload.fromId === targetId) {
        return
      }
      setPendingDrop({
        mode: 'rezone',
        serverRowId: payload.id,
        serverId: payload.serverId,
        target,
        targetName,
        fromName: payload.fromName ?? undefined,
      })
      return
    }
    // 未分配：首次分配确认
    setPendingDrop({ mode: 'assign', serverRowId: payload.id, serverId: payload.serverId, target, targetName })
  }

  // 确认落区：assign 走首次分配、rezone 走换区工单
  const confirmDrop = (reason: string) => {
    const drop = pendingDrop
    if (drop === null) {
      return
    }
    setDropFeedback(null)
    setDropError(null)
    if (drop.mode === 'assign') {
      dropAssign.mutate(
        { serverIds: [drop.serverRowId], target: drop.target },
        {
          onSuccess: (response) => {
            const failed = response.results.find((r) => !r.ok)
            setDropFeedback(
              failed
                ? { tone: 'error', text: t('cluster.zones.drag.dropFail', { serverId: drop.serverId }) }
                : { tone: 'ok', text: t('cluster.zones.drag.dropOk', { serverId: drop.serverId }) },
            )
            setPendingDrop(null)
          },
          onError: (error) => {
            setDropError(assignMessageOf(error))
          },
        },
      )
      return
    }
    rezoneMutation.mutate(
      { serverRowId: drop.serverRowId, target: drop.target, reason },
      {
        onSuccess: (response) => {
          const failed = response.results.find((r) => !r.ok)
          setDropFeedback(
            failed
              ? { tone: 'error', text: t('cluster.zones.drag.dropFail', { serverId: drop.serverId }) }
              : { tone: 'ok', text: t('cluster.zones.drag.rezoneOk', { serverId: drop.serverId }) },
          )
          setPendingDrop(null)
        },
        onError: (error) => {
          setDropError(assignMessageOf(error))
        },
      },
    )
  }

  // 右键菜单状态：光标位置 + 目标服务器
  const [menu, setMenu] = useState<{ x: number; y: number; server: ServerItem } | null>(null)
  // 「改派到…」目标选择弹窗的当前服务器
  const [rezoneServer, setRezoneServer] = useState<ServerItem | null>(null)
  const [rezoneError, setRezoneError] = useState<string | null>(null)
  // 树内解绑：原因确认弹窗目标服
  const [unbindServer, setUnbindServer] = useState<ServerItem | null>(null)
  const [unbindError, setUnbindError] = useState<string | null>(null)

  // 身份表：serverId → 可解绑 identityId（优先 active/disabled/conflict）
  const identitiesQuery = useQuery({
    queryKey: ['identities', 'by-server', namespaceId],
    queryFn: () => fetchIdentities({ namespaceId, pageSize: 1000 }),
  })
  const identityIdOf = (server: ServerItem): string | null => {
    const matches =
      identitiesQuery.data?.items.filter(
        (item) => item.serverId === server.serverId && item.namespaceId === server.namespaceId,
      ) ?? []
    const preferred =
      matches.find((item) => item.status === 'active') ??
      matches.find((item) => item.status === 'disabled') ??
      matches.find((item) => item.status === 'conflict')
    if (preferred) {
      return preferred.identityId
    }
    return matches.length > 0 ? matches[0].identityId : null
  }
  const unbindMutation = useMutation({
    mutationFn: ({ server, reason }: { server: ServerItem; reason: string }) => {
      const identityId = identityIdOf(server)
      if (identityId === null) {
        return Promise.reject(new ApiClientError(404, 'identity_not_found', '未找到该服务器的绑定身份'))
      }
      return unbindIdentity(identityId, reason)
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['zone-tree'] })
      await queryClient.invalidateQueries({ queryKey: ['servers'] })
      await queryClient.invalidateQueries({ queryKey: ['identities'] })
      setUnbindServer(null)
      setUnbindError(null)
    },
    onError: (error) => {
      setUnbindError(messageOf(error))
    },
  })

  const openContextMenu = (e: React.MouseEvent, server: ServerItem) => {
    e.preventDefault()
    setMenu({ x: e.clientX, y: e.clientY, server })
  }

  // 点选式改派确认：reason 必填由弹窗保证
  const confirmRezonePick = (targetId: number, reason: string) => {
    const server = rezoneServer
    if (server === null) {
      return
    }
    const target = { kind: server.kind === 'proxy' ? ('bc_cluster' as const) : ('zone' as const), id: targetId }
    setRezoneError(null)
    rezoneMutation.mutate(
      { serverRowId: server.id, target, reason },
      {
        onSuccess: () => {
          setRezoneServer(null)
        },
        onError: (error) => {
          setRezoneError(assignMessageOf(error))
        },
      },
    )
  }

  // 构造某服务器行的右键菜单项：改派 / 默认入口 / 排空（维护）/ 健康详情 / 解绑
  const menuItemsFor = (server: ServerItem): ContextMenuItem[] => {
    const items: ContextMenuItem[] = []
    if (server.assigned) {
      items.push({
        key: 'rezone',
        label: t('cluster.zones.menu.rezone'),
        icon: <ArrowRightLeft className="size-3.5" />,
        onSelect: () => {
          setRezoneError(null)
          setRezoneServer(server)
        },
      })
    }
    // 仅已分配 backend 可设默认入口 / 排空（与 /servers 资产表一致）
    if (server.kind === 'backend' && server.assigned) {
      items.push({
        key: 'defaultEntry',
        label: server.isDefaultEntry
          ? t('cluster.servers.actions.clearDefaultEntry')
          : t('cluster.servers.actions.setDefaultEntry'),
        icon: <DoorOpen className="size-3.5" />,
        onSelect: () => {
          setTreeOpError(null)
          defaultEntryMutation.mutate({ row: server, next: !server.isDefaultEntry })
        },
      })
      items.push({
        key: 'draining',
        label: server.draining
          ? t('cluster.servers.actions.stopDraining')
          : t('cluster.servers.actions.startDraining'),
        icon: <ShieldAlert className="size-3.5" />,
        onSelect: () => {
          setTreeOpError(null)
          setDrainingServer({ row: server, next: !server.draining })
        },
      })
    }
    items.push({
      key: 'detail',
      label: t('cluster.zones.menu.viewDetail'),
      icon: <Server className="size-3.5" />,
      onSelect: () => {
        navigate(`/servers?keyword=${encodeURIComponent(server.serverId)}`)
      },
    })
    items.push({
      key: 'unbind',
      label: t('cluster.zones.menu.unbind'),
      icon: <Link2Off className="size-3.5" />,
      tone: 'danger',
      onSelect: () => {
        setUnbindError(null)
        setUnbindServer(server)
      },
    })
    return items
  }

  // 树里已分配服务器叶的拖拽 + 右键属性：拖起写入含原归属的载荷（供落区判定改派），右键弹菜单。
  const leafInteractions = (server: ServerItem) => {
    const fromId = server.kind === 'proxy' ? server.bcClusterId : server.zoneId
    const fromName =
      server.kind === 'proxy' ? server.bcClusterName : (server.zoneName ?? server.regionName)
    return {
      draggable: true,
      onDragStart: (e: React.DragEvent) => {
        writeAssignDrag(e.dataTransfer, {
          id: server.id,
          serverId: server.serverId,
          kind: server.kind === 'proxy' ? 'proxy' : 'backend',
          fromId,
          fromName,
        })
        onDraggingKindChange?.(server.kind === 'proxy' ? 'proxy' : 'backend')
      },
      onDragEnd: () => {
        onDraggingKindChange?.(null)
      },
      onContextMenu: (e: React.MouseEvent) => {
        openContextMenu(e, server)
      },
    }
  }

  // 首帧只自动展开集群 + 大区；小区保持收起，子服按需展开（huge 下避免一次挂 1200+ 叶子）
  useEffect(() => {
    if (!tree || autoExpanded) {
      return
    }
    const next = new Set<NodeKey>()
    for (const cluster of tree.clusters) {
      next.add(`cluster:${String(cluster.id)}`)
      for (const region of cluster.regions) {
        next.add(`region:${String(region.id)}`)
      }
    }
    setExpanded(next)
    setAutoExpanded(true)
  }, [tree, autoExpanded])

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
          disabled={namespaceId <= 0}
          title={namespaceId <= 0 ? t('cluster.zones.create.needNamespace') : undefined}
          onClick={() => {
            if (namespaceId <= 0) {
              setErrorText(t('cluster.zones.create.needNamespace'))
              return
            }
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
                          <Button
                            size="sm"
                            variant="ghost"
                            className="gap-1 text-crit hover:text-crit"
                            title={t('cluster.zones.tree.deleteCluster')}
                            onClick={(e) => {
                              e.stopPropagation()
                              setDeleteError(null)
                              setDeleting({
                                level: 'cluster',
                                id: cluster.id,
                                name: cluster.name,
                                canDelete: cluster.regions.length === 0 && cluster.proxyCount === 0,
                              })
                            }}
                          >
                            <Trash2 className="size-3.5" />
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
                              interactions={leafInteractions(proxy)}
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
                                  <>
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
                                    <Button
                                      size="sm"
                                      variant="ghost"
                                      className="gap-1 text-crit hover:text-crit"
                                      title={t('cluster.zones.tree.deleteRegion')}
                                      onClick={(e) => {
                                        e.stopPropagation()
                                        setDeleteError(null)
                                        setDeleting({
                                          level: 'region',
                                          id: region.id,
                                          name: region.name,
                                          canDelete: region.zones.length === 0,
                                        })
                                      }}
                                    >
                                      <Trash2 className="size-3.5" />
                                    </Button>
                                  </>
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
                                              <Button
                                                size="sm"
                                                variant="ghost"
                                                className="gap-1 text-crit hover:text-crit"
                                                title={t('cluster.zones.tree.deleteZone')}
                                                onClick={(e) => {
                                                  e.stopPropagation()
                                                  setDeleteError(null)
                                                  setDeleting({
                                                    level: 'zone',
                                                    id: zone.id,
                                                    name: zone.name,
                                                    canDelete: zone.serverCount === 0,
                                                  })
                                                }}
                                              >
                                                <Trash2 className="size-3.5" />
                                              </Button>
                                            </>
                                          }
                                        />
                                        {zoneOpen && (
                                          <ul role="group">
                                            {(() => {
                                              const limit =
                                                zoneLeafLimit[zoneKey] ?? DEFAULT_ZONE_LEAF_LIMIT
                                              const visible = zoneServers.slice(0, limit)
                                              const rest = zoneServers.length - visible.length
                                              return (
                                                <>
                                                  {visible.map((s) => (
                                                    <li key={s.id}>
                                                      <TreeLeaf
                                                        depth={3}
                                                        icon={<Server className="size-3 text-ink-3" />}
                                                        label={s.serverId}
                                                        role={t('cluster.servers.kind.backend')}
                                                        roleTone="secondary"
                                                        online={s.online}
                                                        interactions={leafInteractions(s)}
                                                        extra={
                                                          <>
                                                            {s.isDefaultEntry && (
                                                              <Badge variant="brand">
                                                                {t('cluster.zones.tree.defaultEntry')}
                                                              </Badge>
                                                            )}
                                                            {s.draining && (
                                                              <Badge variant="warn">
                                                                {t('cluster.zones.tree.draining')}
                                                              </Badge>
                                                            )}
                                                          </>
                                                        }
                                                      />
                                                    </li>
                                                  ))}
                                                  {rest > 0 && (
                                                    <li className="px-2 py-1" style={{ paddingLeft: `${(3 + 1) * 14}px` }}>
                                                      <Button
                                                        size="sm"
                                                        variant="ghost"
                                                        className="h-7 text-[11.5px] text-ink-3"
                                                        onClick={() => {
                                                          setZoneLeafLimit((prev) => ({
                                                            ...prev,
                                                            [zoneKey]: zoneServers.length,
                                                          }))
                                                        }}
                                                      >
                                                        {t('cluster.zones.tree.showMoreServers', {
                                                          count: rest,
                                                          defaultValue: `再显示 ${String(rest)} 台`,
                                                        })}
                                                      </Button>
                                                    </li>
                                                  )}
                                                </>
                                              )
                                            })()}
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

      {/* 拖拽落区 / 改派二次确认（松手后弹，确认才写） */}
      <DragConfirmDialog
        pending={pendingDrop}
        submitting={dropAssign.isPending || rezoneMutation.isPending}
        errorText={dropError}
        onOpenChange={(open) => {
          if (!open) {
            setPendingDrop(null)
            setDropError(null)
          }
        }}
        onConfirm={confirmDrop}
      />

      {/* 右键菜单「改派到…」触发的点选式改派弹窗 */}
      <RezoneDialog
        server={rezoneServer}
        tree={tree}
        pending={rezoneMutation.isPending}
        errorText={rezoneError}
        onConfirm={confirmRezonePick}
        onOpenChange={(open) => {
          if (!open) {
            setRezoneServer(null)
            setRezoneError(null)
          }
        }}
      />

      {/* 服务器行右键操作菜单（自绘绝对定位） */}
      <ServerContextMenu
        position={menu ? { x: menu.x, y: menu.y } : null}
        items={menu ? menuItemsFor(menu.server) : []}
        onClose={() => {
          setMenu(null)
        }}
      />

      {/* 树内解绑：原因确认 → 真调用 unbind，并清归属 */}
      {unbindServer !== null && (
        <ReasonDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setUnbindServer(null)
              setUnbindError(null)
            }
          }}
          title={t('cluster.servers.confirm.unbindTitle')}
          description={t('cluster.servers.confirm.unbindDesc')}
          confirmLabel={t('cluster.servers.actions.unbind')}
          impacts={[`serverId ${unbindServer.serverId}`]}
          pending={unbindMutation.isPending}
          errorText={unbindError}
          onConfirm={(reason) => {
            unbindMutation.mutate({ server: unbindServer, reason })
          }}
        />
      )}

      {/* 删除空节点：二次确认；失败仅 errorText + toast，不叠 impacts、不插页内横幅 */}
      <DestructiveConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null)
            setDeleteError(null)
          }
        }}
        title={
          deleting?.level === 'cluster'
            ? t('cluster.zones.tree.deleteClusterTitle', { name: deleting.name })
            : deleting?.level === 'region'
              ? t('cluster.zones.tree.deleteRegionTitle', { name: deleting.name })
              : t('cluster.zones.tree.deleteZoneTitle', { name: deleting?.name ?? '' })
        }
        description={
          deleting?.level === 'cluster'
            ? t('cluster.zones.tree.deleteClusterDesc')
            : deleting?.level === 'region'
              ? t('cluster.zones.tree.deleteRegionDesc')
              : t('cluster.zones.tree.deleteZoneDesc')
        }
        confirmLabel={t('cluster.zones.tree.deleteConfirm')}
        cancelLabel={t('cluster.zones.drag.cancel')}
        impacts={[
          deleting?.canDelete
            ? t('cluster.zones.tree.deleteImpactEmpty')
            : t('cluster.zones.tree.deleteImpactHasChildren'),
        ]}
        confirmPhrase={deleting?.name}
        pending={deleteMutation.isPending}
        errorText={deleteError}
        onConfirm={() => {
          if (deleting === null) {
            return false
          }
          if (!deleting.canDelete) {
            const text = t('cluster.zones.tree.deleteImpactHasChildren')
            setDeleteError(text)
            notifyError(text)
            // 阻止关窗：错误只在弹窗 errorText + toast
            return false
          }
          setDeleteError(null)
          deleteMutation.mutate(deleting)
          // 异步结果：成功 onSuccess 关窗；失败 onError 留 errorText，弹窗仍开着
          return false
        }}
      />

      {/* 树内排空（维护模式）：原因必填 */}
      {drainingServer !== null && (
        <ReasonDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setDrainingServer(null)
              setTreeOpError(null)
            }
          }}
          title={
            drainingServer.next
              ? t('cluster.servers.confirm.drainingTitle')
              : t('cluster.servers.actions.stopDraining')
          }
          description={t('cluster.servers.confirm.drainingDesc')}
          confirmLabel={
            drainingServer.next
              ? t('cluster.servers.actions.startDraining')
              : t('cluster.servers.actions.stopDraining')
          }
          impacts={[`serverId ${drainingServer.row.serverId}`]}
          pending={drainingMutation.isPending}
          errorText={treeOpError}
          onConfirm={(reason) => {
            drainingMutation.mutate({
              row: drainingServer.row,
              next: drainingServer.next,
              reason,
            })
          }}
        />
      )}
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
// 已分配服务器叶可拖起改派并支持右键菜单（interactions 提供）。
function TreeLeaf({
  depth,
  icon,
  label,
  role,
  roleTone,
  online,
  extra,
  interactions,
}: {
  depth: number
  icon: React.ReactNode
  label: string
  role: string
  roleTone: 'brand' | 'secondary'
  online: boolean
  extra?: React.ReactNode
  // 拖拽 + 右键交互（可选）：已分配服务器叶传入以支持改派拖拽与右键菜单
  interactions?: {
    draggable: boolean
    onDragStart: (e: React.DragEvent) => void
    onDragEnd: () => void
    onContextMenu: (e: React.MouseEvent) => void
  }
}) {
  const { t } = useTranslation()
  return (
    <div
      className={cn(
        'flex items-center gap-1.5 rounded-md px-2 py-1 text-xs hover:bg-surface-2',
        interactions && 'cursor-grab active:cursor-grabbing active:opacity-60',
      )}
      style={{ paddingLeft: `${String(depth * 18 + 8)}px` }}
      draggable={interactions?.draggable}
      onDragStart={interactions?.onDragStart}
      onDragEnd={interactions?.onDragEnd}
      onContextMenu={interactions?.onContextMenu}
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
        aria-label={online ? t('cluster.servers.summary.online') : t('cluster.servers.health.lost')}
      />
    </div>
  )
}
